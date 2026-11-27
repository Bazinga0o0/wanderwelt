package server

// Mitgliederliste, Rollen, Minecraft-Namen und Speicherbudget.
//
// Admins sehen alle Mitglieder (GET /v1/members) und schalten Admin- und
// Op-Recht um (PUT /v1/members/{email}). Seinen Minecraft-Namen meldet ein
// Mitglied einmal selbst, solange keiner eingetragen ist (POST
// /v1/me/minecraft); ein Admin setzt ihn jederzeit (PUT
// /v1/members/{email}/minecraft). Das Speicherbudget setzt ein Admin mit PUT
// /v1/storage/budget. Alles landet in Überlagerungen in data_dir
// (config/roles.go: roles.json, names.json, settings.json), nie in der
// Konfiguration – die ist für den Dienst nur lesbar.
//
// Jede Änderung an Mitgliedern, Rollen oder Namen erhöht MembersVersion
// (Antwort auf das Lebenszeichen); der Host holt dann die Host-Einstellungen
// neu und gleicht Whitelist und Ops seines laufenden Servers an.
//
// Eine kaputte Überlagerung gilt nicht: Fehler im Log, es gelten die Werte
// aus der Konfiguration. Die Datei bleibt liegen, bis sich ein Wert darin
// wieder ändert; dann wird sie als <Datei>.kaputt-<Zeit> beiseitegelegt und
// neu geschrieben.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
)

// MinBudget ist das kleinste Speicherbudget, das ein Admin setzen kann.
const MinBudget int64 = 1 << 30

// ErrUnknownName: Mojang kennt den Minecraft-Namen nicht (UUIDResolver).
var ErrUnknownName = errors.New("Minecraft-Name unbekannt")

// --- Laden ---

// loadOverlays liest beim Start die Überlagerungen aus data_dir und setzt
// wirksame Mitgliederliste und Speicherbudget.
func (s *Server) loadOverlays() {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	s.readMemberOverlaysLocked()
	s.keepAdminLocked()
	s.applyOverlaysLocked()

	s.budget.Store(s.cfg.StorageBudget)
	if s.cfg.DataDir == "" {
		return
	}
	path := filepath.Join(s.cfg.DataDir, config.SettingsFile)
	set, err := config.LoadSettings(path)
	if err != nil {
		s.overlayBroken(path, err)
		return
	}
	if b := set.StorageBudgetBytes; b > 0 {
		if err := s.st.SetBudget(b); err != nil {
			s.log.Error("Speicherbudget aus settings.json nicht übernommen", "err", err)
			return
		}
		s.budget.Store(b)
		s.log.Info("Speicherbudget aus settings.json (überlagert storage_budget)", "budget", formatGiB(b))
	}
}

// readMemberOverlaysLocked liest roles.json und names.json (neu); eine
// kaputte gilt als leer. Aufruf unter overlayMu.
func (s *Server) readMemberOverlaysLocked() {
	s.roles, s.names = config.Roles{}, config.Names{}
	// Eine kaputte members.json oder joins.json lässt die bisher gültigen
	// Werte stehen (beim Start: leer); sonst wären alle angenommenen
	// Mitglieder auf einmal ausgesperrt.
	if s.joined == nil {
		s.joined = config.JoinedMembers{}
	}
	if s.joins == nil {
		s.joins = config.JoinEntries{}
	}
	defer func() {
		s.cfgMembers = config.MergeJoined(s.confMembers, s.joined)
		s.openJoins.Store(int64(s.countOpenJoinsLocked()))
	}()
	if s.cfg.DataDir == "" {
		return
	}
	path := filepath.Join(s.cfg.DataDir, config.MembersFile)
	if j, err := config.LoadJoined(path); err != nil {
		s.overlayBroken(path, err)
		s.log.Error("MEMBERS.JSON KAPUTT – die zuletzt gültigen angenommenen Mitglieder bleiben (beim Start: keine); Datei berichtigen und neu laden")
	} else {
		s.joined = j
		delete(s.corrupt, path)
	}
	path = filepath.Join(s.cfg.DataDir, config.JoinsFile)
	if j, err := config.LoadJoins(path); err != nil {
		s.overlayBroken(path, err)
	} else {
		s.joins = j
		delete(s.corrupt, path)
	}
	path = filepath.Join(s.cfg.DataDir, config.RolesFile)
	if r, err := config.LoadRoles(path); err != nil {
		s.overlayBroken(path, err)
	} else {
		s.roles = r
		delete(s.corrupt, path)
	}
	path = filepath.Join(s.cfg.DataDir, config.NamesFile)
	if n, err := config.LoadNames(path); err != nil {
		s.overlayBroken(path, err)
	} else {
		s.names = n
		delete(s.corrupt, path)
	}
	s.readServerListsLocked()
}

