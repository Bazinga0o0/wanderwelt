package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorcon/rcon"
	"github.com/gorcon/rcon/rcontest"
	"gopkg.in/yaml.v3"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/mcserver"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

const (
	adminEmail   = "admin@example.org"
	hostMachine  = "pc-host"
	seedMachine  = "pc-seed"
	otherMachine = "pc-anderer"
	mcVersion    = "26.2"
	tunnelToken  = "geheimer-token"
	tunnelAddr   = "tunnel.example.org:7443"

	// helperEnv schaltet das Testprogramm in den Helfermodus (TestMain).
	helperEnv = "WANDERWELT_HOST_TEST_HELPER"
)

// TestMain: Im Helfermodus ist das Testprogramm ein harmloser Dauerprozess –
// „tunnel“ und „Java“ der Sitzung, „verwaister Server“ für Recover. Er endet
// spätestens nach zwei Minuten von selbst, falls ihn niemand beendet. Im
// Modus "stdin" endet er wie ein Server nach "stop" sauber (Exit-Code 0),
// sobald stdin geschlossen wird.
func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "sleep":
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	case "stdin":
		go func() {
			time.Sleep(2 * time.Minute)
			os.Exit(0)
		}()
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func helperCmd(ctx context.Context, exe string) *exec.Cmd {
	c := exec.CommandContext(ctx, exe)
	c.Env = append(os.Environ(), helperEnv+"=sleep")
	return c
}

// ---- Fake-Uhr und Gleichschritt ----

var t0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

// fakeClock ist eine von Hand gestellte Uhr. After-Kanäle feuern, wenn set
// die Zeit über ihren Zeitpunkt schiebt.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
	longest time.Duration // längste verlangte Wartezeit
}

// maxWait ist die längste Wartezeit, die die Sitzung je verlangt hat.
func (c *fakeClock) maxWait() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.longest
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.longest = max(c.longest, d)
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(t) {
			keep = append(keep, w)
		} else {
			w.ch <- t
		}
	}
	c.waiters = keep
}

// driver stellt die Fake-Uhr im Gleichschritt mit der Sitzung weiter: erst
// wenn die Sitzung meldet, dass höchstens allow Schritte laufen (sonst
// keiner) und sie auf ihren nächsten Termin wartet (Haken Deps.idle), und
// genau bis zu diesem Termin.
type driver struct {
	t   *testing.T
	clk *fakeClock

	mu       sync.Mutex
	seq      uint64
	next     time.Time
	inflight int
	wake     chan struct{}

	// Nur im Test-Goroutine:
	seen   uint64
	known  bool      // nextAt gilt noch (seitdem nichts gefeuert)
	nextAt time.Time // nächster Termin der Sitzung (Null: keiner)
	allow  int       // so viele laufende Schritte sind beim Weiterstellen erlaubt
	done   <-chan struct{}
}

func newDriver(t *testing.T) *driver {
	return &driver{t: t, clk: &fakeClock{now: t0}, wake: make(chan struct{})}
}

// idle ist der Haken der Sitzung.
func (d *driver) idle(next time.Time, inflight int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	d.next, d.inflight = next, inflight
	close(d.wake)
	d.wake = make(chan struct{})
}

// await wartet auf eine neue Meldung mit höchstens allow laufenden
// Schritten; false, wenn Run vorher endet.
func (d *driver) await() bool {
	timeout := time.After(30 * time.Second)
	for {
		d.mu.Lock()
		if d.seq > d.seen {
			d.seen = d.seq
			if d.inflight <= d.allow {
				d.nextAt = d.next
				d.mu.Unlock()
				return true
			}
		}
		wake := d.wake
		d.mu.Unlock()
		select {
		case <-wake:
		case <-d.done:
			return false
		case <-timeout:
			d.t.Fatalf("Sitzung kommt nicht zur Ruhe (Uhr bei %v)", d.clk.Now().Sub(t0))
		}
	}
}

// resync kommt VOR einem Eingriff außerhalb der Uhr (Prozess beenden,
// Abbruch, Tor öffnen): Alle bisherigen Meldungen gelten als gesehen; vor
// dem nächsten Weiterstellen wird auf eine Meldung nach dem Eingriff
// gewartet. (Schritte melden sich auch mitten im Lauf, etwa nach release;
// eine solche ältere Meldung darf nicht für den Eingriff zählen.)
func (d *driver) resync() {
	d.mu.Lock()
	d.seen = d.seq
	d.mu.Unlock()
	d.known = false
}

// advanceTo stellt die Uhr Termin für Termin bis t0+to. Endet Run
// unterwegs, hört advanceTo auf.
func (d *driver) advanceTo(to time.Duration) {
	d.t.Helper()
	target := t0.Add(to)
	if target.Before(d.clk.Now()) {
		d.t.Fatalf("advanceTo(%v): Uhr steht schon bei %v", to, d.clk.Now().Sub(t0))
	}
	for {
		if !d.known {
			if !d.await() {
				return
			}
			d.known = true
		}
		if d.nextAt.IsZero() || d.nextAt.After(target) {
			d.clk.set(target)
			return
		}
		d.clk.set(d.nextAt)
		d.known = false
	}
}

func (d *driver) now() time.Duration { return d.clk.Now().Sub(t0) }

// ---- Koordinator ----

type noUUIDs struct{}

func (noUUIDs) UUID(context.Context, string) (string, error) {
	return "", errors.New("im Test nicht gebraucht")
}

// gate hält Anfragen an, bis release kommt.
type gate struct {
	arrived chan struct{} // geschlossen, sobald die erste Anfrage wartet
	open    chan struct{}
	once    sync.Once
	opened  sync.Once
}

func newGate(t *testing.T) *gate {
	g := &gate{arrived: make(chan struct{}), open: make(chan struct{})}
	t.Cleanup(g.release)
	return g
}

func (g *gate) pass() {
	g.once.Do(func() { close(g.arrived) })
	select {
	case <-g.open:
	case <-time.After(30 * time.Second):
	}
}

func (g *gate) release() { g.opened.Do(func() { close(g.open) }) }

func (g *gate) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("Anfrage kommt nicht an")
	}
}

// coord ist ein echter Koordinator (Dev-Modus, ein Admin) hinter httptest,
// mit Schaltern für Störungen.
type coord struct {
	c   *client.Client
	url string

	hits       atomic.Int64 // alle Anfragen
	heartbeats atomic.Int64 // Lebenszeichen

	down           atomic.Bool // jede Verbindung bricht ohne Antwort ab
	failHeartbeats atomic.Bool // nur Lebenszeichen brechen ab
	failBlobGets   atomic.Bool // Blobs laden bricht ab
	failBlobPuts   atomic.Bool // Blobs hochladen bricht ab
	rejectCommits  atomic.Bool // Commits von hostMachine → 409
	dropResponse   atomic.Bool // nächster Commit von hostMachine wird angenommen, die Antwort geht verloren
	putGate        atomic.Pointer[gate]
	commitGate     atomic.Pointer[gate]
	hbGate         atomic.Pointer[gate] // hält Lebenszeichen an
	// hbResp: gesetzt, antworten Lebenszeichen 200 mit diesem Körper (neuer
	// Koordinator), sonst 204 wie der Koordinator dieses Stands.
	hbResp atomic.Pointer[api.HeartbeatResponse]
	// rejectPlayers: Commits mit "players" → 400 wie ein älterer
	// Koordinator (unbekanntes Feld).
	rejectPlayers atomic.Bool
	// lists ersetzt Whitelist und Ops der Host-Einstellungen (der
	// Koordinator im Test löst keine UUIDs auf); failSettings lässt sie
	// abbrechen; settingsGets zählt die Abrufe.
	lists        atomic.Pointer[hostLists]
	failSettings atomic.Bool
	settingsGets atomic.Int64
	// handoverH beantwortet /v1/handover/* (der echte Koordinator kennt sie
	// hier noch nicht): true heißt beantwortet.
	handoverH atomic.Pointer[func(w http.ResponseWriter, r *http.Request) bool]

	mu       sync.Mutex
	commits  []api.CommitRequest // Commit-Versuche von hostMachine (auch erfolglose)
	onCommit func()
	readies  []bool // ready aus jedem Lebenszeichen von hostMachine
}

// newCoord: tweak (darf fehlen) ändert die Konfiguration vor dem Start.
// Die Leerlauf-Frist ist hier 2 Minuten (empty_timeout: 2m), damit die
// Tests kurz bleiben; den Standard ohne Angabe prüft
// TestEmptyTimeoutFromHostSettings.
func newCoord(t *testing.T, tweak ...func(*config.Config)) *coord {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:       dir,
		StorageBudget: 1 << 30,
		EmptyTimeout:  2 * time.Minute,
		Members:       []config.Member{{Email: adminEmail, Minecraft: "Admin", Admin: true}},
	}
	cfg.Auth.Mode = "dev"
	for _, f := range tweak {
		f(cfg)
	}
	lm, err := lease.New(filepath.Join(dir, "lease.json"), server.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, lm, st, portholder.Noop(), noUUIDs{})

	co := &coord{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		co.hits.Add(1)
		if h := co.handoverH.Load(); h != nil && strings.HasPrefix(r.URL.Path, "/v1/handover/") {
			if (*h)(w, r) {
				return
			}
		}
		isHB := r.URL.Path == "/v1/lease/heartbeat"
		isBlob := strings.HasPrefix(r.URL.Path, "/v1/blobs/") && r.URL.Path != "/v1/blobs/missing"
		hostCommit, withPlayers := false, false
		if r.Method == http.MethodPost && r.URL.Path == "/v1/generations" {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				var req api.CommitRequest
				if json.Unmarshal(body, &req) == nil && req.MachineID == hostMachine {
					hostCommit = true
					var fields map[string]json.RawMessage
					if json.Unmarshal(body, &fields) == nil {
						_, withPlayers = fields["players"]
					}
					co.mu.Lock()
					co.commits = append(co.commits, req)
					f := co.onCommit
					co.mu.Unlock()
					if f != nil {
						f()
					}
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if r.URL.Path == "/v1/host/settings" {
			co.settingsGets.Add(1)
			if co.failSettings.Load() {
				panic(http.ErrAbortHandler)
			}
			if l := co.lists.Load(); l != nil {
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, r)
				var hs api.HostSettings
				if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &hs) != nil {
					copyResponse(w, rec)
					return
				}
				hs.Whitelist, hs.Ops = l.whitelist, l.ops
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(hs)
				return
			}
		}
		if hostCommit && withPlayers && co.rejectPlayers.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"Anfrage nicht lesbar: json: unknown field \"players\""}`)
			return
		}
		if isHB {
			co.heartbeats.Add(1)
			body, err := io.ReadAll(r.Body)
			if err == nil {
				var req api.HeartbeatRequest
				if json.Unmarshal(body, &req) == nil && req.MachineID == hostMachine {
					co.mu.Lock()
					co.readies = append(co.readies, req.Ready)
					co.mu.Unlock()
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if co.down.Load() || (isHB && co.failHeartbeats.Load()) ||
			(isBlob && r.Method == http.MethodGet && co.failBlobGets.Load()) ||
			(isBlob && r.Method == http.MethodPut && co.failBlobPuts.Load()) {
			panic(http.ErrAbortHandler)
		}
		if g := co.putGate.Load(); g != nil && isBlob && r.Method == http.MethodPut {
			g.pass()
		}
		if g := co.hbGate.Load(); g != nil && isHB {
			g.pass()
		}
		if hostCommit {
			if co.rejectCommits.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"error":"lease lost"}`)
				return
			}
			if g := co.commitGate.Load(); g != nil {
				g.pass()
			}
			if co.dropResponse.CompareAndSwap(true, false) {
				srv.ServeHTTP(httptest.NewRecorder(), r)
				panic(http.ErrAbortHandler)
			}
		}
		if resp := co.hbResp.Load(); resp != nil && isHB {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, r)
			if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
				copyResponse(w, rec)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		if isHB {
			// Ohne hbResp verhält sich der Koordinator wie ein älterer:
			// 204 ohne Körper (der echte antwortet immer mit JSON).
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, r)
			if rec.Code == http.StatusOK {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			copyResponse(w, rec)
			return
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	co.url = ts.URL
	co.c = client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: adminEmail})
	return co
}

