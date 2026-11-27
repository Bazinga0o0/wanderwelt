package host

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// zweiteWelt legt auf dem Koordinator eine zweite Welt an und liefert den
// Client für sie.
func zweiteWelt(t *testing.T, co *coord) (*api.GroupWorld, *client.Client) {
	t.Helper()
	w, err := co.c.CreateWorld(t.Context(), api.WorldCreate{Name: "Creative", Gamemode: "creative", Difficulty: "peaceful"})
	if err != nil {
		t.Fatalf("Welt anlegen: %v", err)
	}
	return w, co.c.ForWorld(w.ID)
}

// seedIn reicht als ein anderer PC einen Stand in die Welt von cw ein
// (wie co.seed, aber mit Weltangabe).
func seedIn(t *testing.T, cw *client.Client, files map[string]string) *api.Generation {
	t.Helper()
	ctx := t.Context()
	req := api.PlayRequest{MachineID: seedMachine, UploadOnly: true}
	if h, err := cw.Head(ctx); err != nil {
		t.Fatal(err)
	} else if h != nil {
		req.BaseWorldID, req.BaseGeneration = h.WorldID, h.Number
	}
	resp, err := cw.Play(ctx, req)
	if err != nil || resp.Role != api.RoleHost {
		t.Fatalf("Zuschlag zum Anlegen: %+v, %v", resp, err)
	}
	dir := t.TempDir()
	writeTree(t, dir, files)
	mf, err := worldsync.BuildManifest(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := worldsync.UploadAndCommit(ctx, cw, dir, api.CommitRequest{
		LeaseID: resp.LeaseID, MachineID: seedMachine, BaseWorldID: req.BaseWorldID, BaseGeneration: req.BaseGeneration,
		MinecraftVersion: mcVersion, Files: mf,
	})
	if err != nil {
		t.Fatalf("Stand anlegen: %v", err)
	}
	if err := cw.Release(ctx, api.ReleaseRequest{LeaseID: resp.LeaseID, MachineID: seedMachine}); err != nil {
		t.Fatal(err)
	}
	return gen
}

// worldEnv ist hostEnv für eine Welt der Gruppe: Zuschlag, Client, Ordner und
// Zustand dieser Welt.
func newWorldEnv(t *testing.T, co *coord, world string, head *api.Generation, players []string) *hostEnv {
	t.Helper()
	cw := co.c.ForWorld(world)
	req := api.PlayRequest{MachineID: hostMachine, UploadOnly: true}
	if head != nil {
		req.BaseWorldID, req.BaseGeneration = head.WorldID, head.Number
	}
	resp, err := cw.Play(t.Context(), req)
	if err != nil || resp.Role != api.RoleHost || resp.LeaseID == 0 {
		t.Fatalf("Zuschlag für %s: %+v, %v", world, resp, err)
	}
	dir, err := group.ServerDir(world)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := group.LoadWorldState(world, &group.State{MachineID: hostMachine})
	if err != nil {
		t.Fatal(err)
	}
	drv := newDriver(t)
	exe := testExe(t)
	srv := &fakeServer{
		t: t, clk: drv.clk, exe: exe, players: players,
		world:  filepath.Join(dir, WorldName),
		marker: filepath.Join(dir, MarkerName),
	}
	e := &hostEnv{co: co, drv: drv, srv: srv, tn: &tunnels{exe: exe}, head: head, lease: resp.LeaseID, dir: dir}
	e.d = Deps{
		Client: client.New(co.groupOf()).ForWorld(world), Server: srv,
		Paths:   &deps.Paths{Java: filepath.Join(group.Home(), "deps", "java", "bin", "java.exe")},
		LeaseID: resp.LeaseID, MachineID: hostMachine,
		Base: resp.HeadGeneration, BaseWorldID: resp.WorldID,
		Clock: drv.clk, TunnelCmd: e.tn.cmd, Dir: dir,
		Group: &group.Group{
			Coordinator: co.url, TunnelServer: tunnelAddr, Token: tunnelToken, DevIdentityEmail: adminEmail,
		},
		State: ws, Profile: api.Profile{XmxMB: 2048}, MinecraftVersion: mcVersion, idle: drv.idle,
	}
	return e
}

func (co *coord) groupOf() *group.Group {
	return &group.Group{Coordinator: co.url, DevIdentityEmail: adminEmail}
}

// Eine Sitzung in der zweiten Welt lädt deren Stand in den Ordner der zweiten
// Welt, reicht ihre Stände in sie ein und lässt die erste Welt (Ordner,
// Zustand, Stand auf dem Koordinator) unberührt.
func TestSitzungInZweiterWeltTrenntOrdnerUndStaende(t *testing.T) {
	co := newCoord(t)
	h1 := co.seed(t, seedFiles) // erste Welt
	w2, cw := zweiteWelt(t, co)
	files2 := map[string]string{"level.dat": "creative-anfang", "datapacks/x.zip": "dp"}
	h2 := seedIn(t, cw, files2)
	if h2.WorldID == h1.WorldID {
		t.Fatal("beide Welten haben dieselbe Weltkennung")
	}
	// Die erste Welt liegt schon lokal (eigener Stand dieses PCs).
	first := filepath.Join(group.Home(), "server", WorldName)
	writeTree(t, first, map[string]string{"level.dat": "mein erster Stand"})
	firstState := &group.State{MachineID: hostMachine, LastUploadedWorldID: h1.WorldID, LastUploadedGeneration: h1.Number}
	if err := firstState.Save(); err != nil {
		t.Fatal(err)
	}

	e := newWorldEnv(t, co, w2.ID, h2, []string{"Alex"})
	r := e.start(t)
	e.drv.advanceTo(5 * time.Minute) // ein Zwischenstand
	if got := e.srv.startWorld; got != "creative-anfang" {
		t.Fatalf("der Server startete mit %q, erwartet den Stand der zweiten Welt", got)
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Erste Welt: lokal und auf dem Koordinator unverändert.
	if b, err := os.ReadFile(filepath.Join(first, "level.dat")); err != nil || string(b) != "mein erster Stand" {
		t.Fatalf("lokale erste Welt verändert: %q %v", b, err)
	}
	if g, err := co.c.ForWorld("w1").Head(t.Context()); err != nil || g == nil || g.Number != h1.Number || g.WorldID != h1.WorldID {
		t.Fatalf("Stand der ersten Welt: %+v %v", g, err)
	}
	if st, err := group.LoadState(); err != nil || st.LastUploadedWorldID != h1.WorldID || st.LastUploadedGeneration != h1.Number || st.PendingUpload {
		t.Fatalf("Zustand der ersten Welt: %+v %v", st, err)
	}
	// Zweite Welt: ihr Ordner, ihr Zustand, ihr Verlauf.
	w2Dir := filepath.Join(group.Home(), "welten", w2.ID, "server", WorldName)
	if b, err := os.ReadFile(filepath.Join(w2Dir, "level.dat")); err != nil || len(b) == 0 {
		t.Fatalf("Welt im Ordner der zweiten Welt: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(w2Dir, "datapacks", "x.zip")); err != nil {
		t.Fatalf("Datei der zweiten Welt nicht geladen: %v", err)
	}
	now, err := cw.Head(t.Context())
	if err != nil || now == nil || now.WorldID != h2.WorldID || now.Number <= h2.Number {
		t.Fatalf("Stand der zweiten Welt: %+v %v", now, err)
	}
	ws, err := group.LoadWorldState(w2.ID, &group.State{MachineID: hostMachine})
	if err != nil || ws.LastUploadedWorldID != h2.WorldID || ws.LastUploadedGeneration != now.Number || ws.PendingUpload {
		t.Fatalf("Zustand der zweiten Welt: %+v %v (Head %d)", ws, err, now.Number)
	}
	// Die Host-Einstellungen der Welt kamen mit Spielmodus und Schwierigkeit.
	// (Der Test-Server schreibt sie nicht; der Koordinator-Test prüft den Inhalt.)
}

// Eine noch leere Welt (frisch angelegt): Die Sitzung lädt nichts, lässt
// Minecraft die Welt anlegen (hier: der Fake-Server) und reicht den ersten
// Stand auf der Basis ""/0 ein; die erste Welt bleibt unberührt.
func TestSitzungInLeererWeltLegtFrischAn(t *testing.T) {
	co := newCoord(t)
	h1 := co.seed(t, seedFiles)
	w2, cw := zweiteWelt(t, co)

	e := newWorldEnv(t, co, w2.ID, nil, []string{"Alex"})
	e.d.Fresh = true
	if e.d.Base != 0 || e.d.BaseWorldID != "" {
		t.Fatalf("Basis einer leeren Welt: %q/%d", e.d.BaseWorldID, e.d.Base)
	}
	world := filepath.Join(e.dir, WorldName)
	// Ein alter, nie eingereichter Rest liegt da: Er wird beiseitegelegt.
	writeTree(t, world, map[string]string{"level.dat": "alter Rest"})
	e.srv.onPrepare = func() {
		if _, err := os.Stat(world); err == nil {
			t.Error("die alte Welt lag beim Vorbereiten noch im Ordner")
		}
		writeTree(t, world, map[string]string{"level.dat": "von Minecraft frisch angelegt"})
	}
	r := e.start(t)
	e.drv.advanceTo(5 * time.Minute)
	if got := e.srv.startWorld; got != "von Minecraft frisch angelegt" {
		t.Fatalf("Server startete mit %q", got)
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	g, err := cw.Head(t.Context())
	if err != nil || g == nil || g.Number < 1 || g.WorldID == h1.WorldID {
		t.Fatalf("Stand der zuvor leeren Welt: %+v %v", g, err)
	}
	if genSHA(t, g, "level.dat") == sha("alter Rest") {
		t.Fatalf("der Stand enthält den alten Rest statt der frischen Welt")
	}
	if matches, _ := filepath.Glob(filepath.Join(group.Home(), "welten", w2.ID, "verworfen-*")); len(matches) != 1 {
		t.Fatalf("der alte Rest liegt nicht beiseite (bei der Welt): %v", matches)
	}
	if h, err := co.c.ForWorld("w1").Head(t.Context()); err != nil || h.Number != h1.Number {
		t.Fatalf("erste Welt: %+v %v", h, err)
	}
	if st, _ := group.LoadWorldState(w2.ID, &group.State{}); st.LastUploadedWorldID != g.WorldID {
		t.Fatalf("Zustand der zweiten Welt: %+v", st)
	}
}

// Ein vorgemerkter Stand der zweiten Welt wird nur in sie nachgereicht; ein
// überholter wird beiseitegelegt – bei der zweiten Welt, nie bei der ersten.
func TestNachreichenInZweiterWelt(t *testing.T) {
	pendingWorld := map[string]string{"level.dat": "vorgemerkt in w2", "region/r.0.0.mca": "chunks"}
	t.Run("nachreichen", func(t *testing.T) {
		co := newCoord(t)
		h1 := co.seed(t, seedFiles)
		w2, cw := zweiteWelt(t, co)
		h2 := seedIn(t, cw, seedFiles)
		dir, _ := group.ServerDir(w2.ID)
		writeTree(t, filepath.Join(dir, WorldName), pendingWorld)
		st, _ := group.LoadWorldState(w2.ID, &group.State{MachineID: hostMachine})
		st.PendingUpload, st.LastUploadedWorldID, st.LastUploadedGeneration = true, h2.WorldID, h2.Number

		if err := Recover(t.Context(), cw, st, dir); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		now, _ := cw.Head(t.Context())
		if now.WorldID != h2.WorldID || now.Number != h2.Number+1 || genSHA(t, now, "level.dat") != sha(pendingWorld["level.dat"]) {
			t.Fatalf("Stand der zweiten Welt: %+v", now)
		}
		if g, _ := co.c.ForWorld("w1").Head(t.Context()); g.Number != h1.Number {
			t.Fatalf("erste Welt hat sich verändert: %+v", g)
		}
		loaded, _ := group.LoadWorldState(w2.ID, &group.State{})
		checkUploaded(t, loaded, now)
		if _, err := os.Stat(filepath.Join(group.Home(), "state.json")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("der Zustand der ersten Welt wurde angelegt/angefasst: %v", err)
		}
	})
	t.Run("ueberholt: beiseite bei der Welt", func(t *testing.T) {
		co := newCoord(t)
		co.seed(t, seedFiles)
		w2, cw := zweiteWelt(t, co)
		h2 := seedIn(t, cw, seedFiles)
		seedIn(t, cw, map[string]string{"level.dat": "anderer Host"})
		dir, _ := group.ServerDir(w2.ID)
		world := filepath.Join(dir, WorldName)
		writeTree(t, world, pendingWorld)
		st, _ := group.LoadWorldState(w2.ID, &group.State{MachineID: hostMachine})
		st.PendingUpload, st.LastUploadedWorldID, st.LastUploadedGeneration = true, h2.WorldID, h2.Number

		err := Recover(t.Context(), cw, st, dir)
		var de *DiscardedError
		if !errors.As(err, &de) {
			t.Fatalf("Recover = %v, erwartet DiscardedError", err)
		}
		if want := filepath.Join(group.Home(), "welten", w2.ID); filepath.Dir(de.Path) != want || !strings.HasPrefix(filepath.Base(de.Path), "verworfen-") {
			t.Fatalf("verworfen nach %s, erwartet unter %s", de.Path, want)
		}
		if m, _ := filepath.Glob(filepath.Join(group.Home(), "verworfen-*")); len(m) != 0 {
			t.Fatalf("etwas liegt beiseite bei der ersten Welt: %v", m)
		}
	})
}

// Ein Stand baut auf ""/0 auf (leere Welt, nie eingereicht): commitPending
// akzeptiert das; ein nicht mehr leeres Ziel (jemand war schneller) wird
// verworfen statt überschrieben.
func TestNachreichenEinerLeerenWelt(t *testing.T) {
	co := newCoord(t)
	co.seed(t, seedFiles)
	w2, cw := zweiteWelt(t, co)
	dir, _ := group.ServerDir(w2.ID)
	writeTree(t, filepath.Join(dir, WorldName), map[string]string{"level.dat": "frisch, nie hochgeladen"})
	st, _ := group.LoadWorldState(w2.ID, &group.State{MachineID: hostMachine})
	st.PendingUpload = true // Basis ""/0

	if err := Recover(t.Context(), cw, st, dir); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	g, _ := cw.Head(t.Context())
	if g == nil || g.Number != 1 || genSHA(t, g, "level.dat") != sha("frisch, nie hochgeladen") {
		t.Fatalf("Stand der zuvor leeren Welt: %+v", g)
	}
}
