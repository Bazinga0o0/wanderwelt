package server_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
)

// GET /v1/ping antwortet leer (204): die Zeitmessung des Starters und
// zugleich sein Zeichen, dass der Koordinator ping_ms kennt.
func TestPingEndpoint(t *testing.T) {
	srv, _, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	if code := call(t, ts.URL, userA, http.MethodGet, "/v1/ping", nil, nil); code != http.StatusNoContent {
		t.Fatalf("GET /v1/ping = %d, erwartet 204", code)
	}
}

// Bei sonst gleicher Hardware wird der PC mit dem besseren Ping Host, auch
// wenn der andere zuerst kam; ein negativer Ping bei der Bereitschaft ist
// eine ungültige Anfrage.
func TestPlayElectionUsesPing(t *testing.T) {
	srv, clk, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	hw := api.Score{UploadMbps: 100, CPUScore: 1000, RAMFreeMB: 20000, RAMTotalMB: 32000, XmxMB: 4096}
	slow, fast := hw, hw
	slow.PingMs, fast.PingMs = 180, 25
	var mu sync.Mutex
	roles := map[string]string{}
	var wg sync.WaitGroup
	send := func(as who, machine string, sc api.Score) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r api.PlayResponse
			call(t, ts.URL, as, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: machine, Score: sc}, &r)
			mu.Lock()
			roles[machine] = r.Role
			mu.Unlock()
		}()
	}
	send(userA, "pc-a", slow) // der mit dem schlechteren Ping kommt zuerst
	clk.waitWindow(t)
	send(userB, "pc-b", fast)
	time.Sleep(150 * time.Millisecond) // die zweite Anfrage ist im Fenster
	clk.Advance(lease.Window)
	wg.Wait()
	if roles["pc-b"] != api.RoleHost || roles["pc-a"] != api.RoleJoin {
		t.Fatalf("Rollen %v, erwartet pc-b host und pc-a join", roles)
	}

	bad := fast
	bad.PingMs = -1
	if code := call(t, ts.URL, userB, http.MethodPost, "/v1/presence", api.PresenceRequest{MachineID: "pc-b", Score: bad}, nil); code != http.StatusBadRequest {
		t.Fatalf("Presence mit negativem Ping = %d, erwartet 400", code)
	}
}
