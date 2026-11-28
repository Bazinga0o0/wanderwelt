package app

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// statusSeq liefert der Reihe nach die Stände aus seq (der letzte bleibt).
type statusSeq struct {
	mu   sync.Mutex
	seq  []statusStep
	call int
}

type statusStep struct {
	st  *api.Status
	err error
}

func (s *statusSeq) get(context.Context) (*api.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.seq[min(s.call, len(s.seq)-1)]
	s.call++
	return st.st, st.err
}

func hostStatus(email string, ready bool) statusStep {
	return statusStep{st: &api.Status{Host: &api.HostInfo{Email: email, MinecraftName: "Alice"}, HostReady: ready}}
}

// I1 (d): Nach „join“ beobachtet watchHost den Host – bereit erst bei
// host_ready, und verschwindet er (vorher oder in den Minuten danach),
// endet es mit hostGone (der Starter fragt dann von selbst neu).
func TestWatchHost(t *testing.T) {
	const a = "alice@example.org"
	noHost := statusStep{st: &api.Status{}}
	for _, tc := range []struct {
		name     string
		seq      []statusStep
		want     watchResult
		ready    int           // so oft ready
		readyMax time.Duration // 0: 10 s
	}{
		{"bereit und bleibt", []statusStep{hostStatus(a, false), hostStatus(a, false), hostStatus(a, true)}, watchDone, 1, 0},
		{"verschwindet vor der Bereitschaft", []statusStep{hostStatus(a, false), noHost}, hostGone, 0, 0},
		{"verschwindet nach der Bereitschaft", []statusStep{hostStatus(a, true), hostStatus(a, true), noHost}, hostGone, 1, 0},
		{"anderer Host", []statusStep{hostStatus(a, false), hostStatus("bob@example.org", true)}, hostGone, 0, 0},
		{"Groß/Klein egal", []statusStep{hostStatus("Alice@Example.org", true)}, watchDone, 1, 0},
		{"Fehler werden übergangen", []statusStep{{err: errors.New("weg")}, hostStatus(a, false), {err: errors.New("weg")}, hostStatus(a, true)}, watchDone, 1, 0},
		{"nie bereit", []statusStep{hostStatus(a, false)}, hostNeverReady, 0, 100 * time.Millisecond},
		{"Übergabe: Host wechselt auf das Ziel", []statusStep{hostStatus(a, true), handoverStep("", api.HandoverReady, "bob@example.org"), handoverStep("bob@example.org", api.HandoverReady, "bob@example.org")}, watchHandedOver, 1, 0},
		{"Übergabe: kurz kein Host", []statusStep{hostStatus(a, true), handoverStep("", api.HandoverReady, "bob@example.org"), handoverStep("", api.HandoverReady, "bob@example.org"), hostStatus(a, true)}, watchDone, 1, 0},
		{"Übergabe gescheitert: Host weg", []statusStep{hostStatus(a, true), handoverStep("", api.HandoverFailed, "bob@example.org")}, hostGone, 1, 0},
		{"anderer Host, nicht das Ziel", []statusStep{hostStatus(a, true), handoverStep("carl@example.org", api.HandoverReady, "bob@example.org")}, hostGone, 1, 0},
		{"Übergabe: Host=Ziel, Handover schon nil (Reihenfolge des Koordinators)", []statusStep{hostStatus(a, true), handoverStep("", api.HandoverStarting, "bob@example.org"), handoverStep("", api.HandoverReady, "bob@example.org"), hostStatus("bob@example.org", true)}, watchHandedOver, 1, 0},
		{"Übergabe gesehen, Host wird jemand anderes", []statusStep{hostStatus(a, true), handoverStep("", api.HandoverReady, "bob@example.org"), hostStatus("carl@example.org", true)}, hostGone, 1, 0},
		{"Übergabe gescheitert gesehen, dann Ziel hostet", []statusStep{hostStatus(a, true), handoverStep("", api.HandoverReady, "bob@example.org"), handoverStep("", api.HandoverFailed, "bob@example.org"), hostStatus("bob@example.org", true)}, hostGone, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &statusSeq{seq: tc.seq}
			readies := 0
			got := watchHost(t.Context(), s.get, api.HostInfo{Email: a}, time.Millisecond, 20*time.Millisecond, cmp.Or(tc.readyMax, 10*time.Second),
				func(st *api.Status) {
					if !st.HostReady {
						t.Errorf("ready mit nicht bereitem Host")
					}
					readies++
				})
			if got != tc.want || readies != tc.ready {
				t.Fatalf("watchHost = %v (ready %d×), erwartet %v (ready %d×)", got, readies, tc.want, tc.ready)
			}
		})
	}
	t.Run("Abbruch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		s := &statusSeq{seq: []statusStep{hostStatus(a, false)}}
		time.AfterFunc(20*time.Millisecond, cancel)
		if got := watchHost(ctx, s.get, api.HostInfo{Email: a}, time.Millisecond, time.Hour, time.Hour, func(*api.Status) {}); got != watchCanceled {
			t.Fatalf("watchHost nach Abbruch = %v", got)
		}
	})
}

