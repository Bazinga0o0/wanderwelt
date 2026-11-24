package server_test

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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const (
	emailA = "alice@example.org" // Admin
	emailB = "bob@example.org"   // kein Admin
	idA    = 101
	idB    = 202
	mcPort = 15565
)

// fakeClock ist eine von Hand gestellte Uhr. After-Kanäle feuern erst, wenn
// Advance die Zeit über ihren Zeitpunkt schiebt.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
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
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	rest := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.now) {
			rest = append(rest, w)
			continue
		}
		w.ch <- c.now
	}
	c.waiters = rest
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// waitWindow wartet, bis ein Sammelfenster offen ist (die Uhr hat einen
// wartenden Timer).
func (c *fakeClock) waitWindow(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("kein Sammelfenster geöffnet")
		}
		time.Sleep(time.Millisecond)
	}
}

// fakePortHolder zeichnet alle Aufrufe auf; mit failSet schlägt Set fehl
// (mit setErr, falls gesetzt).
type fakePortHolder struct {
	mu      sync.Mutex
	calls   []string
	failSet bool
	setErr  error
}

func (p *fakePortHolder) Set(_ context.Context, port int, accountID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf("set %d %d", port, accountID))
	if p.failSet {
		if p.setErr != nil {
			return p.setErr
		}
		return errors.New("tunneld nicht erreichbar")
	}
	return nil
}

func (p *fakePortHolder) setFail(fail bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failSet = fail
}

func (p *fakePortHolder) Clear(_ context.Context, port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf("clear %d", port))
	return nil
}

// take liefert die Aufrufe seit dem letzten take.
func (p *fakePortHolder) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.calls
	p.calls = nil
	return c
}

type fakeUUIDs map[string]string

func (f fakeUUIDs) UUID(_ context.Context, name string) (string, error) {
	if u, ok := f[name]; ok {
		return u, nil
	}
	return "", fmt.Errorf("unbekannt: %s", name)
}

const (
	uuidA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	uuidB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func newServer(t *testing.T, mode string) (*server.Server, *fakeClock, *fakePortHolder) {
	t.Helper()
	return newServerWith(t, mode, fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
}

// newServerWith ist newServer mit eigenem UUID-Resolver.
func newServerWith(t *testing.T, mode string, uuids server.UUIDResolver) (*server.Server, *fakeClock, *fakePortHolder) {
	t.Helper()
	srv, clk, ph := newServerRaw(t, mode, uuids)
	// Wie nach jedem Start räumt der erste Takt den Port-Halter (Zustand
	// unbekannt); die Tests beginnen danach mit leerer Aufzeichnung.
	srv.ExpireTick(t.Context())
	ph.take()
	return srv, clk, ph
}

// newServerRaw baut den Server ohne ersten Takt.
func newServerRaw(t *testing.T, mode string, uuids server.UUIDResolver) (*server.Server, *fakeClock, *fakePortHolder) {
	t.Helper()
	srv, clk, ph, _ := newServerIn(t, t.TempDir(), mode, uuids)
	return srv, clk, ph
}

// newServerIn baut den Server ohne ersten Takt mit DataDir dir.
func newServerIn(t *testing.T, dir, mode string, uuids server.UUIDResolver) (*server.Server, *fakeClock, *fakePortHolder, string) {
	t.Helper()
	cfg := &config.Config{
		DataDir:          dir,
		MinecraftAddress: "mc.example.org:15565",
		StorageBudget:    1 << 30,
		Members: []config.Member{
			{Email: emailA, Minecraft: "Alice", Admin: true, Op: true},
			{Email: emailB, Minecraft: "Bob"},
		},
		WorldSettings: map[string]string{"difficulty": "normal"},
	}
	cfg.Auth.Mode = mode
	cfg.PortHolder.Port = mcPort

	clk := &fakeClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	lm, err := lease.New(filepath.Join(dir, "lease.json"), clk)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	ph := &fakePortHolder{}
	srv := server.New(cfg, lm, st, ph, uuids)
	return srv, clk, ph, dir
}

type who struct {
	email string
	id    int64
}

var (
	userA    = who{emailA, idA}
	userB    = who{emailB, idB}
	stranger = who{"mallory@example.org", 666}
	nobody   = who{}
)

// call schickt in als JSON (falls nicht nil) und liest eine 2xx-Antwort nach
// out (falls nicht nil). Ergebnis ist der Statuscode.
func call(t *testing.T, base string, as who, method, path string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	setIdentity(req, as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &e); err != nil || e.Error == "" {
			t.Errorf("%s %s: %d ohne {\"error\":…}: %q", method, path, resp.StatusCode, data)
		}
		return resp.StatusCode
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: Antwort %q: %v", method, path, data, err)
		}
	}
	return resp.StatusCode
}

