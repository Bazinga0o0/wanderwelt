package server_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

// infoEnv: Koordinator mit Titel, Beschreibung und optional ausgeschaltetem
// Discord-Beitritt.
func infoEnv(t *testing.T, noDiscord bool) *memberEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:          dir,
		MinecraftAddress: "mc.example.org:15565",
		StorageBudget:    1 << 30,
		Members:          []config.Member{{Email: emailA, Minecraft: "Alice", Admin: true}, {Email: emailB, Minecraft: "Bob"}},
		Gruppe:           config.Gruppe{Titel: "Wandergruppe", Beschreibung: "Survival mit Freunden"},
		NoDiscordJoin:    noDiscord,
	}
	cfg.Auth.Mode = "dev"
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
	srv := server.New(cfg, lm, st, &fakePortHolder{}, mojangFake{"Alice": uuidA, "Bob": uuidB, "Dora": uuidA})
	srv.SetNow(clk.Now)
	srv.ExpireTick(t.Context())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(srv.Wait)
	return &memberEnv{t: t, srv: srv, st: st, clk: clk, base: ts.URL, dir: dir}
}

func (e *memberEnv) info(as who) (api.GroupInfo, int) {
	e.t.Helper()
	var gi api.GroupInfo
	code := call(e.t, e.base, as, http.MethodGet, "/v1/info", nil, &gi)
	return gi, code
}

func TestInfoFuerJedesKonto(t *testing.T) {
	e := infoEnv(t, false)
	gi, code := e.info(userB)
	if code != http.StatusOK || gi != (api.GroupInfo{Title: "Wandergruppe", Description: "Survival mit Freunden", Members: 2, You: api.YouMember}) {
		t.Errorf("Mitglied: %d %+v", code, gi)
	}
	gi, code = e.info(guestD)
	if code != http.StatusOK || gi.You != api.YouNone || gi.Title != "Wandergruppe" || gi.Members != 2 {
		t.Errorf("Gast: %d %+v", code, gi)
	}
	if c := e.join(guestD, "Dora"); c != http.StatusCreated {
		t.Fatalf("Anfrage: %d", c)
	}
	if gi, _ = e.info(guestD); gi.You != api.YouRequested || gi.Members != 2 {
		t.Errorf("Gast nach Anfrage: %+v", gi)
	}
	if _, code = e.info(nobody); code != http.StatusUnauthorized {
		t.Errorf("ohne Identität: %d", code)
	}
	// Nur GET, und nur dieser Pfad für Gäste.
	if c := call(t, e.base, guestD, http.MethodPost, "/v1/info", nil, nil); c != http.StatusForbidden {
		t.Errorf("POST /v1/info als Gast: %d", c)
	}
}

func TestStatusNoDiscordJoin(t *testing.T) {
	for _, off := range []bool{false, true} {
		e := infoEnv(t, off)
		var st api.Status
		if c := call(t, e.base, userB, http.MethodGet, "/v1/status", nil, &st); c != http.StatusOK {
			t.Fatalf("Status: %d", c)
		}
		if st.NoDiscordJoin != off {
			t.Errorf("NoDiscordJoin=%v, want %v", st.NoDiscordJoin, off)
		}
	}
}