// keepAdminLocked sorgt dafür, dass es einen Admin gibt: Bliebe mit
// roles.json keiner übrig (von Hand geändert, oder der einzige per
// roles.json ernannte Admin steht nicht mehr in der Konfiguration), gilt
// roles.json nicht – es gelten die Rollen aus der Konfiguration. Die Datei
// bleibt dann liegen wie eine kaputte und wird bei der nächsten
// Rollenänderung beiseitegelegt. Aufruf unter overlayMu, vor
// applyOverlaysLocked.
func (s *Server) keepAdminLocked() {
	isAdmin := func(m config.Member) bool { return m.Admin }
	if len(s.roles) == 0 || slices.ContainsFunc(effectiveMembers(s.cfgMembers, s.names, s.roles), isAdmin) {
		return
	}
	path := filepath.Join(s.cfg.DataDir, config.RolesFile)
	s.roles = config.Roles{}
	s.corrupt[path] = true
	s.log.Error("ROLES.JSON LÄSST KEINEN ADMIN ÜBRIG – sie gilt nicht, es gelten die Rollen aus der Konfiguration; "+
		"die Datei bleibt liegen, bis sich eine Rolle wieder ändert", "datei", path)
	if !slices.ContainsFunc(s.cfgMembers, isAdmin) {
		s.log.Error("KEIN ADMIN – auch die Konfiguration nennt keinen (admin: true eintragen und neu laden)")
	}
}

// overlayBroken merkt sich die kaputte Überlagerung path und meldet sie.
func (s *Server) overlayBroken(path string, err error) {
	s.corrupt[path] = true
	s.log.Error("ÜBERLAGERUNG KAPUTT – sie gilt nicht, es gelten die Werte aus der Konfiguration; "+
		"die Datei bleibt liegen, bis sich ein Wert darin wieder ändert", "datei", path, "err", err)
}

// applyOverlaysLocked bildet die wirksame Mitgliederliste aus cfgMembers,
// names und roles, setzt sie und liefert sie. Aufruf unter overlayMu.
func (s *Server) applyOverlaysLocked() []config.Member {
	m := effectiveMembers(s.cfgMembers, s.names, s.roles)
	s.membersMu.Lock()
	s.members = m
	s.membersMu.Unlock()
	return m
}

// effectiveMembers: erst die Namen, dann die Rollen (Reihenfolge und Länge
// wie cfg).
func effectiveMembers(cfg []config.Member, n config.Names, r config.Roles) []config.Member {
	return config.ApplyRoles(config.ApplyNames(cfg, n), r)
}

// dropStaleOverlaysLocked nimmt Einträge für E-Mails, die nicht mehr in der
// Mitgliederliste stehen, aus roles.json und names.json. Aufruf unter
// overlayMu.
func (s *Server) dropStaleOverlaysLocked() {
	member := map[string]bool{}
	for _, m := range s.cfgMembers {
		member[strings.ToLower(m.Email)] = true
	}
	roles := maps.Clone(s.roles)
	maps.DeleteFunc(roles, func(k string, _ api.RoleRequest) bool { return !member[k] })
	if len(roles) != len(s.roles) {
		if err := s.saveOverlayLocked(config.RolesFile, func(p string) error { return config.SaveRoles(p, roles) }); err != nil {
			s.log.Error("roles.json nicht geschrieben", "err", err)
		}
		s.log.Info("Rollen entfernter Mitglieder aus roles.json genommen", "anzahl", len(s.roles)-len(roles))
		s.roles = roles
	}
	names := maps.Clone(s.names)
	maps.DeleteFunc(names, func(k string, _ config.NameEntry) bool { return !member[k] })
	if len(names) != len(s.names) {
		if err := s.saveOverlayLocked(config.NamesFile, func(p string) error { return config.SaveNames(p, names) }); err != nil {
			s.log.Error("names.json nicht geschrieben", "err", err)
		}
		s.log.Info("Minecraft-Namen entfernter Mitglieder aus names.json genommen", "anzahl", len(s.names)-len(names))
		s.names = names
	}
}

