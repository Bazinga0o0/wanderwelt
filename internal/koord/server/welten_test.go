package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func (e *restoreEnv) worlds(as who) api.WorldList {
	e.t.Helper()
	var l api.WorldList
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/worlds", nil, &l); code != http.StatusOK {
		e.t.Fatalf("GET /v1/worlds: %d", code)
	}
	return l
}

func (e *restoreEnv) addWorld(as who, req api.WorldCreate) (int, api.GroupWorld) {
	e.t.Helper()
	var w api.GroupWorld
	code := call(e.t, e.base, as, http.MethodPost, "/v1/worlds", req, &w)
	return code, w
}

func (e *restoreEnv) deleteWorld(as who, id, confirm string) int {
	e.t.Helper()
	return call(e.t, e.base, as, http.MethodDelete, "/v1/worlds/"+id+"?confirm="+url.QueryEscape(confirm), nil, nil)
}

// playWorld fragt als as die Welt world (ohne Wahl: ältere Starter) und
// schließt das Sammelfenster.
func (e *restoreEnv) playWorld(as who, machine, world string) playResult {
	e.t.Helper()
	return playElection(e.t, e.base, e.clk, as, api.PlayRequest{MachineID: machine, GroupWorld: world})
}

// Die Weltliste: Mitglieder lesen, nur Admins schreiben.
func TestWeltenAdminRechte(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	l := e.worlds(userB)
	if len(l.Worlds) != 1 || l.Worlds[0].ID != "w1" || l.Worlds[0].Name != "Welt" || l.Worlds[0].HeadGeneration != 1 || l.Worlds[0].Empty || l.Running != "" {
		t.Fatalf("Liste: %+v", l)
	}
	create := api.WorldCreate{Name: "Creative", Gamemode: "creative", Difficulty: "peaceful"}
	if code, _ := e.addWorld(userB, create); code != http.StatusForbidden {
		t.Fatalf("Mitglied legt Welt an: %d", code)
	}
	name := "Umbenannt"
	if code := call(t, e.base, userB, http.MethodPatch, "/v1/worlds/w1", api.WorldPatch{Name: &name}, nil); code != http.StatusForbidden {
		t.Fatalf("Mitglied ändert Welt: %d", code)
	}
	if code := e.deleteWorld(userB, "w1", "Welt"); code != http.StatusForbidden {
		t.Fatalf("Mitglied löscht Welt: %d", code)
	}
	if code, _ := e.addWorld(stranger, create); code != http.StatusForbidden {
		t.Fatalf("Fremder legt Welt an: %d", code)
	}
	if code := call(t, e.base, stranger, http.MethodGet, "/v1/worlds", nil, nil); code != http.StatusForbidden {
		t.Fatalf("Fremder liest Welten: %d", code)
	}
	if l := e.worlds(userB); len(l.Worlds) != 1 || l.Worlds[0].Name != "Welt" {
		t.Fatalf("abgelehnte Änderungen haben etwas verändert: %+v", l)
	}

	code, w2 := e.addWorld(userA, create)
	if code != http.StatusCreated || w2.ID != "w2" || w2.Name != "Creative" || !w2.Empty || w2.Gamemode != "creative" {
		t.Fatalf("Admin legt Welt an: %d %+v", code, w2)
	}
	if code, _ := e.addWorld(userA, api.WorldCreate{Name: "creative"}); code != http.StatusConflict {
		t.Fatalf("doppelter Name: %d", code)
	}
	if code, _ := e.addWorld(userA, api.WorldCreate{Name: ""}); code != http.StatusBadRequest {
		t.Fatalf("leerer Name: %d", code)
	}
	if code, _ := e.addWorld(userA, api.WorldCreate{Name: "x", Gamemode: "hardcore"}); code != http.StatusBadRequest {
		t.Fatalf("ungültiger Spielmodus: %d", code)
	}
	if code := call(t, e.base, userA, http.MethodPatch, "/v1/worlds/w2", api.WorldPatch{Name: &name}, nil); code != http.StatusOK {
		t.Fatalf("Admin ändert Welt: %d", code)
	}
	if l := e.worlds(userB); len(l.Worlds) != 2 || l.Worlds[1].Name != "Umbenannt" || l.Worlds[1].Gamemode != "creative" {
		t.Fatalf("Liste: %+v", l)
	}
	// Unbekannte Felder und unbekannte Welt.
	if code, _ := e.raw(userA, http.MethodPatch, "/v1/worlds/w2", map[string]any{"head": 3}); code != http.StatusBadRequest {
		t.Fatalf("unbekanntes Feld: %d", code)
	}
	if code := call(t, e.base, userA, http.MethodPatch, "/v1/worlds/w9", api.WorldPatch{Name: &name}, nil); code != http.StatusNotFound {
		t.Fatalf("unbekannte Welt: %d", code)
	}
}

