package server

// Mehrere Welten pro Gruppe: Weltliste und ihre Verwaltung.
//
// GET /v1/worlds zeigt jedem Mitglied die Welten der Gruppe und welche
// gerade läuft. Anlegen (POST), Ändern (PATCH) und Löschen (DELETE) dürfen
// nur Admins. Gelöscht wird nur, was nicht läuft (kein Zuschlag, kein
// offenes Sammelfenster, kein wartender Zuschlag, kein ausstehendes
// Zurückholen) und nicht die letzte Welt; die Bestätigung ist der Name der
// Welt (?confirm=). Die Stände einer gelöschten Welt bleiben liegen, bis das
// Aufräumen sie unter Platzdruck als Erste wegnimmt (store.Prune).
//
// Es läuft immer nur eine Welt: Der Zuschlag trägt die Welt (lease.World);
// der Koordinator löst „keine Welt angegeben“ zur ersten Welt der Liste auf.

import (
	"context"
	"errors"
	"net/http"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

// normWorld löst die Angabe „keine Welt“ ("") zur ersten Welt der Liste auf
// (""), wenn es noch keine gibt).
func (s *Server) normWorld(id string) string {
	if id != "" {
		return id
	}
	if e, ok := s.st.GroupWorld(""); ok {
		return e.ID
	}
	return ""
}

// runningWorld liefert die Welt des gültigen Zuschlags (auch eines nur zum
// Hochladen); "" wenn keiner gilt.
func (s *Server) runningWorld() string {
	info, ok := s.lm.Info()
	if !ok {
		return ""
	}
	return s.normWorld(info.World)
}

// playWorld löst die gewünschte Welt einer Spielanfrage auf: ohne Angabe die
// erste Welt, ohne jede Welt "" (frische Gruppe). reason ist nicht leer, wenn
// es die Welt nicht (mehr) gibt.
func (s *Server) playWorld(want string) (world, reason string) {
	entries := s.st.GroupWorlds()
	switch {
	case len(entries) == 0:
		return "", ""
	case want == "":
		return entries[0].ID, ""
	}
	for _, e := range entries {
		if e.ID == want {
			return want, ""
		}
	}
	return "", "Die gewählte Welt gibt es nicht (mehr) – bitte im Fenster eine andere Welt wählen."
}

// worldList baut die Weltliste für Status und GET /v1/worlds; running ist die
// Kennung der laufenden Welt.
func (s *Server) worldList(running string) []api.GroupWorld {
	entries := s.st.GroupWorlds()
	if len(entries) == 0 {
		return nil
	}
	out := make([]api.GroupWorld, 0, len(entries))
	for _, e := range entries {
		_, n, created := s.st.HeadInfoOf(e.ID)
		out = append(out, api.GroupWorld{
			ID: e.ID, Name: e.Name, Gamemode: e.Gamemode, Difficulty: e.Difficulty,
			HeadGeneration: n, HeadCreated: created, Empty: e.Empty(), Running: e.ID == running,
		})
	}
	return out
}

func (s *Server) handleWorlds(w http.ResponseWriter, r *http.Request) {
	running := s.runningWorld()
	list := s.worldList(running)
	if list == nil {
		list = []api.GroupWorld{}
	}
	writeJSON(w, http.StatusOK, api.WorldList{Worlds: list, Running: running})
}

// requireAdmin antwortet mit 403 und liefert false, wenn die Anfrage nicht
// von einem Admin kommt.
func requireAdmin(w http.ResponseWriter, r *http.Request, what string) bool {
	if !identityOf(r).member.Admin {
		writeError(w, http.StatusForbidden, what+" dürfen nur Admins")
		return false
	}
	return true
}

func (s *Server) groupWorld(id string) api.GroupWorld {
	for _, g := range s.worldList(s.runningWorld()) {
		if g.ID == id {
			return g
		}
	}
	return api.GroupWorld{ID: id}
}

func (s *Server) handleWorldCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "Welten anlegen") {
		return
	}
	var req api.WorldCreate
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	e, err := s.st.AddGroupWorld(req.Name, req.Gamemode, req.Difficulty, s.now())
	if err != nil {
		s.storeError(w, "Welt anlegen", err)
		return
	}
	s.log.Info("Welt angelegt", "welt", e.ID, "name", e.Name, "admin", identityOf(r).member.Email)
	writeJSON(w, http.StatusCreated, s.groupWorld(e.ID))
}

func (s *Server) handleWorldPatch(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "Welten ändern") {
		return
	}
	var req api.WorldPatch
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	id := r.PathValue("id")
	e, err := s.st.UpdateGroupWorld(id, store.WorldPatch{Name: req.Name, Gamemode: req.Gamemode, Difficulty: req.Difficulty})
	if err != nil {
		s.storeError(w, "Welt ändern", err)
		return
	}
	s.log.Info("Welt geändert", "welt", e.ID, "name", e.Name, "spielmodus", e.Gamemode, "schwierigkeit", e.Difficulty,
		"admin", identityOf(r).member.Email)
	writeJSON(w, http.StatusOK, s.groupWorld(e.ID))
}

// worldBusy meldet, warum die Welt id gerade nicht gelöscht werden darf
// ("" = sie ist frei). Aufruf unter phMu.
func (s *Server) worldBusy(id string) string {
	for _, bw := range s.lm.BusyWorlds() {
		if s.normWorld(bw) == id {
			return "Die Welt läuft gerade (oder wird gerade gestartet) – erst nach dem Ende des Hostens löschen."
		}
	}
	if p := s.pending.Load(); p != nil {
		if o, _, ok := s.st.OwnerOfStore(p.notice.Target.WorldID); ok && o.ID == id {
			return "Für diese Welt steht ein Zurückholen aus – erst danach löschen."
		}
	}
	return ""
}

func (s *Server) handleWorldDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, "Welten löschen") {
		return
	}
	id := r.PathValue("id")
	e, ok := s.st.GroupWorld(id)
	if !ok || id == "" {
		writeError(w, http.StatusNotFound, "diese Welt gibt es nicht (mehr)")
		return
	}
	if r.URL.Query().Get("confirm") != e.Name {
		writeError(w, http.StatusBadRequest, "Zur Bestätigung muss ?confirm= den Namen der Welt „"+e.Name+"“ nennen")
		return
	}
	err := func() error {
		s.phMu.Lock()
		defer s.phMu.Unlock()
		s.settleRestoreLocked(context.WithoutCancel(r.Context()))
		s.handoverStepLocked(context.WithoutCancel(r.Context()))
		if why := s.worldBusy(id); why != "" {
			return &httpError{http.StatusConflict, why}
		}
		return s.st.DeleteGroupWorld(id, s.now())
	}()
	if err != nil {
		if he, ok := errors.AsType[*httpError](err); ok {
			writeError(w, he.code, he.msg)
			return
		}
		if errors.Is(err, store.ErrLastWorld) {
			writeError(w, http.StatusConflict, "Die letzte Welt der Gruppe lässt sich nicht löschen.")
			return
		}
		s.storeError(w, "Welt löschen", err)
		return
	}
	s.log.Warn("Welt gelöscht (Stände bleiben bis zum Aufräumen unter Platzdruck)", "welt", id, "name", e.Name,
		"weltkennung", e.StoreWorld, "admin", identityOf(r).member.Email)
	w.WriteHeader(http.StatusNoContent)
}
