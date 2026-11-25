package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// fakeHandover spielt die Übergabe-Endpunkte des Koordinators.
type fakeHandover struct {
	mu       sync.Mutex
	conflict string // gesetzt: HandoverStart → 409 mit diesem Text
	starts   int
	readies  []api.HandoverReadyRequest
	aborts   []api.HandoverAbortRequest
	abortErr int // so viele Abbrüche zuerst mit 500 beantworten
	abortTry int
	notes    []string // OnHandover: "<Stand> <Name>"
}

const hoID = 7

func (f *fakeHandover) handle(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/handover/start":
		f.starts++
		if f.conflict != "" {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": f.conflict})
			return true
		}
		json.NewEncoder(w).Encode(api.HandoverStartResponse{
			HandoverID: hoID, To: api.HostInfo{MinecraftName: "Neu", MachineID: otherMachine},
		})
	case "/v1/handover/ready":
		var req api.HandoverReadyRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		f.readies = append(f.readies, req)
		w.WriteHeader(http.StatusNoContent)
	case "/v1/handover/abort":
		var req api.HandoverAbortRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		f.abortTry++
		if f.abortTry <= f.abortErr {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		f.aborts = append(f.aborts, req)
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

func (f *fakeHandover) abortsCopy() []api.HandoverAbortRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.aborts)
}

func (f *fakeHandover) note(state, to string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, state+" "+to)
}

func (f *fakeHandover) notesCopy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.notes)
}

func (f *fakeHandover) readiesCopy() []api.HandoverReadyRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.readies)
}

// newHandoverEnv: Sitzung mit Übergabe-Kanal. Der Fake-Server antwortet auf
// die Modus-Abfrage (modes: Name → Zahl), schreibt bei "save-all flush"
// "flushed" in level.dat und leert bei "transfer" die Spielerliste.
func newHandoverEnv(t *testing.T, modes map[string]string, players ...string) (*hostEnv, *fakeHandover, chan struct{}) {
	t.Helper()
	e := newHostEnv(t, players)
	shortenHandoverWaits(t)
	fh := &fakeHandover{}
	h := fh.handle
	e.co.handoverH.Store(&h)
	ch := make(chan struct{}, 1)
	e.d.Handover = ch
	e.d.OnHandover = fh.note
	e.srv.cmdResp = func(f *fakeServer, cmd string) (string, bool) {
		switch {
		case strings.HasPrefix(cmd, "data get entity "):
			name := strings.Fields(cmd)[3]
			return name + " has the following entity data: " + modes[name], true
		case cmd == "save-all flush":
			os.WriteFile(filepath.Join(f.world, "level.dat"), []byte("flushed"), 0o644)
			return "Saved the game", true
		case strings.HasPrefix(cmd, "transfer "):
			f.players = nil
		}
		return "", false
	}
	return e, fh, ch
}

// shortenHandoverWaits kürzt die Ruhezeiten vor dem Kopieren (echte Zeit).
func shortenHandoverWaits(t *testing.T) {
	t.Helper()
	pause, quiet, poll, max, retry := handoverFlushPause, handoverQuietFor, handoverQuietPoll, handoverQuietMax, handoverStageRetryPause
	apause, plimit, climit, reserve := handoverAbortPause, handoverPrepareLimit, handoverCoordLimit, handoverUploadReserve
	handoverAbortPause = time.Millisecond
	handoverFlushPause, handoverQuietFor, handoverQuietPoll, handoverQuietMax, handoverStageRetryPause =
		time.Millisecond, 5*time.Millisecond, time.Millisecond, 200*time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		handoverFlushPause, handoverQuietFor, handoverQuietPoll, handoverQuietMax, handoverStageRetryPause =
			pause, quiet, poll, max, retry
		handoverAbortPause, handoverPrepareLimit, handoverCoordLimit, handoverUploadReserve = apause, plimit, climit, reserve
	})
}

// changedErr ist der Fehler von Stage, wenn sich beim Kopieren eine Datei ändert.
func changedErr() error {
	return fmt.Errorf("region/r.0.0.mca hat sich beim Kopieren geändert: %w", worldsync.ErrChangedWhileCopying)
}