// Löschen: nur mit Bestätigung, nur nicht laufend, nie die letzte Welt; die
// Stände bleiben liegen.
func TestWeltLoeschenNurNichtLaufend(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	_, w2 := e.addWorld(userA, api.WorldCreate{Name: "Creative"})

	if code := e.deleteWorld(userA, "w1", ""); code != http.StatusBadRequest {
		t.Fatalf("Löschen ohne Bestätigung: %d", code)
	}
	if code := e.deleteWorld(userA, "w1", "falsch"); code != http.StatusBadRequest {
		t.Fatalf("Löschen mit falschem Namen: %d", code)
	}
	if code := e.deleteWorld(userA, "w7", "x"); code != http.StatusNotFound {
		t.Fatalf("unbekannte Welt: %d", code)
	}
	if len(e.worlds(userA).Worlds) != 2 {
		t.Fatal("abgelehntes Löschen hat etwas gelöscht")
	}

	// w1 läuft: nicht löschbar, auch nicht, wenn nur ein Zuschlag zum Hochladen sie hält.
	l := e.host(userA, "pc-a")
	if code := e.deleteWorld(userA, "w1", "Welt"); code != http.StatusConflict {
		t.Fatalf("laufende Welt gelöscht: %d", code)
	}
	if code := e.deleteWorld(userA, w2.ID, w2.Name); code != http.StatusNoContent {
		t.Fatalf("andere (nicht laufende) Welt löschen während w1 läuft: %d", code)
	}
	e.release(userA, "pc-a", l)

	// Die letzte Welt bleibt.
	if code := e.deleteWorld(userA, "w1", "Welt"); code != http.StatusConflict {
		t.Fatalf("letzte Welt gelöscht: %d", code)
	}
	if l := e.worlds(userA); len(l.Worlds) != 1 || l.Worlds[0].ID != "w1" || l.Worlds[0].HeadGeneration != 2 {
		t.Fatalf("Liste: %+v", l)
	}
	// Die Stände von w1 sind unberührt (Head 2), die gelöschte Welt ist weg.
	var g api.Generation
	if code := call(t, e.base, userA, http.MethodGet, "/v1/generations/head", nil, &g); code != http.StatusOK || g.Number != 2 || g.WorldID != gens[0].WorldID {
		t.Fatalf("Head: %d %+v", code, g)
	}
	if code := call(t, e.base, userA, http.MethodGet, "/v1/generations/head?world="+w2.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("Head der gelöschten Welt: %d", code)
	}
}

