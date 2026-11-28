package host

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// Wiederholung des Zwischenstands nach „hat sich beim Kopieren geändert“:
// Der ganze Ablauf (SaveBarrier, Stage, release) läuft bis zu 3-mal, mit
// snapRetryPause (10 s, Fake-Uhr) dazwischen, statt 5 Minuten auf den nächsten
// Takt zu warten.

// syncBuf ist ein Log-Ziel, das mehrere Goroutinen beschreiben dürfen.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog leitet das Standard-Log in den Test um.
func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// changedFileErr ist der Fehler von Stage, wenn sich die Datei rel der Welt
// beim Kopieren geändert hat.
func changedFileErr(rel string) error {
	return &worldsync.ChangedWhileCopyingError{Path: rel}
}

// retryStage lässt Stage nach der Vorgabe scheitern und merkt sich die
// Zeiten (Fake-Uhr) der Aufrufe.
type retryStage struct {
	e    *hostEnv
	errs []error // je Aufruf; danach echt
	mu   sync.Mutex
	at   []time.Duration
	// hook (darf nil sein) läuft vor dem Aufruf n (ab 0).
	hook func(n int)
}

func (p *retryStage) stage(ctx context.Context, worldDir, stagingDir string, hc *worldsync.HashCache) error {
	p.mu.Lock()
	n := len(p.at)
	p.at = append(p.at, p.e.drv.now())
	p.mu.Unlock()
	if p.hook != nil {
		p.hook(n)
	}
	if n < len(p.errs) && p.errs[n] != nil {
		return p.errs[n]
	}
	return worldsync.StageCachedContext(ctx, worldDir, stagingDir, hc)
}

func (p *retryStage) times() []time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.at)
}

func newRetryEnv(t *testing.T, errs ...error) (*hostEnv, *retryStage) {
	t.Helper()
	e := newHostEnv(t, []string{"Alex"})
	p := &retryStage{e: e, errs: errs}
	e.d.stage = p.stage
	return e, p
}

// Scheitert Stage zweimal, gelingt der dritte Versuch: genau ein Stand, nach
// 5 s und 10 s versucht, und jede Barriere wird freigegeben (save-on).
func TestSnapshotRetrySucceedsOnThirdTry(t *testing.T) {
	logs := captureLog(t)
	e, p := newRetryEnv(t, changedFileErr("region/r.-2.-5.mca"), changedFileErr("entities/r.-2.-3.mca"))
	r := e.start(t)
	n := e.head.Number

	e.drv.advanceTo(5*time.Minute + 9*time.Second)
	if c := len(e.co.hostCommits()); c != 0 {
		t.Fatalf("%d Commits vor der Wiederholung, erwartet 0", c)
	}
	e.drv.advanceTo(5*time.Minute + 21*time.Second)
	e.drv.advanceTo(6 * time.Minute)
	if got, want := p.times(), []time.Duration{5 * time.Minute, 5*time.Minute + snapRetryPause, 5*time.Minute + 2*snapRetryPause}; !slices.Equal(got, want) {
		t.Fatalf("Stage bei %v, erwartet %v", got, want)
	}
	commits := e.co.hostCommits()
	if len(commits) != 1 {
		t.Fatalf("%d Commits, erwartet genau 1", len(commits))
	}
	checkBases(t, commits, e.lease, e.head.WorldID, n)
	if _, _, barriers, releases := e.srv.counts(); barriers != 3 || releases != 3 {
		t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 3", barriers, releases)
	}
	// Der Takt bleibt: nächster Zwischenstand um 10:00, ohne Wiederholung.
	e.drv.advanceTo(10*time.Minute + 30*time.Second)
	if c := len(e.co.hostCommits()); c != 2 {
		t.Fatalf("%d Commits um 10:30, erwartet 2", c)
	}
	if got := p.times(); len(got) != 4 || got[3] != 10*time.Minute {
		t.Fatalf("Stage bei %v, erwartet einen vierten Aufruf bei 10:00", got)
	}
	e.drv.resync()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	for _, want := range []string{
		"Zwischenstand: Änderung an region/r.-2.-5.mca beim Kopieren (Versuch 1/3, 1. Änderung",
		"Zwischenstand: Versuch 2/3 nach Änderung an region/r.-2.-5.mca",
		"Zwischenstand: Versuch 3/3 nach Änderung an entities/r.-2.-3.mca",
		"Zwischenstand: Versuch 3/3 gelungen",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("Log ohne %q", want)
		}
	}
	if strings.Contains(logs.String(), "nach 3 Versuchen nicht möglich") {
		t.Errorf("Log meldet Aufgeben trotz Erfolg")
	}
}