// stagePlan lässt Stage die ersten Aufrufe mit errs scheitern (nil: echter
// Aufruf) und merkt sich die Befehle des Servers zu jedem Aufruf.
type stagePlan struct {
	e    *hostEnv
	errs []error     // je Aufruf; danach echt
	hook func(n int) // (darf nil sein) vor dem Aufruf n (ab 0)

	mu    sync.Mutex
	calls [][]string
}

func (p *stagePlan) stage(ctx context.Context, worldDir, stagingDir string, hc *worldsync.HashCache) error {
	p.mu.Lock()
	n := len(p.calls)
	p.calls = append(p.calls, p.e.srv.commands())
	p.mu.Unlock()
	if p.hook != nil {
		p.hook(n)
	}
	if n < len(p.errs) && p.errs[n] != nil {
		return p.errs[n]
	}
	return worldsync.StageCachedContext(ctx, worldDir, stagingDir, hc)
}

func (p *stagePlan) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func count(cmds []string, c string) int {
	n := 0
	for _, x := range cmds {
		if x == c {
			n++
		}
	}
	return n
}

// trigger löst die Übergabe aus.
func (e *hostEnv) trigger(ch chan struct{}) {
	e.drv.resync()
	ch <- struct{}{}
}

// shape ersetzt die tellraw-Befehle durch "tellraw" (JSON-Text).
func shape(cmds []string) []string {
	out := slices.Clone(cmds)
	for i, c := range out {
		if strings.HasPrefix(c, "tellraw @a ") {
			out[i] = "tellraw"
		}
	}
	return out
}

func equalStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s:\n got  %q\n want %q", what, got, want)
	}
}

// Die Befehlsreihenfolge der Sperre (save-all flush vor gamemode spectator,
// der Stand enthält nie Spectator), dann „bereit“ → transfer an die Adresse
// des Koordinators, Spieler weg → Stopp ohne Schluss-Stand, Zuschlag zurück.
func TestHandoverGiveAway(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	r := e.start(t)
	n := e.head.Number
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(31 * time.Second)

	want := []string{
		"tick freeze", "tellraw", "save-off",
		"data get entity Alex playerGameType", "data get entity Bob playerGameType",
		"save-all flush",
		"gamemode spectator Alex", "gamemode spectator Bob",
		"tellraw",
	}
	equalStrings(t, "Befehle der Sperre", shape(e.srv.cmdsAfterStart()), want)
	if c := e.srv.cmdsAfterStart()[8]; !strings.Contains(c, "Neu") || !strings.Contains(c, "übergeben") {
		t.Fatalf("Ansage %q nennt den neuen Host nicht", c)
	}
	// Der Stand ist eingereicht und gemeldet; der Server läuft weiter.
	head := e.co.head(t)
	if head.Number != n+1 || head.MachineID != hostMachine || genSHA(t, head, "level.dat") != sha("flushed") {
		t.Fatalf("Head %d von %s, erwartet %d mit dem gespeicherten Stand (flushed)", head.Number, head.MachineID, n+1)
	}
	rd := fh.readiesCopy()
	if len(rd) != 1 || rd[0] != (api.HandoverReadyRequest{
		HandoverID: hoID, LeaseID: e.lease, MachineID: hostMachine, WorldID: head.WorldID, Generation: head.Number,
	}) {
		t.Fatalf("HandoverReady %+v, erwartet einmal mit Stand %d", rd, head.Number)
	}
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt vor „bereit“: %v", s)
	}

	e.co.hbResp.Store(&api.HeartbeatResponse{Handover: &api.HandoverState{
		HandoverID: hoID, State: api.HandoverReady, Address: "neu.example.org:15566",
	}})
	e.drv.advanceTo(120 * time.Second)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cmds := e.srv.commands()
	if got := cmds[len(cmds)-1]; got != "transfer neu.example.org 15566 @a" {
		t.Fatalf("letzter Befehl %q, erwartet transfer an die Adresse des Koordinators", got)
	}
	if s := e.srv.stopTimes(); len(s) != 1 {
		t.Fatalf("Stop %v, erwartet einmal", s)
	}
	// Kein Schluss-Stand: Der Head ist weiter der Übergabe-Stand.
	if c := e.co.hostCommits(); len(c) != 1 {
		t.Fatalf("%d Commits des Hosts, erwartet nur den Übergabe-Stand", len(c))
	}
	if h := e.co.head(t); h.Number != n+1 || genSHA(t, h, "level.dat") != sha("flushed") {
		t.Fatalf("Head %d, erwartet unverändert %d", h.Number, n+1)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
	st := e.checkAfterRun(t)
	if st.PendingUpload || st.LastUploadedGeneration != n+1 || st.LastUploadedWorldID != head.WorldID {
		t.Fatalf("Zustand %+v, erwartet alles auf %d eingereicht", *st, n+1)
	}
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "start Neu", "bereit Neu"})
}