// saveOverlayLocked schreibt die Überlagerung name in data_dir mit save. War
// sie beim Lesen kaputt, wird sie vorher als <name>.kaputt-<Zeit>
// beiseitegelegt. Aufruf unter overlayMu.
func (s *Server) saveOverlayLocked(name string, save func(path string) error) error {
	if s.cfg.DataDir == "" {
		return errors.New("kein data_dir konfiguriert")
	}
	path := filepath.Join(s.cfg.DataDir, name)
	if s.corrupt[path] {
		kaputt := path + ".kaputt-" + s.now().UTC().Format("20060102T150405Z")
		if err := os.Rename(path, kaputt); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("kaputte Überlagerung beiseitelegen: %w", err)
		}
		s.log.Warn("kaputte Überlagerung beiseitegelegt", "datei", path, "kopie", kaputt)
		delete(s.corrupt, path)
	}
	return save(path)
}

// --- Mitgliederliste und Rollen ---

// handleMembers liefert alle Mitglieder mit wirksamen Rollen und Namen (nur
// Admins); online heißt: in der Spielerliste des aktuellen Hosts.
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	if !identityOf(r).member.Admin {
		writeError(w, http.StatusForbidden, "Die Mitgliederliste sehen nur Admins")
		return
	}
	online := s.onlineNames()
	list := s.memberList()
	out := make([]api.MemberInfo, 0, len(list))
	for _, m := range list {
		out = append(out, memberInfo(m, online))
	}
	writeJSON(w, http.StatusOK, out)
}

// onlineNames: Minecraft-Namen (klein) der Spieler des aktuellen Hosts.
func (s *Server) onlineNames() map[string]bool {
	set := map[string]bool{}
	for _, p := range s.lm.Players() {
		set[strings.ToLower(p)] = true
	}
	return set
}

func memberInfo(m config.Member, online map[string]bool) api.MemberInfo {
	return api.MemberInfo{
		Email:         m.Email,
		MinecraftName: m.Minecraft,
		Admin:         m.Admin,
		Op:            m.Op,
		Online:        m.Minecraft != "" && online[strings.ToLower(m.Minecraft)],
	}
}

// handleSetRole setzt Admin- und Op-Recht eines Mitglieds (nur Admins).
func (s *Server) handleSetRole(w http.ResponseWriter, r *http.Request) {
	admin := identityOf(r).member
	if !admin.Admin {
		writeError(w, http.StatusForbidden, "Rollen ändern dürfen nur Admins")
		return
	}
	var req api.RoleRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	m, err := s.setRole(r.PathValue("email"), req, admin.Email)
	if err != nil {
		s.changeError(w, "Rolle speichern", err)
		return
	}
	writeJSON(w, http.StatusOK, memberInfo(m, s.onlineNames()))
}

// setRole setzt die Rollen des Mitglieds email in roles.json. 404 kein
// Mitglied; 409, wenn danach kein Admin übrig wäre (dann bleibt alles, wie
// es ist). Ergebnis ist das Mitglied mit den neuen Rollen.
func (s *Server) setRole(email string, req api.RoleRequest, by string) (config.Member, error) {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	i := indexMember(s.cfgMembers, email)
	if i < 0 {
		return config.Member{}, &httpError{http.StatusNotFound, "kein Mitglied " + email}
	}
	cm := s.cfgMembers[i]
	key := strings.ToLower(cm.Email)
	roles := maps.Clone(s.roles)
	// Was der Konfiguration entspricht, braucht keinen Eintrag; eine spätere
	// Änderung dort gilt dann wieder.
	if req == (api.RoleRequest{Admin: cm.Admin, Op: cm.Op}) {
		delete(roles, key)
	} else {
		roles[key] = req
	}
	after := effectiveMembers(s.cfgMembers, s.names, roles)
	if !slices.ContainsFunc(after, func(m config.Member) bool { return m.Admin }) {
		return config.Member{}, &httpError{http.StatusConflict, fmt.Sprintf(
			"%s ist der einzige Admin – erst einem anderen Mitglied das Admin-Recht geben", cm.Email)}
	}
	if maps.Equal(roles, s.roles) {
		return after[i], nil
	}
	if err := s.saveOverlayLocked(config.RolesFile, func(p string) error { return config.SaveRoles(p, roles) }); err != nil {
		return config.Member{}, err
	}
	s.roles = roles
	s.applyOverlaysLocked()
	s.membersVersion.Add(1)
	s.log.Info("Rollen geändert", "mitglied", cm.Email, "admin", req.Admin, "op", req.Op, "von", by)
	return after[i], nil
}

// indexMember sucht email in members (Groß/Klein egal); -1, wenn es fehlt.
func indexMember(members []config.Member, email string) int {
	return slices.IndexFunc(members, func(m config.Member) bool { return strings.EqualFold(m.Email, email) })
}

// changeError antwortet mit der Ablehnung aus err oder 500.
func (s *Server) changeError(w http.ResponseWriter, what string, err error) {
	if he, ok := errors.AsType[*httpError](err); ok {
		writeError(w, he.code, he.msg)
		return
	}
	s.internal(w, what, err)
}