// Scheitert Stage dreimal: kein Stand, die Meldung im Log, danach geht der
// Takt normal weiter (kein vierter Versuch, der nächste Stand um 10:00).
func TestSnapshotRetryGivesUpAfterThree(t *testing.T) {
	logs := captureLog(t)
	ce := changedFileErr("level.dat")
	e, p := newRetryEnv(t, ce, ce, ce)
	r := e.start(t)

	e.drv.advanceTo(9*time.Minute + 59*time.Second)
	if got, want := p.times(), []time.Duration{5 * time.Minute, 5*time.Minute + snapRetryPause, 5*time.Minute + 2*snapRetryPause}; !slices.Equal(got, want) {
		t.Fatalf("Stage bei %v, erwartet %v (nach dem dritten Versuch keiner mehr)", got, want)
	}
	if c := len(e.co.hostCommits()); c != 0 {
		t.Fatalf("%d Commits ohne gelungenen Versuch, erwartet 0", c)
	}
	if _, _, barriers, releases := e.srv.counts(); barriers != 3 || releases != 3 {
		t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 3", barriers, releases)
	}
	e.drv.advanceTo(10*time.Minute + 30*time.Second)
	if c := len(e.co.hostCommits()); c != 1 {
		t.Fatalf("%d Commits nach dem nächsten Takt, erwartet 1", c)
	}
	e.drv.resync()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "Zwischenstand nach 3 Versuchen nicht möglich: Änderung an level.dat (3 Änderungen, 1 Stände") {
		t.Errorf("Log ohne die Aufgeben-Meldung:\n%s", out)
	}
	if strings.Count(out, "Versuch 2/3 nach Änderung an level.dat") != 1 || strings.Count(out, "Versuch 3/3 nach Änderung an level.dat") != 1 {
		t.Errorf("Log: Versuche 2/3 und 3/3 je einmal erwartet:\n%s", out)
	}
}