// cmdsAfterStart: die Befehle ohne das tick unfreeze nach dem Start.
func (f *fakeServer) cmdsAfterStart() []string {
	c := f.commands()
	if len(c) > 0 && c[0] == "tick unfreeze" {
		return c[1:]
	}
	return c
}

// Scheitert die Übergabe, nachdem der Stand eingereicht ist (hier: der
// Koordinator meldet „gescheitert“), ist der Übergabe-Stand der
// Schluss-Stand: Ansage, Stopp ohne neuen Stand, Zuschlag zurück. Modi
// werden nicht zurückgesetzt (alle sind Spectator, die Welt stand still).
func TestHandoverFallbackAfterCommit(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0", "Cleo": "2"}, "Alex", "Bob")
	r := e.start(t)
	n := e.head.Number
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(31 * time.Second)
	// Cleo kommt während der Übergabe dazu.
	e.srv.setPlayers("Alex", "Bob", "Cleo")
	e.drv.advanceTo(35 * time.Second)
	if !slices.Contains(e.srv.commands(), "gamemode spectator Cleo") {
		t.Fatalf("Neuer Spieler nicht Spectator: %q", e.srv.commands())
	}
	nBefore := len(e.srv.commands())

	e.co.hbResp.Store(&api.HeartbeatResponse{Handover: &api.HandoverState{
		HandoverID: hoID, State: api.HandoverFailed, Reason: "neuer Host abgestürzt",
	}})
	e.drv.advanceTo(120 * time.Second)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	equalStrings(t, "nach dem Scheitern", shape(e.srv.commands()[nBefore:]), []string{"tellraw"})
	cmds := e.srv.commands()
	if c := cmds[len(cmds)-1]; !strings.Contains(c, "Übergabe gescheitert") {
		t.Fatalf("Ansage %q", c)
	}
	if s := e.srv.stopTimes(); len(s) != 1 {
		t.Fatalf("Stop %v, erwartet einmal", s)
	}
	if c := e.co.hostCommits(); len(c) != 1 {
		t.Fatalf("%d Commits, erwartet nur den Übergabe-Stand", len(c))
	}
	head := e.co.head(t)
	if head.Number != n+1 || genSHA(t, head, "level.dat") != sha("flushed") {
		t.Fatalf("Head %d, erwartet unverändert %d", head.Number, n+1)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
	st := e.checkAfterRun(t)
	if st.PendingUpload || st.LastUploadedGeneration != n+1 {
		t.Fatalf("Zustand %+v, erwartet alles auf %d eingereicht", *st, n+1)
	}
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "start Neu", "gescheitert Neu"})
}

