package app

// Mehrere Welten pro Gruppe im Starter.
//
// Welche Welt „Spielen“ startet: die laufende (hostet schon jemand, spielt
// man deren Welt), sonst die gewählte, sonst die erste der Liste. Eine
// Sitzung arbeitet nur auf dem Ordner und Zustand ihrer Welt (group.ServerDir,
// group.LoadWorldState) und fragt den Koordinator mit einem Client für diese
// Welt (client.ForWorld) – Welten werden nie vermischt. Ein Koordinator ohne
// Weltliste (Status ohne Worlds) bekommt keine Weltangabe; dann gilt alles
// wie vor den Welten (Welt "").

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// worldCtx ist, was eine Sitzung (oder ein Nachreichen) für ihre Welt
// braucht: Kennung ("" ohne Weltliste), Server-Ordner, Zustand und einen
// Client, der sich auf die Welt bezieht.
type worldCtx struct {
	ID  string
	Dir string
	St  *group.State
	C   *client.Client
}

// hasWorld meldet, ob die Weltliste die Welt id enthält.
func hasWorld(list []api.GroupWorld, id string) bool {
	return slices.ContainsFunc(list, func(w api.GroupWorld) bool { return w.ID == id })
}

// chooseWorld wählt die Welt für „Spielen“ aus dem Status: die laufende,
// sonst die gewählte (selected), sonst die erste. aware false: Der
// Koordinator kennt keine Welten (id ist dann "").
func chooseWorld(st *api.Status, selected string) (id string, aware bool) {
	if st == nil || len(st.Worlds) == 0 {
		return "", false
	}
	switch {
	case st.RunningWorld != "" && hasWorld(st.Worlds, st.RunningWorld):
		return st.RunningWorld, true
	case selected != "" && hasWorld(st.Worlds, selected):
		return selected, true
	}
	return st.Worlds[0].ID, true
}

// worldName liefert den Namen der Welt id aus dem Status (leer: unbekannt).
func worldName(st *api.Status, id string) string {
	if st == nil {
		return ""
	}
	for _, w := range st.Worlds {
		if w.ID == id {
			return w.Name
		}
	}
	return ""
}