// --- Minecraft-Namen ---

// handleSetMyMinecraft: Das Mitglied meldet seinen Minecraft-Namen, einmal,
// solange keiner eingetragen ist (sonst 409). Antwort wie GET /v1/me.
func (s *Server) handleSetMyMinecraft(w http.ResponseWriter, r *http.Request) {
	me := identityOf(r).member
	var req api.MinecraftNameRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	m, err := s.setMinecraft(r.Context(), me.Email, req.MinecraftName, "")
	if err != nil {
		s.changeError(w, "Minecraft-Name speichern", err)
		return
	}
	writeJSON(w, http.StatusOK, s.meOf(m))
}

// handleSetMemberMinecraft: Ein Admin setzt den Minecraft-Namen eines
// Mitglieds, jederzeit.
func (s *Server) handleSetMemberMinecraft(w http.ResponseWriter, r *http.Request) {
	admin := identityOf(r).member
	if !admin.Admin {
		writeError(w, http.StatusForbidden, "Minecraft-Namen anderer setzen dürfen nur Admins")
		return
	}
	var req api.MinecraftNameRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	m, err := s.setMinecraft(r.Context(), r.PathValue("email"), req.MinecraftName, admin.Email)
	if err != nil {
		s.changeError(w, "Minecraft-Name speichern", err)
		return
	}
	writeJSON(w, http.StatusOK, memberInfo(m, s.onlineNames()))
}

// setMinecraft setzt den Minecraft-Namen des Mitglieds email in names.json:
// mit admin (E-Mail des Admins) jederzeit, sonst (das Mitglied selbst) nur,
// solange keiner gilt (409). 400, wenn der Name ungültig ist, schon einem
// anderen Mitglied gehört oder Mojang ihn nicht kennt; 503, wenn Mojang
// nicht antwortet.
func (s *Server) setMinecraft(ctx context.Context, email, name, admin string) (config.Member, error) {
	name = strings.TrimSpace(name)
	self := admin == ""
	// Erst ohne Mojang prüfen, was sich prüfen lässt.
	s.overlayMu.Lock()
	_, err := s.checkMinecraftLocked(email, name, self)
	s.overlayMu.Unlock()
	if err != nil {
		return config.Member{}, err
	}
	// Mojang ohne Sperre fragen (höchstens uuidTimeout).
	rctx, cancel := context.WithTimeout(ctx, s.uuidTimeout)
	_, err = s.uuids.UUID(rctx, name)
	cancel()
	switch {
	case errors.Is(err, ErrUnknownName):
		return config.Member{}, &httpError{http.StatusBadRequest, fmt.Sprintf(
			"Mojang kennt den Minecraft-Namen %q nicht – bitte die Schreibweise prüfen", name)}
	case err != nil:
		s.log.Warn("Minecraft-Name nicht prüfbar", "minecraft", name, "err", err)
		return config.Member{}, &httpError{http.StatusServiceUnavailable,
			"Der Minecraft-Name lässt sich gerade nicht prüfen (Mojang antwortet nicht) – bitte später noch einmal"}
	}

	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	i, err := s.checkMinecraftLocked(email, name, self)
	if err != nil {
		return config.Member{}, err
	}
	cm := s.cfgMembers[i]
	key := strings.ToLower(cm.Email)
	names := maps.Clone(s.names)
	switch {
	case self:
		names[key] = config.NameEntry{Minecraft: name}
	case name == cm.Minecraft:
		delete(names, key) // wie in der Konfiguration
	default:
		names[key] = config.NameEntry{Minecraft: name, ByAdmin: true}
	}
	after := effectiveMembers(s.cfgMembers, names, s.roles)
	if maps.Equal(names, s.names) {
		return after[i], nil
	}
	if err := s.saveOverlayLocked(config.NamesFile, func(p string) error { return config.SaveNames(p, names) }); err != nil {
		return config.Member{}, err
	}
	s.names = names
	s.applyOverlaysLocked()
	s.membersVersion.Add(1)
	s.log.Info("Minecraft-Name gesetzt", "mitglied", cm.Email, "minecraft", name, "von", cmp.Or(admin, "selbst"))
	return after[i], nil
}