// Scheitert die Übergabe vor dem gelungenen Commit (hier: der Koordinator
// lehnt den Übergabe-Stand ab), baut der Host die Sperre zurück (Modi,
// auftauen, Speichern an), bricht die Übergabe beim Koordinator ab und
// hostet weiter: Der Server stoppt nicht. Die Übergabe darf erneut
// versucht werden; sie läuft dann durch.
func TestHandoverFallbackBeforeCommit(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	var calls int
	e.co.setOnCommit(func() {
		calls++
		e.co.rejectCommits.Store(calls == 1) // nur der erste Commit (Übergabe-Stand)
	})
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)
	cmds := e.srv.cmdsAfterStart()
	equalStrings(t, "Befehle", shape(cmds), []string{
		"tick freeze", "tellraw", "save-off",
		"data get entity Alex playerGameType", "data get entity Bob playerGameType",
		"save-all flush",
		"gamemode spectator Alex", "gamemode spectator Bob", "tellraw",
		"gamemode creative Alex", "gamemode survival Bob", "tick unfreeze", "save-on", "tellraw",
	})
	if c := e.srv.commands(); !strings.Contains(c[len(c)-1], "es geht normal weiter") {
		t.Fatalf("Ansage %q", c[len(c)-1])
	}
	select {
	case <-r.done:
		t.Fatalf("Sitzung endete nach der abgebrochenen Übergabe: %v", r.err)
	default:
	}
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt: %v", s)
	}
	if a := fh.abortsCopy(); len(a) != 1 || a[0] != (api.HandoverAbortRequest{HandoverID: hoID, LeaseID: e.lease, MachineID: hostMachine}) {
		t.Fatalf("Abbrüche %+v, erwartet einer für Übergabe %d", a, hoID)
	}
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "gescheitert Neu"})

	// Folge-Übergabe: Der Host klickt erneut, diesmal geht der Stand durch.
	e.trigger(ch)
	e.drv.advanceTo(50 * time.Second)
	fh.mu.Lock()
	starts := fh.starts
	fh.mu.Unlock()
	if starts != 2 || len(fh.readiesCopy()) != 1 {
		t.Fatalf("%d× HandoverStart, %d× Ready; erwartet 2 und 1", starts, len(fh.readiesCopy()))
	}
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "gescheitert Neu", "vorbereitung Neu", "start Neu"})
	e.co.hbResp.Store(&api.HeartbeatResponse{Handover: &api.HandoverState{
		HandoverID: hoID, State: api.HandoverReady, Address: "neu.example.org:15566",
	}})
	e.drv.advanceTo(120 * time.Second)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c := e.srv.commands(); c[len(c)-1] != "transfer neu.example.org 15566 @a" {
		t.Fatalf("letzter Befehl %q, erwartet transfer", c[len(c)-1])
	}
}

// Ändert sich beim Kopieren einmal eine Datei, wird erneut gespeichert und
// noch einmal bereitgestellt; die Übergabe läuft weiter. Spectator kommt erst
// nach dem gelungenen Kopieren.
func TestHandoverStageRetryOnce(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	plan := &stagePlan{e: e, errs: []error{changedErr()}}
	e.d.stage = plan.stage
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)

	if plan.count() != 2 {
		t.Fatalf("%d× Stage, erwartet 2", plan.count())
	}
	equalStrings(t, "Befehle", shape(e.srv.cmdsAfterStart()), []string{
		"tick freeze", "tellraw", "save-off",
		"data get entity Alex playerGameType", "data get entity Bob playerGameType",
		"save-all flush", "save-all flush",
		"gamemode spectator Alex", "gamemode spectator Bob", "tellraw",
	})
	// Beim ersten und zweiten Stage war noch niemand Spectator; das zweite
	// kam nach dem zweiten Speichern.
	for i, cs := range plan.calls {
		if count(cs, "gamemode spectator Alex") != 0 {
			t.Fatalf("Stage %d lief nach dem Spectator-Setzen: %q", i+1, cs)
		}
		if got, want := count(cs, "save-all flush"), i+1; got != want {
			t.Fatalf("Stage %d nach %d× save-all flush, erwartet %d", i+1, got, want)
		}
	}
	if len(fh.readiesCopy()) != 1 || len(fh.abortsCopy()) != 0 {
		t.Fatalf("Ready %d×, Abbruch %d×; erwartet 1 und 0", len(fh.readiesCopy()), len(fh.abortsCopy()))
	}
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt: %v", s)
	}
	// Der Stand enthält den gespeicherten Zustand (flushed), nie Spectator-Daten.
	head := e.co.head(t)
	if genSHA(t, head, "level.dat") != sha("flushed") {
		t.Fatalf("Head %d ohne den gespeicherten Stand", head.Number)
	}
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "start Neu"})
	r.cancel()
	e.drv.advanceTo(60 * time.Second)
	r.wait(t)
}

