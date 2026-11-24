package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// fakeACPower stellt den Netzstatus für den Dauerbetrieb.
func fakeACPower(t *testing.T, on bool) *atomic.Bool {
	t.Helper()
	ac := new(atomic.Bool)
	ac.Store(on)
	old := onACPower
	onACPower = ac.Load
	t.Cleanup(func() { onACPower = old })
	return ac
}

// fakeProgress ist ein Fortschrittsfenster, das sich die Werte merkt.
type fakeProgress struct {
	max  int
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	values   []int
	early    []int // Werte ≥ max vor Complete (zenity: „OK“ klickbar)
	complete bool
}

func (f *fakeProgress) Text(string) error { return nil }
func (f *fakeProgress) MaxValue() int     { return f.max }
func (f *fakeProgress) Done() <-chan struct{} {
	return f.done
}

func (f *fakeProgress) Value(v int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.complete && v >= f.max {
		f.early = append(f.early, v)
	}
	f.values = append(f.values, v)
	return nil
}

func (f *fakeProgress) Complete() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.complete = true
	return nil
}

func (f *fakeProgress) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}

// Beim Einrichten zeigt der Balken den Gesamtfortschritt: Er erreicht den
// Höchstwert (bei zenity wird „OK“ klickbar, und OK bricht ab) nie, bevor
// alles geladen ist – auch nicht, wenn eine einzelne Datei 100 % erreicht –,
// und läuft nie zurück. Fertig ist er erst mit Complete.
func TestInstallProgressReachesEndOnlyWhenDone(t *testing.T) {
	dlg := &fakeProgress{max: depsProgressMax, done: make(chan struct{})}
	oldOpen, oldEnsure := openDepsProgress, ensureDeps
	t.Cleanup(func() { openDepsProgress, ensureDeps = oldOpen, oldEnsure })
	openDepsProgress = func() (zenity.ProgressDialog, error) { return dlg, nil }
	m := &api.Manifest{
		Java:         api.Artifact{Name: "java"},
		FabricServer: api.Artifact{Name: "fabric-server"},
		Tunnel:       api.Artifact{Name: "tunnel"},
		Mods:         []api.Artifact{{Name: "lithium"}, {Name: "c2me-fabric"}, {Name: "ferritecore"}},
	}
	var completeDuring bool
	ensureDeps = func(_ context.Context, gotM *api.Manifest, _ api.Profile, _ deps.ArtifactSource, progress func(string, int64, int64)) (*deps.Paths, error) {
		// Fabric-Server liegt schon da (kein Aufruf), der tunnel ohne
		// bekannte Größe; jede Datei bis 100 %.
		for _, name := range []string{"java", "tunnel", "lithium", "ferritecore"} {
			total := int64(4000)
			if name == "tunnel" {
				total = -1
			}
			for done := int64(0); done <= 4000; done += 1000 {
				progress(name, done, total)
			}
		}
		dlg.mu.Lock()
		completeDuring = dlg.complete
		dlg.mu.Unlock()
		return &deps.Paths{}, nil
	}
	if err := ensureDepsDialog(t.Context(), nil, m, api.Profile{}); err != nil {
		t.Fatalf("ensureDepsDialog: %v", err)
	}
	dlg.mu.Lock()
	defer dlg.mu.Unlock()
	if len(dlg.early) > 0 || completeDuring {
		t.Fatalf("Höchstwert vor dem Ende erreicht: %v (Complete während des Ladens: %v)", dlg.early, completeDuring)
	}
	if !dlg.complete {
		t.Fatal("am Ende kein Complete")
	}
	for i := 1; i < len(dlg.values); i++ {
		if dlg.values[i] <= dlg.values[i-1] {
			t.Fatalf("Balken läuft nicht stetig vorwärts: %v", dlg.values)
		}
	}
	// Fünf Abschnitte (C2ME fehlt ohne Haken im Profil); der letzte (Ferrite
	// Core) voll: knapp unter dem Höchstwert.
	if last := dlg.values[len(dlg.values)-1]; last != depsProgressMax-1 {
		t.Fatalf("letzter Wert %d, erwartet %d (Werte %v)", last, depsProgressMax-1, dlg.values)
	}
	if first := dlg.values[0]; first >= depsProgressMax/5 {
		t.Fatalf("erste Datei (Java) springt schon auf %d", first)
	}
}