// hostLists sind Whitelist und Ops für coord.lists.
type hostLists struct {
	whitelist, ops []api.Player
}

// copyResponse gibt eine aufgezeichnete Antwort weiter.
func copyResponse(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	w.Write(rec.Body.Bytes())
}

func (co *coord) hostCommits() []api.CommitRequest {
	co.mu.Lock()
	defer co.mu.Unlock()
	return append([]api.CommitRequest(nil), co.commits...)
}

// heartbeatReadies liefert ready aus allen Lebenszeichen von hostMachine.
func (co *coord) heartbeatReadies() []bool {
	co.mu.Lock()
	defer co.mu.Unlock()
	return slices.Clone(co.readies)
}

func (co *coord) setOnCommit(f func()) {
	co.mu.Lock()
	defer co.mu.Unlock()
	co.onCommit = f
}

func (co *coord) head(t *testing.T) *api.Generation {
	t.Helper()
	g, err := co.c.Head(t.Context())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	return g
}

func (co *coord) status(t *testing.T) *api.Status {
	t.Helper()
	s, err := co.c.Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return s
}

// seed reicht als ein anderer PC einen neuen Stand mit files ein und gibt
// den Zuschlag zurück (wie eine abgeschlossene Sitzung eines anderen Hosts).
func (co *coord) seed(t *testing.T, files map[string]string) *api.Generation {
	t.Helper()
	return co.seedAs(t, seedMachine, files)
}

// seedAs ist seed als PC machine.
func (co *coord) seedAs(t *testing.T, machine string, files map[string]string) *api.Generation {
	t.Helper()
	ctx := t.Context()
	seedMachine := machine
	req := api.PlayRequest{MachineID: seedMachine, UploadOnly: true}
	if h := co.head(t); h != nil {
		req.BaseWorldID, req.BaseGeneration = h.WorldID, h.Number
	}
	resp, err := co.c.Play(ctx, req)
	if err != nil || resp.Role != api.RoleHost {
		t.Fatalf("Zuschlag zum Anlegen: %+v, %v", resp, err)
	}
	dir := t.TempDir()
	writeTree(t, dir, files)
	mf, err := worldsync.BuildManifest(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := worldsync.UploadAndCommit(ctx, co.c, dir, api.CommitRequest{
		LeaseID:          resp.LeaseID,
		MachineID:        seedMachine,
		BaseWorldID:      req.BaseWorldID,
		BaseGeneration:   req.BaseGeneration,
		MinecraftVersion: mcVersion,
		Files:            mf,
	})
	if err != nil {
		t.Fatalf("Stand anlegen: %v", err)
	}
	if err := co.c.Release(ctx, api.ReleaseRequest{LeaseID: resp.LeaseID, MachineID: seedMachine}); err != nil {
		t.Fatal(err)
	}
	return gen
}

// acquire holt für machine einen Zuschlag auf head (ohne Sammelfenster).
func (co *coord) acquire(t *testing.T, head *api.Generation, machine string) int64 {
	t.Helper()
	resp, err := co.c.Play(t.Context(), api.PlayRequest{
		MachineID: machine, UploadOnly: true,
		BaseWorldID: head.WorldID, BaseGeneration: head.Number,
	})
	if err != nil || resp.Role != api.RoleHost || resp.LeaseID == 0 {
		t.Fatalf("Zuschlag: %+v, %v", resp, err)
	}
	return resp.LeaseID
}

// ---- Fake-Server ----

// fakeServer spielt den Minecraft-Server. Er „speichert“ bei SaveBarrier,
// bei release (save-on: das automatische Speichern schreibt weiter) und bei
// Stop in level.dat, jedes Mal mit neuer Größe, damit Stage die Änderung
// sicher sieht. Mit withProcess läuft als „Java“ ein Helferprozess, dessen
// PID PID() liefert.
type fakeServer struct {
	t           *testing.T
	clk         *fakeClock
	world       string
	marker      string
	exe         string
	withProcess bool
	stopErr     error
	startErr    error // Start scheitert damit (nach dem Helferprozess, falls withProcess)
	// hidePIDInStart: PID liefert 0, solange Start läuft (die PID ist erst
	// zu sehen, wenn Start zurückkehrt).
	hidePIDInStart bool
	inStart        bool
	onPrepare      func()
	onStart        func()
	onStop         func()
	// stopGate: nil heißt, Stop endet sofort. Sonst endet Stop erst, wenn
	// stopGate geschlossen wird, sauber und mit Speichern; endet ctx vorher,
	// wird der Server hart beendet (Stop meldet einen Fehler, hardAt die
	// Uhrzeit der Sitzung).
	stopGate chan struct{}
	// lateStop: Stop darf mit schon beendetem Kontext kommen (die Frist nach
	// dem Fencing ist schon um); der Server wird dann sofort hart beendet.
	// Ohne lateStop ist das ein Testfehler.
	lateStop bool

	mu           sync.Mutex
	players      []string
	prepares     int
	starts       int
	startWorld   string // level.dat beim Start
	stops        []time.Duration
	hardAt       []time.Duration // hart beendet (stopUntil nicht erreicht)
	barriers     int
	releases     int
	holding      bool     // zwischen SaveBarrier und release
	barrierSaved []string // level.dat je SaveBarrier
	writes       int
	lastSaved    string
	exited       chan error
	proc         *exec.Cmd
	procDone     chan struct{}
	says         []timedText // Ansagen (Say) mit der Uhrzeit der Sitzung
	cmds         []timedText // Befehle (Command)
	// cmdResp (darf nil sein) antwortet auf Befehle; läuft unter f.mu (nicht
	// f.mu selbst nehmen, Zustand über players direkt ändern).
	cmdResp func(f *fakeServer, cmd string) (string, bool)
}

// timedText ist eine Ansage oder ein Befehl samt Uhrzeit der Sitzung.
type timedText struct {
	at   time.Duration
	text string
}

func (f *fakeServer) Prepare(_ context.Context, in mcserver.PrepareInput) error {
	if in.Dir == "" || in.Paths == nil || in.WorldDirName != WorldName {
		return fmt.Errorf("Prepare mit unvollständiger Eingabe: %+v", in)
	}
	if f.onPrepare != nil {
		f.onPrepare()
	}
	f.mu.Lock()
	f.prepares++
	f.mu.Unlock()
	return nil
}

func (f *fakeServer) Start(context.Context) error {
	f.mu.Lock()
	f.inStart = true
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inStart = false
		f.mu.Unlock()
	}()
	if _, err := os.Stat(f.marker); err != nil {
		return fmt.Errorf("Markierungsdatei fehlt beim Start: %w", err)
	}
	world, err := os.ReadFile(filepath.Join(f.world, "level.dat"))
	if err != nil {
		return fmt.Errorf("Welt nicht geladen: %w", err)
	}
	f.mu.Lock()
	f.starts++
	f.startWorld = string(world)
	f.exited = make(chan error, 1)
	if f.withProcess {
		cmd := helperCmd(context.Background(), f.exe)
		if err := cmd.Start(); err != nil {
			f.mu.Unlock()
			return err
		}
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		f.t.Cleanup(func() { cmd.Process.Kill(); <-done })
		f.proc, f.procDone = cmd, done
	}
	f.mu.Unlock()
	if f.onStart != nil {
		f.onStart()
	}
	return f.startErr
}

func (f *fakeServer) PID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.proc == nil || f.hidePIDInStart && f.inStart {
		return 0
	}
	select {
	case <-f.procDone:
		return 0
	default:
		return f.proc.Process.Pid
	}
}

func (f *fakeServer) Stop(ctx context.Context) error {
	late := ctx.Err() != nil
	if late && !f.lateStop {
		f.t.Errorf("Stop mit schon beendetem Kontext: %v", ctx.Err())
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Errorf("Stop ohne eigene Frist")
	}
	if f.isHolding() {
		f.t.Errorf("Stop, während ein Zwischenstand zwischen SaveBarrier und release steht")
	}
	f.mu.Lock()
	f.stops = append(f.stops, f.clk.Now().Sub(t0))
	f.mu.Unlock()
	if f.onStop != nil {
		f.onStop()
	}
	stopErr := f.stopErr
	hard := func(err error) {
		// Hart beendet, nichts mehr gespeichert.
		f.mu.Lock()
		f.hardAt = append(f.hardAt, f.clk.Now().Sub(t0))
		f.mu.Unlock()
		stopErr = err
	}
	switch {
	case late:
		hard(errors.New("Frist schon um, hart beendet"))
	case f.stopGate != nil:
		select {
		case <-f.stopGate:
		case <-ctx.Done():
			hard(errors.New("Server hat auf stop nicht reagiert, hart beendet"))
		}
	}
	if stopErr == nil {
		f.save("gestoppt") // beim Stoppen speichert der Server ein letztes Mal
	}
	f.mu.Lock()
	ch, proc, done := f.exited, f.proc, f.procDone
	f.exited = nil
	f.mu.Unlock()
	if proc != nil {
		proc.Process.Kill()
		<-done
	}
	if ch != nil {
		ch <- stopErr
		close(ch)
	}
	return stopErr
}