// I4: Die Spielerliste im Dialog „Spiel für mich beenden“ kommt aus dem
// Status, wenn das eigene Konto hostet (der Status nennt keine MachineID).
func TestOwnPlayers(t *testing.T) {
	me := &api.Me{Email: "alice@example.org"}
	st := &api.Status{Host: &api.HostInfo{Email: "Alice@example.org"}, Players: []string{"Alice", "Bob"}}
	if got := ownPlayers(st, me); !slices.Equal(got, []string{"Alice", "Bob"}) {
		t.Fatalf("eigener Host: %v", got)
	}
	other := &api.Status{Host: &api.HostInfo{Email: "bob@example.org"}, Players: []string{"Bob"}}
	for _, tc := range []struct {
		st *api.Status
		me *api.Me
	}{{other, me}, {&api.Status{}, me}, {nil, me}, {st, nil}, {st, &api.Me{}}} {
		if got := ownPlayers(tc.st, tc.me); got != nil {
			t.Fatalf("ownPlayers(%+v, %+v) = %v, erwartet nichts", tc.st, tc.me, got)
		}
	}
}

// Endet der Tray, während gehostet wird, wird das Hosten erst sauber
// beendet; gewartet wird höchstens die Frist.
func TestFinishHostingWaitsForSession(t *testing.T) {
	a := &App{}
	done := make(chan struct{})
	var canceled atomic.Bool
	a.hostCancel = func() {
		canceled.Store(true)
		time.AfterFunc(50*time.Millisecond, func() { close(done) })
	}
	a.hostDone = done
	start := time.Now()
	a.finishHosting(10 * time.Second)
	if !canceled.Load() {
		t.Fatal("Hosten nicht beendet")
	}
	select {
	case <-done:
	default:
		t.Fatal("finishHosting kehrte vor dem Ende der Sitzung zurück")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("finishHosting brauchte %v", d)
	}

	// Endet die Sitzung nicht, gilt die Frist.
	b := &App{hostCancel: func() {}, hostDone: make(chan struct{})}
	start = time.Now()
	b.finishHosting(100 * time.Millisecond)
	if d := time.Since(start); d < 100*time.Millisecond || d > 5*time.Second {
		t.Fatalf("Frist: %v", d)
	}
	// Ohne Sitzung: sofort.
	(&App{}).finishHosting(time.Hour)
}