// Ein anderer Fehler als eine Änderung beim Kopieren wird nicht wiederholt.
func TestSnapshotNoRetryForOtherErrors(t *testing.T) {
	e, p := newRetryEnv(t, errors.New("Platte voll"))
	r := e.start(t)
	e.drv.advanceTo(9 * time.Minute)
	if got := p.times(); len(got) != 1 {
		t.Fatalf("Stage %d× vor dem nächsten Takt, erwartet 1", len(got))
	}
	e.drv.resync()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// Endet die Sitzung in der Pause vor der Wiederholung, kommt keine mehr:
// weder ein zweiter SaveBarrier noch ein Stage.
func TestSnapshotRetryAbortedByEnd(t *testing.T) {
	e, p := newRetryEnv(t, changedFileErr("region/r.0.0.mca"))
	r := e.start(t)
	e.drv.advanceTo(5*time.Minute + 2*time.Second)
	if got := p.times(); len(got) != 1 {
		t.Fatalf("Stage %d× bis 5:02, erwartet 1", len(got))
	}
	e.drv.resync()
	r.cancel() // Ende in der Pause
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := p.times(); len(got) != 2 { // 1 Versuch + Stage des Schluss-Stands
		t.Fatalf("Stage bei %v, erwartet den gescheiterten Versuch und den Schluss-Stand", got)
	}
	if _, _, barriers, releases := e.srv.counts(); barriers != 1 || releases != 1 {
		t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 1 (keine Wiederholung nach dem Ende)", barriers, releases)
	}
}

// Das Fencing in der Pause streicht die Wiederholung: kein weiterer
// SaveBarrier, nichts wird hochgeladen.
func TestSnapshotRetryAbortedByFencing(t *testing.T) {
	var e *hostEnv
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	e = newHostEnv(t, []string{"Alex"})
	t.Cleanup(open)
	p := &retryStage{e: e}
	arrived := make(chan struct{})
	p.hook = func(n int) {
		if n == 0 {
			close(arrived)
			<-gate // der erste Versuch hängt in Stage …
		}
	}
	p.errs = []error{changedFileErr("region/r.0.0.mca")} // … und scheitert dann mit einer Änderung
	e.d.stage = p.stage
	r := e.start(t)

	e.drv.advanceTo(4*time.Minute + 25*time.Second)
	e.co.failHeartbeats.Store(true) // letztes erfolgreiches Lebenszeichen 4:20 → Fencing 5:20
	e.drv.advanceTo(5*time.Minute - time.Second)
	e.drv.allow = 1 // ab 5:00 hängt der Zwischenstand in Stage
	e.drv.advanceTo(5 * time.Minute)
	// Erst weiterstellen, wenn der Versuch wirklich in Stage hängt: Vor Stage
	// wartet er noch (echte Zeit) auf die Ruhe der Welt.
	select {
	case <-arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("Stage kommt nicht an")
	}
	e.drv.advanceTo(5*time.Minute + 19*time.Second)
	if got := p.times(); len(got) != 1 {
		t.Fatalf("Stage %d× bis 5:19, erwartet 1", len(got))
	}
	// Der Versuch scheitert um 5:19 (die Wiederholung wäre für 5:29 geplant);
	// um 5:20 fenct die Sitzung.
	e.drv.allow = 0
	e.drv.resync()
	open()
	e.drv.advanceTo(6 * time.Minute)
	err := r.wait(t)
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("Run = %v, erwartet ErrFenced", err)
	}
	if got := p.times(); len(got) != 1 {
		t.Fatalf("Stage bei %v, erwartet nur den ersten Versuch", got)
	}
	if _, _, barriers, releases := e.srv.counts(); barriers != 1 || releases != 1 {
		t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 1", barriers, releases)
	}
	if c := e.co.hostCommits(); len(c) != 0 {
		t.Fatalf("%d Commits nach dem Fencing, erwartet keinen", len(c))
	}
}

// writerServer schreibt nach jedem SaveBarrier noch eine Weile in die Welt,
// wie C2ME es nach "Saved the game" tut (Schübe, dann Ruhe).
type writerServer struct {
	*fakeServer
	tail  time.Duration // so lange wird nach SaveBarrier noch geschrieben
	every time.Duration
	file  string

	mu        sync.Mutex
	lastWrite time.Time
	n         int
}

func (w *writerServer) SaveBarrier(ctx context.Context) (func(context.Context) error, error) {
	release, err := w.fakeServer.SaveBarrier(ctx)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.lastWrite = time.Now()
	w.mu.Unlock()
	go func() {
		end := time.Now().Add(w.tail)
		for time.Now().Before(end) {
			time.Sleep(w.every)
			w.mu.Lock()
			w.n++
			os.WriteFile(w.file, bytes.Repeat([]byte("x"), w.n), 0o644)
			w.lastWrite = time.Now()
			w.mu.Unlock()
		}
	}()
	return release, nil
}

func (w *writerServer) sinceLastWrite() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Since(w.lastWrite)
}

