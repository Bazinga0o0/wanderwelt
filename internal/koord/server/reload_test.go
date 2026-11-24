package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Neuladen (SIGHUP): neue Mitglieder gelten sofort, auch in Whitelist und
// Ops; eine ungültige Datei lässt die alte Liste stehen.
func TestReloadMembers(t *testing.T) {
	const uuidC = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	var (
		mu       sync.Mutex
		resolved []string
	)
	uuids := map[string]string{"Alice": uuidA, "Bob": uuidB, "Carol": uuidC}
	res := resolverFunc(func(_ context.Context, name string) (string, error) {
		mu.Lock()
		resolved = append(resolved, name)
		mu.Unlock()
		if u, ok := uuids[name]; ok {
			return u, nil
		}
		return "", fmt.Errorf("unbekannt: %s", name)
	})
	srv, clk, _ := newServerWith(t, "dev", res)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	carol := who{"carol@example.org", 303}

	path := filepath.Join(t.TempDir(), "koordinator.yaml")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Bob geht, Carol kommt (als Op); listen ändert sich auch, zählt aber
	// erst nach einem Neustart.
	write(`
listen: 127.0.0.1:9999
auth: {mode: dev}
members:
  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}
  - {email: carol@example.org, minecraft: Carol, op: true}
`)
	if err := srv.ReloadMembers(t.Context(), path); err != nil {
		t.Fatalf("ReloadMembers: %v", err)
	}
	mu.Lock()
	warmed := slices.Contains(resolved, "Carol")
	mu.Unlock()
	if !warmed {
		t.Error("Carol beim Neuladen nicht aufgelöst")
	}
	if code := call(t, ts.URL, carol, http.MethodGet, "/v1/me", nil, nil); code != http.StatusOK {
		t.Errorf("Carol nach dem Neuladen: %d, want 200", code)
	}
	if code := call(t, ts.URL, userB, http.MethodGet, "/v1/me", nil, nil); code != http.StatusForbidden {
		t.Errorf("Bob nach dem Neuladen: %d, want 403", code)
	}

	g := playElection(t, ts.URL, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if g.resp.Role != api.RoleHost {
		t.Fatalf("Zuschlag: %+v", g.resp)
	}
	var hs api.HostSettings
	if code := call(t, ts.URL, userA, http.MethodGet, "/v1/host/settings", nil, &hs); code != http.StatusOK {
		t.Fatalf("Host-Einstellungen: %d", code)
	}
	want := []api.Player{{UUID: uuidA, Name: "Alice"}, {UUID: uuidC, Name: "Carol"}}
	if !slices.Equal(hs.Whitelist, want) || !slices.Equal(hs.Ops, want) {
		t.Errorf("Whitelist %+v, Ops %+v; want Alice und Carol in beiden", hs.Whitelist, hs.Ops)
	}

	// Ungültige Datei (unbekannter Schlüssel): Fehler, Carol bleibt, Bob
	// bleibt draußen.
	write(`
auth: {mode: dev}
farbe: blau
members:
  - {email: alice@example.org, minecraft: Alice, admin: true}
  - {email: bob@example.org, minecraft: Bob}
`)
	if err := srv.ReloadMembers(t.Context(), path); err == nil {
		t.Fatal("ReloadMembers mit ungültiger Datei: kein Fehler")
	}
	if code := call(t, ts.URL, carol, http.MethodGet, "/v1/me", nil, nil); code != http.StatusOK {
		t.Errorf("Carol nach gescheitertem Neuladen: %d, want 200 (alte Liste)", code)
	}
	if code := call(t, ts.URL, userB, http.MethodGet, "/v1/me", nil, nil); code != http.StatusForbidden {
		t.Errorf("Bob nach gescheitertem Neuladen: %d, want 403 (alte Liste)", code)
	}
}