// Ändert sich die Datei auch beim dritten Versuch, scheitert die Übergabe:
// Modi zurück, auftauen, Speichern an, kein Stopp, kein Spectator, der
// Koordinator erfährt vom Abbruch; danach ist eine neue Übergabe möglich.
func TestHandoverStageThreeFailuresKeepPlaying(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	plan := &stagePlan{e: e, errs: []error{changedErr(), changedErr(), changedErr()}}
	e.d.stage = plan.stage
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)

	if plan.count() != 3 {
		t.Fatalf("%d× Stage, erwartet 3", plan.count())
	}
	equalStrings(t, "Befehle", shape(e.srv.cmdsAfterStart()), []string{
		"tick freeze", "tellraw", "save-off",
		"data get entity Alex playerGameType", "data get entity Bob playerGameType",
		"save-all flush", "save-all flush", "save-all flush",
		"gamemode creative Alex", "gamemode survival Bob", "tick unfreeze", "save-on", "tellraw",
	})
	select {
	case <-r.done:
		t.Fatalf("Sitzung endete: %v", r.err)
	default:
	}
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt: %v", s)
	}
	if len(fh.abortsCopy()) != 1 || len(fh.readiesCopy()) != 0 {
		t.Fatalf("Abbruch %d×, Ready %d×; erwartet 1 und 0", len(fh.abortsCopy()), len(fh.readiesCopy()))
	}
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "gescheitert Neu"})
	if c := e.co.hostCommits(); len(c) != 0 {
		t.Fatalf("%d Commits, erwartet keiner", len(c))
	}

	// Die Sitzung hostet weiter: Folge-Übergabe klappt (Stage geht jetzt durch).
	e.trigger(ch)
	e.drv.advanceTo(50 * time.Second)
	if len(fh.readiesCopy()) != 1 {
		t.Fatalf("Folge-Übergabe: %d× Ready, erwartet 1", len(fh.readiesCopy()))
	}
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "gescheitert Neu", "vorbereitung Neu", "start Neu"})
	r.cancel()
	e.drv.advanceTo(90 * time.Second)
	r.wait(t)
}

// Ein anderer Fehler beim Bereitstellen wird nicht wiederholt.
func TestHandoverStageOtherErrorNoRetry(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	plan := &stagePlan{e: e, errs: []error{errors.New("Platte voll")}}
	e.d.stage = plan.stage
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)
	if plan.count() != 1 {
		t.Fatalf("%d× Stage, erwartet 1", plan.count())
	}
	if n := count(e.srv.commands(), "save-all flush"); n != 1 {
		t.Fatalf("%d× save-all flush, erwartet 1", n)
	}
	if len(fh.abortsCopy()) != 1 || len(e.srv.stopTimes()) != 0 {
		t.Fatalf("Abbruch %d×, Stopps %v; erwartet 1 und keiner", len(fh.abortsCopy()), e.srv.stopTimes())
	}
	r.cancel()
	e.drv.advanceTo(90 * time.Second)
	r.wait(t)
}

// Wird während des Bereitstellens ein Ende verlangt, wird zurückgebaut und die
// Sitzung endet normal (Schluss-Stand), statt weiterzuhosten.
func TestHandoverEndWhileStaging(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	g := newStageGate(t)
	var calls atomic.Int32
	e.d.stage = func(ctx context.Context, w, st string, hc *worldsync.HashCache) error {
		err := g.stage(ctx, w, st, hc)
		if calls.Add(1) == 1 {
			return errors.New("Platte voll") // nur der Übergabe-Stand scheitert
		}
		return err
	}
	r := e.start(t)
	n := e.head.Number
	e.drv.advanceTo(30 * time.Second)
	e.drv.resync()
	ch <- struct{}{}
	e.drv.allow = 1
	e.drv.advanceTo(31 * time.Second)
	g.waitArrived(t)
	e.drv.allow = 0
	e.drv.resync()
	r.cancel()
	g.openNow()
	e.drv.advanceTo(60 * time.Second)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(e.srv.stopTimes()) != 1 {
		t.Fatalf("Stopps %v, erwartet einer", e.srv.stopTimes())
	}
	if h := e.co.head(t); h.Number != n+1 {
		t.Fatalf("Head %d, erwartet %d (Schluss-Stand)", h.Number, n+1)
	}
	if len(fh.abortsCopy()) != 0 {
		t.Fatalf("Abbruch beim Koordinator trotz Ende")
	}
}

