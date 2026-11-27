package server

// Reiter „Server“: Whitelist, Ops und Banns (nur Admins).
//
// Eine Wahrheit: Mitglieder stehen immer auf der Whitelist, ihr Op-Recht
// steht in roles.json (wie im Reiter „Mitglieder“); Gäste (Whitelist ohne
// Mitgliedschaft, optional Op) und Banns stehen in data_dir/serverlisten.json.
// Jede Änderung erhöht MembersVersion; der laufende Host holt dann die
// Host-Einstellungen neu und gleicht seinen Server an, ein späterer Host
// startet gleich mit den neuen Listen.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/serverbefehl"
)

// Texte der Rückmeldung.
const (
	textLaeuft      = " Der laufende Server übernimmt das in wenigen Sekunden."
	textLaeuftNicht = " Läuft gerade nicht – gespeichert für den nächsten Start."
)

// readServerListsLocked liest serverlisten.json (neu). Ist sie kaputt,
// gelten die zuletzt gültigen Listen weiter (beim Start: keine) – ein
// kaputter Bann soll nicht alle Banns aufheben, ohne dass es laut wird.
// Aufruf unter overlayMu.
func (s *Server) readServerListsLocked() {
	if s.cfg.DataDir == "" {
		return
	}
	path := filepath.Join(s.cfg.DataDir, config.ServerListsFile)
	l, err := config.LoadServerLists(path)
	if err != nil {
		s.overlayBroken(path, err)
		s.log.Error("SERVERLISTEN.JSON KAPUTT – die zuletzt gültigen Gäste und Banns bleiben (beim Start: keine); Datei berichtigen und neu laden")
		return
	}
	s.lists = l
	delete(s.corrupt, path)
}

// serverLists liefert eine Kopie von Gästen und Banns.
func (s *Server) serverLists() config.ServerLists {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	return config.ServerLists{Guests: slices.Clone(s.lists.Guests), Bans: slices.Clone(s.lists.Bans)}
}

// serverRunning: Ein Host hat die Welt gestartet und ist bereit.
func (s *Server) serverRunning() bool {
	info, ok := s.lm.Info()
	return ok && info.Ready && !info.UploadOnly
}

// adminOf antwortet Nicht-Admins mit 403 und liefert sonst den Admin.
func adminOf(w http.ResponseWriter, r *http.Request, was string) (config.Member, bool) {
	m := identityOf(r).member
	if !m.Admin {
		writeError(w, http.StatusForbidden, was+" dürfen nur Admins")
		return config.Member{}, false
	}
	return m, true
}

// handleServerLists liefert Whitelist, Ops und Banns (nur Admins).
func (s *Server) handleServerLists(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminOf(w, r, "Whitelist, Ops und Banns sehen"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.listView(r.Context()))
}

// listView baut die Listen fürs Fenster: Mitglieder (UUID aus dem
// Zwischenspeicher des Resolvers, "" wenn nicht auflösbar), dann Gäste.
func (s *Server) listView(ctx context.Context) api.ServerLists {
	members := slices.DeleteFunc(slices.Clone(s.memberList()), func(m config.Member) bool { return m.Minecraft == "" })
	uuids := s.resolveMembers(ctx, members)
	l := s.serverLists()
	out := api.ServerLists{Whitelist: []api.ListPlayer{}, Ops: []api.ListPlayer{}, Bans: []api.ListBan{}, Running: s.serverRunning()}
	isMember := map[string]bool{}
	for i, m := range members {
		p := api.ListPlayer{Name: m.Minecraft, UUID: uuids[i], Member: true}
		isMember[strings.ToLower(m.Minecraft)] = true
		if uuids[i] != "" {
			isMember[uuids[i]] = true
		}
		out.Whitelist = append(out.Whitelist, p)
		if m.Op {
			out.Ops = append(out.Ops, p)
		}
	}
	for _, g := range l.Guests {
		p := api.ListPlayer{Name: g.Name, UUID: g.UUID}
		out.Whitelist = append(out.Whitelist, p)
		if g.Op {
			out.Ops = append(out.Ops, p)
		}
	}
	for _, b := range l.Bans {
		out.Bans = append(out.Bans, api.ListBan{
			Name: b.Name, UUID: b.UUID, By: s.adminName(b.By), Since: b.Added,
			Member: isMember[b.UUID] || isMember[strings.ToLower(b.Name)],
		})
	}
	return out
}

