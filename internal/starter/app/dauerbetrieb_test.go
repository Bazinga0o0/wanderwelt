package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// fakeDauerPlay ersetzt dauerPlay: zählt die Aufrufe und wartet, bis gate
// zu ist oder ctx endet; liefert dann err (bei Abbruch den Abbruch).
type fakeDauerPlay struct {
	calls atomic.Int32
	gate  chan struct{}
	once  sync.Once
	err   error
}

func installFakeDauerPlay(t *testing.T, err error) *fakeDauerPlay {
	t.Helper()
	f := &fakeDauerPlay{gate: make(chan struct{}), err: err}
	old := dauerPlay
	dauerPlay = func(ctx context.Context, _ *App) error {
		f.calls.Add(1)
		select {
		case <-f.gate:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() {
		f.release()
		dauerPlay = old
	})
	return f
}

func (f *fakeDauerPlay) release() { f.once.Do(func() { close(f.gate) }) }

// countNotify ersetzt notify und zählt die Hinweise.
func countNotify(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	old := notify
	notify = func(text string) {
		mu.Lock()
		got = append(got, text)
		mu.Unlock()
	}
	t.Cleanup(func() { notify = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// fakeDauerNow stellt die Uhr des Dauerbetriebs.
func fakeDauerNow(t *testing.T, start time.Time) *atomic.Int64 {
	t.Helper()
	var now atomic.Int64
	now.Store(start.UnixNano())
	old := dauerNow
	dauerNow = func() time.Time { return time.Unix(0, now.Load()) }
	t.Cleanup(func() { dauerNow = old })
	return &now
}

func busyOf(a *App) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

var (
	freeStatus   = &api.Status{}
	hostedStatus = &api.Status{Host: &api.HostInfo{Email: "bob@example.org", MinecraftName: "Bob"}}
)

// Der Dauerbetrieb-Takt fordert „Spielen“ nur an, wenn sicher niemand
// hostet (Status bekannt, kein Host) und keine Aktion läuft – so bittet er
// nie um einen zweiten Zuschlag, solange einer besteht, und nie neben einem
// „Spielen“, das gerade läuft.
func TestDauerbetriebTaktOnlyWhenNobodyHosts(t *testing.T) {
	f := installFakeDauerPlay(t, nil)
	for _, tc := range []struct {
		name string
		prep func(a *App)
	}{
		{"anderer PC hostet", func(a *App) { a.status = hostedStatus }},
		{"anderer PC hostet, Aktion läuft", func(a *App) { a.status, a.busy = hostedStatus, "Spielen" }},
		{"Aktion läuft", func(a *App) { a.status, a.busy = freeStatus, "Welt übernehmen" }},
		{"dieser PC hostet", func(a *App) { a.status, a.hostCancel = freeStatus, func() {} }},
		{"Status unbekannt", func(a *App) {}},
		{"Koordinator nicht erreichbar", func(a *App) { a.status, a.statusErr = freeStatus, errors.New("weg") }},
		{"beobachtet als Mitspieler den Host", func(a *App) { a.status, a.joinCancel = freeStatus, func() {} }},
		{"Programm endet", func(a *App) { a.status, a.quitting = freeStatus, true }},
		{"Windows-Sitzung endet", func(a *App) { a.status, a.ending = freeStatus, true }},
		{"pausiert", func(a *App) { a.status, a.dauer.paused = freeStatus, true }},
		{"Wartezeit nach Fehlschlag", func(a *App) { a.status, a.dauer.nextTry = freeStatus, time.Now().Add(time.Minute) }},
	} {
		a := &App{ctx: t.Context()}
		tc.prep(a)
		busy := a.busy
		if a.autoPlay() {
			t.Errorf("%s: Takt fordert Spielen an", tc.name)
		}
		if a.busy != busy {
			t.Errorf("%s: laufende Aktion %q, erwartet %q", tc.name, a.busy, busy)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("%d× Spielen angefordert, erwartet keins", n)
	}

	// Niemand hostet, nichts läuft: genau eine Anforderung; ein Takt,
	// während sie noch läuft, fordert keine zweite an.
	a := &App{ctx: t.Context(), status: freeStatus}
	if !a.autoPlay() {
		t.Fatal("niemand hostet, nichts läuft: Takt fordert nichts an")
	}
	if a.autoPlay() {
		t.Fatal("Takt fordert ein zweites Mal an, während das erste läuft")
	}
	waitFor(t, "Anforderung läuft", func() bool { return f.calls.Load() == 1 })

	// Danach hostet ein PC (hier der Status): kein weiterer Zuschlag.
	f.release()
	waitFor(t, "Aktion beendet", func() bool { return busyOf(a) == "" })
	a.mu.Lock()
	a.status = hostedStatus
	a.mu.Unlock()
	if a.autoPlay() {
		t.Fatal("Takt fordert an, obwohl ein Host besteht")
	}
	time.Sleep(20 * time.Millisecond)
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("%d× Spielen angefordert, erwartet 1", n)
	}
}

// Scheitert das Hosten im Dauerbetrieb wiederholt, gibt es einen Hinweis je
// Fehlerserie, sonst nur Log und Tray-Zustand, und der Takt wartet nach
// jedem weiteren Fehlschlag länger: 1, 2, 4, 8, dann 15 Minuten. Erreicht
// eine Sitzung „bereit“, beginnt alles von vorn.
func TestDauerbetriebBackoffAndSingleNotice(t *testing.T) {
	notes := countNotify(t)
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	now := fakeDauerNow(t, t0)
	fakeACPower(t, true)
	a := &App{ctx: t.Context()}
	a.dauer.on.Store(true) // sonst kommt kein Versuch von selbst (und keine Serie)

	for i, want := range []time.Duration{1, 2, 4, 8, 15, 15} {
		a.hostEnded(errors.New("Java abgestürzt"), false, false, true)
		a.dauer.mu.Lock()
		next := a.dauer.nextTry
		a.dauer.mu.Unlock()
		if got := next.Sub(t0); got != want*time.Minute {
			t.Fatalf("%d. Fehlschlag: nächster Versuch nach %v, erwartet %v", i+1, got, want*time.Minute)
		}
	}
	waitFor(t, "Hinweis", func() bool { return len(notes()) >= 1 })
	time.Sleep(20 * time.Millisecond)
	if n := notes(); len(n) != 1 || !strings.Contains(n[0], "Java abgestürzt") {
		t.Fatalf("Hinweise %q, erwartet genau einen zum Fehler", n)
	}
	a.mu.Lock()
	ph, lastErr := a.phase, a.lastErr
	a.mu.Unlock()
	if ph != phError || !strings.Contains(lastErr, "Java abgestürzt") {
		t.Fatalf("Tray-Zustand %v %q, erwartet Fehler mit Grund", ph, lastErr)
	}

	// Der Takt wartet die Frist ab.
	f := installFakeDauerPlay(t, nil)
	a.mu.Lock()
	a.status, a.phase = freeStatus, phReady
	a.mu.Unlock()
	now.Store(t0.Add(15*time.Minute - time.Second).UnixNano())
	if a.autoPlay() {
		t.Fatal("Takt fordert vor Ablauf der Wartezeit an")
	}
	now.Store(t0.Add(15 * time.Minute).UnixNano())
	if !a.autoPlay() {
		t.Fatal("Takt fordert nach Ablauf der Wartezeit nicht an")
	}
	f.release()
	waitFor(t, "Aktion beendet", func() bool { return busyOf(a) == "" })

	// Bereit: von vorn – nächster Fehlschlag wieder mit Hinweis und 1 min.
	a.dauerReady()
	now.Store(t0.Add(time.Hour).UnixNano())
	a.hostEnded(errors.New("Verbindung weg"), false, false, true)
	waitFor(t, "zweiter Hinweis", func() bool { return len(notes()) == 2 })
	a.dauer.mu.Lock()
	next := a.dauer.nextTry
	a.dauer.mu.Unlock()
	if got := next.Sub(t0.Add(time.Hour)); got != time.Minute {
		t.Fatalf("nach „bereit“: nächster Versuch nach %v, erwartet 1m", got)
	}

	// Eine Sitzung ohne Dauerbetrieb meldet jeden Fehler wie bisher.
	a.hostEnded(errors.New("eins"), false, false, false)
	a.hostEnded(errors.New("zwei"), false, false, false)
	waitFor(t, "Hinweise ohne Dauerbetrieb", func() bool { return len(notes()) == 4 })
}

// „Spiel für mich beenden“ auf einem Dauerbetrieb-PC pausiert den
// Dauerbetrieb bis zum nächsten „Spielen“ (oder Neustart); ohne Haken
// pausiert nichts.
func TestStopHostingPausesDauerbetrieb(t *testing.T) {
	countNotify(t)
	oldAC := onACPower
	onACPower = func() bool { return true }
	t.Cleanup(func() { onACPower = oldAC })
	f := installFakeDauerPlay(t, nil)
	a := &App{ctx: t.Context(), status: freeStatus}
	a.dauer.on.Store(true)
	var canceled atomic.Bool
	a.hostCancel = func() { canceled.Store(true) }

	a.stopHosting()
	if !canceled.Load() {
		t.Fatal("Hosten nicht beendet")
	}
	a.mu.Lock()
	a.hostCancel = nil
	a.mu.Unlock()
	if a.autoPlay() {
		t.Fatal("Takt fordert nach „Spiel für mich beenden“ an")
	}
	if a.dauerbetriebActive() {
		t.Fatal("Dauerbetrieb wirkt trotz Pause")
	}

	// „Spielen“ (hier gleich mit „Moment …“ abgewiesen) hebt die Pause auf.
	a.mu.Lock()
	a.busy = "Welt übernehmen"
	a.mu.Unlock()
	a.Play()
	a.mu.Lock()
	a.busy = ""
	a.mu.Unlock()
	if !a.autoPlay() {
		t.Fatal("Takt fordert nach „Spielen“ nicht wieder an")
	}
	f.release()
	waitFor(t, "Aktion beendet", func() bool { return busyOf(a) == "" })

	b := &App{ctx: t.Context(), status: freeStatus}
	b.hostCancel = func() {}
	b.stopHosting()
	b.mu.Lock()
	b.hostCancel = nil
	b.mu.Unlock()
	b.dauer.on.Store(true) // Haken erst danach gesetzt: keine Pause
	if !b.autoPlay() {
		t.Fatal("ohne Haken beim Beenden pausiert trotzdem")
	}
	waitFor(t, "Aktion beendet", func() bool { return busyOf(b) == "" })
}

// Fährt Windows herunter, während das Dauerbetrieb-Play läuft, hält der
// Starter das Herunterfahren auf (Hosting-Haken), bricht das Play ab und
// wartet auf sein Ende; Hinweis gibt es dafür keinen. Danach hostet der
// Takt nicht mehr.
func TestShutdownDuringDauerPlay(t *testing.T) {
	notes := countNotify(t)
	f := installFakeDauerPlay(t, nil)
	a := &App{ctx: t.Context(), status: freeStatus}
	if a.shutdownHosting() {
		t.Fatal("Haken meldet Hosten ohne Sitzung und ohne Play")
	}
	if !a.autoPlay() {
		t.Fatal("Takt fordert nicht an")
	}
	waitFor(t, "Play läuft", func() bool { return f.calls.Load() == 1 })
	if !a.shutdownHosting() {
		t.Fatal("Haken meldet kein Hosten, während das Dauerbetrieb-Play läuft")
	}
	done := make(chan struct{})
	go func() {
		a.finishForShutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("finishForShutdown kehrt nicht zurück")
	}
	if b := busyOf(a); b != "" {
		t.Fatalf("nach finishForShutdown läuft noch %q", b)
	}
	if a.shutdownHosting() || a.autoPlay() {
		t.Fatal("nach dem Herunterfahren-Beginn hostet der Takt wieder")
	}
	time.Sleep(20 * time.Millisecond)
	if n := notes(); len(n) != 0 {
		t.Fatalf("Hinweise %q beim Abbruch für das Herunterfahren", n)
	}

	// Herunterfahren abgebrochen: Der Takt darf wieder.
	a.mu.Lock()
	a.ending = false
	a.mu.Unlock()
	if !a.autoPlay() {
		t.Fatal("Takt fordert nach abgebrochenem Herunterfahren nicht an")
	}
	f.release()
	waitFor(t, "Aktion beendet", func() bool { return busyOf(a) == "" })
}

// „Spielen“, während das Dauerbetrieb-Play läuft: keine „Moment“-Meldung,
// es wird zum gewöhnlichen Spielen (Fehler mit Hinweis wie bei „Spielen“,
// als Host mit Launcher), und eine Pause ist aufgehoben.
func TestPlayDuringDauerPlayJoinsIt(t *testing.T) {
	notes := countNotify(t)
	f := installFakeDauerPlay(t, errors.New("Koordinator weg"))
	a := &App{ctx: t.Context(), status: freeStatus}
	if !a.autoPlay() {
		t.Fatal("Takt fordert nicht an")
	}
	waitFor(t, "Play läuft", func() bool { return f.calls.Load() == 1 })
	a.Play()
	time.Sleep(20 * time.Millisecond)
	if n := notes(); len(n) != 0 {
		t.Fatalf("Hinweise %q, erwartet keinen", n)
	}
	if !a.dauer.manual.Load() {
		t.Fatal("nach „Spielen“ läuft das Play weiter still (ohne Launcher und Hinweise)")
	}
	f.release()
	waitFor(t, "Hinweis wie bei „Spielen“", func() bool { return len(notes()) == 1 })
	if n := notes(); !strings.HasPrefix(n[0], "Spielen: ") {
		t.Fatalf("Hinweis %q, erwartet den von „Spielen“", n[0])
	}
	a.dauer.mu.Lock()
	fails := a.dauer.fails
	a.dauer.mu.Unlock()
	if fails != 0 {
		t.Fatalf("Fehlschlag zählt für den Dauerbetrieb (%d), obwohl es „Spielen“ war", fails)
	}
}

// waitFor wartet höchstens 5 s darauf, dass cond gilt.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: nach 5 s nicht erreicht", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Dauerbetrieb wirkt nur mit Haken und am Netz; auf Akku ruht er, und ein
// Wechsel gilt sofort (die Sitzung fragt bei jeder Entscheidung neu).
func TestDauerbetriebOnlyOnACPower(t *testing.T) {
	var ac atomic.Bool
	old := onACPower
	onACPower = ac.Load
	t.Cleanup(func() { onACPower = old })
	a := &App{}
	for _, tc := range []struct {
		name         string
		on, ac, want bool
	}{
		{"Haken und am Netz", true, true, true},
		{"Haken, auf Akku", true, false, false},
		{"Haken, wieder am Netz", true, true, true},
		{"ohne Haken am Netz", false, true, false},
		{"ohne Haken auf Akku", false, false, false},
	} {
		a.dauer.on.Store(tc.on)
		ac.Store(tc.ac)
		if got := a.dauerbetriebActive(); got != tc.want {
			t.Errorf("%s: dauerbetriebActive = %v, erwartet %v", tc.name, got, tc.want)
		}
	}
}
