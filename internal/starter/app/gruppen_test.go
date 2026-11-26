package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

func TestMigrateLayoutBehaeltNotfallOrdner(t *testing.T) {
	home := t.TempDir()
	docs := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)
	t.Setenv(notfallDokumenteEnv, docs)
	if err := os.WriteFile(filepath.Join(home, "gruppe.yaml"),
		[]byte("coordinator: https://alt.example.org\ntunnel_server: t:1\ntoken: tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(docs, notfallName), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := notfallOrdnerName(); got != notfallName {
		t.Fatalf("altes Layout: %q", got)
	}
	migrateLayout()
	if group.Legacy() || group.ActiveID() != "alt.example.org" {
		t.Fatalf("nicht migriert: %q", group.ActiveID())
	}
	if got := notfallOrdnerName(); got != notfallName {
		t.Errorf("migrierte Gruppe soll ihren Ordner behalten: %q", got)
	}
	// Eine zweite Gruppe bekommt einen eigenen Ordner.
	if err := group.AddGroup(&group.Group{Coordinator: "https://neu.example.org", TunnelServer: "t:1", Token: "x"}, true); err != nil {
		t.Fatal(err)
	}
	if got := notfallOrdnerName(); got != notfallName+"-neu.example.org" {
		t.Errorf("zweite Gruppe: %q", got)
	}
	// Von Hand verdorbener Ordnername wird ignoriert.
	if err := speichereNotfall(notfallEinstellung{Ordner: "..\\..\\Windows"}); err != nil {
		t.Fatal(err)
	}
	if got := notfallOrdnerName(); got != notfallName+"-neu.example.org" {
		t.Errorf("Pfadtrick: %q", got)
	}
}