// adminName: Minecraft-Name des Mitglieds email, sonst die E-Mail.
func (s *Server) adminName(email string) string {
	if m, ok := s.member(email); ok && m.Minecraft != "" {
		return m.Minecraft
	}
	return email
}

// memberByName sucht ein Mitglied über den Minecraft-Namen (Groß/Klein egal).
func (s *Server) memberByName(name string) (config.Member, bool) {
	for _, m := range s.memberList() {
		if m.Minecraft != "" && strings.EqualFold(m.Minecraft, name) {
			return m, true
		}
	}
	return config.Member{}, false
}

// handleServerListChange ändert Whitelist, Ops oder Banns (nur Admins).
func (s *Server) handleServerListChange(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminOf(w, r, "Whitelist, Ops und Banns ändern")
	if !ok {
		return
	}
	var req api.ServerListChange
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	text, err := s.changeServerList(r.Context(), req, admin)
	if err != nil {
		s.log.Info("Serverliste nicht geändert", "liste", req.List, "aktion", req.Action, "spieler", req.Name,
			"admin", admin.Email, "admin_minecraft", admin.Minecraft, "grund", err.Error())
		s.changeError(w, "Serverliste ändern", err)
		return
	}
	s.log.Info("Serverliste geändert", "liste", req.List, "aktion", req.Action, "spieler", req.Name,
		"admin", admin.Email, "admin_minecraft", admin.Minecraft)
	if s.serverRunning() {
		text += textLaeuft
	} else {
		text += textLaeuftNicht
	}
	writeJSON(w, http.StatusOK, api.ServerListResult{Text: text, Lists: s.listView(r.Context())})
}