// Es läuft immer nur eine Welt. Ein Host einer Welt lässt andere
// Wünsche nicht durch; wer dieselbe Welt (oder keine) nennt, wird Mitspieler.
func TestZuschlagNurEineWelt(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	e.addWorld(userA, api.WorldCreate{Name: "Creative", Gamemode: "creative", Difficulty: "peaceful"})
	// Admin A startet die leere zweite Welt: Host, Welt meldet „leer“.
	r := e.playWorld(userA, "pc-a", "w2")
	if r.resp.Role != api.RoleHost || r.resp.GroupWorld != "w2" || r.resp.GroupWorldName != "Creative" || !r.resp.Empty ||
		r.resp.WorldID != "" || r.resp.HeadGeneration != 0 {
		t.Fatalf("Host für leere Welt: %+v", r.resp)
	}
	st := getStatus(t, e.base, userB)
	if st.RunningWorld != "w2" || len(st.Worlds) != 2 || !st.Worlds[1].Running || st.Worlds[0].Running {
		t.Fatalf("Status: %+v", st)
	}
	if l := e.worlds(userB); l.Running != "w2" {
		t.Fatalf("Liste: %+v", l)
	}
	// B will Welt 1: abgelehnt, mit dem Namen der laufenden Welt.
	var d api.PlayResponse
	if code := call(t, e.base, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b", GroupWorld: "w1"}, &d); code != http.StatusOK ||
		d.Role != api.RoleDenied || !strings.Contains(d.Reason, "Creative") || d.GroupWorld != "w2" {
		t.Fatalf("andere Welt: %d %+v", code, d)
	}
	// Ein älterer Starter (keine Wahl) will die erste Welt: ebenfalls abgelehnt.
	d = api.PlayResponse{}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b"}, &d); code != http.StatusOK || d.Role != api.RoleDenied {
		t.Fatalf("älterer Starter: %d %+v", code, d)
	}
	// B will die laufende Welt: Mitspieler.
	if code := call(t, e.base, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b", GroupWorld: "w2"}, &d); code != http.StatusOK ||
		d.Role != api.RoleJoin || d.GroupWorld != "w2" {
		t.Fatalf("laufende Welt: %d %+v", code, d)
	}
	// Eine unbekannte Welt gibt es nicht.
	d = api.PlayResponse{}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b", GroupWorld: "w9"}, &d); code != http.StatusOK || d.Role != api.RoleDenied || d.Reason == "" {
		t.Fatalf("unbekannte Welt: %d %+v", code, d)
	}
	// Host-Einstellungen kommen mit Spielmodus und Schwierigkeit der Welt.
	var hs api.HostSettings
	if code := call(t, e.base, userA, http.MethodGet, "/v1/host/settings", nil, &hs); code != http.StatusOK ||
		hs.Properties["gamemode"] != "creative" || hs.Properties["difficulty"] != "peaceful" {
		t.Fatalf("Host-Einstellungen: %d %+v", code, hs.Properties)
	}
	// Der erste Stand füllt die leere Welt; die erste Welt bleibt unberührt.
	g := e.commit(userA, "pc-a", r.resp.LeaseID, 5, false, manifestMC)
	if g.Number != 1 || g.Parent != 0 {
		t.Fatalf("erster Stand der zweiten Welt: %+v", g)
	}
	e.release(userA, "pc-a", r.resp.LeaseID)
	l := e.worlds(userB)
	if l.Worlds[0].HeadGeneration != 1 || l.Worlds[0].Empty || l.Worlds[1].HeadGeneration != 1 || l.Worlds[1].Empty || l.Running != "" {
		t.Fatalf("Liste nach dem Stand: %+v", l)
	}
	var h1, h2 api.Generation
	call(t, e.base, userB, http.MethodGet, "/v1/generations/head", nil, &h1)
	call(t, e.base, userB, http.MethodGet, "/v1/generations/head?world=w2", nil, &h2)
	if h1.WorldID == h2.WorldID || h2.WorldID != g.WorldID {
		t.Fatalf("Köpfe: w1 %s, w2 %s (Stand %s)", h1.WorldID, h2.WorldID, g.WorldID)
	}
	// Die Welt-Einstellungen von w1 (ohne Spielmodus) bleiben beim Gruppenstandard.
	r1 := e.playWorld(userA, "pc-a", "")
	if r1.resp.Role != api.RoleHost || r1.resp.GroupWorld != "w1" {
		t.Fatalf("ohne Wahl: %+v", r1.resp)
	}
	hs = api.HostSettings{}
	call(t, e.base, userA, http.MethodGet, "/v1/host/settings", nil, &hs)
	if _, ok := hs.Properties["gamemode"]; ok {
		t.Fatalf("w1 bekommt einen Spielmodus: %+v", hs.Properties)
	}
	// Der Stand geht in die Welt des Zuschlags (w1), nicht in w2.
	g1 := e.commit(userA, "pc-a", r1.resp.LeaseID, 6, false, manifestMC)
	if g1.WorldID != h1.WorldID || g1.Number != 2 {
		t.Fatalf("Stand in w1: %+v", g1)
	}
	if _, n := headOf(t, e, "w2"); n != 1 {
		t.Fatalf("w2 hat sich verändert: %d", n)
	}
}

