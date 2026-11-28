package server

// Beitrittsanfragen. Ein Konto, das tunneld kennt, aber nicht in der
// Mitgliederliste steht (identity.guest), darf nur POST und GET /v1/join:
// Es nennt seinen Minecraft-Namen (von Mojang geprüft) und fragt seinen
// Stand ab. Admins sehen die offenen Anfragen (GET /v1/join-requests) und
// nehmen sie an oder lehnen sie ab (POST /v1/join-requests/{email}/accept
// bzw. /decline).
//
// Grenzen: höchstens eine offene Anfrage je Konto, höchstens
// api.MaxOpenJoins offene insgesamt (sonst 429), nach einer Ablehnung
// frühestens nach api.JoinRetryAfter eine neue (429). Ein Minecraft-Name,
// der schon einem Mitglied gehört oder in einer offenen Anfrage steht, wird
// abgelehnt (409).
//
// Anfragen stehen in joins.json, angenommene Mitglieder in members.json
// (config/joins.go), beide in data_dir und damit über einen Neustart
// hinweg. Annehmen erhöht MembersVersion; der Host nimmt den Spieler dann in
// die Whitelist auf (Lebenszeichen), ohne Neustart.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/kennung"
)

// handleJoin nimmt die Beitrittsanfrage eines Nicht-Mitglieds entgegen.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	if !id.guest {
		writeError(w, http.StatusConflict, "Du bist schon Mitglied der Gruppe")
		return
	}
	var req api.JoinRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	e, err := s.requestJoin(r.Context(), id.member.Email, req.MinecraftName)
	if err != nil {
		s.changeError(w, "Beitrittsanfrage speichern", err)
		return
	}
	writeJSON(w, http.StatusCreated, api.JoinStatus{State: api.JoinOpen, Since: e.Requested})
}

// requestJoin legt die Anfrage von email mit dem Minecraft-Namen name an.
func (s *Server) requestJoin(ctx context.Context, email, name string) (config.JoinEntry, error) {
	name = strings.TrimSpace(name)
	// Erst ohne Mojang prüfen, was sich prüfen lässt.
	s.overlayMu.Lock()
	err := s.checkJoinLocked(email, name)
	s.overlayMu.Unlock()
	if err != nil {
		return config.JoinEntry{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.uuidTimeout)
	_, err = s.uuids.UUID(rctx, name)
	cancel()
	switch {
	case errors.Is(err, ErrUnknownName):
		return config.JoinEntry{}, &httpError{http.StatusBadRequest, fmt.Sprintf(
			"Mojang kennt den Minecraft-Namen %q nicht – bitte die Schreibweise prüfen", name)}
	case err != nil:
		s.log.Warn("Minecraft-Name nicht prüfbar", "minecraft", name, "err", err)
		return config.JoinEntry{}, &httpError{http.StatusServiceUnavailable,
			"Der Minecraft-Name lässt sich gerade nicht prüfen (Mojang antwortet nicht) – bitte später noch einmal"}
	}

	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	if err := s.checkJoinLocked(email, name); err != nil {
		return config.JoinEntry{}, err
	}
	e := config.JoinEntry{Email: email, Minecraft: name, Requested: s.now().UTC(), State: config.JoinStateOpen}
	if err := s.putJoinLocked(e); err != nil {
		return config.JoinEntry{}, err
	}
	s.log.Info("Beitrittsanfrage", "konto", email, "minecraft", name)
	return e, nil
}

// checkJoinLocked prüft, ob email mit dem Namen name anfragen darf.
// Aufruf unter overlayMu.
func (s *Server) checkJoinLocked(email, name string) error {
	if _, ok := s.member(email); ok {
		return &httpError{http.StatusConflict, "Du bist schon Mitglied der Gruppe"}
	}
	if !validMinecraftName(name) {
		return &httpError{http.StatusBadRequest, fmt.Sprintf(
			"%q ist kein gültiger Minecraft-Name (nur Buchstaben, Ziffern und _)", name)}
	}
	key := strings.ToLower(email)
	if e, ok := s.joins[key]; ok {
		switch e.State {
		case config.JoinStateOpen:
			return &httpError{http.StatusTooManyRequests,
				"Du hast schon eine offene Beitrittsanfrage – bitte warten, bis ein Admin sie bearbeitet"}
		case config.JoinStateDeclined:
			if wait := e.Decided.Add(api.JoinRetryAfter).Sub(s.now()); wait > 0 {
				return &httpError{http.StatusTooManyRequests, fmt.Sprintf(
					"Deine letzte Anfrage wurde abgelehnt; eine neue ist erst in etwa %d Stunden möglich",
					int((wait+time.Hour-1)/time.Hour))}
			}
		}
	}
	if s.countOpenJoinsLocked() >= api.MaxOpenJoins {
		return &httpError{http.StatusTooManyRequests,
			"Es liegen gerade zu viele Beitrittsanfragen vor – bitte später noch einmal"}
	}
	if m := s.nameOwnerLocked(name, key); m != "" {
		return &httpError{http.StatusConflict, fmt.Sprintf(
			"Der Minecraft-Name %s gehört schon einem Mitglied oder ist schon angefragt", name)}
	}
	return nil
}

// nameOwnerLocked liefert die E-Mail, der der Minecraft-Name name (Groß/Klein
// egal) gehört oder für die er angefragt ist, außer dem Konto except (klein);
// "" wenn er frei ist. Aufruf unter overlayMu.
func (s *Server) nameOwnerLocked(name, except string) string {
	for _, m := range s.memberList() {
		if strings.EqualFold(m.Minecraft, name) {
			return m.Email
		}
	}
	for k, e := range s.joins {
		if k != except && e.State == config.JoinStateOpen && strings.EqualFold(e.Minecraft, name) &&
			indexMember(s.cfgMembers, e.Email) < 0 {
			return e.Email
		}
	}
	return ""
}

// countOpenJoinsLocked zählt die offenen Anfragen (ohne die von Konten, die
// inzwischen Mitglied sind). Aufruf unter overlayMu.
func (s *Server) countOpenJoinsLocked() int {
	return len(s.openJoinsLocked())
}

// openJoinsLocked liefert die offenen Anfragen, älteste zuerst.
func (s *Server) openJoinsLocked() []config.JoinEntry {
	var out []config.JoinEntry
	for _, e := range s.joins {
		if e.State == config.JoinStateOpen && indexMember(s.cfgMembers, e.Email) < 0 {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b config.JoinEntry) int {
		if c := a.Requested.Compare(b.Requested); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email))
	})
	return out
}