func badRequest(format string, a ...any) error {
	return &httpError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

func conflict(format string, a ...any) error {
	return &httpError{http.StatusConflict, fmt.Sprintf(format, a...)}
}

// changeServerList führt die Änderung aus und liefert den Text dazu.
func (s *Server) changeServerList(ctx context.Context, req api.ServerListChange, admin config.Member) (string, error) {
	name := req.Name
	// Erst die Zeichen: nur dann geht der Name weiter (Log, Mojang, Befehle).
	if !config.ValidMinecraftName(name) {
		return "", badRequest("„%s“ ist kein gültiger Minecraft-Name (3 bis 16 Buchstaben, Ziffern oder _)", shortName(name))
	}
	switch req.List + "/" + req.Action {
	case api.ListWhitelist + "/" + api.ListAdd:
		return s.addGuest(ctx, name, admin)
	case api.ListWhitelist + "/" + api.ListRemove:
		return s.removeGuest(name)
	case api.ListOps + "/" + api.ListAdd:
		return s.setOp(name, true, admin)
	case api.ListOps + "/" + api.ListRemove:
		return s.setOp(name, false, admin)
	case api.ListBans + "/" + api.ListAdd:
		return s.addBan(ctx, name, admin)
	case api.ListBans + "/" + api.ListRemove:
		return s.removeBan(name)
	}
	return "", badRequest("unbekannte Änderung %q/%q", shortName(req.List), shortName(req.Action))
}

// shortName kürzt einen ungeprüften Wert für eine Meldung.
func shortName(s string) string {
	s = strings.ToValidUTF8(s, "?")
	if r := []rune(s); len(r) > 32 {
		return string(r[:32]) + "…"
	}
	return s
}

// resolveNew prüft einen neuen Namen (heutige Regeln) und löst ihn bei
// Mojang auf (ohne Sperre).
func (s *Server) resolveNew(ctx context.Context, name string, strict bool) (string, error) {
	if strict && !serverbefehl.GueltigerName(name) {
		return "", badRequest("„%s“ ist kein gültiger Minecraft-Name (3 bis 16 Buchstaben, Ziffern oder _)", name)
	}
	rctx, cancel := context.WithTimeout(ctx, s.uuidTimeout)
	uuid, err := s.uuids.UUID(rctx, name)
	cancel()
	switch {
	case errors.Is(err, ErrUnknownName):
		return "", badRequest("Mojang kennt den Minecraft-Namen %q nicht – bitte die Schreibweise prüfen", name)
	case err != nil:
		s.log.Warn("Minecraft-Name nicht prüfbar", "minecraft", name, "err", err)
		return "", &httpError{http.StatusServiceUnavailable,
			"Der Minecraft-Name lässt sich gerade nicht prüfen (Mojang antwortet nicht) – bitte später noch einmal"}
	}
	return uuid, nil
}

// saveListsLocked schreibt l, übernimmt es und erhöht MembersVersion.
// Aufruf unter overlayMu.
func (s *Server) saveListsLocked(l config.ServerLists) error {
	if err := s.saveOverlayLocked(config.ServerListsFile, func(p string) error { return config.SaveServerLists(p, l) }); err != nil {
		return err
	}
	s.lists = l
	s.membersVersion.Add(1)
	return nil
}

func guestIndex(gs []config.Guest, name, uuid string) int {
	return slices.IndexFunc(gs, func(g config.Guest) bool {
		return strings.EqualFold(g.Name, name) || (uuid != "" && g.UUID == uuid)
	})
}

func banIndex(bs []config.Ban, name, uuid string) int {
	return slices.IndexFunc(bs, func(b config.Ban) bool {
		return strings.EqualFold(b.Name, name) || (uuid != "" && b.UUID == uuid)
	})
}

func (s *Server) addGuest(ctx context.Context, name string, admin config.Member) (string, error) {
	if m, ok := s.memberByName(name); ok {
		return "", conflict("%s ist Mitglied und steht damit schon auf der Whitelist", m.Minecraft)
	}
	pre := s.serverLists()
	if guestIndex(pre.Guests, name, "") >= 0 {
		return "", conflict("%s steht schon auf der Whitelist", name)
	}
	if banIndex(pre.Bans, name, "") >= 0 {
		return "", conflict("%s ist gebannt – erst den Bann aufheben", name)
	}
	uuid, err := s.resolveNew(ctx, name, true)
	if err != nil {
		return "", err
	}
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	l := config.ServerLists{Guests: slices.Clone(s.lists.Guests), Bans: slices.Clone(s.lists.Bans)}
	switch {
	case guestIndex(l.Guests, name, uuid) >= 0:
		return "", conflict("%s steht schon auf der Whitelist", name)
	case banIndex(l.Bans, name, uuid) >= 0:
		return "", conflict("%s ist gebannt – erst den Bann aufheben", name)
	case len(l.Guests) >= config.MaxGuests:
		return "", conflict("Höchstens %d Gäste – erst einen entfernen", config.MaxGuests)
	}
	l.Guests = append(l.Guests, config.Guest{Name: name, UUID: uuid, By: admin.Email, Added: s.now().UTC()})
	if err := s.saveListsLocked(l); err != nil {
		return "", err
	}
	return name + " steht jetzt als Gast auf der Whitelist.", nil
}

func (s *Server) removeGuest(name string) (string, error) {
	if m, ok := s.memberByName(name); ok {
		return "", conflict("%s ist Mitglied – Mitglieder stehen immer auf der Whitelist (entfernen nur im Reiter „Mitglieder“)", m.Minecraft)
	}
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	i := guestIndex(s.lists.Guests, name, "")
	if i < 0 {
		return "", &httpError{http.StatusNotFound, name + " steht nicht auf der Whitelist"}
	}
	g := s.lists.Guests[i]
	l := config.ServerLists{Guests: slices.Delete(slices.Clone(s.lists.Guests), i, i+1), Bans: slices.Clone(s.lists.Bans)}
	if err := s.saveListsLocked(l); err != nil {
		return "", err
	}
	if g.Op {
		return g.Name + " steht nicht mehr auf der Whitelist und ist kein Op mehr.", nil
	}
	return g.Name + " steht nicht mehr auf der Whitelist.", nil
}

// setOp setzt oder nimmt Op: bei Mitgliedern über roles.json (wie im Reiter
// „Mitglieder“), bei Gästen in serverlisten.json. Wer nicht auf der
// Whitelist steht, wird kein Op (Ops kämen an der Whitelist vorbei).
func (s *Server) setOp(name string, op bool, admin config.Member) (string, error) {
	if m, ok := s.memberByName(name); ok {
		if m.Op == op {
			if op {
				return "", conflict("%s ist schon Op", m.Minecraft)
			}
			return "", conflict("%s ist kein Op", m.Minecraft)
		}
		if _, err := s.setRole(m.Email, api.RoleRequest{Admin: m.Admin, Op: op}, admin.Email); err != nil {
			return "", err
		}
		if op {
			return m.Minecraft + " (Mitglied) ist jetzt Op.", nil
		}
		return m.Minecraft + " (Mitglied) ist kein Op mehr.", nil
	}
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	i := guestIndex(s.lists.Guests, name, "")
	switch {
	case i < 0 && op:
		return "", conflict("%s steht nicht auf der Whitelist – erst als Gast eintragen (Ops kämen sonst an der Whitelist vorbei)", name)
	case i < 0:
		return "", &httpError{http.StatusNotFound, name + " ist kein Op"}
	case s.lists.Guests[i].Op == op && op:
		return "", conflict("%s ist schon Op", s.lists.Guests[i].Name)
	case s.lists.Guests[i].Op == op:
		return "", conflict("%s ist kein Op", s.lists.Guests[i].Name)
	}
	l := config.ServerLists{Guests: slices.Clone(s.lists.Guests), Bans: slices.Clone(s.lists.Bans)}
	l.Guests[i].Op = op
	if err := s.saveListsLocked(l); err != nil {
		return "", err
	}
	if op {
		return l.Guests[i].Name + " (Gast) ist jetzt Op.", nil
	}
	return l.Guests[i].Name + " (Gast) ist kein Op mehr.", nil
}

func (s *Server) addBan(ctx context.Context, name string, admin config.Member) (string, error) {
	if admin.Minecraft != "" && strings.EqualFold(admin.Minecraft, name) {
		return "", badRequest("Dich selbst kannst du nicht bannen")
	}
	m, member := s.memberByName(name)
	if member {
		name = m.Minecraft
	}
	if banIndex(s.serverLists().Bans, name, "") >= 0 {
		return "", conflict("%s ist schon gebannt", name)
	}
	// Mitglieder dürfen einen älteren Namen haben (config.ValidMinecraftName);
	// alle anderen nur einen heutigen.
	uuid, err := s.resolveNew(ctx, name, !member)
	if err != nil {
		return "", err
	}
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	l := config.ServerLists{Guests: slices.Clone(s.lists.Guests), Bans: slices.Clone(s.lists.Bans)}
	switch {
	case banIndex(l.Bans, name, uuid) >= 0:
		return "", conflict("%s ist schon gebannt", name)
	case len(l.Bans) >= config.MaxBans:
		return "", conflict("Höchstens %d Banns – erst einen aufheben", config.MaxBans)
	}
	text := name + " ist gebannt."
	if i := guestIndex(l.Guests, name, uuid); i >= 0 {
		l.Guests = slices.Delete(l.Guests, i, i+1)
		text += " Steht nicht mehr als Gast auf der Whitelist."
	}
	if member {
		text += " Bleibt Mitglied, kommt aber nicht mehr auf den Server."
	}
	l.Bans = append(l.Bans, config.Ban{Name: name, UUID: uuid, By: admin.Email, Added: s.now().UTC()})
	if err := s.saveListsLocked(l); err != nil {
		return "", err
	}
	return text, nil
}

func (s *Server) removeBan(name string) (string, error) {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	i := banIndex(s.lists.Bans, name, "")
	if i < 0 {
		return "", &httpError{http.StatusNotFound, name + " ist nicht gebannt"}
	}
	b := s.lists.Bans[i]
	l := config.ServerLists{Guests: slices.Clone(s.lists.Guests), Bans: slices.Delete(slices.Clone(s.lists.Bans), i, i+1)}
	if err := s.saveListsLocked(l); err != nil {
		return "", err
	}
	return "Der Bann für " + b.Name + " ist aufgehoben.", nil
}

// addListsToSettings ergänzt die Host-Einstellungen um Gäste (Whitelist,
// Ops) und Banns; doppelte UUIDs (ein Gast, der zugleich Mitglied ist)
// kommen nur einmal hinein.
func (s *Server) addListsToSettings(hs *api.HostSettings) {
	l := s.serverLists()
	has := func(ps []api.Player, uuid string) bool {
		return slices.ContainsFunc(ps, func(p api.Player) bool { return strings.EqualFold(p.UUID, uuid) })
	}
	for _, g := range l.Guests {
		p := api.Player{UUID: g.UUID, Name: g.Name}
		if !has(hs.Whitelist, g.UUID) {
			hs.Whitelist = append(hs.Whitelist, p)
		}
		if g.Op && !has(hs.Ops, g.UUID) {
			hs.Ops = append(hs.Ops, p)
		}
	}
	for _, b := range l.Bans {
		if !has(hs.Bans, b.UUID) {
			hs.Bans = append(hs.Bans, api.Player{UUID: b.UUID, Name: b.Name})
		}
	}
}
