package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

func statusMit(running string, ids ...string) *api.Status {
	st := &api.Status{RunningWorld: running}
	for _, id := range ids {
		st.Worlds = append(st.Worlds, api.GroupWorld{ID: id, Name: "Welt " + id, Running: id == running})
	}
	return st
}

// Welche Welt „Spielen“ nimmt: die laufende, sonst die gewählte, sonst die
// erste; ohne Weltliste keine.
func TestChooseWorld(t *testing.T) {
	for _, tc := range []struct {
		name     string
		st       *api.Status
		selected string
		want     string
		aware    bool
	}{
		{"kein Status", nil, "w2", "", false},
		{"Koordinator ohne Welten", &api.Status{}, "w2", "", false},
		{"nichts gewählt: erste", statusMit("", "w1", "w2"), "", "w1", true},
		{"gewählt", statusMit("", "w1", "w2"), "w2", "w2", true},
		{"gewählte gibt es nicht (mehr): erste", statusMit("", "w1", "w3"), "w2", "w1", true},
		{"laufende geht vor der Wahl", statusMit("w2", "w1", "w2"), "w1", "w2", true},
		{"laufende steht nicht in der Liste: Wahl", statusMit("w9", "w1", "w2"), "w2", "w2", true},
	} {
		got, aware := chooseWorld(tc.st, tc.selected)
		if got != tc.want || aware != tc.aware {
			t.Errorf("%s: %q/%v, erwartet %q/%v", tc.name, got, aware, tc.want, tc.aware)
		}
	}
}

// Jede Welt hat ihren Ordner und Zustand; die erste liegt wie schon immer im
// Gruppenordner. Ein Koordinator ohne Welten bekommt keine Weltangabe.
func TestWorldForTrenntOrdnerZustandClient(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	a := busyApp(t, client.New(&group.Group{Coordinator: "http://127.0.0.1:1"}))
	a.st = &group.State{MachineID: "pc-1", LastUploadedWorldID: "A", LastUploadedGeneration: 5}
	base := a.c

	w1, err := a.worldFor("w1", base, true)
	if err != nil {
		t.Fatal(err)
	}
	if w1.ID != "w1" || w1.Dir != filepath.Join(group.Dir(), "server") || w1.St != a.st || w1.C.World() != "w1" {
		t.Fatalf("erste Welt: %+v", w1)
	}
	w2, err := a.worldFor("w2", base, true)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Dir != filepath.Join(group.Dir(), "welten", "w2", "server") || w2.St == a.st || w2.C.World() != "w2" {
		t.Fatalf("zweite Welt: %+v", w2)
	}
	if w2.St.MachineID != "pc-1" || w2.St.LastUploadedWorldID != "" {
		t.Fatalf("Zustand der zweiten Welt: %+v", w2.St)
	}
	// Derselbe Zustand bei jedem Zugriff (eine Instanz je Welt), der Ordner der anderen unberührt.
	again, _ := a.worldFor("w2", base, true)
	if again.St != w2.St {
		t.Fatal("zwei Zustände für dieselbe Welt")
	}
	w3, _ := a.worldFor("w3", base, true)
	if w3.St == w2.St || w3.Dir == w2.Dir {
		t.Fatal("zwei Welten teilen sich Ordner oder Zustand")
	}
	// Der Basis-Client trägt keine Welt (ältere Koordinatoren würden das Feld ablehnen).
	if base.World() != "" {
		t.Fatalf("der Basis-Client wurde verändert: %q", base.World())
	}
	legacy, err := a.worldFor("w2", base, false)
	if err != nil || legacy.ID != "" || legacy.C.World() != "" || legacy.St != a.st || legacy.Dir != filepath.Join(group.Dir(), "server") {
		t.Fatalf("ohne Weltliste: %+v %v", legacy, err)
	}
	if _, err := a.worldFor("../x", base, true); err == nil {
		t.Fatal("ungültige Welt-Kennung angenommen")
	}
	// Ein anderer Gruppenordner hat andere Zustände.
	key := group.Dir() + "|w2"
	if _, ok := a.wstates[key]; !ok {
		t.Fatalf("Zustand nicht unter %q gemerkt: %v", key, a.wstates)
	}
}

