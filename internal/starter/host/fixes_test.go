package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// I1 (c): Das Lebenszeichen meldet ready erst, wenn der Server läuft und
// tunnel tunnelSettle lang steht – sofort mit einem Lebenszeichen außer der
// Reihe, ohne den Takt zu verschieben. OnReady läuft genau einmal.
func TestReadyReportedOnceTunnelRuns(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	var mu sync.Mutex
	var readyAt []time.Duration
	e.d.OnReady = func() {
		mu.Lock()
		readyAt = append(readyAt, e.drv.now())
		mu.Unlock()
	}
	r := e.start(t)

	e.drv.advanceTo(2 * time.Second)
	if got := e.co.heartbeatReadies(); !slices.Equal(got, []bool{false}) {
		t.Fatalf("ready bis 2 s: %v, erwartet [false]", got)
	}
	// 3 s: tunnel steht → bereit, Lebenszeichen außer der Reihe; der Takt
	// (10 s, 20 s) bleibt.
	e.drv.advanceTo(25 * time.Second)
	if got := e.co.heartbeatReadies(); !slices.Equal(got, []bool{false, true, true, true}) {
		t.Fatalf("ready bis 25 s: %v, erwartet [false true true true] (0, 3, 10, 20 s)", got)
	}
	// tunnel endet bei 29 s, startet bei 31 s neu: Das Lebenszeichen um 30 s
	// meldet nicht bereit, um 34 s (tunnel steht wieder) bereit.
	e.drv.advanceTo(29 * time.Second)
	e.killTunnel(t)
	e.drv.advanceTo(41 * time.Second)
	if got := e.co.heartbeatReadies()[4:]; !slices.Equal(got, []bool{false, true, true}) {
		t.Fatalf("ready 30–40 s: %v, erwartet [false true true] (30, 34, 40 s)", got)
	}
	mu.Lock()
	at := slices.Clone(readyAt)
	mu.Unlock()
	if !slices.Equal(at, []time.Duration{3 * time.Second}) {
		t.Fatalf("OnReady bei %v, erwartet einmal bei 3 s", at)
	}

	// Beim Ende meldet kein Lebenszeichen mehr bereit.
	n := len(e.co.heartbeatReadies())
	e.drv.resync()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := e.co.heartbeatReadies()[n:]; slices.Contains(got, true) {
		t.Fatalf("Lebenszeichen nach dem Ende mit ready: %v", got)
	}
}

// Hängt das Lebenszeichen gerade, wenn tunnel steht, wartet die Schleife
// auf dessen Ergebnis, statt leer zu laufen; danach meldet sie die
// Bereitschaft.
func TestNoBusyLoopWhileHeartbeatPending(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	g := newGate(t)
	e.co.hbGate.Store(g)
	e.drv.allow = 1 // das erste Lebenszeichen hängt
	r := e.start(t)
	g.waitArrived(t)
	idles := func() uint64 {
		e.drv.mu.Lock()
		defer e.drv.mu.Unlock()
		return e.drv.seq
	}
	before, start := idles(), time.Now()
	e.drv.advanceTo(5 * time.Second) // tunnel steht ab 3 s, das Lebenszeichen hängt noch
	time.Sleep(200 * time.Millisecond)
	if n, d := idles()-before, time.Since(start); n > 50 || d > 5*time.Second {
		t.Fatalf("Schleife lief %d-mal leer (%v), während das Lebenszeichen hing", n, d.Round(time.Millisecond))
	}

	e.co.hbGate.Store(nil)
	e.drv.resync()
	g.release()
	e.drv.allow = 0
	e.drv.advanceTo(6 * time.Second)
	if got := e.co.heartbeatReadies(); !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("ready: %v, erwartet [false true]", got)
	}
	e.drv.resync()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// Schluss-Upload: Scheitert er, wird er in der Sitzung wiederholt (bis