// 503 von tunneld (Koordinator kurz nicht erreichbar) wird wie 401 einmal
// nach 2 s wiederholt; bleibt es bei 401, nennt der Text auch ein noch
// nicht freigeschaltetes Konto.
func TestRetryOn503And401(t *testing.T) {
	var calls atomic.Int64
	var codes []int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		code := codes[min(int(n)-1, len(codes)-1)]
		w.WriteHeader(code)
		if code == http.StatusOK {
			w.Write([]byte(`{"email":"a@example.org"}`))
		}
	}))
	defer ts.Close()
	c := client.New(&group.Group{Coordinator: ts.URL})
	ctx := t.Context()

	codes = []int{http.StatusServiceUnavailable, http.StatusOK}
	me, err := retry401(ctx, func() (*api.Me, error) { return c.Me(ctx) })
	if err != nil || me.Email != "a@example.org" || calls.Load() != 2 {
		t.Fatalf("503 → 200: %+v, %v nach %d Anfragen", me, err, calls.Load())
	}
	calls.Store(0)
	codes = []int{http.StatusUnauthorized}
	_, err = retry401(ctx, func() (*api.Me, error) { return c.Me(ctx) })
	if !errors.Is(err, errTokenInvalid) || calls.Load() != 2 || !strings.Contains(explain(err), "freigeschaltet") {
		t.Fatalf("401 → 401: %v (%q) nach %d Anfragen", err, explain(err), calls.Load())
	}
	calls.Store(0)
	codes = []int{http.StatusInternalServerError}
	if _, err := retry401(ctx, func() (*api.Me, error) { return c.Me(ctx) }); err == nil || calls.Load() != 1 {
		t.Fatalf("500: %v nach %d Anfragen, erwartet keine Wiederholung", err, calls.Load())
	}
}