func (f *fakeServer) hardTimes() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.hardAt...)
}

func (f *fakeServer) SaveBarrier(context.Context) (func(context.Context) error, error) {
	f.mu.Lock()
	f.barriers++
	f.holding = true
	n := f.barriers
	f.mu.Unlock()
	content := f.save(fmt.Sprintf("stand %d", n))
	f.mu.Lock()
	f.barrierSaved = append(f.barrierSaved, content)
	f.mu.Unlock()
	return func(context.Context) error {
		f.mu.Lock()
		if !f.holding {
			f.t.Errorf("release ohne SaveBarrier")
		}
		f.holding = false
		f.releases++
		f.mu.Unlock()
		f.save(fmt.Sprintf("frei %d", n))
		return nil
	}, nil
}

func (f *fakeServer) Players(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.players...), nil
}

func (f *fakeServer) Exited() <-chan error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exited
}

func (f *fakeServer) Command(_ context.Context, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exited == nil {
		f.t.Errorf("Befehl %q, ohne dass der Server läuft", cmd)
	}
	f.cmds = append(f.cmds, timedText{f.clk.Now().Sub(t0), cmd})
	if f.cmdResp != nil {
		if resp, ok := f.cmdResp(f, cmd); ok {
			return resp, nil
		}
	}
	return "ok", nil
}

func (f *fakeServer) Say(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exited == nil {
		f.t.Errorf("Ansage %q, ohne dass der Server läuft", text)
	}
	f.says = append(f.says, timedText{f.clk.Now().Sub(t0), text})
	return nil
}

func (f *fakeServer) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.cmds {
		out = append(out, c.text)
	}
	return out
}

func (f *fakeServer) announcements() []timedText {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.says)
}

func (f *fakeServer) save(label string) string {
	f.mu.Lock()
	f.writes++
	content := label + " " + strings.Repeat("#", f.writes)
	f.lastSaved = content
	f.mu.Unlock()
	if err := os.WriteFile(filepath.Join(f.world, "level.dat"), []byte(content), 0o644); err != nil {
		f.t.Errorf("Fake-Server speichert: %v", err)
	}
	return content
}

// crash: Der Server endet von selbst (Absturz).
func (f *fakeServer) crash(err error) {
	f.mu.Lock()
	ch, proc, done := f.exited, f.proc, f.procDone
	f.exited = nil
	f.mu.Unlock()
	if proc != nil {
		proc.Process.Kill()
		<-done
	}
	ch <- err
	close(ch)
}

func (f *fakeServer) setPlayers(p ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.players = p
}

func (f *fakeServer) stopTimes() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.stops...)
}

func (f *fakeServer) counts() (prepares, starts, barriers, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prepares, f.starts, f.barriers, f.releases
}

func (f *fakeServer) isHolding() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.holding
}

func (f *fakeServer) saved() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSaved
}

// ---- tunnel ----

// tunnels erzeugt für TunnelCmd einen harmlosen Dauerprozess (das
// Testprogramm im Helfermodus) und merkt sich jeden.
type tunnels struct {
	exe string

	mu   sync.Mutex
	cmds []*exec.Cmd
}

func (tn *tunnels) cmd(ctx context.Context) *exec.Cmd {
	c := helperCmd(ctx, tn.exe)
	tn.mu.Lock()
	tn.cmds = append(tn.cmds, c)
	tn.mu.Unlock()
	return c
}

func (tn *tunnels) count() int {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return len(tn.cmds)
}

// running zählt gestartete tunnel-Prozesse und die davon noch nicht
// beendeten. Nur aufrufen, wenn die Sitzung gerade nicht auf sie wartet
// (nach Run oder aus Stop heraus, das erst nach dem Beenden von tunnel
// kommt).
func (tn *tunnels) running() (started, running int) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	for _, c := range tn.cmds {
		if c.Process == nil {
			continue
		}
		started++
		if c.ProcessState == nil {
			running++
		}
	}
	return started, running
}

// ---- Sitzung im Test ----

type hostEnv struct {
	co    *coord
	drv   *driver
	srv   *fakeServer
	tn    *tunnels
	head  *api.Generation
	lease int64
	dir   string
	d     Deps
	job   *procJob // über Deps.onJob, wenn ein Test es braucht
}

var seedFiles = map[string]string{
	"level.dat":        "anfang",
	"region/r.0.0.mca": "chunks",
}

func newHostEnv(t *testing.T, players []string) *hostEnv {
	t.Helper()
	co := newCoord(t)
	return newHostEnvOn(t, co, co.seed(t, seedFiles), players)
}

// newHostEnvOn: Sitzung für hostMachine mit einem Zuschlag auf head.
func newHostEnvOn(t *testing.T, co *coord, head *api.Generation, players []string) *hostEnv {
	t.Helper()
	lease := co.acquire(t, head, hostMachine)
	drv := newDriver(t)
	dir := filepath.Join(group.Home(), "server")
	exe := testExe(t)
	srv := &fakeServer{
		t: t, clk: drv.clk, exe: exe, players: players,
		world:  filepath.Join(dir, WorldName),
		marker: filepath.Join(dir, MarkerName),
	}
	e := &hostEnv{co: co, drv: drv, srv: srv, tn: &tunnels{exe: exe}, head: head, lease: lease, dir: dir}
	e.d = Deps{
		Client:      client.New(&group.Group{Coordinator: co.url, DevIdentityEmail: adminEmail}),
		Server:      srv,
		Paths:       &deps.Paths{Java: filepath.Join(group.Home(), "deps", "java", "bin", "java.exe")},
		LeaseID:     lease,
		MachineID:   hostMachine,
		Base:        head.Number,
		BaseWorldID: head.WorldID,
		Clock:       drv.clk,
		TunnelCmd:   e.tn.cmd,
		Dir:         dir,
		Group: &group.Group{
			Coordinator: co.url, TunnelServer: tunnelAddr, Token: tunnelToken,
			DevIdentityEmail: adminEmail,
		},
		State:            &group.State{MachineID: hostMachine},
		Profile:          api.Profile{XmxMB: 2048},
		MinecraftVersion: mcVersion,
		idle:             drv.idle,
	}
	return e
}

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func (e *hostEnv) start(t *testing.T) *runner {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runner{cancel: cancel, done: make(chan struct{})}
	e.drv.done = r.done
	go func() {
		r.err = Run(ctx, e.d)
		close(r.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(60 * time.Second):
			t.Errorf("Run endet auch nach Abbruch nicht")
		}
	})
	return r
}

func (r *runner) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(30 * time.Second):
		t.Fatal("Run endet nicht")
		return nil
	}
}

// killTunnel beendet den zuletzt gestarteten tunnel von außen und wartet,
// bis die Sitzung das verarbeitet hat.
func (e *hostEnv) killTunnel(t *testing.T) {
	t.Helper()
	e.tn.mu.Lock()
	c := e.tn.cmds[len(e.tn.cmds)-1]
	e.tn.mu.Unlock()
	e.drv.resync()
	if err := c.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	e.drv.advanceTo(e.drv.now())
}

// checkAfterRun prüft, was nach jedem Ende mit sauberem Stop gilt:
// Markierung weg, alle tunnel-Prozesse beendet, Zustand auf der Platte wie
// im Speicher.
func (e *hostEnv) checkAfterRun(t *testing.T) *group.State {
	t.Helper()
	if _, err := os.Lstat(e.srv.marker); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Markierungsdatei noch da (%v), obwohl sauber gestoppt", err)
	}
	started, running := e.tn.running()
	if started == 0 {
		t.Errorf("tunnel wurde nie gestartet")
	}
	if running != 0 {
		t.Errorf("%d tunnel-Prozesse laufen nach Run noch", running)
	}
	if m := e.drv.clk.maxWait(); m > time.Second {
		t.Errorf("Sitzung wartete %v am Stück, höchstens 1 s erlaubt", m)
	}
	st, err := group.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if *st != *e.d.State {
		t.Errorf("Zustand auf der Platte %+v, im Speicher %+v", *st, *e.d.State)
	}
	return st
}

// stageGate hält das erste Bereitstellen (den ersten Zwischenstand) an, bis
// sein Kontext endet. So steht ein Zwischenstand zwischen SaveBarrier und
// release, während Ende oder Fencing kommen. Danach stellt es wie gewohnt
// bereit (StageCachedContext bricht dann selbst ab) – oder, mit complete,
// hat es schon vorher vollständig bereitgestellt und meldet trotz Abbruch
// Erfolg (ein Stage, das den Abbruch nicht mehr bemerkt hat). Mit
// ignoreCancel wartet es auch nach dem Abbruch weiter, bis openNow kommt
// (ein Stage, das hängt).
type stageGate struct {
	complete     bool
	ignoreCancel bool

	arrived chan struct{}
	open    chan struct{}
	calls   atomic.Int32
	opened  sync.Once
}

func newStageGate(t *testing.T) *stageGate {
	g := &stageGate{arrived: make(chan struct{}), open: make(chan struct{})}
	t.Cleanup(func() { g.opened.Do(func() { close(g.open) }) })
	return g
}

func (g *stageGate) stage(ctx context.Context, worldDir, stagingDir string, hc *worldsync.HashCache) error {
	if g.calls.Add(1) != 1 {
		return worldsync.StageCachedContext(ctx, worldDir, stagingDir, hc)
	}
	var err error
	if g.complete {
		err = worldsync.StageCachedContext(ctx, worldDir, stagingDir, hc)
	}
	close(g.arrived)
	cancelled := ctx.Done()
	if g.ignoreCancel {
		cancelled = nil
	}
	select {
	case <-cancelled:
	case <-g.open:
	case <-time.After(30 * time.Second):
	}
	if g.complete {
		return err
	}
	return worldsync.StageCachedContext(ctx, worldDir, stagingDir, hc)
}

func (g *stageGate) openNow() { g.opened.Do(func() { close(g.open) }) }

func (g *stageGate) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("Zwischenstand beginnt nicht")
	}
}

func checkPending(t *testing.T, st *group.State, world string, gen int64) {
	t.Helper()
	if !st.PendingUpload || st.LastUploadedWorldID != world || st.LastUploadedGeneration != gen {
		t.Fatalf("Zustand %+v, erwartet PendingUpload auf Basis %s/%d", *st, world, gen)
	}
}