// putJoinLocked setzt oder ersetzt die Anfrage e und schreibt joins.json. Aufruf unter overlayMu.
func (s *Server) putJoinLocked(e config.JoinEntry) error {
	joins := maps.Clone(s.joins)
	joins[strings.ToLower(e.Email)] = e
	return s.storeJoinsLocked(joins)
}

// storeJoinsLocked schreibt joins und übernimmt sie. Aufruf unter overlayMu.
func (s *Server) storeJoinsLocked(joins config.JoinEntries) error {
	joins = config.PruneDeclined(joins, s.now())
	if err := s.saveOverlayLocked(config.JoinsFile, func(p string) error { return config.SaveJoins(p, joins) }); err != nil {
		return err
	}
	s.joins = joins
	s.openJoins.Store(int64(s.countOpenJoinsLocked()))
	return nil
}

// handleInfo liefert die öffentliche Gruppenbeschreibung (GET /v1/info) für
// jedes gültige Konto, auch für Nicht-Mitglieder.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	info := api.GroupInfo{
		Title:       s.cfg.Gruppe.Titel,
		Description: s.cfg.Gruppe.Beschreibung,
		Members:     len(s.memberList()),
		You:         api.YouMember,
	}
	if id.guest {
		info.You = api.YouNone
		s.overlayMu.Lock()
		if e, ok := s.joins[strings.ToLower(id.member.Email)]; ok && e.State == config.JoinStateOpen {
			info.You = api.YouRequested
		}
		s.overlayMu.Unlock()
	}
	writeJSON(w, http.StatusOK, info)
}