// Import: Öffnet jemand die Welt in Minecraft, während sie kopiert wird,
// wird nichts eingereicht (Prüfung nach dem Kopieren, vor dem Commit).
func TestImportRechecksWorldOpenAfterCopy(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, StorageBudget: 1 << 30,
		Members: []config.Member{{Email: "admin@example.org", Minecraft: "Admin", Admin: true}}}
	cfg.Auth.Mode = "dev"
	lm, err := lease.New(filepath.Join(dir, "lease.json"), server.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(cfg, lm, st, portholder.Noop(), nil))
	defer ts.Close()
	c := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "admin@example.org"})

	saveDir := filepath.Join(t.TempDir(), "Meine Welt")
	if err := os.MkdirAll(filepath.Join(saveDir, "region"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"level.dat": "level", "region/r.0.0.mca": "chunks"} {
		if err := os.WriteFile(filepath.Join(saveDir, filepath.FromSlash(name)), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	save := SaveInfo{Dir: saveDir, Folder: "Meine Welt"}
	old := worldOpenAfterCopy
	t.Cleanup(func() { worldOpenAfterCopy = old })
	var checked atomic.Int64
	worldOpenAfterCopy = func(d string) bool {
		checked.Add(1)
		if _, err := os.Stat(filepath.Join(group.Home(), importStagingName, "level.dat")); err != nil {
			t.Errorf("Prüfung vor dem Kopieren: %v", err)
		}
		return d == saveDir
	}
	ctx := t.Context()
	_, err = importWorld(ctx, c, "pc-admin", save, "26.2", nil, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "geöffnet") || checked.Load() != 1 {
		t.Fatalf("importWorld = %v (%d Prüfungen), erwartet Abbruch", err, checked.Load())
	}
	if head, err := c.Head(ctx); err != nil || head != nil {
		t.Fatalf("Head nach Abbruch: %+v, %v", head, err)
	}
	if s, err := c.Status(ctx); err != nil || s.Host != nil {
		t.Fatalf("Zuschlag nach Abbruch: %+v, %v", s, err)
	}

	// Bleibt die Welt zu, wird übernommen.
	worldOpenAfterCopy = func(string) bool { return false }
	gen, err := importWorld(ctx, c, "pc-admin", save, "26.2", nil, func(string) {})
	if err != nil || gen.Number != 1 {
		t.Fatalf("importWorld = %+v, %v", gen, err)
	}
}

// ---- B1 (Gesamtlauf): Ergebnisfenster erst nach dem Ende der Aktion ----

// dialogProbe ersetzt showInfo und showError. Ein modales Fenster bleibt
// offen, bis jemand es wegklickt (im Gesamtlauf 6 Minuten); beim Anzeigen
// hält die Probe fest, ob gerade „Spielen“ beginnen könnte und ob der
// Welt-Ordner frei ist.
type dialogProbe struct {
	a     *App
	mu    sync.Mutex
	shown []shownDialog
}

type shownDialog struct {
	text        string
	busy        string // beim Anzeigen noch laufende Aktion ("" = keine)
	worldHolder string // beim Anzeigen belegter Welt-Ordner ("" = frei)
}

func probeDialogs(t *testing.T, a *App) *dialogProbe {
	t.Helper()
	p := &dialogProbe{a: a}
	oldInfo, oldErr, oldProgress, oldNotify := showInfo, showError, openImportProgress, notify
	t.Cleanup(func() { showInfo, showError, openImportProgress, notify = oldInfo, oldErr, oldProgress, oldNotify })
	showInfo, showError = p.show, p.show
	openImportProgress = func() (zenity.ProgressDialog, error) { return nil, errors.New("kein Fenster im Test") }
	notify = func(string) {}
	return p
}

func (p *dialogProbe) show(text string) {
	d := shownDialog{text: text}
	if ok, running := p.a.begin("Spielen"); ok {
		p.a.end()
	} else {
		d.busy = running
	}
	if p.a.tryWorld("host") {
		p.a.releaseWorld()
	} else {
		p.a.mu.Lock()
		d.worldHolder = cmp.Or(p.a.worldHolder, "?")
		p.a.mu.Unlock()
	}
	p.mu.Lock()
	p.shown = append(p.shown, d)
	p.mu.Unlock()
}

// check: genau ein Fenster mit want, angezeigt bei freier Aktion und
// freiem Welt-Ordner.
func (p *dialogProbe) check(t *testing.T, want string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.shown) != 1 || !strings.Contains(p.shown[0].text, want) {
		t.Fatalf("angezeigt: %+v, erwartet ein Fenster mit %q", p.shown, want)
	}
	if d := p.shown[0]; d.busy != "" || d.worldHolder != "" {
		t.Errorf("Fenster %q kam, während „%s“ noch lief (Welt-Ordner: %q) – „Spielen“ wäre bis zum Wegklicken gesperrt",
			d.text, d.busy, d.worldHolder)
	}
	if ok, running := p.a.begin("Spielen"); !ok {
		t.Errorf("danach läuft noch „%s“", running)
	} else {
		p.a.end()
	}
}

// importCoordinator startet einen Koordinator (Dev-Anmeldung) mit einem
// Admin und Manifest-Version 26.2.
func importCoordinator(t *testing.T) *client.Client {
	t.Helper()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifest, []byte("minecraft_version: \"26.2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: dir, StorageBudget: 1 << 30, ManifestFile: manifest,
		Members: []config.Member{{Email: "admin@example.org", Minecraft: "Admin", Admin: true}}}
	cfg.Auth.Mode = "dev"
	lm, err := lease.New(filepath.Join(dir, "lease.json"), server.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(cfg, lm, st, portholder.Noop(), nil))
	t.Cleanup(ts.Close)
	return client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "admin@example.org"})
}

func busyApp(t *testing.T, c *client.Client) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &App{ctx: ctx, cancel: cancel, c: c, machineID: "pc-admin",
		world: make(chan struct{}, 1), refreshCh: make(chan struct{}, 1),
		playCh: make(chan struct{}, 1), quitCh: make(chan struct{})}
}