func checkUploaded(t *testing.T, st *group.State, g *api.Generation) {
	t.Helper()
	if st.PendingUpload || st.LastUploadedWorldID != g.WorldID || st.LastUploadedGeneration != g.Number {
		t.Fatalf("Zustand %+v, erwartet zuletzt %s/%d ohne PendingUpload", *st, g.WorldID, g.Number)
	}
}

func checkBases(t *testing.T, commits []api.CommitRequest, lease int64, world string, bases ...int64) {
	t.Helper()
	if len(commits) != len(bases) {
		t.Fatalf("%d Commit-Versuche, erwartet %d", len(commits), len(bases))
	}
	for i, c := range commits {
		if c.LeaseID != lease || c.BaseWorldID != world || c.BaseGeneration != bases[i] {
			t.Fatalf("Commit %d: Zuschlag %d, Basis %s/%d; erwartet %d, %s/%d",
				i+1, c.LeaseID, c.BaseWorldID, c.BaseGeneration, lease, world, bases[i])
		}
	}
}

// ---- Tests: Sitzung ----

func TestStopsWithin60sWithoutHeartbeat(t *testing.T) {
	t.Run("ab Serverstart nicht erreichbar", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		// Ab dem Serverstart (t = 0) ist der Koordinator nicht mehr erreichbar.
		e.srv.onStart = func() { e.co.down.Store(true) }
		tunnelsAtStop := -1
		e.srv.onStop = func() { _, tunnelsAtStop = e.tn.running() }
		r := e.start(t)

		e.drv.advanceTo(59 * time.Second)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Stop schon vor 60 s: %v", s)
		}
		e.drv.advanceTo(10 * time.Minute)
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != 60*time.Second {
			t.Fatalf("Stop bei %v, erwartet genau einmal bei 60s", s)
		}
		if tunnelsAtStop != 0 {
			t.Fatalf("beim Stop liefen noch %d tunnel-Prozesse", tunnelsAtStop)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche, erwartet keinen", len(c))
		}
		checkPending(t, e.checkAfterRun(t), e.head.WorldID, e.head.Number)
	})

	t.Run("Upload läuft beim Fencing: kein Commit mehr", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		r := e.start(t)
		e.drv.advanceTo(4*time.Minute + 25*time.Second)
		// Letztes erfolgreiches Lebenszeichen um 4:20; der Zwischenstand um
		// 5:00 hängt beim Hochladen.
		e.co.failHeartbeats.Store(true)
		g := newGate(t)
		e.co.putGate.Store(g)
		e.drv.advanceTo(5*time.Minute - time.Second)
		// Ab 5:00 darf der hängende Upload laufen, während die Uhr weitergeht.
		e.drv.allow = 1
		e.drv.advanceTo(5 * time.Minute)
		g.waitArrived(t)
		e.drv.advanceTo(5*time.Minute + 19*time.Second)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Stop schon vor 5:20: %v", s)
		}
		// Um 5:20 muss das Fencing auch den hängenden Upload beenden.
		e.drv.allow = 0
		e.drv.advanceTo(6 * time.Minute)
		g.release()
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != 5*time.Minute+20*time.Second {
			t.Fatalf("Stop bei %v, erwartet 5m20s", s)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche nach dem Fencing, erwartet keinen", len(c))
		}
		checkPending(t, e.checkAfterRun(t), e.head.WorldID, e.head.Number)
	})

	t.Run("Fencing während Stage: Zwischenstand verworfen", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		sg := newStageGate(t)
		e.d.stage = sg.stage
		r := e.start(t)
		e.drv.advanceTo(4*time.Minute + 25*time.Second)
		e.co.failHeartbeats.Store(true) // letztes erfolgreiches Lebenszeichen 4:20
		e.drv.advanceTo(5*time.Minute - time.Second)
		e.drv.allow = 1 // ab 5:00 steht der Zwischenstand in Stage
		e.drv.advanceTo(5 * time.Minute)
		sg.waitArrived(t)
		e.drv.advanceTo(5*time.Minute + 19*time.Second)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Stop vor dem Fencing: %v", s)
		}
		e.drv.allow = 0 // das Fencing muss Stage abbrechen
		e.drv.advanceTo(6 * time.Minute)
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != 5*time.Minute+20*time.Second {
			t.Fatalf("Stop bei %v, erwartet 5m20s", s)
		}
		if _, _, barriers, releases := e.srv.counts(); barriers != 1 || releases != 1 {
			t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 1", barriers, releases)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche aus dem abgebrochenen Zwischenstand", len(c))
		}
		checkPending(t, e.checkAfterRun(t), e.head.WorldID, e.head.Number)
	})

	t.Run("Fencing beim Start: Java startet nicht", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		prepared := make(chan struct{})
		proceed := make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(proceed) }) }
		t.Cleanup(release)
		// Prepare bemerkt den Abbruch nicht und kehrt erst nach dem Fencing
		// zurück; der Koordinator ist ab hier weg.
		e.srv.onPrepare = func() {
			e.co.down.Store(true)
			close(prepared)
			<-proceed
		}
		r := e.start(t)
		<-prepared
		e.drv.allow = 1 // der Start hängt in Prepare
		e.drv.advanceTo(time.Minute)
		release()
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if _, starts, _, _ := e.srv.counts(); starts != 0 {
			t.Fatalf("Server %d× gestartet nach dem Fencing", starts)
		}
		if _, err := os.Lstat(e.srv.marker); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Markierungsdatei nach dem Fencing geschrieben: %v", err)
		}
	})

	t.Run("Vormerkung nicht speicherbar: Markierung bleibt", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		// state.json ist ein Ordner: Der Zustand lässt sich nicht speichern.
		if err := os.MkdirAll(filepath.Join(group.Home(), "state.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		e.srv.onStart = func() { e.co.down.Store(true) }
		r := e.start(t)
		e.drv.advanceTo(10 * time.Minute)
		if err := r.wait(t); !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 {
			t.Fatalf("Stop %v, erwartet einmal", s)
		}
		if _, err := os.Lstat(e.srv.marker); err != nil {
			t.Fatalf("Markierung gelöscht, obwohl die Vormerkung nicht gespeichert ist: %v", err)
		}
	})

	t.Run("schon beim Laden nicht erreichbar: nicht starten", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		e.co.failBlobGets.Store(true)
		r := e.start(t)
		err := r.wait(t)
		if err == nil || errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet Abbruch beim Laden", err)
		}
		if prepares, starts, _, _ := e.srv.counts(); prepares != 0 || starts != 0 {
			t.Fatalf("Prepare %d×, Start %d× nach gescheitertem Laden", prepares, starts)
		}
		if n := e.tn.count(); n != 0 {
			t.Fatalf("tunnel %d× gestartet", n)
		}
		if _, err := os.Lstat(e.srv.marker); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Markierungsdatei geschrieben: %v", err)
		}
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
	})
}

func TestStopsImmediatelyOnLeaseLost(t *testing.T) {
	t.Run("Lebenszeichen 409", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		tunnelsAtStop := -1
		e.srv.onStop = func() { _, tunnelsAtStop = e.tn.running() }
		r := e.start(t)

		// tunnel endet von selbst: Neustart nach 2 s, 5 s, 10 s, 30 s, 30 s.
		at := 30 * time.Second
		e.drv.advanceTo(at)
		for i, pause := range []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 30 * time.Second} {
			e.killTunnel(t)
			e.drv.advanceTo(at + pause - time.Second)
			if n := e.tn.count(); n != i+1 {
				t.Fatalf("Neustart %d: %d tunnel nach %v, erwartet %d", i+1, n, pause-time.Second, i+1)
			}
			e.drv.advanceTo(at + pause)
			if n := e.tn.count(); n != i+2 {
				t.Fatalf("Neustart %d: %d tunnel nach %v, erwartet %d", i+1, n, pause, i+2)
			}
			at += pause
		}
		// Lief tunnel länger als eine Minute, beginnt die Pause wieder bei 2 s.
		e.drv.advanceTo(4 * time.Minute)
		e.killTunnel(t)
		e.drv.advanceTo(4*time.Minute + time.Second)
		if n := e.tn.count(); n != 6 {
			t.Fatalf("%d tunnel, erwartet 6", n)
		}
		e.drv.advanceTo(4*time.Minute + 2*time.Second)
		if n := e.tn.count(); n != 7 {
			t.Fatalf("%d tunnel, erwartet 7 (Pause nach langem Lauf wieder 2 s)", n)
		}

		e.drv.advanceTo(5*time.Minute + 5*time.Second) // Zwischenstand von 5:00 eingereicht
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, e.head.Number)
		// Der Zuschlag gilt beim Koordinator nicht mehr (abgelaufen, neu vergeben).
		if err := e.co.c.Release(t.Context(), api.ReleaseRequest{LeaseID: e.lease, MachineID: hostMachine}); err != nil {
			t.Fatal(err)
		}
		// tunnel endet kurz vor dem nächsten Lebenszeichen; sein Neustart
		// (5:11) käme erst nach dem Fencing.
		e.drv.advanceTo(5*time.Minute + 9*time.Second)
		e.killTunnel(t)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Stop vor dem abgelehnten Lebenszeichen: %v", s)
		}
		e.drv.advanceTo(10 * time.Minute) // 5:10: Lebenszeichen → 409
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) || !errors.Is(err, client.ErrLeaseLost) {
			t.Fatalf("Run = %v, erwartet ErrFenced mit ErrLeaseLost", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != 5*time.Minute+10*time.Second {
			t.Fatalf("Stop bei %v, erwartet sofort (5m10s)", s)
		}
		if tunnelsAtStop != 0 {
			t.Fatalf("beim Stop liefen noch %d tunnel-Prozesse", tunnelsAtStop)
		}
		if n := e.tn.count(); n != 7 {
			t.Fatalf("%d tunnel, erwartet 7 (kein Neustart ohne gültigen Zuschlag)", n)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, e.head.Number)
		// Basis des vorgemerkten Stands ist die eigene Generation von 5:00.
		checkPending(t, e.checkAfterRun(t), e.head.WorldID, e.head.Number+1)
	})

	t.Run("Commit 409", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		e.co.rejectCommits.Store(true)
		tunnelsAtStop := -1
		e.srv.onStop = func() { _, tunnelsAtStop = e.tn.running() }
		r := e.start(t)
		e.drv.advanceTo(5*time.Minute - time.Second)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Stop vor dem Commit: %v", s)
		}
		e.drv.advanceTo(10 * time.Minute)
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) || !errors.Is(err, client.ErrLeaseLost) {
			t.Fatalf("Run = %v, erwartet ErrFenced mit ErrLeaseLost", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != 5*time.Minute {
			t.Fatalf("Stop bei %v, erwartet sofort (5m)", s)
		}
		if tunnelsAtStop != 0 {
			t.Fatalf("beim Stop liefen noch %d tunnel-Prozesse", tunnelsAtStop)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, e.head.Number)
		checkPending(t, e.checkAfterRun(t), e.head.WorldID, e.head.Number)
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nach dem Fencing nicht zurückgegeben: %+v", h)
		}
	})
}