// finalRetryFor), solange der Zuschlag gehalten wird – kommt er durch, ist
// nichts vorgemerkt.
func TestFinalUploadRetriedInSession(t *testing.T) {
	e := newHostEnv(t, []string{})
	e.co.failBlobPuts.Store(true)
	r := e.start(t)

	// 2:00 niemand da → Stop, Schluss-Stand scheitert (2:00, 2:10).
	e.drv.advanceTo(2*time.Minute + 15*time.Second)
	select {
	case <-r.done:
		t.Fatalf("Run endete nach dem ersten gescheiterten Schluss-Stand: %v", r.err)
	default:
	}
	if h := e.co.status(t).Host; h == nil {
		t.Fatal("Zuschlag schon freigegeben, obwohl der Schluss-Stand noch wiederholt wird")
	}
	if len(e.co.hostCommits()) != 0 {
		t.Fatal("Commit trotz gescheitertem Upload")
	}

	// Ab jetzt geht es wieder: Der Versuch um 2:20 kommt durch.
	e.co.failBlobPuts.Store(false)
	e.drv.advanceTo(3 * time.Minute)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if at := e.drv.now(); at != 2*time.Minute+20*time.Second {
		t.Fatalf("Run endete bei %v, erwartet 2m20s", at)
	}
	final := e.co.head(t)
	if final.Number != e.head.Number+1 || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
		t.Fatalf("Head %d, erwartet %d mit dem Stand nach Stop", final.Number, e.head.Number+1)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
	checkUploaded(t, e.checkAfterRun(t), final)
}

// Ist der Head mit einer neueren Minecraft-Version gespeichert als die, die
// gehostet wird, startet kein Server (ein älterer würde die Welt
// beschädigen); nicht vergleichbare Versionen nur bei Gleichheit.
func TestRefusesWorldFromNewerVersion(t *testing.T) {
	for _, tc := range []struct {
		hosted string
		refuse bool
	}{
		{"26.1", true},
		{"26.2-rc1", true},
		{"26w14a", true},
		{"26.2", false},
		{"26.2.1", false},
	} {
		t.Run(tc.hosted, func(t *testing.T) {
			e := newHostEnv(t, []string{}) // Head in mcVersion (26.2)
			e.d.MinecraftVersion = tc.hosted
			r := e.start(t)
			if !tc.refuse {
				e.drv.advanceTo(5 * time.Minute)
				if err := r.wait(t); err != nil {
					t.Fatalf("Run = %v", err)
				}
				return
			}
			err := r.wait(t)
			if err == nil || !strings.Contains(err.Error(), mcVersion) || !strings.Contains(err.Error(), tc.hosted) {
				t.Fatalf("Run = %v, erwartet Ablehnung mit beiden Versionen", err)
			}
			if prepares, starts, _, _ := e.srv.counts(); prepares != 0 || starts != 0 {
				t.Fatalf("Prepare %d×, Start %d×", prepares, starts)
			}
			if _, err := os.Lstat(filepath.Join(e.dir, WorldName)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Welt trotzdem geladen (%v)", err)
			}
			if h := e.co.status(t).Host; h != nil {
				t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
			}
		})
	}
}

// T10 Minor 2: Scheitert der Start, bevor je ein Java-Prozess zu sehen war,
// ist die Welt unberührt: Die Markierung muss weg, sonst verwürfe der
// nächste Start einen gültigen vorgemerkten Stand. Lief Java, bleibt sie.
func TestStartWithoutJavaKeepsWorld(t *testing.T) {
	pending := map[string]string{"level.dat": "sauber gestoppt, vorgemerkt", "region/r.0.0.mca": "chunks"}
	t.Run("Java startet nie", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		writeTree(t, filepath.Join(e.dir, WorldName), pending)
		e.d.State = &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: e.head.WorldID, LastUploadedGeneration: e.head.Number}
		e.srv.startErr = errors.New("java.exe nicht gefunden")
		r := e.start(t)
		if err := r.wait(t); err == nil || !strings.Contains(err.Error(), "java.exe nicht gefunden") {
			t.Fatalf("Run = %v", err)
		}
		if _, err := os.Lstat(e.srv.marker); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Markierung nach Start ohne Java noch da (%v)", err)
		}
		if !e.d.State.PendingUpload {
			t.Fatal("Vormerkung verloren")
		}
		// Der nächste Start reicht den Stand nach, statt ihn zu verwerfen.
		if err := Recover(t.Context(), e.co.c, e.d.State, e.dir); err != nil {
			t.Fatalf("Recover = %v, erwartet Nachreichen", err)
		}
		if head := e.co.head(t); head.Number != e.head.Number+1 || genSHA(t, head, "level.dat") != sha(pending["level.dat"]) {
			t.Fatalf("Head %d, erwartet den nachgereichten Stand", head.Number)
		}
	})
	for _, hide := range []bool{false, true} {
		// hide: Die PID ist erst zu sehen, wenn Start (mit Fehler) zurückkehrt.
		t.Run(fmt.Sprintf("Java lief, PID erst am Ende %v", hide), func(t *testing.T) {
			e := newHostEnv(t, []string{"Alex"})
			e.srv.withProcess = true
			e.srv.hidePIDInStart = hide
			e.srv.startErr = errors.New("Server nach 5 Minuten nicht bereit")
			r := e.start(t)
			if err := r.wait(t); err == nil {
				t.Fatal("Run ohne Fehler")
			}
			if _, err := os.Lstat(e.srv.marker); err != nil {
				t.Fatalf("Markierung nach Start mit Java weg: %v", err)
			}
		})
	}
}