// handleJoinStatus liefert den Stand der eigenen Anfrage.
func (s *Server) handleJoinStatus(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	key := strings.ToLower(id.member.Email)
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	if !id.guest {
		writeJSON(w, http.StatusOK, api.JoinStatus{State: api.JoinAccepted, Since: s.joined[key].At})
		return
	}
	st := api.JoinStatus{State: api.JoinNone}
	if e, ok := s.joins[key]; ok {
		switch e.State {
		case config.JoinStateOpen:
			st = api.JoinStatus{State: api.JoinOpen, Since: e.Requested}
		case config.JoinStateDeclined:
			st = api.JoinStatus{State: api.JoinDeclined, Since: e.Decided}
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleJoinRequests liefert die offenen Anfragen (nur Admins).
func (s *Server) handleJoinRequests(w http.ResponseWriter, r *http.Request) {
	if !identityOf(r).member.Admin {
		writeError(w, http.StatusForbidden, "Die Beitrittsanfragen sehen nur Admins")
		return
	}
	s.overlayMu.Lock()
	open := s.openJoinsLocked()
	s.overlayMu.Unlock()
	out := make([]api.JoinRequestInfo, 0, len(open))
	for _, e := range open {
		id := s.id(e.Email)
		// Email trägt die Kennung (nie die Adresse): nur für ältere Starter.
		out = append(out, api.JoinRequestInfo{Email: id, ID: id, MinecraftName: e.Minecraft, Requested: e.Requested})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleJoinAccept nimmt die Anfrage an (nur Admins): Das Konto wird
// Mitglied, MembersVersion steigt.
func (s *Server) handleJoinAccept(w http.ResponseWriter, r *http.Request) {
	admin := identityOf(r).member
	if !admin.Admin {
		writeError(w, http.StatusForbidden, "Beitrittsanfragen annehmen dürfen nur Admins")
		return
	}
	key := r.PathValue("email")
	if !pfadSchluessel(key, admin.Email) {
		writeError(w, http.StatusNotFound, "keine offene Beitrittsanfrage mit dieser Kennung")
		return
	}
	if err := s.acceptJoin(key, admin.Email); err != nil {
		s.changeError(w, "Beitrittsanfrage annehmen", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleJoinDecline lehnt die Anfrage ab (nur Admins).
func (s *Server) handleJoinDecline(w http.ResponseWriter, r *http.Request) {
	admin := identityOf(r).member
	if !admin.Admin {
		writeError(w, http.StatusForbidden, "Beitrittsanfragen ablehnen dürfen nur Admins")
		return
	}
	key := r.PathValue("email")
	if !pfadSchluessel(key, admin.Email) {
		writeError(w, http.StatusNotFound, "keine offene Beitrittsanfrage mit dieser Kennung")
		return
	}
	if err := s.declineJoin(key, admin.Email); err != nil {
		s.changeError(w, "Beitrittsanfrage ablehnen", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// openJoinLocked liefert die offene Anfrage von email; 404, wenn es keine
// gibt. Aufruf unter overlayMu.
func (s *Server) openJoinLocked(email string) (config.JoinEntry, error) {
	e, ok := s.joins[strings.ToLower(email)]
	if !ok && kennung.IstID(email) {
		for _, c := range s.joins {
			if s.id(c.Email) == email {
				e, ok = c, true
				break
			}
		}
	}
	if !ok || e.State != config.JoinStateOpen {
		return config.JoinEntry{}, &httpError{http.StatusNotFound, "keine offene Beitrittsanfrage mit dieser Kennung"}
	}
	return e, nil
}

func (s *Server) acceptJoin(email, by string) error {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	e, err := s.openJoinLocked(email)
	if err != nil {
		return err
	}
	key := strings.ToLower(e.Email)
	if indexMember(s.cfgMembers, e.Email) >= 0 {
		return &httpError{http.StatusConflict, cmp.Or(e.Minecraft, "Mitglied "+s.id(e.Email)) + " ist schon Mitglied"}
	}
	for _, m := range s.memberList() {
		if strings.EqualFold(m.Minecraft, e.Minecraft) {
			return &httpError{http.StatusConflict, fmt.Sprintf(
				"Der Minecraft-Name %s gehört inzwischen %s – die Anfrage besser ablehnen", e.Minecraft, s.who(m))}
		}
	}
	// Erst Mitglied machen, dann die Anfrage streichen: Bleibt das Streichen
	// stehen, zählt die Anfrage eines Mitglieds nicht mehr (openJoinsLocked).
	joined := maps.Clone(s.joined)
	joined[key] = config.Joined{Email: e.Email, Minecraft: e.Minecraft, AcceptedBy: by, At: s.now().UTC()}
	if err := s.saveOverlayLocked(config.MembersFile, func(p string) error { return config.SaveJoined(p, joined) }); err != nil {
		return err
	}
	s.joined = joined
	s.cfgMembers = config.MergeJoined(s.confMembers, s.joined)
	s.applyOverlaysLocked()
	s.membersVersion.Add(1)
	joins := maps.Clone(s.joins)
	delete(joins, key)
	if err := s.storeJoinsLocked(joins); err != nil {
		s.joins = joins
		s.openJoins.Store(int64(s.countOpenJoinsLocked()))
		s.log.Error("angenommene Anfrage nicht aus joins.json gestrichen", "konto", e.Email, "err", err)
	}
	s.log.Info("Beitritt angenommen", "mitglied", e.Email, "minecraft", e.Minecraft, "von", by)
	return nil
}

func (s *Server) declineJoin(email, by string) error {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()
	e, err := s.openJoinLocked(email)
	if err != nil {
		return err
	}
	e.State, e.Decided, e.DecidedBy = config.JoinStateDeclined, s.now().UTC(), by
	if err := s.putJoinLocked(e); err != nil {
		return err
	}
	s.log.Info("Beitritt abgelehnt", "konto", e.Email, "minecraft", e.Minecraft, "von", by)
	return nil
}