func TestSnapshotEvery5Minutes(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	e.d.Group.TunnelInsecureSkipVerify = true
	e.srv.withProcess = true
	e.d.onJob = func(j *procJob) { e.job = j }
	var held atomic.Int64
	e.co.setOnCommit(func() {
		if e.srv.isHolding() {
			held.Add(1)
		}
	})
	r := e.start(t)
	n := e.head.Number

	// Brief: Fake-Uhr 11 min → genau 2 Commits mit steigender Basis.
	for _, s := range []struct {
		at      time.Duration
		commits int
	}{
		{5*time.Minute - time.Second, 0},
		{5 * time.Minute, 1},
		{10*time.Minute - time.Second, 1},
		{10 * time.Minute, 2},
		{11 * time.Minute, 2},
	} {
		e.drv.advanceTo(s.at)
		if c := len(e.co.hostCommits()); c != s.commits {
			t.Fatalf("bei %v: %d Commits, erwartet %d", s.at, c, s.commits)
		}
	}
	commits := e.co.hostCommits()
	checkBases(t, commits, e.lease, e.head.WorldID, n, n+1)
	// Bereitgestellt wird zwischen SaveBarrier und release: genau der
	// Stand der Barriere, und beim Hochladen ist release schon gelaufen.
	e.srv.mu.Lock()
	saved := slices.Clone(e.srv.barrierSaved)
	e.srv.mu.Unlock()
	for i, c := range commits {
		if fileSHA(t, c, "level.dat") != sha(saved[i]) {
			t.Fatalf("Zwischenstand %d ist nicht der Stand der Barriere", i+1)
		}
	}
	if _, _, barriers, releases := e.srv.counts(); barriers != 2 || releases != 2 || held.Load() != 0 {
		t.Fatalf("SaveBarrier %d×, release %d×, Commit während der Barriere %d×; erwartet 2, 2, 0",
			barriers, releases, held.Load())
	}
	if h := e.co.head(t); h.Number != n+2 {
		t.Fatalf("Head %d, erwartet %d", h.Number, n+2)
	}
	// Lebenszeichen alle 10 s: 0 s, 10 s, …, 660 s, dazu eins bei 3 s, das
	// die Bereitschaft meldet (tunnel steht), ohne den Takt zu verschieben.
	if hb := e.co.heartbeats.Load(); hb != 68 {
		t.Fatalf("%d Lebenszeichen in 11 min, erwartet 68", hb)
	}
	checkMarkerPID(t, e)
	cfg, err := os.ReadFile(filepath.Join(group.Home(), "tunnel.yaml"))
	if err != nil || !strings.Contains(string(cfg), "insecure_skip_verify: true") {
		t.Fatalf("tunnel.yaml ohne insecure_skip_verify (%v):\n%s", err, cfg)
	}

	// Spielerliste alle 15 s, gemeldet mit dem nächsten Lebenszeichen.
	e.srv.setPlayers("Alex", "Bea")
	e.drv.advanceTo(11*time.Minute + 10*time.Second)
	if p := e.co.status(t).Players; !slices.Equal(p, []string{"Alex"}) {
		t.Fatalf("Spieler um 11:10 %v, erwartet [Alex]", p)
	}
	e.drv.advanceTo(11*time.Minute + 20*time.Second)
	if p := e.co.status(t).Players; !slices.Equal(p, []string{"Alex", "Bea"}) {
		t.Fatalf("Spieler um 11:20 %v, erwartet [Alex Bea]", p)
	}

	// Läuft der vorige Upload noch, wird der Zwischenstand übersprungen –
	// Lebenszeichen gehen derweil weiter.
	g := newGate(t)
	e.co.commitGate.Store(g)
	e.drv.advanceTo(15*time.Minute - time.Second)
	e.drv.allow = 1 // ab 15:00 hängt nur der Commit des Zwischenstands
	e.drv.advanceTo(15 * time.Minute)
	g.waitArrived(t)
	e.drv.advanceTo(20 * time.Minute)
	if _, _, barriers, _ := e.srv.counts(); barriers != 3 {
		t.Fatalf("%d SaveBarrier, erwartet 3 (um 20:00 übersprungen)", barriers)
	}
	e.co.commitGate.Store(nil)
	e.drv.allow = 0
	e.drv.resync()
	g.release()
	e.drv.advanceTo(20 * time.Minute)
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt: %v", s)
	}
	e.drv.advanceTo(25 * time.Minute)
	checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n, n+1, n+2, n+3)

	// Geht die Antwort auf einen Commit verloren, klärt der nächste
	// Zwischenstand die Basis am Head.
	e.co.dropResponse.Store(true)
	e.drv.advanceTo(30 * time.Minute)
	e.drv.advanceTo(35 * time.Minute)
	checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n, n+1, n+2, n+3, n+4, n+5)
	if h := e.co.head(t); h.Number != n+6 {
		t.Fatalf("Head %d, erwartet %d", h.Number, n+6)
	}

	// Ab 35:15 ist niemand da; wer um 37:15 wieder da ist, wird nicht
	// hinausgeworfen, und die 2 Minuten beginnen danach von vorn (37:30).
	e.srv.setPlayers()
	e.drv.advanceTo(37 * time.Minute)
	e.srv.setPlayers("Alex")
	e.drv.advanceTo(37*time.Minute + 20*time.Second)
	e.srv.setPlayers()
	e.drv.advanceTo(39*time.Minute + 29*time.Second)
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt, obwohl die 2 Minuten neu begonnen haben: %v", s)
	}

	// Sauberes Ende: Stop, Schluss-Stand auf der letzten eigenen
	// Generation, Zuschlag zurück.
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n, n+1, n+2, n+3, n+4, n+5, n+6)
	final := e.co.head(t)
	if final.Number != n+7 || final.MachineID != hostMachine || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
		t.Fatalf("Head %d von %s, erwartet %d mit dem Stand nach Stop", final.Number, final.MachineID, n+7)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
	checkUploaded(t, e.checkAfterRun(t), final)
}

// Den Abstand der Zwischenstände gibt der Koordinator in den
// Host-Einstellungen vor (snapshot_interval_s); fehlt er (älterer
// Koordinator) oder liegt er außerhalb von 60–1800 s, gelten 5 Minuten.
func TestSnapshotIntervalFromHostSettings(t *testing.T) {
	for _, tt := range []struct {
		name     string
		interval time.Duration // snapshot_interval des Koordinators (0: fehlt)
		want     time.Duration
	}{
		{"2 min vom Koordinator", 2 * time.Minute, 2 * time.Minute},
		{"fehlt → 5 min", 0, 5 * time.Minute},
		{"zu klein → 5 min", 59 * time.Second, 5 * time.Minute},
		{"zu groß → 5 min", 31 * time.Minute, 5 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			co := newCoord(t, func(c *config.Config) { c.SnapshotInterval = tt.interval })
			e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
			r := e.start(t)
			for _, s := range []struct {
				at      time.Duration
				commits int
			}{
				{tt.want - time.Second, 0},
				{tt.want, 1},
				{2*tt.want - time.Second, 1},
				{2 * tt.want, 2},
			} {
				e.drv.advanceTo(s.at)
				if c := len(e.co.hostCommits()); c != s.commits {
					t.Fatalf("bei %v: %d Commits, erwartet %d", s.at, c, s.commits)
				}
			}
			r.cancel()
			if err := r.wait(t); err != nil {
				t.Fatalf("Run = %v", err)
			}
			checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, e.head.Number, e.head.Number+1, e.head.Number+2)
		})
	}
}

// checkMarkerPID: Während der Server läuft, steht seine PID samt Programm
// und Startzeit in der Markierung.
func checkMarkerPID(t *testing.T, e *hostEnv) {
	t.Helper()
	m, err := readMarker(e.srv.marker)
	if err != nil {
		t.Fatal(err)
	}
	pid := e.srv.PID()
	if pid == 0 || m.PID != pid || m.Java != e.d.Paths.Java {
		t.Fatalf("Markierung %+v, erwartet PID %d und Java %s", m, pid, e.d.Paths.Java)
	}
	if runtime.GOOS != "windows" {
		return
	}
	p, err := openProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	exe, _ := p.exe()
	created, _ := p.created()
	if !samePath(m.Exe, exe) || m.Created != created || created == 0 {
		t.Fatalf("Markierung %+v, erwartet Programm %s, Startzeit %d", m, exe, created)
	}

	// Auch der laufende tunnel steht darin, und er ist im Job-Objekt der
	// Sitzung (stirbt also mit dem Starter).
	e.tn.mu.Lock()
	tpid := e.tn.cmds[len(e.tn.cmds)-1].Process.Pid
	e.tn.mu.Unlock()
	want := identify(tpid)
	if m.Tunnel == nil || *m.Tunnel != want || want.Exe == "" || want.Created == 0 {
		t.Fatalf("tunnel in der Markierung %+v, erwartet %+v", m.Tunnel, want)
	}
	if e.job == nil {
		t.Fatal("kein Job-Objekt für tunnel")
	}
	if in, err := e.job.contains(tpid); err != nil || !in {
		t.Fatalf("tunnel (PID %d) nicht im Job-Objekt: %v", tpid, err)
	}
}