// T10 Minor 4b: Lässt sich der Prozess aus der Markierung nicht prüfen
// (etwa Zugriff verweigert), läuft er vielleicht noch: Recover fasst die
// Welt nicht an. Stammt die Markierung von vor dem Neustart des PCs, läuft
// nichts mehr aus ihr.
func TestRecoverFailsClosedOnUncheckableProcess(t *testing.T) {
	co := newCoord(t)
	dir := filepath.Join(group.Home(), "server")
	world := filepath.Join(dir, WorldName)
	files := map[string]string{"level.dat": "halb", "region/r.0.0.mca": "chunks"}
	writeTree(t, world, files)
	const pid = 424242
	old := openProc
	t.Cleanup(func() { openProc = old })
	openProc = func(p int) (*procHandle, error) {
		if p == pid {
			return nil, errors.New("Zugriff verweigert")
		}
		return old(p)
	}
	m := marker{PID: pid, Exe: `C:\deps\java\bin\java.exe`, Java: `C:\deps\java\bin\java.exe`, Written: time.Now().UTC()}
	if err := writeMarker(filepath.Join(dir, MarkerName), m); err != nil {
		t.Fatal(err)
	}
	st := &group.State{MachineID: hostMachine}

	err := Recover(t.Context(), co.c, st, dir)
	if !errors.Is(err, ErrMaybeRunning) {
		t.Fatalf("Recover = %v, erwartet ErrMaybeRunning", err)
	}
	if got := readTree(t, world); len(got) != len(files) {
		t.Fatalf("Welt angefasst: %v", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, MarkerName)); err != nil {
		t.Fatalf("Markierung weg: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(group.Home(), "verworfen-*")); len(matches) != 0 {
		t.Fatalf("verworfen: %v", matches)
	}
	// Auch der tunnel aus der Markierung zählt.
	m.PID, m.Tunnel = 0, &procRef{PID: pid, Exe: `C:\deps\tunnel\tunnel.exe`}
	if err := writeMarker(filepath.Join(dir, MarkerName), m); err != nil {
		t.Fatal(err)
	}
	if err := Recover(t.Context(), co.c, st, dir); !errors.Is(err, ErrMaybeRunning) {
		t.Fatalf("Recover mit tunnel = %v, erwartet ErrMaybeRunning", err)
	}

	if runtime.GOOS != "windows" {
		return // ohne Startzeit des PCs gibt es keine Ausnahme
	}
	m.PID, m.Tunnel = pid, nil
	m.Written = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := writeMarker(filepath.Join(dir, MarkerName), m); err != nil {
		t.Fatal(err)
	}
	err = Recover(t.Context(), co.c, st, dir)
	var de *DiscardedError
	if !errors.As(err, &de) {
		t.Fatalf("Recover mit Markierung von vor dem Neustart = %v, erwartet DiscardedError", err)
	}
	checkDiscarded(t, de.Path, world, files)
}

// T10 Minor 7: removeMarker wiederholt wie die Renames, solange ein anderer
// (Virenscanner, Indexdienst) die Markierung kurz offen hält.
func TestRemoveMarkerRetriesWhileOpen(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("nur unter Windows verhindert eine offene Datei das Löschen")
	}
	p := filepath.Join(t.TempDir(), MarkerName)
	if err := writeMarker(p, marker{Written: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		f.Close()
	}()
	err = removeMarker(p)
	<-done
	if err != nil {
		t.Fatalf("removeMarker: %v", err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Markierung noch da (%v)", err)
	}
}

// Verwerfen: Run meldet jeden verworfenen Stand (Deps.Discarded), und es
// bleiben nur die zwei neuesten verworfen-* liegen.
func TestDiscardedReportedAndPruned(t *testing.T) {
	t.Run("überholter vorgemerkter Stand", func(t *testing.T) {
		co := newCoord(t)
		g1 := co.seed(t, seedFiles)
		g2 := co.seed(t, map[string]string{"level.dat": "weiter", "region/r.0.0.mca": "chunks 2"})
		e := newHostEnvOn(t, co, g2, []string{"Alex"})
		home := group.Home()
		for _, d := range []string{"verworfen-20260101-000000", "verworfen-20260102-000000", "verworfen-20260102-000000-2"} {
			writeTree(t, filepath.Join(home, d), map[string]string{"level.dat": d})
		}
		pending := map[string]string{"level.dat": "alt, vorgemerkt"}
		writeTree(t, filepath.Join(e.dir, WorldName), pending)
		e.d.State = &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: g1.WorldID, LastUploadedGeneration: g1.Number}
		var mu sync.Mutex
		var got []*DiscardedError
		e.d.Discarded = func(de *DiscardedError) {
			mu.Lock()
			got = append(got, de)
			mu.Unlock()
		}
		r := e.start(t)
		e.drv.advanceTo(10 * time.Second)
		e.drv.resync()
		r.cancel()
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 1 || !strings.Contains(got[0].Reason, "weitergespielt") {
			t.Fatalf("gemeldet: %+v, erwartet einen überholten Stand", got)
		}
		checkDiscarded(t, got[0].Path, filepath.Join(e.dir, "nirgends"), pending)
		left, _ := filepath.Glob(filepath.Join(home, "verworfen-*"))
		want := []string{filepath.Join(home, "verworfen-20260102-000000-2"), got[0].Path}
		slices.Sort(left)
		slices.Sort(want)
		if !slices.Equal(left, want) {
			t.Fatalf("verworfen-* danach: %v, erwartet %v", left, want)
		}
	})

	t.Run("Markierung beim Start der Sitzung", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		dirty := map[string]string{"level.dat": "halb geschrieben"}
		writeTree(t, filepath.Join(e.dir, WorldName), dirty)
		if err := writeMarker(e.srv.marker, marker{Written: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var got []*DiscardedError
		e.d.Discarded = func(de *DiscardedError) {
			mu.Lock()
			got = append(got, de)
			mu.Unlock()
		}
		r := e.start(t)
		e.drv.advanceTo(10 * time.Second)
		e.drv.resync()
		r.cancel()
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 1 || !strings.Contains(got[0].Reason, "nicht sauber gestoppt") {
			t.Fatalf("gemeldet: %+v", got)
		}
		checkDiscarded(t, got[0].Path, filepath.Join(e.dir, "nirgends"), dirty)
	})
}

// B4 (Gesamtlauf): Der Name verworfen-<Zeit> trägt Ortszeit. Der gerade
// verworfene Stand bleibt beim Aufräumen immer liegen, auch wenn sein Name
// vor älteren sortiert (Zeitumstellung im Herbst, UTC-Namen älterer
// Versionen).
func TestDiscardWorldLocalTimeKeepsNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)
	for _, d := range []string{"verworfen-20990101-000000", "verworfen-20990102-000000"} {
		writeTree(t, filepath.Join(home, d), map[string]string{"level.dat": d})
	}
	world := filepath.Join(t.TempDir(), WorldName)
	writeTree(t, world, map[string]string{"level.dat": "jetzt"})
	before := time.Now()
	dest, err := discardWorld(world)
	if err != nil {
		t.Fatalf("discardWorld: %v", err)
	}
	stamp, _ := strings.CutPrefix(filepath.Base(dest), "verworfen-")
	at, err := time.ParseInLocation("20060102-150405", stamp, time.Local)
	if err != nil || at.Before(before.Truncate(time.Second)) || at.After(time.Now()) {
		t.Fatalf("Name %s: Zeit %v (%v), erwartet Ortszeit zwischen %v und jetzt", filepath.Base(dest), at, err, before)
	}
	left, _ := filepath.Glob(filepath.Join(home, "verworfen-*"))
	want := []string{filepath.Join(home, "verworfen-20990102-000000"), dest}
	slices.Sort(left)
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("verworfen-* danach: %v, erwartet %v", left, want)
	}
}