// Wird ein vorgemerkter Stand später nachgereicht, gibt es genau einen
// Hinweis. Versucht wird sofort, wenn der Koordinator nach Fehlern wieder
// antwortet, nicht erst im 2-min-Takt; eine Antwort ohne vorherigen Fehler
// löst keinen Versuch aus.
func TestRecoverLoopRetriesWhenCoordinatorIsBack(t *testing.T) {
	notes := countNotify(t)
	var down atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			http.NotFound(w, r)
			return
		}
		if down.Load() {
			http.Error(w, `{"error":"weg"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(api.Status{})
	}))
	t.Cleanup(ts.Close)
	var calls atomic.Int32
	old := recoverWorld
	recoverWorld = func(_ context.Context, _ *client.Client, st *group.State, _ string, _ *worldsync.HashCache, _ func(string)) error {
		calls.Add(1)
		st.PendingUpload = false
		return nil
	}
	t.Cleanup(func() { recoverWorld = old })
	a := &App{
		ctx:   t.Context(),
		world: make(chan struct{}, 1),
		c:     client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "a@example.org"}),
		st:    &group.State{PendingUpload: true},
		me:    &api.Me{},
	}
	a.startRecoverLoop()

	a.pollStatus(false) // erreichbar, vorher kein Fehler
	down.Store(true)
	a.pollStatus(false) // Fehler
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d Versuche ohne Rückkehr des Koordinators", n)
	}
	down.Store(false)
	a.pollStatus(false) // wieder da
	waitFor(t, "Nachreichen gleich nach der Rückkehr", func() bool { return calls.Load() == 1 })
	waitFor(t, "Hinweis", func() bool { return len(notes()) == 1 })
	if n := notes(); n[0] != "Der vorgemerkte Stand ist jetzt hochgeladen." {
		t.Fatalf("Hinweis %q", n[0])
	}
	waitFor(t, "Nachreichen beendet", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.recoverLoop
	})
	if n := notes(); len(n) != 1 || calls.Load() != 1 {
		t.Fatalf("Hinweise %q, %d Versuche; erwartet genau einen", n, calls.Load())
	}
}

// Scheitert das Dauerbetrieb-Play, zählt der Fehlschlag (Wartezeit), bevor
// die Aktion frei wird: Kein Takt kann dazwischen an der Wartezeit
// vorbei neu anfordern.
func TestDauerFailureCountsBeforeActionIsFree(t *testing.T) {
	countNotify(t)
	fakeACPower(t, true)
	f := installFakeDauerPlay(t, errors.New("Messung gescheitert"))
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	a := &App{ctx: t.Context(), status: freeStatus}
	a.dauer.on.Store(true)
	var (
		armed  atomic.Bool
		mu     sync.Mutex
		busyAt []string // laufende Aktion, als der Fehlschlag zählte
	)
	old := dauerNow
	dauerNow = func() time.Time {
		if armed.Load() {
			b := busyOf(a)
			mu.Lock()
			busyAt = append(busyAt, b)
			mu.Unlock()
		}
		return t0
	}
	t.Cleanup(func() { dauerNow = old })

	if !a.autoPlay() {
		t.Fatal("Takt fordert nicht an")
	}
	waitFor(t, "Play läuft", func() bool { return f.calls.Load() == 1 })
	armed.Store(true)
	f.release()
	waitFor(t, "Aktion beendet", func() bool { return busyOf(a) == "" })
	waitFor(t, "Fehlschlag gezählt", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(busyAt) > 0
	})
	armed.Store(false)
	mu.Lock()
	got := slices.Clone(busyAt)
	mu.Unlock()
	if !slices.Equal(got, []string{dauerLabel}) {
		t.Fatalf("Fehlschlag gezählt bei laufender Aktion %q, erwartet %q", got, dauerLabel)
	}
	if a.autoPlay() {
		t.Fatal("Takt fordert gleich nach dem Fehlschlag wieder an")
	}
}

// „Neuer Versuch in …“ steht nur im Hinweis, wenn wirklich einer kommt:
// nicht bei ausgeschaltetem, pausiertem Dauerbetrieb oder auf Akku.
func TestDauerFailureMentionsRetryOnlyIfOneComes(t *testing.T) {
	ac := fakeACPower(t, true)
	for _, tc := range []struct {
		name          string
		on, paused    bool
		onAC          bool
		wantRetryText bool
	}{
		{"wirkt", true, false, true, true},
		{"aus", false, false, true, false},
		{"pausiert", true, true, true, false},
		{"auf Akku", true, false, false, false},
	} {
		notes := countNotify(t)
		ac.Store(tc.onAC)
		a := &App{ctx: t.Context()}
		a.dauer.on.Store(tc.on)
		a.dauer.paused = tc.paused
		a.dauerFailed(errors.New("Java abgestürzt"))
		waitFor(t, tc.name+": Hinweis", func() bool { return len(notes()) == 1 })
		a.mu.Lock()
		lastErr := a.lastErr
		a.mu.Unlock()
		for what, text := range map[string]string{"Hinweis": notes()[0], "Tray": lastErr} {
			if strings.Contains(text, "neuer Versuch") != tc.wantRetryText || !strings.Contains(text, "Java abgestürzt") {
				t.Errorf("%s: %s %q", tc.name, what, text)
			}
		}
	}
}

// „Spiel für mich beenden“, während das Dauerbetrieb-Play noch startet:
// Das Play endet, der Tray steht wieder auf „bereit“ (nicht „startet“), und
// der Dauerbetrieb pausiert bis zum nächsten „Spielen“.
func TestStopPlayingCancelsStartingDauerPlay(t *testing.T) {
	notes := countNotify(t)
	fakeACPower(t, true)
	started := make(chan struct{})
	old := dauerPlay
	dauerPlay = func(ctx context.Context, a *App) error {
		a.setPhase(phStarting, "misst die Leistung …")
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { dauerPlay = old })
	a := &App{ctx: t.Context(), status: freeStatus}
	a.dauer.on.Store(true)
	if !a.autoPlay() {
		t.Fatal("Takt fordert nicht an")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Play startet nicht")
	}

	a.stopPlaying()
	a.mu.Lock()
	busy, ph := a.busy, a.phase
	a.mu.Unlock()
	if busy != "" || ph != phReady {
		t.Fatalf("nach „Spiel für mich beenden“: Aktion %q, Zustand %v; erwartet keine und bereit", busy, ph)
	}
	if a.dauerbetriebActive() || a.autoPlay() {
		t.Fatal("Dauerbetrieb nicht pausiert")
	}
	waitFor(t, "Hinweis", func() bool { return len(notes()) == 1 })
	if n := notes(); !strings.Contains(n[0], "pausiert") {
		t.Fatalf("Hinweis %q", n[0])
	}
	a.resumeDauerbetrieb("Spielen")
	if !a.dauerbetriebActive() {
		t.Fatal("nach „Spielen“ wirkt der Dauerbetrieb nicht wieder")
	}
}

// In der Testumgebung (eigener Minecraft-Ordner) öffnet „Server bereit“
// keinen Launcher und bittet, ihn selbst zu starten – ohne den Hinweis,
// einen offenen Launcher neu zu starten.
func TestServerReadyInTestEnvironment(t *testing.T) {
	t.Setenv("WANDERWELT_MINECRAFT_DIR", t.TempDir())
	notes := countNotify(t)
	a := &App{ctx: t.Context(), hostCancel: func() {}}
	a.serverReady("mc.example.org:15565", "26.2", false, nil)
	n := notes()
	if len(n) != 1 || !strings.HasSuffix(n[0], " Minecraft-Launcher bitte selbst starten.") || strings.Contains(n[0], launcherHint) {
		t.Fatalf("Hinweise %q", n)
	}
}
