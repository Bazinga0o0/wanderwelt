package host

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// fakeHandover spielt die Übergabe-Endpunkte des Koordinators.
type fakeHandover struct {
	mu       sync.Mutex
	conflict string // gesetzt: HandoverStart → 409 mit diesem Text
	starts   int
	readies  []api.HandoverReadyRequest
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
	default:
		return false
	}
	return true
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
		"tick freeze", "save-off",
		"data get entity Alex playerGameType", "data get entity Bob playerGameType",
		"save-all flush",
		"gamemode spectator Alex", "gamemode spectator Bob",
		"tellraw",
	}
	equalStrings(t, "Befehle der Sperre", shape(e.srv.cmdsAfterStart()), want)
	if c := e.srv.cmdsAfterStart()[7]; !strings.Contains(c, "Neu") || !strings.Contains(c, "übergeben") {
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
// auftauen, Speichern an) und endet normal mit Schluss-Stand.
func TestHandoverFallbackBeforeCommit(t *testing.T) {
	e, fh, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	var calls int
	e.co.setOnCommit(func() {
		calls++
		e.co.rejectCommits.Store(calls == 1) // nur der erste Commit (Übergabe-Stand)
	})
	r := e.start(t)
	n := e.head.Number
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(120 * time.Second)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cmds := e.srv.cmdsAfterStart()
	equalStrings(t, "Befehle", shape(cmds[8:]), []string{
		"gamemode creative Alex", "gamemode survival Bob", "tick unfreeze", "save-on", "tellraw",
	})
	commits := e.co.hostCommits()
	if len(commits) != 2 || commits[0].BaseGeneration != n || commits[1].BaseGeneration != n {
		t.Fatalf("Commits %+v, erwartet zwei auf Basis %d", commits, n)
	}
	final := e.co.head(t)
	if final.Number != n+1 || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
		t.Fatalf("Head %d, erwartet %d mit dem Stand nach Stop", final.Number, n+1)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
	checkUploaded(t, e.checkAfterRun(t), final)
	equalStrings(t, "Hinweise", fh.notesCopy(), []string{"vorbereitung Neu", "gescheitert Neu"})
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