// Beim Start werden alle lokalen Welten aufgeräumt/nachgereicht, jede mit
// ihrem Ordner, Zustand und Client; ohne weitere Welten und ohne Weltliste
// wie vor den Welten.
func TestRecoverWorlds(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	a := busyApp(t, client.New(&group.Group{Coordinator: "http://127.0.0.1:1"}))
	a.st = &group.State{MachineID: "pc-1"}
	ws := a.recoverWorlds(a.c)
	if len(ws) != 1 || ws[0].ID != "" || ws[0].C.World() != "" || ws[0].St != a.st {
		t.Fatalf("nur die erste Welt, ohne Weltangabe: %+v", ws)
	}
	// Ist der Koordinator als welt-fähig bekannt, bekommt auch die erste Welt ihre Kennung.
	a.status = statusMit("", "w1")
	if ws := a.recoverWorlds(a.c); len(ws) != 1 || ws[0].ID != "w1" || ws[0].C.World() != "w1" {
		t.Fatalf("mit Weltliste: %+v", ws)
	}
	a.status = nil
	// Eine weitere Welt liegt lokal: alle werden bedient, jede mit eigenem Ordner.
	if err := os.MkdirAll(filepath.Join(group.Dir(), "welten", "w2", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws = a.recoverWorlds(a.c)
	if len(ws) != 2 || ws[0].ID != "w1" || ws[1].ID != "w2" || ws[0].Dir == ws[1].Dir || ws[0].St == ws[1].St ||
		ws[1].C.World() != "w2" {
		t.Fatalf("zwei lokale Welten: %+v", ws)
	}
}

// Die Notfallkopie jeder Welt liegt in einem eigenen Ordner (die erste behält
// den bisherigen Namen) und merkt sich ihre Angaben getrennt.
func TestNotfallkopieJeWelt(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	if got := notfallOrdnerNameFor(""); got != notfallName {
		t.Errorf("ohne Welt: %q", got)
	}
	if got := notfallOrdnerNameFor("w1"); got != notfallName {
		t.Errorf("erste Welt: %q", got)
	}
	if got := notfallOrdnerNameFor("w2"); got != notfallName+"-w2" {
		t.Errorf("zweite Welt: %q", got)
	}
	if got := notfallOrdnerNameFor("../x"); got != notfallName {
		t.Errorf("ungültige Kennung: %q", got)
	}
	var e notfallEinstellung
	e.setzeLetzte("w1", notfallLetzte{WorldID: "A", Generation: 3})
	e.setzeLetzte("w2", notfallLetzte{WorldID: "B", Generation: 9, NichtHochgeladen: true})
	if l := e.letzte("w1"); l.WorldID != "A" || l.Generation != 3 || l.NichtHochgeladen {
		t.Errorf("erste Welt: %+v", l)
	}
	if l := e.letzte("w2"); l.WorldID != "B" || l.Generation != 9 || !l.NichtHochgeladen {
		t.Errorf("zweite Welt: %+v", l)
	}
	if e.WorldID != "A" {
		t.Errorf("die Angaben der ersten Welt stehen nicht mehr oben: %+v", e)
	}
	if l := e.letzte("w3"); l.WorldID != "" {
		t.Errorf("dritte Welt: %+v", l)
	}
}

// Fenster → Instanz → Koordinator: Welten anlegen, wählen, ändern und
// löschen; Wechsel und Löschen nicht, solange gehostet wird.
func TestWeltenAktionenDesFensters(t *testing.T) {
	c := importCoordinator(t)
	ft := startFenster(t, c)
	a := ft.a
	a.st = &group.State{MachineID: "pc-admin"}
	ctx := t.Context()

	a.me = &api.Me{Email: "mitglied@example.org"}
	for _, name := range []string{"weltneu", "weltaendern", "weltloeschen"} {
		if err := ft.fc.Aktion(ctx, name, map[string]any{"id": "w1", "name": "x"}); err == nil || !strings.Contains(err.Error(), "nur Admins") {
			t.Fatalf("%s als Mitglied: %v", name, err)
		}
	}
	if l, _ := c.Worlds(ctx); l == nil || len(l.Worlds) != 0 {
		t.Fatalf("Mitglied hat Welten angelegt: %+v", l)
	}

	a.me = &api.Me{Email: "admin@example.org", Admin: true}
	if err := ft.fc.Aktion(ctx, "weltneu", api.WorldCreate{Name: "Survival", Gamemode: "survival", Difficulty: "hard"}); err != nil {
		t.Fatal(err)
	}
	if err := ft.fc.Aktion(ctx, "weltneu", api.WorldCreate{Name: "Creative", Gamemode: "creative"}); err != nil {
		t.Fatal(err)
	}
	if err := ft.fc.Aktion(ctx, "weltneu", api.WorldCreate{Name: "creative"}); err == nil || !strings.Contains(err.Error(), "Es gibt schon eine Welt") {
		t.Fatalf("doppelter Name: %v", err)
	}
	l, err := ft.fc.Welten(ctx)
	if err != nil || len(l.Worlds) != 2 || l.Worlds[0].ID != "w1" || l.Worlds[1].Name != "Creative" || l.Worlds[1].Gamemode != "creative" {
		t.Fatalf("Welten: %+v %v", l, err)
	}

	// Wählen: gilt für „Spielen“ (Zustand), merkt sich die Gruppe.
	a.pollStatus(false)
	if z, _ := ft.fc.Zustand(ctx); z.Welt.Aktiv != "w1" || z.Welt.Gewaehlt != "" {
		t.Fatalf("vor der Wahl: %+v", z.Welt)
	}
	if err := ft.fc.Aktion(ctx, "welt", map[string]string{"id": "w2"}); err != nil {
		t.Fatal(err)
	}
	if err := ft.fc.Aktion(ctx, "welt", map[string]string{"id": "w9"}); err == nil || !strings.Contains(err.Error(), "gibt es nicht") {
		t.Fatalf("unbekannte Welt gewählt: %v", err)
	}
	if got := group.SelectedWorld(); got != "w2" {
		t.Fatalf("Auswahl: %q", got)
	}
	if z, _ := ft.fc.Zustand(ctx); z.Welt.Aktiv != "w2" || z.Welt.Gewaehlt != "w2" || z.Welt.Sperre != "" {
		t.Fatalf("nach der Wahl: %+v", z.Welt)
	}

	// Umbenennen / Eigenschaften.
	name, mode := "Bauen", "adventure"
	if err := ft.fc.Aktion(ctx, "weltaendern", map[string]any{"id": "w2", "patch": api.WorldPatch{Name: &name, Gamemode: &mode}}); err != nil {
		t.Fatal(err)
	}
	if l, _ := ft.fc.Welten(ctx); l.Worlds[1].Name != "Bauen" || l.Worlds[1].Gamemode != "adventure" {
		t.Fatalf("nach dem Ändern: %+v", l.Worlds[1])
	}

	// Hostet jemand (hier: ein Zuschlag auf w1), spielt man dessen Welt: kein Wechsel, kein Löschen.
	resp, err := c.ForWorld("w1").Play(ctx, api.PlayRequest{MachineID: "pc-x", UploadOnly: true})
	if err != nil || resp.Role != api.RoleHost {
		t.Fatalf("Zuschlag: %+v %v", resp, err)
	}
	a.pollStatus(false)
	if z, _ := ft.fc.Zustand(ctx); z.Welt.Laufend != "w1" || z.Welt.Aktiv != "w1" || z.Welt.Sperre == "" {
		t.Fatalf("während gehostet wird: %+v", z.Welt)
	}
	if err := ft.fc.Aktion(ctx, "welt", map[string]string{"id": "w2"}); err == nil || !strings.Contains(err.Error(), "hostet gerade") {
		t.Fatalf("Wechsel während gehostet wird: %v", err)
	}
	if err := ft.fc.Aktion(ctx, "weltloeschen", map[string]string{"id": "w1", "name": "Survival"}); err == nil || !strings.Contains(err.Error(), "läuft gerade") {
		t.Fatalf("laufende Welt gelöscht: %v", err)
	}
	if err := c.Release(ctx, api.ReleaseRequest{LeaseID: resp.LeaseID, MachineID: "pc-x"}); err != nil {
		t.Fatal(err)
	}

	// Löschen: nur mit dem richtigen Namen; die gewählte Welt fällt auf die erste zurück.
	if err := ft.fc.Aktion(ctx, "weltloeschen", map[string]string{"id": "w2", "name": "falsch"}); err == nil {
		t.Fatal("Löschen mit falschem Namen")
	}
	if err := ft.fc.Aktion(ctx, "weltloeschen", map[string]string{"id": "w2", "name": "Bauen"}); err != nil {
		t.Fatalf("Löschen: %v", err)
	}
	if l, _ := ft.fc.Welten(ctx); len(l.Worlds) != 1 || l.Worlds[0].ID != "w1" {
		t.Fatalf("nach dem Löschen: %+v", l)
	}
	if got := group.SelectedWorld(); got != "w1" {
		t.Fatalf("Auswahl nach dem Löschen der gewählten Welt: %q", got)
	}
	if err := ft.fc.Aktion(ctx, "weltloeschen", map[string]string{"id": "w1", "name": "Survival"}); err == nil || !strings.Contains(err.Error(), "letzte Welt") {
		t.Fatalf("letzte Welt gelöscht: %v", err)
	}
}

// „Welt übernehmen“ geht in die gewählte Welt; die andere bleibt leer.
func TestImportGehtInGewaehlteWelt(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	saves := t.TempDir()
	writeSave(t, saves, "Meine Welt", levelDat("26.2", false, testUUID), true)
	c := importCoordinator(t)
	ctx := t.Context()
	if _, err := c.CreateWorld(ctx, api.WorldCreate{Name: "Survival"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateWorld(ctx, api.WorldCreate{Name: "Creative"}); err != nil {
		t.Fatal(err)
	}
	a := busyApp(t, c)
	a.st = &group.State{MachineID: "pc-admin"}
	if err := group.SetSelectedWorld("w2"); err != nil {
		t.Fatal(err)
	}
	p := probeDialogs(t, a)
	a.importFromControl(importRequest{Dir: filepath.Join(saves, "Meine Welt")})
	p.check(t, "ist jetzt die Gruppenwelt")
	if g, err := c.ForWorld("w2").Head(ctx); err != nil || g == nil || g.Number != 1 {
		t.Fatalf("zweite Welt nach dem Übernehmen: %+v %v", g, err)
	}
	if g, err := c.ForWorld("w1").Head(ctx); err != nil || g != nil {
		t.Fatalf("die erste Welt hat einen Stand bekommen: %+v %v", g, err)
	}
}

// Eine Host-Sitzung arbeitet auf Ordner, Zustand und Client der Welt ihres
// Zuschlags – hier über die Übernahme einer Übergabe, die die Welt im Angebot
// nennt. Ohne Weltangabe (älterer Koordinator) bleibt es bei Gruppenordner
// und Gruppenzustand.
func TestHostSitzungNutztOrdnerDerWelt(t *testing.T) {
	notes := countNotify(t)
	got := fakeHostRun(t)
	a := takeoverApp(t)
	a.c = client.New(&group.Group{Coordinator: "http://127.0.0.1:1"})
	a.phase = phJoined
	a.takeOver(&api.HandoverOffer{HandoverID: 1, LeaseID: 41, WorldID: "W-B", GroupWorld: "w2", HeadGeneration: 4, Port: 15566,
		From: api.HostInfo{MinecraftName: "Mara"}, MinecraftAddress: "mc.example.org"})
	var d host.Deps
	select {
	case d = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("keine Host-Sitzung")
	}
	if want := filepath.Join(group.Dir(), "welten", "w2", "server"); d.Dir != want {
		t.Errorf("Ordner %q, erwartet %q", d.Dir, want)
	}
	if d.State == a.st || d.State == nil || d.State.MachineID != "pc-admin" {
		t.Errorf("Zustand der zweiten Welt: %p (Gruppe %p)", d.State, a.st)
	}
	if d.Client.World() != "w2" || d.Base != 4 || d.BaseWorldID != "W-B" || d.Fresh {
		t.Errorf("Client %q, Basis %q/%d, Fresh %v", d.Client.World(), d.BaseWorldID, d.Base, d.Fresh)
	}
	a.endHosting("Test")
	waitFor(t, "Sitzung zu", func() bool {
		return !a.isHosting() && strings.Count(strings.Join(notes(), "|"), "Hosten beendet") >= 1
	})

	// Ohne Welt im Angebot: wie bisher.
	a.phase = phJoined
	a.takeOver(&api.HandoverOffer{HandoverID: 2, LeaseID: 42, WorldID: "W-A", HeadGeneration: 7, Port: 15566})
	select {
	case d = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("keine zweite Host-Sitzung")
	}
	if d.Dir != filepath.Join(group.Dir(), "server") || d.State != a.st || d.Client.World() != "" {
		t.Errorf("ohne Welt: Ordner %q, Zustand %p, Client %q", d.Dir, d.State, d.Client.World())
	}
	a.endHosting("Test")
	waitFor(t, "Sitzung zu", func() bool {
		return !a.isHosting() && strings.Count(strings.Join(notes(), "|"), "Hosten beendet") >= 2
	})
}

// Die Notfallkopie der zweiten Welt liegt neben der der ersten und
// überschreibt sie nicht; der Zustand zeigt die Kopie der gewählten Welt.
func TestNotfallkopieZweierWeltenUeberschreibtNicht(t *testing.T) {
	docs := notfallUmgebung(t)
	a := busyApp(t, nil)
	a.st = &group.State{MachineID: "pc-1"}
	a.notfall.cancels = map[int]context.CancelFunc{}
	t.Cleanup(func() { warteKopie(t, a) })

	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "erste"}), WorldID: "WA", Generation: 3, GroupWorld: "w1"})
	warteKopie(t, a)
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "zweite"}), WorldID: "WB", Generation: 8, GroupWorld: "w2"})
	warteKopie(t, a)

	if got := lies(t, filepath.Join(docs, notfallName, "level.dat")); got != "erste" {
		t.Fatalf("Kopie der ersten Welt: %q", got)
	}
	if got := lies(t, filepath.Join(docs, notfallName+"-w2", "level.dat")); got != "zweite" {
		t.Fatalf("Kopie der zweiten Welt: %q", got)
	}
	e, err := ladeNotfall()
	if err != nil {
		t.Fatal(err)
	}
	if l := e.letzte("w1"); l.WorldID != "WA" || l.Generation != 3 {
		t.Errorf("Angaben der ersten Welt: %+v", l)
	}
	if l := e.letzte("w2"); l.WorldID != "WB" || l.Generation != 8 {
		t.Errorf("Angaben der zweiten Welt: %+v", l)
	}
	// Der Zustand zeigt die Kopie der Welt, die „Spielen“ nimmt.
	a.status = statusMit("", "w1", "w2")
	if n := a.notfallZustand(); n.Ordner != filepath.Join(docs, notfallName) || n.Generation != 3 || !n.Vorhanden {
		t.Errorf("Zustand für w1: %+v", n)
	}
	if err := group.SetSelectedWorld("w2"); err != nil {
		t.Fatal(err)
	}
	if n := a.notfallZustand(); n.Ordner != filepath.Join(docs, notfallName+"-w2") || n.Generation != 8 || !n.Vorhanden {
		t.Errorf("Zustand für w2: %+v", n)
	}
}