// Kommt „bereit“ nicht, endet das Warten nach 6 Minuten wie ein Scheitern
// (nach dem Commit: Stopp ohne neuen Stand).
func TestHandoverReadyTimeout(t *testing.T) {
	e, _, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(30*time.Second + 6*time.Minute - time.Second)
	if len(e.srv.stopTimes()) != 0 {
		t.Fatalf("Server vor Ablauf der 6 Minuten gestoppt")
	}
	e.drv.advanceTo(30*time.Second + 7*time.Minute)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(e.srv.stopTimes()) != 1 || len(e.co.hostCommits()) != 1 {
		t.Fatalf("Stops %v, Commits %d; erwartet ein Stopp, nur der Übergabe-Stand", e.srv.stopTimes(), len(e.co.hostCommits()))
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
}

// Kommt das Übergabe-Signal während eines Zwischenstands und endet die
// Sitzung, bevor der fertig ist, wird beim Koordinator keine Übergabe mehr
// geöffnet und nichts eingefroren.
func TestHandoverPendingDroppedOnEnd(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	g := newGate(t)
	e.co.commitGate.Store(g)
	r := e.start(t)
	e.drv.advanceTo(SnapshotInterval - time.Second)
	e.drv.allow = 1
	e.drv.advanceTo(SnapshotInterval)
	g.waitArrived(t)
	e.drv.allow = 0
	e.drv.resync()
	ch <- struct{}{}
	r.cancel()
	g.release()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	fh.mu.Lock()
	starts := fh.starts
	fh.mu.Unlock()
	if starts != 0 {
		t.Fatalf("%d× HandoverStart, erwartet keiner", starts)
	}
	if c := e.srv.cmdsAfterStart(); len(c) != 0 {
		t.Fatalf("Befehle %q, erwartet keine", c)
	}
}

// Ein Ende während der Übergabe (nach dem Einreichen) wird zurückgehalten:
// Der Server endet ohne neuen Stand, der Zuschlag geht zurück.
func TestHandoverEndWhileWaiting(t *testing.T) {
	e, _, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	r := e.start(t)
	n := e.head.Number
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(31 * time.Second)
	e.drv.resync()
	r.cancel()
	e.drv.advanceTo(40 * time.Second)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c := e.co.hostCommits(); len(c) != 1 {
		t.Fatalf("%d Commits, erwartet nur den Übergabe-Stand", len(c))
	}
	if h := e.co.head(t); h.Number != n+1 {
		t.Fatalf("Head %d, erwartet %d", h.Number, n+1)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
}

// Ist kein Kandidat da (409 auf HandoverStart), wird nichts eingefroren: Die
// Sitzung endet normal, mit Schluss-Stand und Rückgabe.
func TestHandoverNobody(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, nil, "Alex")
	fh.conflict = "kein Kandidat bereit"
	r := e.start(t)
	n := e.head.Number
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cmds := e.srv.cmdsAfterStart(); len(cmds) != 0 {
		t.Fatalf("Befehle %q, erwartet keine (nichts eingefroren)", cmds)
	}
	final := e.co.head(t)
	if final.Number != n+1 || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
		t.Fatalf("Head %d, erwartet %d mit dem Stand nach Stop", final.Number, n+1)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
	checkUploaded(t, e.checkAfterRun(t), final)
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"keiner da "})
}

// Neuer Host: Der erste tunnel nimmt den Ersatz-Port; meldet das
// Lebenszeichen MainPort, kommt ein zweiter tunnel dazu (und endet mit der
// Sitzung). Vorher wird ein abgelehnter Zwischenstand (409) still
// ausgelassen statt zu fencen. Nach dem Start taut der Host einmal auf.
func TestHandoverAcceptMainPort(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	e.d.TunnelPort = 15566
	e.co.hbResp.Store(&api.HeartbeatResponse{})
	e.co.rejectCommits.Store(true) // der wartende Zuschlag darf noch nicht einreichen
	r := e.start(t)
	n := e.head.Number
	e.drv.advanceTo(SnapshotInterval + 20*time.Second)
	select {
	case <-r.done:
		t.Fatalf("Sitzung endete nach dem abgelehnten Zwischenstand: %v", r.err)
	default:
	}
	if c := len(e.co.hostCommits()); c == 0 {
		t.Fatalf("kein Zwischenstand versucht")
	}
	if got := e.srv.commands(); !slices.Equal(got, []string{"tick unfreeze"}) {
		t.Fatalf("Befehle %q, erwartet einmal tick unfreeze", got)
	}
	if c := e.tn.count(); c != 1 {
		t.Fatalf("%d tunnel vor der Beförderung, erwartet 1", c)
	}

	e.co.rejectCommits.Store(false)
	e.co.hbResp.Store(&api.HeartbeatResponse{MainPort: true})
	e.drv.advanceTo(SnapshotInterval + 40*time.Second)
	if c := e.tn.count(); c != 2 {
		t.Fatalf("%d tunnel nach der Beförderung, erwartet 2", c)
	}
	// Weitere MainPort-Meldungen öffnen nichts Neues.
	e.drv.advanceTo(SnapshotInterval + 70*time.Second)
	if c := e.tn.count(); c != 2 {
		t.Fatalf("%d tunnel, erwartet weiter 2", c)
	}
	e.srv.setPlayers()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, running := e.tn.running(); running != 0 {
		t.Fatalf("%d tunnel laufen nach Run noch", running)
	}
	final := e.co.head(t)
	if final.Number <= n || final.MachineID != hostMachine {
		t.Fatalf("Head %d von %s, erwartet ein Stand des neuen Hosts", final.Number, final.MachineID)
	}
	checkUploaded(t, e.checkAfterRun(t), final)
}