func headOf(t *testing.T, e *restoreEnv, world string) (string, int64) {
	t.Helper()
	var g api.Generation
	if code := call(t, e.base, userA, http.MethodGet, "/v1/generations/head?world="+world, nil, &g); code != http.StatusOK {
		t.Fatalf("Head %s: %d", world, code)
	}
	return g.WorldID, g.Number
}

// Eine leere Welt startet nur ein Admin; ein Mitglied bekommt einen Hinweis
// (kann aber mitspielen, sobald der Admin hostet).
func TestLeereWeltNurAdminStartet(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	e.addWorld(userA, api.WorldCreate{Name: "Neu"})
	var d api.PlayResponse
	if code := call(t, e.base, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b", GroupWorld: "w2"}, &d); code != http.StatusOK ||
		d.Role != api.RoleDenied || !strings.Contains(d.Reason, "leer") {
		t.Fatalf("Mitglied startet leere Welt: %d %+v", code, d)
	}
	// Auch ein Commit eines Nicht-Admins in eine leere Welt scheitert (403), selbst wenn er an den Zuschlag käme.
	r := e.playWorld(userA, "pc-a", "w2")
	if r.resp.Role != api.RoleHost {
		t.Fatalf("%+v", r.resp)
	}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b", GroupWorld: "w2"}, &d); code != http.StatusOK || d.Role != api.RoleJoin {
		t.Fatalf("Mitspielen bei laufendem Admin: %d %+v", code, d)
	}
	req := api.CommitRequest{LeaseID: r.resp.LeaseID, MachineID: "pc-b", MinecraftVersion: manifestMC, Files: e.files(1)}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/generations", req, nil); code == http.StatusOK {
		t.Fatal("Nicht-Admin hat in die leere Welt eingereicht")
	}
}

// Abwärtskompatibilität: Ohne group_world (älterer Starter) gilt die erste
// Welt; Zuschlag nur zum Hochladen prüft die Basis dieser Welt.
func TestAelterStarterSpieltErsteWelt(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(1)
	e.addWorld(userA, api.WorldCreate{Name: "Zweite"})
	// Nur zum Hochladen auf der Basis der ersten Welt: geht ohne Weltangabe.
	var up api.PlayResponse
	if code := call(t, e.base, userA, http.MethodPost, "/v1/play",
		api.PlayRequest{MachineID: "pc-a", UploadOnly: true, BaseWorldID: gens[0].WorldID, BaseGeneration: 1}, &up); code != http.StatusOK || up.Role != api.RoleHost || up.GroupWorld != "w1" {
		t.Fatalf("Hochladen: %d %+v", code, up)
	}
	e.release(userA, "pc-a", up.LeaseID)
	// Dieselbe Basis für die (leere) zweite Welt passt nicht.
	up = api.PlayResponse{}
	if code := call(t, e.base, userA, http.MethodPost, "/v1/play",
		api.PlayRequest{MachineID: "pc-a", UploadOnly: true, GroupWorld: "w2", BaseWorldID: gens[0].WorldID, BaseGeneration: 1}, &up); code != http.StatusOK || up.Role != api.RoleDenied {
		t.Fatalf("Basis der anderen Welt: %d %+v", code, up)
	}
	// Der Status ohne laufende Welt zeigt den Head der ersten Welt (wie bisher).
	if st := getStatus(t, e.base, userB); st.WorldID != gens[0].WorldID || st.HeadGeneration != 1 || st.RunningWorld != "" {
		t.Fatalf("Status: %+v", st)
	}
}

// Jede Welt hat ihren eigenen Verlauf, die Stände-Liste nennt die Welt.
func TestStaendeListeNenntWelt(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	e.addWorld(userA, api.WorldCreate{Name: "Zweite"})
	r := e.playWorld(userA, "pc-a", "w2")
	e.commit(userA, "pc-a", r.resp.LeaseID, 2, false, manifestMC)
	e.release(userA, "pc-a", r.resp.LeaseID)
	resp := e.generations(userB)
	if len(resp.Worlds) != 2 {
		t.Fatalf("%+v", resp.Worlds)
	}
	b, _ := json.Marshal(resp.Worlds)
	if !strings.Contains(string(b), `"group_world":"w1"`) || !strings.Contains(string(b), `"group_name":"Zweite"`) {
		t.Fatalf("Stände-Liste: %s", b)
	}
}