// zweiGruppen legt zwei Gruppen an (A aktiv) und liefert eine Instanz, die A
// geladen hat.
func zweiGruppen(t *testing.T) *App {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	for _, c := range []string{"http://127.0.0.1:1", "http://localhost:1"} {
		g := &group.Group{Coordinator: c, DevIdentityEmail: "x@example.org"}
		if err := group.AddGroup(g, c == "http://127.0.0.1:1"); err != nil {
			t.Fatal(err)
		}
	}
	for id, m := range map[string]string{"127.0.0.1-1": "pc-a", "localhost-1": "pc-b"} {
		dir := group.IDDir(id)
		if err := os.WriteFile(filepath.Join(dir, "state.json"),
			[]byte(`{"machine_id":"`+m+`","installed_version":"1"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := recoverWorld
	recoverWorld = func(context.Context, *client.Client, *group.State, string, *worldsync.HashCache, func(string)) error {
		return nil
	}
	t.Cleanup(func() { recoverWorld = old })
	a := busyApp(t, nil)
	if err := a.load(); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSwitchGroupNimmtDatenDerNeuenGruppe(t *testing.T) {
	a := zweiGruppen(t)
	if a.machineID != "pc-a" || host.DefaultDir() != filepath.Join(group.IDDir("127.0.0.1-1"), "server") {
		t.Fatalf("Start: %q %q", a.machineID, host.DefaultDir())
	}
	// Profil von A: Dauerbetrieb an.
	p := bench.DefaultProfile()
	p.Dauerbetrieb = true
	if err := bench.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := a.switchGroup("localhost-1"); err != nil {
		t.Fatal(err)
	}
	if a.machineID != "pc-b" || a.g.Coordinator != "http://localhost:1" || a.st.MachineID != "pc-b" {
		t.Fatalf("nach dem Wechsel: %q %+v", a.machineID, a.g)
	}
	if host.DefaultDir() != filepath.Join(group.IDDir("localhost-1"), "server") {
		t.Errorf("Server-Ordner: %s", host.DefaultDir())
	}
	if _, err := os.Stat(bench.ProfilePath()); err == nil {
		t.Error("B sieht das Profil von A")
	}
	if a.dauer.on.Load() {
		t.Error("Dauerbetrieb von A gilt in B")
	}
	// Schreiben nach dem Wechsel landet in B, nie in A.
	a.st.LastUploadedGeneration = 5
	if err := a.st.Save(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(group.IDDir("127.0.0.1-1"), "state.json")); strings.Contains(string(b), "last_uploaded_generation\": 5") {
		t.Error("Zustand von B in A geschrieben")
	}
	if err := a.switchGroup("gibtesnicht"); err == nil {
		t.Error("unbekannte Gruppe")
	}
	if err := a.switchGroup("../x"); err == nil {
		t.Error("Pfadtrick")
	}
	if got := group.ActiveID(); got != "localhost-1" {
		t.Errorf("aktiv %q", got)
	}
}

func TestSwitchGroupGesperrt(t *testing.T) {
	cases := map[string]func(a *App){
		"hostet":      func(a *App) { a.hostCancel = func() {} },
		"spielt mit":  func(a *App) { a.phase = phJoined },
		"startet":     func(a *App) { a.phase = phStarting },
		"lädt hoch":   func(a *App) { a.phase = phUploading },
		"nachreichen": func(a *App) { a.recoverLoop = true },
		"vorladen":    func(a *App) { a.prefetchCancel = func() {} },
		"vorgang":     func(a *App) { a.busy = "Welt übernehmen" },
		"welt belegt": func(a *App) { a.world <- struct{}{} },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			a := zweiGruppen(t)
			set(a)
			err := a.switchGroup("localhost-1")
			if err == nil {
				t.Fatal("Wechsel hätte gesperrt sein müssen")
			}
			if a.wechselSperre() == "" {
				t.Error("wechselSperre meldet nichts")
			}
			if group.ActiveID() != "127.0.0.1-1" || a.machineID != "pc-a" {
				t.Errorf("trotz Sperre gewechselt: %q %q", group.ActiveID(), a.machineID)
			}
			if !strings.Contains(err.Error(), "Gruppe wechseln") && !strings.Contains(err.Error(), "wechsle dann die Gruppe") {
				t.Errorf("Meldung: %v", err)
			}
		})
	}
}

func TestZustandListetGruppen(t *testing.T) {
	a := zweiGruppen(t)
	a.setTitel("localhost-1", "Zweite Gruppe")
	l, aktiv := a.gruppenListe()
	if aktiv != "127.0.0.1-1" || len(l) != 2 || !l[0].Aktiv || l[1].Aktiv || l[1].Titel != "Zweite Gruppe" {
		t.Fatalf("%v %q", l, aktiv)
	}
	if !strings.Contains(a.trayGruppe(), "127.0.0.1-1") {
		t.Errorf("Tray: %q", a.trayGruppe())
	}
}

func TestFensterGruppeWechseln(t *testing.T) {
	probeAnzeige(t)
	ft := startFenster(t, nil) // setzt WANDERWELT_HOME
	for _, c := range []string{"http://127.0.0.1:1", "http://localhost:1"} {
		if err := group.AddGroup(&group.Group{Coordinator: c, DevIdentityEmail: "x@example.org"}, c == "http://127.0.0.1:1"); err != nil {
			t.Fatal(err)
		}
		dir := group.IDDir(map[string]string{"http://127.0.0.1:1": "127.0.0.1-1", "http://localhost:1": "localhost-1"}[c])
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"machine_id":"pc","installed_version":"1"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := recoverWorld
	recoverWorld = func(context.Context, *client.Client, *group.State, string, *worldsync.HashCache, func(string)) error {
		return nil
	}
	t.Cleanup(func() { recoverWorld = old })
	if err := ft.a.load(); err != nil {
		t.Fatal(err)
	}
	z := ft.a.zustand()
	if len(z.Gruppen) != 2 || z.WechselSperre != "" {
		t.Fatalf("Zustand: %+v %q", z.Gruppen, z.WechselSperre)
	}
	// Beim Hosten gesperrt, mit klarer Meldung.
	ft.a.mu.Lock()
	ft.a.hostCancel = func() {}
	ft.a.mu.Unlock()
	if z := ft.a.zustand(); !strings.Contains(z.WechselSperre, "hostest") {
		t.Errorf("Sperre im Zustand: %q", z.WechselSperre)
	}
	code, body := ft.mit(t, http.MethodPost, "/v1/gruppe", `{"id":"localhost-1"}`)
	if code != http.StatusConflict || !strings.Contains(fehlerText(t, body), "hostest") || group.ActiveID() != "127.0.0.1-1" {
		t.Fatalf("beim Hosten: %d %s aktiv %q", code, body, group.ActiveID())
	}
	ft.a.mu.Lock()
	ft.a.hostCancel = nil
	ft.a.mu.Unlock()
	if code, body := ft.mit(t, http.MethodPost, "/v1/gruppe", `{"id":"localhost-1"}`); code != http.StatusOK || group.ActiveID() != "localhost-1" {
		t.Fatalf("Wechsel: %d %s aktiv %q", code, body, group.ActiveID())
	}
	if code, _ := ft.mit(t, http.MethodPost, "/v1/gruppe", `{"id":"../.."}`); code != http.StatusConflict {
		t.Errorf("Pfadtrick: %d", code)
	}
	if code, _ := ft.mit(t, http.MethodPost, "/v1/gruppe", `müll`); code != http.StatusBadRequest {
		t.Errorf("Müll: %d", code)
	}
}