func TestResultDialogAfterBusyEnds(t *testing.T) {
	c := importCoordinator(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kaputt", http.StatusInternalServerError)
	}))
	defer broken.Close()
	brokenClient := client.New(&group.Group{Coordinator: broken.URL, DevIdentityEmail: "admin@example.org"})

	t.Run("Welt übernehmen: Ergebnis", func(t *testing.T) {
		t.Setenv("WANDERWELT_HOME", t.TempDir())
		saves := t.TempDir()
		writeSave(t, saves, "Meine Welt", levelDat("26.2", false, testUUID), true)
		a := busyApp(t, c)
		p := probeDialogs(t, a)
		a.importFromControl(importRequest{Dir: filepath.Join(saves, "Meine Welt")})
		p.check(t, "ist jetzt die Gruppenwelt")
		if head, err := c.Head(t.Context()); err != nil || head == nil || head.Number != 1 {
			t.Fatalf("Head nach dem Übernehmen: %+v, %v", head, err)
		}
	})
	t.Run("Welt übernehmen: Welt abgelehnt", func(t *testing.T) {
		t.Setenv("WANDERWELT_HOME", t.TempDir())
		saves := t.TempDir()
		writeSave(t, saves, "Alt", levelDat("1.21.4", false, testUUID), true)
		a := busyApp(t, c)
		p := probeDialogs(t, a)
		a.importFromControl(importRequest{Dir: filepath.Join(saves, "Alt")})
		p.check(t, "Welten vor 26.1")
	})
	t.Run("Welt übernehmen: Koordinator antwortet nicht", func(t *testing.T) {
		t.Setenv("WANDERWELT_HOME", t.TempDir())
		a := busyApp(t, brokenClient)
		p := probeDialogs(t, a)
		a.importFromControl(importRequest{Dir: t.TempDir()})
		p.check(t, "Koordinator:")
	})
	t.Run("Welt übernehmen beim Hosten", func(t *testing.T) {
		a := busyApp(t, c)
		a.hostCancel = func() {}
		p := probeDialogs(t, a)
		a.importFromControl(importRequest{Dir: t.TempDir()})
		p.check(t, "Während du hostest")
	})
	t.Run("Welt übernehmen im Tray: keine Welten", func(t *testing.T) {
		t.Setenv("WANDERWELT_HOME", t.TempDir())
		t.Setenv("WANDERWELT_MINECRAFT_DIR", t.TempDir())
		a := busyApp(t, c)
		p := probeDialogs(t, a)
		a.importDialog()
		p.check(t, "keine Minecraft-Welten")
	})
	t.Run("Gruppendatei laden beim Hosten", func(t *testing.T) {
		a := busyApp(t, c)
		a.hostCancel = func() {}
		p := probeDialogs(t, a)
		a.reloadGroup()
		p.check(t, "nicht tauschen")
	})
	// a.fail zeigt keinen modalen Dialog, sondern einen Hinweis (go notify):
	// Auch ein hängender Hinweis hält „Spielen“ nicht fest.
	t.Run("Spielen gescheitert", func(t *testing.T) {
		a := busyApp(t, brokenClient)
		probeDialogs(t, a)
		hang := make(chan struct{})
		defer close(hang)
		notify = func(string) { <-hang }
		a.startPlay(0)
		deadline := time.Now().Add(10 * time.Second)
		for {
			a.mu.Lock()
			busy, ph := a.busy, a.phase
			a.mu.Unlock()
			if busy == "" && ph == phError {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("„Spielen“ nach dem Fehler noch belegt (%q, Zustand %v)", busy, ph)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// handoverStep: Status mit Übergabe an to; newHost "": kein Host.
func handoverStep(newHost, state, to string) statusStep {
	st := &api.Status{Handover: &api.HandoverState{State: state, To: api.HostInfo{Email: to, MinecraftName: "Bob"}}}
	switch {
	case newHost != "":
		st.Host = &api.HostInfo{Email: newHost, MinecraftName: newHost}
		st.HostReady = true
	default:
	}
	return statusStep{st: st}
}