// statusFresh fragt den Status frisch; scheitert das, gilt der zuletzt
// bekannte (nil, wenn es keinen gibt).
func (a *App) statusFresh(ctx context.Context, c *client.Client) *api.Status {
	if st, err := c.Status(ctx); err == nil {
		return st
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// groupState ist der Zustand der Gruppe (für die erste Welt und für die PC-
// Kennung der übrigen).
func (a *App) groupState() *group.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st
}

// worldState liefert den Zustand der Welt id der aktiven Gruppe: für die erste
// Welt der der Gruppe, sonst den (nur einmal geladenen, dann geteilten)
// Zustand der Welt.
func (a *App) worldState(id string) (*group.State, error) {
	base := a.groupState()
	if group.DefaultWorld(id) {
		return base, nil
	}
	key := group.Dir() + "|" + id
	a.mu.Lock()
	if ws, ok := a.wstates[key]; ok {
		a.mu.Unlock()
		return ws, nil
	}
	a.mu.Unlock()
	ws, err := group.LoadWorldState(id, base)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if prev, ok := a.wstates[key]; ok {
		return prev, nil
	}
	if a.wstates == nil {
		a.wstates = map[string]*group.State{}
	}
	a.wstates[key] = ws
	return ws, nil
}

// worldFor baut den Weltkontext für die Welt id; c ist der Client der Gruppe.
// aware: Der Koordinator kennt Welten (sonst wird dem Client keine Welt
// mitgegeben).
func (a *App) worldFor(id string, c *client.Client, aware bool) (worldCtx, error) {
	if !aware {
		id = ""
	}
	st, err := a.worldState(id)
	if err != nil {
		return worldCtx{}, fmt.Errorf("Zustand der Welt %s: %w", id, err)
	}
	dir, err := group.ServerDir(id)
	if err != nil {
		return worldCtx{}, err
	}
	return worldCtx{ID: id, Dir: dir, St: st, C: c.ForWorld(id)}, nil
}

// pickWorld wählt die Welt für „Spielen“ (frischer Status) und baut ihren
// Kontext.
func (a *App) pickWorld(ctx context.Context, c *client.Client) (worldCtx, error) {
	st := a.statusFresh(ctx, c)
	id, aware := chooseWorld(st, group.SelectedWorld())
	if aware {
		log.Printf("Welt: %s „%s“ (laufend: %q, gewählt: %q)", id, worldName(st, id), st.RunningWorld, group.SelectedWorld())
	}
	return a.worldFor(id, c, aware)
}

// recoverWorlds liefert die Welten, deren lokaler Stand beim Start
// aufzuräumen oder nachzureichen ist, ohne den Koordinator zu fragen: jede
// Welt, die diese Gruppe lokal hat. Ist nur die erste da und kennt der
// Koordinator (zuletzt gesehen) keine Welten, läuft alles wie vor den
// Welten ohne Weltangabe.
func (a *App) recoverWorlds(c *client.Client) []worldCtx {
	locals := group.LocalWorlds()
	a.mu.Lock()
	st := a.status
	a.mu.Unlock()
	aware := len(locals) > 1 || (st != nil && len(st.Worlds) > 0)
	if !aware {
		locals = locals[:1]
	}
	var out []worldCtx
	for _, id := range locals {
		wc, err := a.worldFor(id, c, aware)
		if err != nil {
			log.Printf("Nachreichen: Welt %s übersprungen: %v", id, err)
			continue
		}
		out = append(out, wc)
	}
	return out
}

// wechselSperreWelt: Grund, aus dem die Welt gerade nicht gewechselt werden
// darf ("" = erlaubt): solange jemand hostet (auch dieser PC), spielt man
// dessen Welt.
func (a *App) wechselSperreWelt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.hostCancel != nil:
		return "Du hostest gerade – die Welt lässt sich erst nach dem Hosten wechseln."
	case a.status != nil && a.status.Host != nil:
		return "Es hostet gerade jemand – gespielt wird seine Welt. Wechseln geht, wenn niemand hostet."
	case a.busy != "":
		return "Gerade läuft „" + a.busy + "“ – bitte gleich noch einmal."
	}
	return ""
}

// selectWorld wählt die Welt id für „Spielen“ (Aktion des Fensters). Es geht
// nur, solange niemand hostet; die Welt muss es geben.
func (a *App) selectWorld(ctx context.Context, id string) error {
	if why := a.wechselSperreWelt(); why != "" {
		return errors.New(why)
	}
	c := a.client()
	st := a.statusFresh(ctx, c)
	if st == nil || len(st.Worlds) == 0 {
		return errors.New("Dieser Koordinator kennt noch keine mehreren Welten.")
	}
	if !hasWorld(st.Worlds, id) {
		return errors.New("Diese Welt gibt es nicht (mehr).")
	}
	if st.Host != nil {
		return errors.New("Es hostet gerade jemand – gespielt wird seine Welt. Wechseln geht, wenn niemand hostet.")
	}
	if err := group.SetSelectedWorld(id); err != nil {
		return err
	}
	log.Printf("Welt gewählt: %s „%s“", id, worldName(st, id))
	a.refresh()
	return nil
}

// weltZustand füllt den Teil des Fenster-Zustands zu den Welten.
func (a *App) weltZustand() fensterapi.WeltZustand {
	a.mu.Lock()
	st := a.status
	a.mu.Unlock()
	if st == nil || len(st.Worlds) == 0 {
		return fensterapi.WeltZustand{}
	}
	id, _ := chooseWorld(st, group.SelectedWorld())
	return fensterapi.WeltZustand{
		Aktiv:    id,
		Gewaehlt: group.SelectedWorld(),
		Laufend:  st.RunningWorld,
		Sperre:   a.wechselSperreWelt(),
	}
}