func TestAutoStopAfter2MinutesEmpty(t *testing.T) {
	e := newHostEnv(t, []string{})
	// Ein Token mit YAML-Sonderzeichen darf die tunnel-Konfiguration nicht
	// verändern.
	token := "tok: #x\nserver: böse.example.org"
	e.d.Group.Token = token
	tunnelsAtStop := -1
	e.srv.onStop = func() { _, tunnelsAtStop = e.tn.running() }
	r := e.start(t)

	e.drv.advanceTo(2*time.Minute - time.Second)
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Stop schon vor 2 min: %v", s)
	}
	e.drv.advanceTo(2 * time.Minute)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if s := e.srv.stopTimes(); len(s) != 1 || s[0] != 2*time.Minute {
		t.Fatalf("Stop bei %v, erwartet genau einmal bei 2m", s)
	}
	// Beim Ende ist niemand mehr über tunnel hereingekommen.
	if tunnelsAtStop != 0 {
		t.Fatalf("beim Stop liefen noch %d tunnel-Prozesse", tunnelsAtStop)
	}
	checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, e.head.Number)
	final := e.co.head(t)
	if final.Number != e.head.Number+1 || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
		t.Fatalf("Head %d, erwartet %d mit dem Stand nach Stop", final.Number, e.head.Number+1)
	}
	if h := e.co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
	checkUploaded(t, e.checkAfterRun(t), final)

	// tunnel-Konfiguration mit Server und Token, ohne insecure_skip_verify,
	// lesbar nur für den eigenen Benutzer.
	path := filepath.Join(group.Home(), "tunnel.yaml")
	cfg, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tc map[string]any
	if err := yaml.Unmarshal(cfg, &tc); err != nil || len(tc) != 2 || tc["server"] != tunnelAddr || tc["token"] != token {
		t.Fatalf("tunnel.yaml (%v):\n%s", err, cfg)
	}
	checkOwnerOnly(t, path)

	t.Run("Ende während eines Zwischenstands, Schluss-Upload scheitert", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		g := newGate(t)
		e.co.commitGate.Store(g)
		r := e.start(t)
		n := e.head.Number
		e.drv.advanceTo(5*time.Minute - time.Second)
		e.drv.allow = 1 // ab 5:00 hängt der Commit des Zwischenstands
		e.drv.advanceTo(5 * time.Minute)
		g.waitArrived(t)

		// Ende verlangt: Stop kommt sofort, der Schluss-Stand wartet auf den
		// laufenden Zwischenstand; der Zuschlag wird derweil gehalten.
		e.drv.resync()
		r.cancel()
		e.drv.advanceTo(5 * time.Minute)
		if s := e.srv.stopTimes(); len(s) != 1 {
			t.Fatalf("Stop %v, erwartet einmal", s)
		}
		hb := e.co.heartbeats.Load()
		e.drv.advanceTo(6 * time.Minute)
		if d := e.co.heartbeats.Load() - hb; d != 6 {
			t.Fatalf("%d Lebenszeichen zwischen 5:00 und 6:00, erwartet 6", d)
		}

		// Der Zwischenstand kommt an, der Schluss-Stand nicht: Er wird
		// 60 s lang wiederholt (6:00 … 7:00), dann bleibt er vorgemerkt –
		// auf der Basis des Zwischenstands.
		e.co.failBlobPuts.Store(true)
		e.co.commitGate.Store(nil)
		e.drv.resync()
		g.release()
		e.drv.allow = 0
		e.drv.advanceTo(10 * time.Minute)
		if at := e.drv.now(); at != 7*time.Minute {
			t.Fatalf("Run endete bei %v, erwartet 7m (60 s Wiederholen ab 6:00)", at)
		}
		err := r.wait(t)
		if err == nil || errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet Fehler beim Schluss-Upload", err)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		checkPending(t, e.checkAfterRun(t), e.head.WorldID, n+1)
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
	})

	t.Run("Ende während Stage: Zwischenstand verworfen", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		sg := newStageGate(t)
		// Stage hat schon alles kopiert und meldet Erfolg, das Ende kam aber
		// vor release: Auch dann wird dieser Zwischenstand nicht eingereicht.
		sg.complete = true
		e.d.stage = sg.stage
		r := e.start(t)
		n := e.head.Number
		e.drv.advanceTo(5*time.Minute - time.Second)
		e.drv.allow = 1 // ab 5:00 steht der Zwischenstand in Stage
		e.drv.advanceTo(5 * time.Minute)
		sg.waitArrived(t)
		e.drv.resync()
		r.cancel()
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
		// Eingereicht wird nur der Schluss-Stand nach Stop, nicht der
		// abgebrochene Zwischenstand; Stop kam erst nach release.
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		final := e.co.head(t)
		if final.Number != n+1 || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
			t.Fatalf("Head %d, erwartet %d mit dem Stand nach Stop", final.Number, n+1)
		}
		if _, _, barriers, releases := e.srv.counts(); barriers != 1 || releases != 1 {
			t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 1", barriers, releases)
		}
		if s := e.srv.stopTimes(); len(s) != 1 {
			t.Fatalf("Stop %v, erwartet einmal", s)
		}
		checkUploaded(t, e.checkAfterRun(t), final)
	})

	t.Run("Server stürzt ab: nichts hochladen", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		r := e.start(t)
		e.drv.advanceTo(30 * time.Second)
		e.drv.resync()
		e.srv.crash(errors.New("Java abgestürzt"))
		e.drv.advanceTo(10 * time.Minute)
		err := r.wait(t)
		if err == nil || errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet Fehler", err)
		}
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Stop nach Absturz: %v", s)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche nach Absturz", len(c))
		}
		if _, err := os.Lstat(e.srv.marker); err != nil {
			t.Fatalf("Markierungsdatei weg nach Absturz: %v", err)
		}
		if st, err := group.LoadState(); err != nil || st.PendingUpload || e.d.State.PendingUpload {
			t.Fatalf("PendingUpload nach Absturz: %+v, %v", st, err)
		}
		if _, running := e.tn.running(); running != 0 {
			t.Fatalf("%d tunnel-Prozesse laufen nach Absturz", running)
		}
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
	})

	t.Run("Stop nicht sauber: nichts hochladen", func(t *testing.T) {
		e := newHostEnv(t, []string{})
		e.srv.stopErr = errors.New("hart beendet")
		r := e.start(t)
		e.drv.advanceTo(2 * time.Minute)
		err := r.wait(t)
		if err == nil || errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet Fehler", err)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche nach unsauberem Stop", len(c))
		}
		if _, err := os.Lstat(e.srv.marker); err != nil {
			t.Fatalf("Markierungsdatei weg nach unsauberem Stop: %v", err)
		}
		if st, err := group.LoadState(); err != nil || st.PendingUpload || e.d.State.PendingUpload {
			t.Fatalf("PendingUpload nach unsauberem Stop: %+v, %v", st, err)
		}
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
	})
}

// ---- Tests: Recover ----