// Nach SaveBarrier wartet der Zwischenstand, bis die Welt snapQuietFor lang
// ruhig ist, bevor Stage kopiert: Schreibt der Server noch 300 ms weiter,
// beginnt Stage erst danach – und scheitert so nicht.
func TestSnapshotWaitsForQuietWorld(t *testing.T) {
	oldMin, oldCap, oldP, oldM := snapQuietMin, snapQuietCap, snapQuietPoll, snapQuietMax
	snapQuietMin, snapQuietCap, snapQuietPoll, snapQuietMax = 300*time.Millisecond, 300*time.Millisecond, 10*time.Millisecond, 10*time.Second
	t.Cleanup(func() { snapQuietMin, snapQuietCap, snapQuietPoll, snapQuietMax = oldMin, oldCap, oldP, oldM })

	e, p := newRetryEnv(t)
	ws := &writerServer{fakeServer: e.srv, tail: 400 * time.Millisecond, every: 20 * time.Millisecond,
		file: filepath.Join(e.srv.world, "tail.dat")}
	e.d.Server = ws
	var qmu sync.Mutex
	var quietAtStage []time.Duration
	p.hook = func(n int) {
		qmu.Lock()
		defer qmu.Unlock()
		quietAtStage = append(quietAtStage, ws.sinceLastWrite())
	}
	r := e.start(t)
	e.drv.advanceTo(5*time.Minute + 30*time.Second)
	if c := len(e.co.hostCommits()); c != 1 {
		t.Fatalf("%d Commits, erwartet 1 (ohne Wiederholung)", c)
	}
	if got := p.times(); len(got) != 1 {
		t.Fatalf("Stage %d×, erwartet 1: Die Wartezeit hätte die Änderung abfangen müssen", len(got))
	}
	qmu.Lock()
	got := slices.Clone(quietAtStage)
	qmu.Unlock()
	if len(got) != 1 || got[0] < 300*time.Millisecond {
		t.Fatalf("Stage begann %v nach dem letzten Schreiben, erwartet mindestens 300ms", got)
	}
	time.Sleep(ws.tail + 100*time.Millisecond) // der Schreiber ist fertig, bevor der Schluss-Stand kopiert
	e.drv.resync()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// Wird die Welt nicht ruhig, geht es nach snapQuietMax trotzdem weiter (Stage
// prüft selbst), und das Log sagt es.
func TestSnapshotQuietGivesUpAfterMax(t *testing.T) {
	logs := captureLog(t)
	oldMin, oldCap, oldP, oldM := snapQuietMin, snapQuietCap, snapQuietPoll, snapQuietMax
	snapQuietMin, snapQuietCap, snapQuietPoll, snapQuietMax = 200*time.Millisecond, 200*time.Millisecond, 10*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { snapQuietMin, snapQuietCap, snapQuietPoll, snapQuietMax = oldMin, oldCap, oldP, oldM })

	e, p := newRetryEnv(t)
	ws := &writerServer{fakeServer: e.srv, tail: 400 * time.Millisecond, every: 10 * time.Millisecond,
		file: filepath.Join(e.srv.world, "tail.dat")}
	e.d.Server = ws
	r := e.start(t)
	e.drv.advanceTo(5*time.Minute + 30*time.Second)
	if got := p.times(); len(got) < 1 {
		t.Fatalf("Stage nie aufgerufen")
	}
	time.Sleep(ws.tail + 100*time.Millisecond) // der Schreiber ist fertig, bevor der Schluss-Stand kopiert
	e.drv.resync()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if !strings.Contains(logs.String(), "Welt in 100ms nicht ruhig, es wird trotzdem kopiert") {
		t.Errorf("Log ohne die Meldung zur fehlenden Ruhe")
	}
}

// Wie lange die Welt ruhig sein muss, hängt von der Dauer des Speicherns ab:
// so lange wie das Speichern, mindestens snapQuietMin, höchstens snapQuietCap.
func TestSnapQuietFor(t *testing.T) {
	oldMin, oldCap := snapQuietMin, snapQuietCap
	snapQuietMin, snapQuietCap = time.Second, 5*time.Second // Werte des Betriebs
	t.Cleanup(func() { snapQuietMin, snapQuietCap = oldMin, oldCap })
	for _, c := range []struct{ took, want time.Duration }{
		{0, time.Second},
		{100 * time.Millisecond, time.Second},
		{2500 * time.Millisecond, 2500 * time.Millisecond},
		{6500 * time.Millisecond, 5 * time.Second},
		{time.Minute, 5 * time.Second},
	} {
		if got := snapQuietFor(c.took); got != c.want {
			t.Errorf("snapQuietFor(%v) = %v, erwartet %v", c.took, got, c.want)
		}
	}
}