// weltenFehlerText macht aus einem Fehler einer Verwaltungsaktion Status und Text
// fürs Fenster (403/409/400/404 mit dem Text des Koordinators).
func weltenFehlerText(err error) (int, string) {
	if se, ok := errors.AsType[*client.StatusError](err); ok {
		switch se.StatusCode {
		case http.StatusForbidden:
			return http.StatusForbidden, "Welten verwalten dürfen nur Admins."
		case http.StatusConflict, http.StatusBadRequest, http.StatusNotFound:
			if strings.TrimSpace(se.Message) != "" {
				return se.StatusCode, se.Message
			}
		}
	}
	return koordFehler(err)
}

// bindWorld setzt Ordner, Zustand und Client der Welt wc in die Eingaben der
// Sitzung.
func (in *playInput) bindWorld(wc worldCtx) {
	in.c, in.st, in.dir, in.world = wc.C, wc.St, wc.Dir, wc.ID
}

// weltNom und weltDat nennen die Welt in einem Satz (Nominativ/Dativ):
// ohne Namen „die Gruppenwelt“ (Koordinator ohne mehrere Welten).
func weltNom(name string) string {
	if name == "" {
		return "die Gruppenwelt"
	}
	return "die Welt „" + name + "“"
}

func weltDat(name string) string {
	if name == "" {
		return "der Gruppenwelt"
	}
	return "der Welt „" + name + "“"
}

// weltenAktion führt die Aktionen des Fensters zu den Welten aus: wählen
// (jedes Mitglied) sowie anlegen, ändern und löschen (nur Admins; der
// Koordinator prüft es ebenfalls).
func (a *App) weltenAktion(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	if name == aktionWelt {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.ID == "" {
			antwortFehler(w, http.StatusBadRequest, "Welche Welt? Erwartet {\"id\": …}.")
			return
		}
		if err := a.selectWorld(r.Context(), req.ID); err != nil {
			antwortFehler(w, http.StatusConflict, err.Error())
			return
		}
		antwortJSON(w, http.StatusOK, struct{}{})
		return
	}
	a.mu.Lock()
	admin := a.me != nil && a.me.Admin
	a.mu.Unlock()
	if !admin {
		antwortFehler(w, http.StatusForbidden, "Welten verwalten dürfen nur Admins.")
		return
	}
	var (
		res any
		err error
	)
	c := a.client()
	if c == nil {
		antwortFehler(w, http.StatusConflict, textStartetNoch)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), koordWait)
	defer cancel()
	switch name {
	case aktionWeltNeu:
		var req api.WorldCreate
		if err := json.Unmarshal(body, &req); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Welt anlegen: Körper nicht lesbar: "+err.Error())
			return
		}
		res, err = retry401(ctx, func() (*api.GroupWorld, error) { return c.CreateWorld(ctx, req) })
	case aktionWeltAendern:
		var req struct {
			ID    string         `json:"id"`
			Patch api.WorldPatch `json:"patch"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.ID == "" {
			antwortFehler(w, http.StatusBadRequest, "Welche Welt? Erwartet {\"id\": …, \"patch\": …}.")
			return
		}
		res, err = retry401(ctx, func() (*api.GroupWorld, error) { return c.UpdateWorld(ctx, req.ID, req.Patch) })
	case aktionWeltLoeschen:
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.ID == "" || req.Name == "" {
			antwortFehler(w, http.StatusBadRequest, "Welche Welt? Erwartet {\"id\": …, \"name\": …} (der Name als Bestätigung).")
			return
		}
		res, err = struct{}{}, retry0(ctx, func() error { return c.DeleteWorld(ctx, req.ID, req.Name) })
		if err == nil && group.SelectedWorld() == req.ID {
			// Die gewählte Welt gibt es nicht mehr: Die Wahl fällt auf die erste zurück.
			if err := group.SetSelectedWorld(group.FirstWorld); err != nil {
				log.Printf("Welt: Auswahl nicht zurückgesetzt: %v", err)
			}
		}
	}
	if err != nil {
		log.Printf("Fenster: %s: %s", name, logFehler(err))
		code, text := weltenFehlerText(err)
		antwortFehler(w, code, text)
		return
	}
	go a.pollStatus(false) // die Liste im Status neu
	antwortJSON(w, http.StatusOK, res)
}