func TestParseGameMode(t *testing.T) {
	for in, want := range map[string]string{
		"Alex has the following entity data: 0":   "survival",
		"Alex has the following entity data: 1":   "creative",
		"Alex has the following entity data: 2":   "adventure",
		"Alex has the following entity data: 3":   "spectator",
		"Alex has the following entity data: 3\n": "spectator",
		"Alex has the following entity data: 7":   "",
		"No entity was found":                     "",
		"":                                        "",
	} {
		got, ok := parseGameMode(in)
		if got != want || ok != (want != "") {
			t.Errorf("parseGameMode(%q) = %q, %v; erwartet %q", in, got, ok, want)
		}
	}
}

func TestValidTransferTarget(t *testing.T) {
	for in, ok := range map[string]bool{
		"neu.example.org:15566": true,
		"203.0.113.5:15566":     true,
		"neu.example.org":       false,
		"neu example:15566":     false,
		"a;b:15566":             false,
		"neu.example.org:0":     false,
		"neu.example.org:99999": false,
		":15566":                false,
		"x:15566 @a":            false,
	} {
		if _, _, got := validTransferTarget(in); got != ok {
			t.Errorf("validTransferTarget(%q) = %v, erwartet %v", in, got, ok)
		}
	}
}

// Endet ein wartender, nie beförderter Zuschlag, nimmt der Koordinator den
// Schluss-Stand nicht an (409): kein Fencing, nichts nachzureichen.
func TestHandoverAcceptEndsUnpromoted(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	e.d.TunnelPort = 15566
	e.co.rejectCommits.Store(true)
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.srv.setPlayers()
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st := e.checkAfterRun(t); st.PendingUpload {
		t.Fatalf("Zustand %+v, erwartet nichts nachzureichen", *st)
	}
}

// Läuft die Gesamtfrist der Vorbereitung ab (Stage hängt), wird abgebrochen:
// kein Spectator, Modi zurück, der Server läuft weiter.
func TestHandoverPrepareLimit(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "1"}, "Alex")
	handoverPrepareLimit = 300 * time.Millisecond
	var calls atomic.Int32
	e.d.stage = func(ctx context.Context, w, st string, hc *worldsync.HashCache) error {
		if calls.Add(1) > 1 {
			return worldsync.StageCachedContext(ctx, w, st, hc)
		}
		<-ctx.Done() // hängt, bis die Frist den Kontext beendet
		return context.Cause(ctx)
	}
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)
	cmds := e.srv.commands()
	if count(cmds, "gamemode spectator Alex") != 0 {
		t.Fatalf("Spectator trotz abgelaufener Frist: %q", cmds)
	}
	if count(cmds, "tick unfreeze") != 2 || count(cmds, "save-on") != 1 || count(cmds, "gamemode creative Alex") != 1 {
		t.Fatalf("Rückbau fehlt: %q", cmds)
	}
	if len(fh.abortsCopy()) != 1 || len(e.srv.stopTimes()) != 0 {
		t.Fatalf("Abbrüche %d, Stopps %v; erwartet 1 und keiner", len(fh.abortsCopy()), e.srv.stopTimes())
	}
	r.cancel()
	e.drv.advanceTo(90 * time.Second)
	r.wait(t)
}