func TestRecoverDirtyMarkerDiscards(t *testing.T) {
	co := newCoord(t)
	dir := filepath.Join(group.Home(), "server")
	world := filepath.Join(dir, WorldName)
	files := map[string]string{"level.dat": "halb geschrieben", "region/r.0.0.mca": "chunks"}
	writeTree(t, world, files)
	if err := writeMarker(filepath.Join(dir, MarkerName), marker{Java: `C:\nirgends\java.exe`}); err != nil {
		t.Fatal(err)
	}
	// Auch ein „nachzureichender“ Stand zählt nicht: Die Markierung sagt, dass
	// der Server nicht sauber gestoppt wurde.
	st := &group.State{MachineID: hostMachine, PendingUpload: true, LastUploadedWorldID: "w", LastUploadedGeneration: 3}

	err := Recover(t.Context(), co.c, st, dir)
	var de *DiscardedError
	if !errors.As(err, &de) {
		t.Fatalf("Recover = %v, erwartet DiscardedError", err)
	}
	if n := co.hits.Load(); n != 0 {
		t.Fatalf("%d Anfragen an den Koordinator, erwartet keine", n)
	}
	checkDiscarded(t, de.Path, world, files)
	if _, err := os.Lstat(filepath.Join(dir, MarkerName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Markierungsdatei noch da: %v", err)
	}
	if loaded, err := group.LoadState(); err != nil || st.PendingUpload || loaded.PendingUpload {
		t.Fatalf("PendingUpload nicht gelöscht: %+v / %+v, %v", *st, loaded, err)
	}

	t.Run("Markierung unlesbar: trotzdem verwerfen", func(t *testing.T) {
		t.Setenv("WANDERWELT_HOME", t.TempDir())
		dir := filepath.Join(group.Home(), "server")
		world := filepath.Join(dir, WorldName)
		writeTree(t, world, files)
		writeTree(t, dir, map[string]string{MarkerName: "{abgeschnitten"})
		st := &group.State{MachineID: hostMachine, PendingUpload: true, LastUploadedWorldID: "w", LastUploadedGeneration: 3}
		err := Recover(t.Context(), co.c, st, dir)
		var de *DiscardedError
		if !errors.As(err, &de) {
			t.Fatalf("Recover = %v, erwartet DiscardedError", err)
		}
		checkDiscarded(t, de.Path, world, files)
		if st.PendingUpload {
			t.Fatal("PendingUpload nicht gelöscht")
		}
	})

	// Läuft der Server aus der Markierung noch (Starter abgestürzt, Java
	// nicht), beendet Recover ihn vor dem Verschieben – aber nur ihn.
	if runtime.GOOS != "windows" {
		return
	}
	t.Run("verwaister Server wird beendet", func(t *testing.T) {
		o := startOrphan(t)
		recoverOrphan(t, co, o.marker)
		if !o.gone(10 * time.Second) {
			t.Fatal("verwaister Server läuft nach Recover noch")
		}
	})
	// Endet er nach "stop" mit Fehler (hier Exit-Code 1), war das kein
	// sauberes Speichern: verwerfen (rescue_test.go: sauberes Ende).
	t.Run("verwaister Server wird zuerst über RCON gestoppt", func(t *testing.T) {
		o := startOrphan(t)
		var stopped atomic.Bool
		rs := rcontest.NewServer(
			rcontest.SetSettings(rcontest.Settings{Password: "rcon-pw"}),
			rcontest.SetCommandHandler(func(c *rcontest.Context) {
				if c.Request().Body() == "stop" {
					stopped.Store(true)
					o.cmd.Process.Kill() // der Server endet nach "stop", aber mit Fehler
				}
				rcon.NewPacket(rcon.SERVERDATA_RESPONSE_VALUE, c.Request().ID, "Stopping the server").WriteTo(c.Conn())
			}),
		)
		defer rs.Close()
		_, port, err := net.SplitHostPort(rs.Addr())
		if err != nil {
			t.Fatal(err)
		}
		recoverOrphan(t, co, o.marker, "enable-rcon=true\nrcon.port="+port+"\nrcon.password=rcon-pw\n")
		if !stopped.Load() || !o.gone(10*time.Second) {
			t.Fatalf("stop über RCON: %v, beendet: %v", stopped.Load(), o.gone(0))
		}
	})
	t.Run("anderes Programm bleibt", func(t *testing.T) {
		o := startOrphan(t)
		m := o.marker
		m.Exe, m.Java = `C:\nirgends\java.exe`, `C:\nirgends\java.exe`
		recoverOrphan(t, co, m)
		if o.gone(2 * time.Second) {
			t.Fatal("Recover hat einen fremden Prozess beendet")
		}
	})
	t.Run("andere Startzeit bleibt", func(t *testing.T) {
		o := startOrphan(t)
		m := o.marker
		m.Created += int64(time.Second)
		recoverOrphan(t, co, m)
		if o.gone(2 * time.Second) {
			t.Fatal("Recover hat einen Prozess mit neu vergebener PID beendet")
		}
	})

	// Der tunnel aus der Markierung gibt den verwaisten Server öffentlich
	// frei; Recover beendet ihn mit denselben Prüfungen.
	t.Run("verwaister tunnel wird beendet", func(t *testing.T) {
		o := startOrphan(t)
		ref := procRef{PID: o.marker.PID, Exe: o.marker.Exe, Created: o.marker.Created}
		recoverOrphan(t, co, marker{Java: `C:\nirgends\java.exe`, Tunnel: &ref})
		if !o.gone(10 * time.Second) {
			t.Fatal("verwaister tunnel läuft nach Recover noch")
		}
	})
	t.Run("tunnel mit anderer Startzeit bleibt", func(t *testing.T) {
		o := startOrphan(t)
		ref := procRef{PID: o.marker.PID, Exe: o.marker.Exe, Created: o.marker.Created + int64(time.Second)}
		recoverOrphan(t, co, marker{Java: `C:\nirgends\java.exe`, Tunnel: &ref})
		if o.gone(2 * time.Second) {
			t.Fatal("Recover hat einen Prozess mit neu vergebener PID beendet")
		}
	})
	t.Run("tunnel endet mit dem Starter (Job-Objekt)", func(t *testing.T) {
		o := startOrphan(t)
		j, err := newProcJob()
		if err != nil {
			t.Fatal(err)
		}
		if err := j.assign(o.cmd.Process.Pid); err != nil {
			j.close()
			t.Fatal(err)
		}
		if o.gone(500 * time.Millisecond) {
			t.Fatal("Prozess endet schon beim Aufnehmen in den Job")
		}
		j.close() // so schließt Windows den Handle, wenn der Starter stirbt
		if !o.gone(10 * time.Second) {
			t.Fatal("Prozess im Job läuft weiter, nachdem der Job-Handle geschlossen ist")
		}
	})
}

type orphan struct {
	cmd    *exec.Cmd
	exited chan struct{}
	marker marker // passend zum Prozess
}

// startOrphan startet einen Dauerprozess und liefert die Markierung, die
// die Sitzung für ihn geschrieben hätte.
func startOrphan(t *testing.T) *orphan {
	t.Helper()
	cmd := helperCmd(context.Background(), testExe(t))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o := &orphan{cmd: cmd, exited: make(chan struct{})}
	go func() { cmd.Wait(); close(o.exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-o.exited
	})
	p, err := openProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	exe, err := p.exe()
	if err != nil {
		t.Fatal(err)
	}
	created, err := p.created()
	if err != nil {
		t.Fatal(err)
	}
	o.marker = marker{PID: cmd.Process.Pid, Exe: exe, Created: created, Java: exe}
	return o
}

func (o *orphan) gone(d time.Duration) bool {
	select {
	case <-o.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// recoverOrphan legt eine Welt mit der Markierung m (und optional einer
// server.properties) in einem eigenen Home an und ruft Recover; die Welt
// muss verworfen werden.
func recoverOrphan(t *testing.T, co *coord, m marker, properties ...string) {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	dir := filepath.Join(group.Home(), "server")
	world := filepath.Join(dir, WorldName)
	files := map[string]string{"level.dat": "halb"}
	writeTree(t, world, files)
	if len(properties) > 0 {
		writeTree(t, dir, map[string]string{"server.properties": properties[0]})
	}
	if err := writeMarker(filepath.Join(dir, MarkerName), m); err != nil {
		t.Fatal(err)
	}
	err := Recover(t.Context(), co.c, &group.State{MachineID: hostMachine}, dir)
	var de *DiscardedError
	if !errors.As(err, &de) {
		t.Fatalf("Recover = %v, erwartet DiscardedError", err)
	}
	checkDiscarded(t, de.Path, world, files)
}

func TestRecoverPendingUploadWhenHeadUnchanged(t *testing.T) {
	pendingWorld := map[string]string{"level.dat": "sauber gestoppt, vorgemerkt", "region/r.0.0.mca": "chunks"}

	t.Run("Head gleich Basis: einreichen", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		dir := filepath.Join(group.Home(), "server")
		world := filepath.Join(dir, WorldName)
		writeTree(t, world, pendingWorld)
		st := &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}

		if err := Recover(t.Context(), co.c, st, dir); err != nil {
			t.Fatalf("Recover = %v", err)
		}
		commits := co.hostCommits()
		if len(commits) != 1 || commits[0].BaseWorldID != head.WorldID || commits[0].BaseGeneration != head.Number {
			t.Fatalf("Commits %+v, erwartet einen auf %s/%d", commits, head.WorldID, head.Number)
		}
		now := co.head(t)
		if now.Number != head.Number+1 || now.MachineID != hostMachine || genSHA(t, now, "level.dat") != sha(pendingWorld["level.dat"]) {
			t.Fatalf("Head %d von %s, erwartet den nachgereichten Stand als %d", now.Number, now.MachineID, head.Number+1)
		}
		if h := co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
		loaded, err := group.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		checkUploaded(t, st, now)
		checkUploaded(t, loaded, now)
		if _, err := os.Stat(filepath.Join(world, "level.dat")); err != nil {
			t.Fatalf("Welt nach dem Nachreichen weg: %v", err)
		}
	})

	t.Run("Head weiter: verwerfen", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		later := co.seed(t, map[string]string{"level.dat": "anderer Host", "region/r.0.0.mca": "chunks"})
		dir := filepath.Join(group.Home(), "server")
		world := filepath.Join(dir, WorldName)
		writeTree(t, world, pendingWorld)
		st := &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}

		err := Recover(t.Context(), co.c, st, dir)
		var de *DiscardedError
		if !errors.As(err, &de) {
			t.Fatalf("Recover = %v, erwartet DiscardedError", err)
		}
		if c := co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commits, erwartet keinen", len(c))
		}
		if now := co.head(t); now.Number != later.Number {
			t.Fatalf("Head %d, erwartet unverändert %d", now.Number, later.Number)
		}
		checkDiscarded(t, de.Path, world, pendingWorld)
		if loaded, err := group.LoadState(); err != nil || st.PendingUpload || loaded.PendingUpload {
			t.Fatalf("PendingUpload nicht gelöscht: %+v / %+v, %v", *st, loaded, err)
		}
		if h := co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag hängt: %+v", h)
		}
	})

	t.Run("Head gleich Basis, aber jemand hostet: vorgemerkt lassen", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		co.acquire(t, head, otherMachine)
		dir := filepath.Join(group.Home(), "server")
		world := filepath.Join(dir, WorldName)
		writeTree(t, world, pendingWorld)
		st := &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}

		err := Recover(t.Context(), co.c, st, dir)
		if !errors.Is(err, ErrPendingLater) {
			t.Fatalf("Recover = %v, erwartet ErrPendingLater", err)
		}
		if c := co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commits, erwartet keinen", len(c))
		}
		if got := readTree(t, world); got["level.dat"] != pendingWorld["level.dat"] {
			t.Fatalf("vorgemerkte Welt verändert: %v", got)
		}
		checkPending(t, st, head.WorldID, head.Number)
	})

	t.Run("vorgemerkte Welt fehlt: Vormerkung löschen", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		hits := co.hits.Load()
		dir := filepath.Join(group.Home(), "server")
		st := &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}
		if err := Recover(t.Context(), co.c, st, dir); err != nil {
			t.Fatalf("Recover = %v", err)
		}
		if n := co.hits.Load() - hits; n != 0 {
			t.Fatalf("%d Anfragen an den Koordinator, erwartet keine", n)
		}
		if loaded, err := group.LoadState(); err != nil || st.PendingUpload || loaded.PendingUpload {
			t.Fatalf("PendingUpload nicht gelöscht: %+v / %+v, %v", *st, loaded, err)
		}
	})

	t.Run("eigener Commit kam beim Fencing noch an: übernehmen", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		e := newHostEnvOn(t, co, head, []string{"Alex"})
		n := head.Number
		r := e.start(t)
		e.drv.advanceTo(5*time.Minute - time.Second)
		// Der Zwischenstand um 5:00 kommt an, die Antwort geht verloren;
		// danach ist der Koordinator für Lebenszeichen weg → Fencing 6:00.
		e.co.dropResponse.Store(true)
		e.drv.advanceTo(5*time.Minute + 5*time.Second)
		e.co.failHeartbeats.Store(true)
		e.drv.advanceTo(10 * time.Minute)
		if err := r.wait(t); !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		st := e.d.State
		checkPending(t, st, head.WorldID, n) // der Commit von 5:00 ist unbestätigt
		if h := co.head(t); h.Number != n+1 || h.MachineID != hostMachine {
			t.Fatalf("Head %d von %s, erwartet den eigenen Commit %d", h.Number, h.MachineID, n+1)
		}

		e.co.failHeartbeats.Store(false)
		if err := Recover(t.Context(), co.c, st, e.dir); err != nil {
			t.Fatalf("Recover = %v, erwartet Nachreichen auf dem eigenen Commit", err)
		}
		now := co.head(t)
		if now.Number != n+2 || now.Parent != n+1 || genSHA(t, now, "level.dat") != sha(e.srv.saved()) {
			t.Fatalf("Head %d (Eltern %d), erwartet den lokalen Stand als %d auf %d", now.Number, now.Parent, n+2, n+1)
		}
		checkUploaded(t, st, now)
		if m, _ := filepath.Glob(filepath.Join(group.Home(), "verworfen-*")); len(m) != 0 {
			t.Fatalf("verworfen: %v", m)
		}
	})

	t.Run("eigener Commit, aber nicht direkt auf der Basis: verwerfen", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		co.seedAs(t, hostMachine, map[string]string{"level.dat": "eins", "region/r.0.0.mca": "chunks"})
		later := co.seedAs(t, hostMachine, map[string]string{"level.dat": "zwei", "region/r.0.0.mca": "chunks"})
		dir := filepath.Join(group.Home(), "server")
		world := filepath.Join(dir, WorldName)
		writeTree(t, world, pendingWorld)
		st := &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}
		err := Recover(t.Context(), co.c, st, dir)
		var de *DiscardedError
		if !errors.As(err, &de) {
			t.Fatalf("Recover = %v, erwartet DiscardedError", err)
		}
		if now := co.head(t); now.Number != later.Number {
			t.Fatalf("Head %d, erwartet unverändert %d", now.Number, later.Number)
		}
		checkDiscarded(t, de.Path, world, pendingWorld)
	})

	t.Run("Run übernimmt den eigenen, unbestätigten Commit", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		own := co.seedAs(t, hostMachine, map[string]string{"level.dat": "Zwischenstand", "region/r.0.0.mca": "chunks"})
		e := newHostEnvOn(t, co, own, []string{})
		writeTree(t, e.srv.world, pendingWorld)
		*e.d.State = group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}
		r := e.start(t)
		e.drv.advanceTo(2 * time.Minute)
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
		if e.srv.startWorld != pendingWorld["level.dat"] {
			t.Fatalf("Server startete mit %q, erwartet den vorgemerkten Stand", e.srv.startWorld)
		}
		commits := e.co.hostCommits()
		if c := commits[len(commits)-1]; c.BaseWorldID != own.WorldID || c.BaseGeneration != own.Number {
			t.Fatalf("Schluss-Commit auf %s/%d, erwartet %s/%d", c.BaseWorldID, c.BaseGeneration, own.WorldID, own.Number)
		}
		if m, _ := filepath.Glob(filepath.Join(group.Home(), "verworfen-*")); len(m) != 0 {
			t.Fatalf("verworfen: %v", m)
		}
		checkUploaded(t, e.checkAfterRun(t), co.head(t))
	})

	t.Run("Run spielt auf dem vorgemerkten Stand weiter", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		e := newHostEnvOn(t, co, head, []string{})
		writeTree(t, e.srv.world, pendingWorld)
		*e.d.State = group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}
		r := e.start(t)
		e.drv.advanceTo(2 * time.Minute)
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
		if e.srv.startWorld != pendingWorld["level.dat"] {
			t.Fatalf("Server startete mit %q, erwartet den vorgemerkten Stand", e.srv.startWorld)
		}
		checkBases(t, e.co.hostCommits(), e.lease, head.WorldID, head.Number)
		checkUploaded(t, e.checkAfterRun(t), co.head(t))
		if m, _ := filepath.Glob(filepath.Join(group.Home(), "verworfen-*")); len(m) != 0 {
			t.Fatalf("verworfen: %v", m)
		}
	})

	t.Run("Run legt überholten vorgemerkten Stand beiseite", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		later := co.seed(t, map[string]string{"level.dat": "anderer Host", "region/r.0.0.mca": "chunks"})
		e := newHostEnvOn(t, co, later, []string{})
		writeTree(t, e.srv.world, pendingWorld)
		*e.d.State = group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}
		r := e.start(t)
		e.drv.advanceTo(2 * time.Minute)
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
		if e.srv.startWorld != "anderer Host" {
			t.Fatalf("Server startete mit %q, erwartet den Head", e.srv.startWorld)
		}
		m, _ := filepath.Glob(filepath.Join(group.Home(), "verworfen-*"))
		if len(m) != 1 || readTree(t, m[0])["level.dat"] != pendingWorld["level.dat"] {
			t.Fatalf("verworfen: %v, erwartet den vorgemerkten Stand", m)
		}
		checkBases(t, e.co.hostCommits(), e.lease, later.WorldID, later.Number)
		checkUploaded(t, e.checkAfterRun(t), co.head(t))
	})
}