// checkMinecraftLocked prüft, ob name der Minecraft-Name des Mitglieds
// email werden darf, und liefert dessen Index. Aufruf unter overlayMu.
func (s *Server) checkMinecraftLocked(email, name string, self bool) (int, error) {
	i := indexMember(s.cfgMembers, email)
	if i < 0 {
		return -1, &httpError{http.StatusNotFound, "kein Mitglied " + email}
	}
	eff := s.memberList() // gleiche Reihenfolge wie cfgMembers
	if self && eff[i].Minecraft != "" {
		return -1, &httpError{http.StatusConflict, fmt.Sprintf(
			"Dein Minecraft-Name ist schon eingetragen (%s); ändern kann ihn nur ein Admin", eff[i].Minecraft)}
	}
	if !validMinecraftName(name) {
		return -1, &httpError{http.StatusBadRequest, fmt.Sprintf(
			"%q ist kein gültiger Minecraft-Name (nur Buchstaben, Ziffern und _)", name)}
	}
	for j, m := range eff {
		if j != i && strings.EqualFold(m.Minecraft, name) {
			return -1, &httpError{http.StatusBadRequest, fmt.Sprintf(
				"Der Minecraft-Name %s ist schon vergeben", name)}
		}
	}
	return i, nil
}

// meOf ist die Antwort von GET /v1/me für das Mitglied m.
func (s *Server) meOf(m config.Member) api.Me {
	me := api.Me{
		Email:            m.Email,
		MinecraftName:    m.Minecraft,
		Admin:            m.Admin,
		Op:               m.Op,
		MinecraftAddress: s.cfg.MinecraftAddress,
	}
	if m.Admin {
		me.PendingJoins = int(s.openJoins.Load())
	}
	return me
}

// --- Speicherbudget ---

// handleSetBudget setzt das Speicherbudget (nur Admins) in settings.json und
// räumt danach mit dem neuen Budget auf (im Hintergrund; die Antwort wartet
// nicht darauf).
func (s *Server) handleSetBudget(w http.ResponseWriter, r *http.Request) {
	admin := identityOf(r).member
	if !admin.Admin {
		writeError(w, http.StatusForbidden, "Das Speicherbudget dürfen nur Admins ändern")
		return
	}
	var req api.BudgetRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	if err := s.setBudget(req.BudgetBytes, admin.Email); err != nil {
		s.changeError(w, "Speicherbudget speichern", err)
		return
	}
	// Läuft weiter, auch wenn der Admin die Verbindung schließt; Wait
	// wartet beim Beenden darauf.
	s.bg.Go(func() { s.PruneTick(context.WithoutCancel(r.Context())) })
	writeJSON(w, http.StatusOK, s.storageInfo())
}

// setBudget prüft und speichert das Budget b. 400 unter MinBudget oder über
// belegt + frei. Belegt ist die Belegung der behaltenen Stände aus der
// letzten Rechnung (Aufräumlauf oder Start, store.Usage); frei der Platz
// auf dem Dateisystem von data_dir. Ist der unbekannt (außerhalb von
// Linux), gilt nur die Untergrenze.
func (s *Server) setBudget(b int64, by string) error {
	if b < MinBudget {
		return &httpError{http.StatusBadRequest, fmt.Sprintf(
			"Das Speicherbudget muss mindestens %s sein (verlangt: %s)", formatGiB(MinBudget), formatGiB(b))}
	}
	if !s.st.UsageKnown() {
		return &httpError{http.StatusServiceUnavailable, "Die Speicherbelegung wird noch berechnet, gleich nochmal"}
	}
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	used, _ := s.st.Usage()
	if free, ok := s.diskFree(s.cfg.DataDir); ok && b > used+free {
		return &httpError{http.StatusBadRequest, fmt.Sprintf(
			"Das Speicherbudget %s ist größer als belegt plus frei auf dem Server (%s + %s = %s); "+
				"belegt zählt, was die behaltenen Stände laut letzter Rechnung brauchen",
			formatGiB(b), formatGiB(used), formatGiB(free), formatGiB(used+free))}
	}
	set := config.Settings{StorageBudgetBytes: b}
	if err := s.saveOverlayLocked(config.SettingsFile, func(p string) error { return config.SaveSettings(p, set) }); err != nil {
		return err
	}
	if err := s.st.SetBudget(b); err != nil {
		return err
	}
	s.budget.Store(b)
	s.log.Info("Speicherbudget geändert", "budget", formatGiB(b), "von", by)
	return nil
}

// Wait wartet auf die Hintergrundarbeit des Servers (Aufräumen nach einem
// neuen Speicherbudget); beim Beenden nach dem Schließen des HTTP-Servers.
func (s *Server) Wait() { s.bg.Wait() }

// formatGiB schreibt n Bytes als GiB mit einer Nachkommastelle („20,0 GiB“).
func formatGiB(n int64) string {
	return strings.Replace(fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)), ".", ",", 1)
}