func setIdentity(req *http.Request, as who) {
	if as.email != "" {
		req.Header.Set("X-Tunnel-Account-Email", as.email)
	}
	if as.id != 0 {
		req.Header.Set("X-Tunnel-Account-Id", strconv.FormatInt(as.id, 10))
	}
}

type playResult struct {
	status int
	resp   api.PlayResponse
}

// playElection schickt eine Spielanfrage, die ins Sammelfenster geht,
// schließt das Fenster über die Uhr und liefert die Antwort.
func playElection(t *testing.T, base string, clk *fakeClock, as who, req api.PlayRequest) playResult {
	t.Helper()
	ch := make(chan playResult, 1)
	go func() {
		var r playResult
		r.status = call(t, base, as, http.MethodPost, "/v1/play", req, &r.resp)
		ch <- r
	}()
	clk.waitWindow(t)
	clk.Advance(lease.Window)
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("keine Antwort auf /v1/play")
		return playResult{}
	}
}

func putPart(t *testing.T, base string, as who, sha, contentRange string, data []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/v1/blobs/"+sha, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	setIdentity(req, as)
	req.Header.Set("Content-Range", contentRange)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestEndToEndLeaseAndCommit(t *testing.T) {
	srv, clk, ph := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	base := ts.URL
	ctx := t.Context()
	cliA := client.New(&group.Group{Coordinator: base, DevIdentityEmail: emailA})

	// Noch keine Welt: Head → 404, der Client macht daraus nil.
	if g, err := cliA.Head(ctx); err != nil || g != nil {
		t.Fatalf("Head ohne Welt: %v, %v", g, err)
	}

	// A spielt → Sammelfenster → host; Port-Halter auf A.
	resA := playElection(t, base, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if resA.status != http.StatusOK || resA.resp.Role != api.RoleHost || resA.resp.LeaseID == 0 {
		t.Fatalf("A: %d %+v", resA.status, resA.resp)
	}
	if h := resA.resp.Host; h == nil || h.Email != emailA || h.MachineID != "pc-a" {
		t.Fatalf("A: Host %+v", h)
	}
	if got := ph.take(); !slices.Equal(got, []string{"set 15565 101"}) {
		t.Fatalf("Port-Halter nach Zuschlag an A: %q", got)
	}
	leaseA := resA.resp.LeaseID

	// B spielt → join mit Host A, ohne LeaseID und ohne MachineID des Hosts.
	var resB api.PlayResponse
	if code := call(t, base, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b"}, &resB); code != http.StatusOK {
		t.Fatalf("B play: %d", code)
	}
	if resB.Role != api.RoleJoin || resB.LeaseID != 0 {
		t.Fatalf("B: %+v", resB)
	}
	if h := resB.Host; h == nil || h.Email != emailA || h.MinecraftName != "Alice" || h.MachineID != "" {
		t.Fatalf("B: Host %+v", h)
	}
	if got := ph.take(); len(got) != 0 {
		t.Fatalf("Port-Halter nach join: %q", got)
	}

	// A lädt zwei Blobs hoch: einen in zwei Teilen, einen leeren (über den
	// Client, "bytes */0"). Ein Teil mit falschem Hash → 400.
	data := []byte("hallo wanderwelt")
	shaData, shaEmpty := shaHex(data), shaHex(nil)
	if code := putPart(t, base, userA, shaData, "bytes 0-4/16", data[:5]); code != http.StatusAccepted {
		t.Fatalf("erster Teil: %d", code)
	}
	if code := putPart(t, base, userA, shaData, "bytes 5-15/16", data[5:]); code != http.StatusCreated {
		t.Fatalf("letzter Teil: %d", code)
	}
	if err := cliA.PutBlob(ctx, shaEmpty, bytes.NewReader(nil), 0); err != nil {
		t.Fatalf("leerer Blob: %v", err)
	}
	if code := putPart(t, base, userA, shaHex([]byte("xy")), "bytes 0-1/2", []byte("yy")); code != http.StatusBadRequest {
		t.Fatalf("falscher Hash: %d", code)
	}
	shaOther := shaHex([]byte("fehlt"))
	missing, err := cliA.Missing(ctx, []string{shaData, shaEmpty, shaOther})
	if err != nil || !slices.Equal(missing, []string{shaOther}) {
		t.Fatalf("Missing: %q, %v", missing, err)
	}

	// A committet auf Basis 0 → Generation 1 einer neuen Welt.
	files := []api.FileEntry{
		{Path: "level.dat", Size: int64(len(data)), SHA256: shaData},
		{Path: "region/r.0.0.mca", Size: 0, SHA256: shaEmpty},
	}
	commitA := api.CommitRequest{LeaseID: leaseA, MachineID: "pc-a", MinecraftVersion: "26.2", Files: files}
	var gen1 api.Generation
	if code := call(t, base, userA, http.MethodPost, "/v1/generations", commitA, &gen1); code != http.StatusOK {
		t.Fatalf("Commit A: %d", code)
	}
	if gen1.Number != 1 || gen1.WorldID == "" || gen1.HostEmail != emailA {
		t.Fatalf("Generation 1: %+v", gen1)
	}
	if head, err := cliA.Head(ctx); err != nil || head == nil || head.Number != 1 || head.WorldID != gen1.WorldID {
		t.Fatalf("Head: %+v, %v", head, err)
	}
	var got bytes.Buffer
	if err := cliA.GetBlob(ctx, shaData, &got); err != nil || !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("GetBlob: %q, %v", got.Bytes(), err)
	}

	// B (Mitspieler) mit LeaseID und MachineID von A: Lebenszeichen,
	// Commit, Host-Einstellungen und Rückgabe → 409.
	if code := call(t, base, userB, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: leaseA, MachineID: "pc-a"}, nil); code != http.StatusConflict {
		t.Fatalf("Lebenszeichen von B: %d", code)
	}
	commitB := commitA
	commitB.BaseWorldID, commitB.BaseGeneration = gen1.WorldID, 1
	if code := call(t, base, userB, http.MethodPost, "/v1/generations", commitB, nil); code != http.StatusConflict {
		t.Fatalf("Commit von B mit A's Zuschlag: %d", code)
	}
	if code := call(t, base, userB, http.MethodGet, "/v1/host/settings", nil, nil); code != http.StatusConflict {
		t.Fatalf("Host-Einstellungen für B: %d", code)
	}
	if code := call(t, base, userB, http.MethodPost, "/v1/lease/release",
		api.ReleaseRequest{LeaseID: leaseA, MachineID: "pc-a"}, nil); code != http.StatusConflict {
		t.Fatalf("Rückgabe durch B: %d", code)
	}

	// A's Zuschlag hat das überstanden.
	if code := call(t, base, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: leaseA, MachineID: "pc-a", Players: []string{"Alice", "Bob"}}, nil); code != http.StatusOK {
		t.Fatalf("Lebenszeichen von A: %d", code)
	}
	var status api.Status
	if code := call(t, base, userB, http.MethodGet, "/v1/status", nil, &status); code != http.StatusOK {
		t.Fatalf("Status: %d", code)
	}
	if status.Host == nil || status.Host.Email != emailA || status.Host.MachineID != "" ||
		!slices.Equal(status.Players, []string{"Alice", "Bob"}) || status.HeadGeneration != 1 {
		t.Fatalf("Status: %+v (Host %+v)", status, status.Host)
	}
	var hs api.HostSettings
	if code := call(t, base, userA, http.MethodGet, "/v1/host/settings", nil, &hs); code != http.StatusOK {
		t.Fatalf("Host-Einstellungen für A: %d", code)
	}
	wantWL := []api.Player{{UUID: uuidA, Name: "Alice"}, {UUID: uuidB, Name: "Bob"}}
	if !slices.Equal(hs.Whitelist, wantWL) || !slices.Equal(hs.Ops, wantWL[:1]) || hs.Properties["difficulty"] != "normal" {
		t.Fatalf("Host-Einstellungen: %+v", hs)
	}

	// 91 s ohne Lebenszeichen: Ticker räumt den Port genau einmal.
	clk.Advance(lease.LeaseTTL + time.Second)
	srv.ExpireTick(ctx)
	if got := ph.take(); !slices.Equal(got, []string{"clear 15565"}) {
		t.Fatalf("Port-Halter nach Ablauf: %q", got)
	}
	srv.ExpireTick(ctx)
	if got := ph.take(); len(got) != 0 {
		t.Fatalf("zweiter Takt: %q", got)
	}
	if code := call(t, base, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: leaseA, MachineID: "pc-a"}, nil); code != http.StatusConflict {
		t.Fatalf("Lebenszeichen nach Ablauf: %d", code)
	}

	// B wird Host. Als Nicht-Admin darf er keine Welt anlegen, einen Stand
	// auf dem Head aber einreichen.
	resB2 := playElection(t, base, clk, userB, api.PlayRequest{MachineID: "pc-b"})
	if resB2.status != http.StatusOK || resB2.resp.Role != api.RoleHost || resB2.resp.LeaseID <= leaseA {
		t.Fatalf("B als Host: %d %+v", resB2.status, resB2.resp)
	}
	if got := ph.take(); !slices.Equal(got, []string{"set 15565 202"}) {
		t.Fatalf("Port-Halter nach Zuschlag an B: %q", got)
	}
	leaseB := resB2.resp.LeaseID
	newWorld := api.CommitRequest{LeaseID: leaseB, MachineID: "pc-b", NewWorld: true, MinecraftVersion: "26.2", Files: files}
	if code := call(t, base, userB, http.MethodPost, "/v1/generations", newWorld, nil); code != http.StatusForbidden {
		t.Fatalf("NewWorld durch Nicht-Admin: %d", code)
	}
	commitB2 := api.CommitRequest{LeaseID: leaseB, MachineID: "pc-b", BaseWorldID: gen1.WorldID, BaseGeneration: 1,
		MinecraftVersion: "26.2", Files: files}
	var gen2 api.Generation
	if code := call(t, base, userB, http.MethodPost, "/v1/generations", commitB2, &gen2); code != http.StatusOK || gen2.Number != 2 {
		t.Fatalf("Commit B: %d %+v", code, gen2)
	}
	staleA := commitA
	staleA.BaseWorldID, staleA.BaseGeneration = gen1.WorldID, 2
	if code := call(t, base, userA, http.MethodPost, "/v1/generations", staleA, nil); code != http.StatusConflict {
		t.Fatalf("Commit mit abgelaufenem Zuschlag: %d", code)
	}

	// Nachreichen (UploadOnly): nur ohne Host und mit Basis == Head, ohne
	// Sammelfenster.
	upload := api.PlayRequest{MachineID: "pc-a", UploadOnly: true, BaseWorldID: gen1.WorldID, BaseGeneration: 2}
	var up api.PlayResponse
	if code := call(t, base, userA, http.MethodPost, "/v1/play", upload, &up); code != http.StatusOK || up.Role != api.RoleDenied || up.Reason == "" {
		t.Fatalf("UploadOnly bei Host B: %d %+v", code, up)
	}
	if code := call(t, base, userB, http.MethodPost, "/v1/lease/release",
		api.ReleaseRequest{LeaseID: leaseB, MachineID: "pc-b"}, nil); code != http.StatusNoContent {
		t.Fatalf("Rückgabe durch B: %d", code)
	}
	if got := ph.take(); !slices.Equal(got, []string{"clear 15565"}) {
		t.Fatalf("Port-Halter nach Rückgabe: %q", got)
	}
	stale := upload
	stale.BaseGeneration = 1
	up = api.PlayResponse{}
	if code := call(t, base, userA, http.MethodPost, "/v1/play", stale, &up); code != http.StatusOK || up.Role != api.RoleDenied {
		t.Fatalf("UploadOnly mit alter Basis: %d %+v", code, up)
	}
	up = api.PlayResponse{}
	if code := call(t, base, userA, http.MethodPost, "/v1/play", upload, &up); code != http.StatusOK || up.Role != api.RoleHost || up.LeaseID <= leaseB {
		t.Fatalf("UploadOnly: %d %+v", code, up)
	}
	if clk.pending() != 0 {
		t.Fatal("UploadOnly hat ein Sammelfenster geöffnet")
	}
	if got := ph.take(); !slices.Equal(got, []string{"set 15565 101"}) {
		t.Fatalf("Port-Halter nach UploadOnly: %q", got)
	}
	if code := call(t, base, userA, http.MethodPost, "/v1/lease/release",
		api.ReleaseRequest{LeaseID: up.LeaseID, MachineID: "pc-a"}, nil); code != http.StatusNoContent {
		t.Fatalf("Rückgabe nach UploadOnly: %d", code)
	}
	ph.take()

	// Port-Halter nicht erreichbar: Zuschlag sofort zurück, 503; der nächste
	// Takt räumt den Halter, dessen Zustand offen ist.
	ph.setFail(true)
	resFail := playElection(t, base, clk, userB, api.PlayRequest{MachineID: "pc-b"})
	if resFail.status != http.StatusServiceUnavailable {
		t.Fatalf("Zuschlag ohne Port-Halter: %d %+v", resFail.status, resFail.resp)
	}
	status = api.Status{}
	if code := call(t, base, userB, http.MethodGet, "/v1/status", nil, &status); code != http.StatusOK || status.Host != nil {
		t.Fatalf("Status nach gescheitertem Set: %d %+v", code, status.Host)
	}
	ph.setFail(false)
	srv.ExpireTick(ctx)
	if got := ph.take(); !slices.Equal(got, []string{"set 15565 202", "clear 15565"}) {
		t.Fatalf("Port-Halter nach gescheitertem Set: %q", got)
	}

	// Identität: Nicht-Mitglied → 403, keine Identität → 401.
	if code := call(t, base, stranger, http.MethodGet, "/v1/me", nil, nil); code != http.StatusForbidden {
		t.Fatalf("Nicht-Mitglied: %d", code)
	}
	if code := call(t, base, nobody, http.MethodGet, "/v1/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("ohne Identität: %d", code)
	}

	// tunneld-Modus: Header zählen nur über Loopback.
	tsrv, _, _ := newServer(t, "tunneld")
	for _, tc := range []struct {
		remote string
		want   int
	}{
		{"192.0.2.7:40000", http.StatusUnauthorized},
		{"127.0.0.1:40000", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.RemoteAddr = tc.remote
		setIdentity(req, userA)
		rec := httptest.NewRecorder()
		tsrv.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("tunneld von %s: %d, erwartet %d (%s)", tc.remote, rec.Code, tc.want, rec.Body)
		}
	}
}