// Bleibt bis zur Frist des Koordinators zu wenig Zeit für Upload und Ready,
// wird gar nicht erst Spectator.
func TestHandoverNoSpectatorWithoutTime(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	handoverCoordLimit, handoverUploadReserve = time.Minute, time.Minute
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)
	if n := count(e.srv.commands(), "gamemode spectator Alex"); n != 0 {
		t.Fatalf("%d× Spectator trotz zu wenig Zeit", n)
	}
	if len(fh.abortsCopy()) != 1 || len(fh.readiesCopy()) != 0 || len(e.srv.stopTimes()) != 0 {
		t.Fatalf("Abbrüche %d, Ready %d, Stopps %v", len(fh.abortsCopy()), len(fh.readiesCopy()), e.srv.stopTimes())
	}
	r.cancel()
	e.drv.advanceTo(90 * time.Second)
	r.wait(t)
}

// Der Abbruch beim Koordinator wird wiederholt.
func TestHandoverAbortRetried(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	fh.abortErr = 2
	e.d.stage = (&stagePlan{e: e, errs: []error{errors.New("Platte voll")}}).stage
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)
	fh.mu.Lock()
	tries, ok := fh.abortTry, len(fh.aborts)
	fh.mu.Unlock()
	if tries != 3 || ok != 1 {
		t.Fatalf("%d Versuche, %d angekommen; erwartet 3 und 1", tries, ok)
	}
	r.cancel()
	e.drv.advanceTo(90 * time.Second)
	r.wait(t)
}

// Hängt eine frühere Übergabe noch beim Koordinator (409 „läuft schon“),
// stoppt der Server nicht: Hinweis, und ein neuer Versuch ist möglich.
func TestHandoverBusyKeepsPlaying(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "0"}, "Alex")
	fh.conflict = "Es läuft schon eine Host-Übergabe"
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)
	select {
	case <-r.done:
		t.Fatalf("Sitzung endete: %v", r.err)
	default:
	}
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt: %v", s)
	}
	if a := e.srv.announcements(); len(a) == 0 || !strings.Contains(a[len(a)-1].text, "bitte gleich nochmal") {
		t.Fatalf("Ansagen %+v", a)
	}
	fh.mu.Lock()
	fh.conflict = ""
	fh.mu.Unlock()
	e.trigger(ch)
	e.drv.advanceTo(50 * time.Second)
	if len(fh.readiesCopy()) != 1 {
		t.Fatalf("zweiter Versuch: %d× Ready, erwartet 1", len(fh.readiesCopy()))
	}
	r.cancel()
	e.drv.advanceTo(90 * time.Second)
	r.wait(t)
}

// Wer während der Sperre geht, bekommt beim Rückbau keinen Befehl, aber nach
// dem Wiedereinloggen seinen Modus.
func TestHandoverRestoreForLeaver(t *testing.T) {
	e, _, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	plan := &stagePlan{e: e}
	plan.errs = []error{changedErr(), changedErr(), changedErr()}
	plan.hook = func(n int) {
		if n == 2 {
			e.srv.setPlayers("Bob") // Alex geht vor dem Scheitern
		}
	}
	e.d.stage = plan.stage
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(40 * time.Second)
	cmds := e.srv.commands()
	if count(cmds, "gamemode creative Alex") != 0 || count(cmds, "gamemode survival Bob") != 1 {
		t.Fatalf("Rückbau: %q", cmds)
	}
	e.srv.setPlayers("Alex", "Bob")
	e.drv.advanceTo(80 * time.Second)
	if n := count(e.srv.commands(), "gamemode creative Alex"); n != 1 {
		t.Fatalf("%d× Modus für den Heimkehrer, erwartet 1: %q", n, e.srv.commands())
	}
	r.cancel()
	e.drv.advanceTo(120 * time.Second)
	r.wait(t)
}

// Kommt ein Spieler mit bekanntem Modus während der Sperre zurück, wird er
// wieder Spectator.
func TestHandoverReturnerLockedAgain(t *testing.T) {
	e, _, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(31 * time.Second)
	e.srv.setPlayers("Bob")
	e.drv.advanceTo(35 * time.Second)
	e.srv.setPlayers("Alex", "Bob")
	e.drv.advanceTo(40 * time.Second)
	if n := count(e.srv.commands(), "gamemode spectator Alex"); n != 2 {
		t.Fatalf("%d× Spectator für Alex, erwartet 2: %q", n, e.srv.commands())
	}
	if n := count(e.srv.commands(), "data get entity Alex playerGameType"); n != 1 {
		t.Fatalf("Modus von Alex %d× gelesen, erwartet 1 (der bekannte bleibt)", n)
	}
	r.cancel()
	e.drv.advanceTo(90 * time.Second)
	r.wait(t)
}