// ---- Hilfen ----

// checkDiscarded: Die Welt liegt jetzt vollständig unter
// Home()/verworfen-*, am alten Ort ist nichts mehr.
func checkDiscarded(t *testing.T, path, world string, files map[string]string) {
	t.Helper()
	if filepath.Dir(path) != group.Home() || !strings.HasPrefix(filepath.Base(path), "verworfen-") {
		t.Fatalf("verworfen nach %s, erwartet %s", path, filepath.Join(group.Home(), "verworfen-…"))
	}
	if _, err := os.Lstat(world); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Welt noch am alten Ort: %v", err)
	}
	got := readTree(t, path)
	if len(got) != len(files) {
		t.Fatalf("verworfen: %v, erwartet %v", got, files)
	}
	for p, want := range files {
		if got[p] != want {
			t.Fatalf("verworfen %s = %q, erwartet %q", p, got[p], want)
		}
	}
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func fileSHA(t *testing.T, req api.CommitRequest, path string) string {
	t.Helper()
	for _, f := range req.Files {
		if f.Path == path {
			return f.SHA256
		}
	}
	t.Fatalf("%s fehlt im Commit", path)
	return ""
}

func genSHA(t *testing.T, g *api.Generation, path string) string {
	t.Helper()
	return fileSHA(t, api.CommitRequest{Files: g.Files}, path)
}

// ---- Tests: Zusammenspiel mit Speicher pro Chunk (Task 6b) ----

// regionFile ist eine Regionsdatei aus Kopf und einem Chunk mit payload
// (drei Sektoren): zwei Segmente, also mit Rezept.
func regionFile(payload string) string {
	data := make([]byte, 3*4096)
	binary.BigEndian.PutUint32(data, 2<<8|1)
	copy(data[2*4096:], payload)
	return string(data)
}

// Der erste Zwischenstand nach einer Übergabe zerlegt die Welt nicht neu:
// Bereitstellen und Manifest nutzen den Cache, den Download gefüllt hat.
// Beweis: Nach dem Laden wird die Regionsdatei heimlich geändert (gleiche
// Größe und Änderungszeit). Mit dem Cache aus dem Download nennt der
// Zwischenstand Hash und Rezept des geladenen Stands; neu zerlegt stünde
// dort der Hash des Inhalts.
func TestSnapshotUsesDownloadCache(t *testing.T) {
	co := newCoord(t)
	head := co.seed(t, map[string]string{"level.dat": "anfang", "region/r.0.0.mca": regionFile("Chunk A")})
	var loaded api.FileEntry
	for _, f := range head.Files {
		if f.Path == "region/r.0.0.mca" {
			loaded = f
		}
	}
	if loaded.Recipe == "" {
		t.Fatalf("Testaufbau: Regionsdatei ohne Rezept: %+v", loaded)
	}
	e := newHostEnvOn(t, co, head, []string{"Alex"})
	e.d.HashCache = worldsync.LoadHashCache()
	region := filepath.Join(e.srv.world, "region", "r.0.0.mca")
	e.srv.onPrepare = func() {
		info, err := os.Lstat(region)
		if err == nil {
			err = os.WriteFile(region, []byte(regionFile("Chunk B")), 0o644)
		}
		if err == nil {
			err = os.Chtimes(region, time.Time{}, info.ModTime())
		}
		if err != nil {
			t.Errorf("Regionsdatei heimlich ändern: %v", err)
		}
	}
	r := e.start(t)
	e.drv.advanceTo(5 * time.Minute)
	commits := e.co.hostCommits()
	if len(commits) != 1 {
		t.Fatalf("%d Commits nach 5 min, erwartet den ersten Zwischenstand", len(commits))
	}
	staged, err := os.ReadFile(filepath.Join(e.dir, StagingName, "region", "r.0.0.mca"))
	if err != nil || string(staged) != regionFile("Chunk B") {
		t.Fatalf("Testaufbau: bereitgestellt wurde nicht der geänderte Inhalt (%v)", err)
	}
	for _, f := range commits[0].Files {
		if f.Path == "region/r.0.0.mca" && f != loaded {
			t.Fatalf("Zwischenstand nennt %+v, erwartet den Eintrag aus dem Download %+v (neu zerlegt statt Cache)", f, loaded)
		}
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// Ein Weltordner mit abgebrochenem Download wird nie hochgeladen und nie
// gestartet.
func TestIncompleteDownloadNeverUploadedOrStarted(t *testing.T) {
	halfLoaded := map[string]string{
		"level.dat":            "halb geladen",
		".wanderwelt-download": "Download nicht abgeschlossen\n",
	}

	t.Run("Recover verwirft den vorgemerkten Stand", func(t *testing.T) {
		co := newCoord(t)
		head := co.seed(t, seedFiles)
		dir := filepath.Join(group.Home(), "server")
		world := filepath.Join(dir, WorldName)
		writeTree(t, world, halfLoaded)
		if !worldsync.DownloadIncomplete(world) {
			t.Fatal("Testaufbau: Download gilt als abgeschlossen")
		}
		st := &group.State{MachineID: hostMachine, PendingUpload: true,
			LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}
		hits := co.hits.Load()
		err := Recover(t.Context(), co.c, st, dir)
		de, ok := errors.AsType[*DiscardedError](err)
		if !ok {
			t.Fatalf("Recover = %v, erwartet DiscardedError", err)
		}
		if n := co.hits.Load() - hits; n != 0 {
			t.Fatalf("%d Koordinator-Anfragen, erwartet keine", n)
		}
		if st.PendingUpload {
			t.Fatal("PendingUpload noch gesetzt")
		}
		checkDiscarded(t, de.Path, world, halfLoaded)
	})

	t.Run("Run startet nicht darauf", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		writeTree(t, e.srv.world, halfLoaded)
		// Vorgemerkt auf dem Head: ohne die Prüfung würde darauf weitergespielt.
		e.d.State.PendingUpload = true
		e.d.State.LastUploadedWorldID, e.d.State.LastUploadedGeneration = e.head.WorldID, e.head.Number
		r := e.start(t)
		e.drv.advanceTo(time.Second)
		e.srv.mu.Lock()
		started := e.srv.startWorld
		e.srv.mu.Unlock()
		if started != seedFiles["level.dat"] {
			t.Fatalf("Server startete auf level.dat %q, erwartet den geladenen Head %q", started, seedFiles["level.dat"])
		}
		r.cancel()
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
	})
}

// Endet ctx zwischen Markierung und Start, hat Java die Welt nie berührt:
// Die Markierung muss weg, sonst verwürfe der nächste Start einen gültigen
// Stand.
func TestStartAbortAfterMarkerRemovesIt(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	ready := make(chan struct{})
	var r *runner
	e.d.onMarker = func(ctx context.Context) {
		<-ready
		r.cancel() // Ende verlangt, genau nach dem Schreiben der Markierung
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
			t.Error("Start-Abbruch kommt nicht an")
		}
	}
	r = e.start(t)
	close(ready)
	if err := r.wait(t); err == nil || errors.Is(err, ErrFenced) {
		t.Fatalf("Run = %v, erwartet Abbruch beim Start", err)
	}
	if _, starts, _, _ := e.srv.counts(); starts != 0 {
		t.Fatalf("Server %d× gestartet", starts)
	}
	if _, err := os.Lstat(e.srv.marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Markierungsdatei nach Start-Abbruch noch da: %v", err)
	}
}