// zeros liefert beliebig viele Nullbytes (ohne Speicher dafür).
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// rawFrame baut einen Batch-Rahmen mit angegebenem Hash und Daten.
func rawFrame(sha string, data []byte) []byte {
	raw, _ := hex.DecodeString(sha)
	return append(binary.BigEndian.AppendUint64(raw, uint64(len(data))), data...)
}

// post schickt body roh an path und liefert Status und Antwort.
func post(t *testing.T, base, path string, body io.Reader, size int64) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = size
	setIdentity(req, userA)
	req.Header.Set("X-Tunnel-Account-Email", emailA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestBlobFetchAndBatch(t *testing.T) {
	srv, _, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := t.Context()
	cli := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: emailA})

	a, b, c := []byte("Segment A"), []byte("Segment B, etwas länger"), []byte("C")
	frames := func(blobs ...[]byte) []client.BlobFrame {
		var out []client.BlobFrame
		for _, d := range blobs {
			out = append(out, client.BlobFrame{SHA256: shaHex(d), Size: int64(len(d)), Data: bytes.NewReader(d)})
		}
		return out
	}
	// Gewollter Pfad: Batch hoch, dann in anderer Reihenfolge (mit
	// Wiederholung) als ein Strom zurück.
	if err := cli.PutBlobs(ctx, frames(a, b, c)); err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	if m, err := cli.Missing(ctx, []string{shaHex(a), shaHex(b), shaHex(c)}); err != nil || len(m) != 0 {
		t.Fatalf("Missing nach PutBlobs: %v, %v", m, err)
	}
	rc, err := cli.FetchBlobs(ctx, []string{shaHex(b), shaHex(a), shaHex(b), shaHex(c)})
	if err != nil {
		t.Fatalf("FetchBlobs: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if want := slices.Concat(b, a, b, c); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("Strom %q (%v), want %q", got, err, want)
	}

	// Fehlende Blobs: 404 mit der Liste der fehlenden (jeder einmal), als
	// *client.MissingBlobsError.
	m1, m2 := shaHex([]byte("fehlt 1")), shaHex([]byte("fehlt 2"))
	if rc, err = cli.FetchBlobs(ctx, []string{shaHex(a), m1, m2, m1}); err == nil {
		rc.Close()
	}
	if me, ok := errors.AsType[*client.MissingBlobsError](err); !ok || !slices.Equal(me.Hashes, []string{m1, m2}) {
		t.Fatalf("FetchBlobs mit fehlenden: %v", err)
	}
	var missing struct {
		Error   string   `json:"error"`
		Missing []string `json:"missing"`
	}
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/blobs/fetch", map[string]any{"hashes": []string{m2, shaHex(c)}}, nil); code != http.StatusNotFound {
		t.Fatalf("fetch mit fehlendem: %d", code)
	}
	body, _ := json.Marshal(map[string]any{"hashes": []string{m2, shaHex(c)}})
	if code, data := post(t, ts.URL, "/v1/blobs/fetch", bytes.NewReader(body), int64(len(body))); code != http.StatusNotFound ||
		json.Unmarshal(data, &missing) != nil || !slices.Equal(missing.Missing, []string{m2}) || missing.Error == "" {
		t.Fatalf("404-Antwort %d: %s", code, data)
	}
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/blobs/fetch", map[string]any{"hashes": []string{"abc"}}, nil); code != http.StatusBadRequest {
		t.Errorf("fetch mit ungültigem Hash: %d", code)
	}

	// Grenzen: höchstens 4096 Hashes, höchstens 32 MiB.
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/blobs/fetch", map[string]any{"hashes": slices.Repeat([]string{shaHex(c)}, 4096)}, nil); code != http.StatusOK {
		t.Errorf("fetch mit 4096 Hashes: %d", code)
	}
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/blobs/fetch", map[string]any{"hashes": slices.Repeat([]string{shaHex(c)}, 4097)}, nil); code != http.StatusRequestEntityTooLarge {
		t.Errorf("fetch mit 4097 Hashes: %d, want 413", code)
	}
	half := bytes.Repeat([]byte{7}, 16<<20)
	if err := cli.PutBlobs(ctx, frames(half)); err != nil {
		t.Fatalf("PutBlobs 16 MiB: %v", err)
	}
	if rc, err := cli.FetchBlobs(ctx, []string{shaHex(half), shaHex(half)}); err != nil {
		t.Errorf("fetch genau 32 MiB: %v", err)
	} else {
		n, err := io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil || n != 32<<20 {
			t.Errorf("fetch genau 32 MiB lieferte %d Bytes, %v", n, err)
		}
	}
	if rc, err = cli.FetchBlobs(ctx, []string{shaHex(half), shaHex(half), shaHex(c)}); err == nil {
		rc.Close()
	}
	if se, ok := errors.AsType[*client.StatusError](err); !ok || se.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("fetch über 32 MiB: %v, want 413", err)
	}

	// Batch: falscher Hash im zweiten Rahmen → 400, dieser Blob entsteht nicht.
	d, e := []byte("D"), []byte("E")
	bad := slices.Concat(rawFrame(shaHex(d), d), rawFrame(shaHex(e), []byte("nicht E")))
	if code, data := post(t, ts.URL, "/v1/blobs/batch", bytes.NewReader(bad), int64(len(bad))); code != http.StatusBadRequest {
		t.Errorf("batch mit falschem Hash: %d %s", code, data)
	}
	if m, err := cli.Missing(ctx, []string{shaHex(d), shaHex(e)}); err != nil || !slices.Equal(m, []string{shaHex(e)}) {
		t.Errorf("nach falschem Rahmen: Missing = %v, %v; want nur E", m, err)
	}
	// Batch über 32 MiB → 413 (vorab per Content-Length und beim Lesen).
	big := int64(32<<20) + 1
	head := rawFrame(shaHex(e), nil)[:32]
	head = binary.BigEndian.AppendUint64(head, uint64(big-40))
	if code, _ := post(t, ts.URL, "/v1/blobs/batch", io.MultiReader(bytes.NewReader(head), io.LimitReader(zeros{}, big-40)), big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("batch mit %d Bytes: %d, want 413", big, code)
	}
	if code, _ := post(t, ts.URL, "/v1/blobs/batch", io.MultiReader(bytes.NewReader(head), io.LimitReader(zeros{}, big-40)), -1); code != http.StatusRequestEntityTooLarge {
		t.Errorf("batch mit %d Bytes ohne Content-Length: %d, want 413", big, code)
	}
	if err := cli.PutBlobs(ctx, []client.BlobFrame{{SHA256: shaHex(e), Size: big - 40, Data: io.LimitReader(zeros{}, big-40)}}); err == nil {
		t.Error("PutBlobs über 32 MiB ohne Fehler")
	}

	// Höchstens 4096 Rahmen je Batch (leere Blobs: 4097 passen leicht in
	// 32 MiB).
	empty := rawFrame(shaHex(nil), nil)
	for n, want := range map[int]int{4096: http.StatusOK, 4097: http.StatusRequestEntityTooLarge} {
		body := bytes.Repeat(empty, n)
		if code, data := post(t, ts.URL, "/v1/blobs/batch", bytes.NewReader(body), int64(len(body))); code != want {
			t.Errorf("batch mit %d Rahmen: %d %s, want %d", n, code, data, want)
		}
	}
	if err := cli.PutBlobs(ctx, slices.Repeat([]client.BlobFrame{{SHA256: shaHex(nil)}}, 4097)); err == nil {
		t.Error("PutBlobs mit 4097 Rahmen ohne Fehler")
	}
}
