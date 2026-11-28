package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
)

// getStatus holt /v1/status als as.
func getStatus(t *testing.T, base string, as who) api.Status {
	t.Helper()
	var st api.Status
	if code := call(t, base, as, http.MethodGet, "/v1/status", nil, &st); code != http.StatusOK {
		t.Fatalf("Status: %d", code)
	}
	return st
}

// I1 (a): Spielt derselbe PC (Konto und MachineID) noch einmal normal, ist
// sein alter Zuschlag tot (Starter neu gestartet). Er wird freigegeben, der
// Port-Halter geräumt, und die Anfrage normal entschieden – statt „join“ zum
// eigenen toten Zuschlag.
func TestSameMachinePlayReleasesStaleLease(t *testing.T) {
	srv, clk, ph := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	first := playElection(t, ts.URL, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if first.status != http.StatusOK || first.resp.Role != api.RoleHost {
		t.Fatalf("erster Zuschlag: %d %+v", first.status, first.resp)
	}
	ph.take()

	// Anderer PC desselben Kontos und anderes Konto: join, der Zuschlag bleibt.
	for _, tc := range []struct {
		as      who
		machine string
	}{{userA, "pc-a2"}, {userB, "pc-b"}} {
		var resp api.PlayResponse
		if code := call(t, ts.URL, tc.as, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: tc.machine}, &resp); code != http.StatusOK || resp.Role != api.RoleJoin {
			t.Fatalf("%s/%s: %d %+v, erwartet join", tc.as.email, tc.machine, code, resp)
		}
	}
	if got := ph.take(); len(got) != 0 {
		t.Fatalf("Port-Halter nach join: %q", got)
	}

	// Derselbe PC: alter Zuschlag frei, Halter geräumt, neue Wahl, neuer Zuschlag.
	again := playElection(t, ts.URL, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if again.status != http.StatusOK || again.resp.Role != api.RoleHost || again.resp.LeaseID <= first.resp.LeaseID {
		t.Fatalf("zweite Anfrage desselben PCs: %d %+v (alter Zuschlag %d)", again.status, again.resp, first.resp.LeaseID)
	}
	if got := ph.take(); !slices.Equal(got, []string{"clear 15565", "set 15565 101"}) {
		t.Fatalf("Port-Halter: %q, erwartet erst räumen, dann neu setzen", got)
	}
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: first.resp.LeaseID, MachineID: "pc-a"}, nil); code != http.StatusConflict {
		t.Fatalf("Lebenszeichen auf dem alten Zuschlag: %d, erwartet 409", code)
	}
}

// Ein Zuschlag nur zum Hochladen vom selben PC (Konto und MachineID) gibt
// dessen toten Zuschlag ebenso frei: Der neu gestartete Starter reicht den
// Stand seiner abgestürzten Sitzung sofort nach, statt 90 s zu warten.
// Anfragen anderer PCs – auch desselben Kontos – lassen den Zuschlag
// unberührt und werden abgelehnt wie bisher.
func TestSameMachineUploadOnlyReleasesStaleLease(t *testing.T) {
	srv, clk, ph := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	first := playElection(t, ts.URL, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if first.status != http.StatusOK || first.resp.Role != api.RoleHost {
		t.Fatalf("erster Zuschlag: %d %+v", first.status, first.resp)
	}
	ph.take()

	for _, tc := range []struct {
		as      who
		machine string
	}{{userA, "pc-a2"}, {userB, "pc-b"}} {
		var resp api.PlayResponse
		if code := call(t, ts.URL, tc.as, http.MethodPost, "/v1/play",
			api.PlayRequest{MachineID: tc.machine, UploadOnly: true}, &resp); code != http.StatusOK || resp.Role != api.RoleDenied || resp.LeaseID != 0 {
			t.Fatalf("UploadOnly von %s/%s: %d %+v, erwartet denied", tc.as.email, tc.machine, code, resp)
		}
	}
	if got := ph.take(); len(got) != 0 {
		t.Fatalf("Port-Halter nach fremdem UploadOnly: %q", got)
	}
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: first.resp.LeaseID, MachineID: "pc-a"}, nil); code != http.StatusOK {
		t.Fatalf("Lebenszeichen nach fremdem UploadOnly: %d, erwartet 200 (Zuschlag unberührt)", code)
	}

	// Derselbe PC: alter Zuschlag frei, Halter geräumt, Zuschlag zum
	// Hochladen erteilt (Basis = Head, hier noch keine Welt).
	var up api.PlayResponse
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/play",
		api.PlayRequest{MachineID: "pc-a", UploadOnly: true}, &up); code != http.StatusOK || up.Role != api.RoleHost || up.LeaseID <= first.resp.LeaseID {
		t.Fatalf("UploadOnly desselben PCs: %d %+v (alter Zuschlag %d)", code, up, first.resp.LeaseID)
	}
	if got := ph.take(); !slices.Equal(got, []string{"clear 15565", "set 15565 101"}) {
		t.Fatalf("Port-Halter: %q, erwartet erst räumen, dann neu setzen", got)
	}
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: first.resp.LeaseID, MachineID: "pc-a"}, nil); code != http.StatusConflict {
		t.Fatalf("Lebenszeichen auf dem alten Zuschlag: %d, erwartet 409", code)
	}
	if st := getStatus(t, ts.URL, userB); st.Host == nil || st.Host.Email != "" || st.Host.MinecraftName != "Alice" || !st.HostUploading {
		t.Fatalf("Status nach UploadOnly desselben PCs: Host %+v, lädt hoch %v", st.Host, st.HostUploading)
	}
}

// I1 (a) auch für einen Zuschlag nur zum Hochladen: Ein abgestürztes
// Nachreichen hält den Zuschlag sonst bis zum Ablauf.
func TestSameMachinePlayReleasesStaleUploadLease(t *testing.T) {
	srv, clk, ph := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	var up api.PlayResponse
	if code := call(t, ts.URL, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b", UploadOnly: true}, &up); code != http.StatusOK || up.Role != api.RoleHost {
		t.Fatalf("UploadOnly: %d %+v", code, up)
	}
	ph.take()
	res := playElection(t, ts.URL, clk, userB, api.PlayRequest{MachineID: "pc-b"})
	if res.status != http.StatusOK || res.resp.Role != api.RoleHost || res.resp.LeaseID <= up.LeaseID {
		t.Fatalf("Spielen nach totem Nachreichen: %d %+v", res.status, res.resp)
	}
	if got := ph.take(); !slices.Equal(got, []string{"clear 15565", "set 15565 202"}) {
		t.Fatalf("Port-Halter: %q", got)
	}
}

// I1 (b): Ein Zuschlag nur zum Hochladen ist kein Host zum Mitspielen:
// Status meldet ihn als hochladend und nie bereit, Play anderer bekommt
// denied mit Grund statt join.
func TestUploadOnlyLeaseIsNotJoinable(t *testing.T) {
	srv, _, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	var up api.PlayResponse
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-a", UploadOnly: true}, &up); code != http.StatusOK || up.Role != api.RoleHost {
		t.Fatalf("UploadOnly: %d %+v", code, up)
	}
	// Selbst ein Lebenszeichen mit ready macht ihn nicht bereit.
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: up.LeaseID, MachineID: "pc-a", Ready: true}, nil); code != http.StatusOK {
		t.Fatalf("Lebenszeichen: %d", code)
	}
	st := getStatus(t, ts.URL, userB)
	if st.Host == nil || st.Host.Email != "" || st.Host.MinecraftName != "Alice" || st.HostReady || !st.HostUploading {
		t.Fatalf("Status bei Zuschlag nur zum Hochladen: Host %+v, bereit %v, lädt hoch %v", st.Host, st.HostReady, st.HostUploading)
	}

	var resp api.PlayResponse
	if code := call(t, ts.URL, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b"}, &resp); code != http.StatusOK {
		t.Fatalf("Play von B: %d", code)
	}
	if resp.Role != api.RoleDenied || resp.Reason != lease.ReasonUploading || resp.LeaseID != 0 {
		t.Fatalf("Play von B während des Hochladens: %+v, erwartet denied (%q)", resp, lease.ReasonUploading)
	}
	if resp.Host == nil || resp.Host.Email != "" || resp.Host.MinecraftName != "Alice" || resp.Host.MachineID != "" {
		t.Fatalf("Host in der Ablehnung: %+v", resp.Host)
	}
}

// I1 (c): Der Status meldet den Host erst als bereit, wenn sein
// Lebenszeichen ready trägt.
func TestHostReadyFollowsHeartbeat(t *testing.T) {
	srv, clk, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	res := playElection(t, ts.URL, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if res.resp.Role != api.RoleHost {
		t.Fatalf("Zuschlag: %+v", res.resp)
	}
	if st := getStatus(t, ts.URL, userB); st.Host == nil || st.HostReady || st.HostUploading {
		t.Fatalf("Status direkt nach dem Zuschlag: Host %+v, bereit %v, lädt hoch %v", st.Host, st.HostReady, st.HostUploading)
	}
	for _, ready := range []bool{false, true, false, true} {
		hb := api.HeartbeatRequest{LeaseID: res.resp.LeaseID, MachineID: "pc-a", Players: []string{}, Ready: ready}
		if code := call(t, ts.URL, userA, http.MethodPost, "/v1/lease/heartbeat", hb, nil); code != http.StatusOK {
			t.Fatalf("Lebenszeichen: %d", code)
		}
		if st := getStatus(t, ts.URL, userB); st.HostReady != ready {
			t.Fatalf("Lebenszeichen ready=%v → Status bereit %v", ready, st.HostReady)
		}
	}
	// Nach der Rückgabe gibt es keinen bereiten Host mehr.
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/lease/release",
		api.ReleaseRequest{LeaseID: res.resp.LeaseID, MachineID: "pc-a"}, nil); code != http.StatusNoContent {
		t.Fatalf("Rückgabe: %d", code)
	}
	if st := getStatus(t, ts.URL, userB); st.Host != nil || st.HostReady {
		t.Fatalf("Status nach Rückgabe: %+v", st)
	}
}

// B3 (Gesamtlauf): Nach einem harten Absturz des Hosts kommen keine
// Lebenszeichen mehr. Der Zuschlag gilt bis zum Ablauf (90 s) weiter, aber
// bereit ist der Host nach zwei verpassten Lebenszeichen (25 s) nicht mehr –
// sonst hieße es für Mitspieler „Server bereit, verbinde …“ zu einem toten
// Server.
func TestHostReadyEndsWithoutHeartbeat(t *testing.T) {
	srv, clk, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()

	res := playElection(t, ts.URL, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if res.resp.Role != api.RoleHost {
		t.Fatalf("Zuschlag: %+v", res.resp)
	}
	hb := api.HeartbeatRequest{LeaseID: res.resp.LeaseID, MachineID: "pc-a", Players: []string{}, Ready: true}
	if code := call(t, ts.URL, userA, http.MethodPost, "/v1/lease/heartbeat", hb, nil); code != http.StatusOK {
		t.Fatalf("Lebenszeichen: %d", code)
	}
	if st := getStatus(t, ts.URL, userB); !st.HostReady {
		t.Fatalf("Status nach Lebenszeichen ready: %+v", st)
	}
	clk.Advance(20 * time.Second)
	if st := getStatus(t, ts.URL, userB); !st.HostReady {
		t.Fatalf("Status 20 s nach dem letzten Lebenszeichen: %+v, erwartet noch bereit", st)
	}
	clk.Advance(5 * time.Second)
	st := getStatus(t, ts.URL, userB)
	if st.HostReady {
		t.Fatalf("Status 25 s nach dem letzten Lebenszeichen: bereit, erwartet nicht mehr")
	}
	if st.Host == nil || st.Host.Email != "" || st.Host.MinecraftName != "Alice" {
		t.Fatalf("Host 25 s nach dem letzten Lebenszeichen: %+v, erwartet weiter %s (Zuschlag gilt bis 90 s)", st.Host, emailA)
	}
}

// T4(5): Nach einem Neustart ist unbekannt, ob der Port-Halter noch auf
// einem alten Host steht; der erste Takt räumt ihn (ohne gültigen Zuschlag).
func TestFirstTickClearsPortHolder(t *testing.T) {
	srv, _, ph := newServerRaw(t, "dev", fakeUUIDs{})
	srv.ExpireTick(t.Context())
	if got := ph.take(); !slices.Equal(got, []string{"clear 15565"}) {
		t.Fatalf("erster Takt: %q, erwartet clear", got)
	}
	srv.ExpireTick(t.Context())
	if got := ph.take(); len(got) != 0 {
		t.Fatalf("zweiter Takt: %q", got)
	}
}

// Port-Halter-PUT 404 (tunneld kennt das Konto nicht oder hat es nicht
// freigeschaltet): klare Meldung mit 403 statt „gleich nochmal“ (503).
func TestPortHolderUnknownAccount(t *testing.T) {
	srv, clk, ph := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ph.mu.Lock()
	ph.failSet, ph.setErr = true, fmt.Errorf("portholder: PUT Port 15565: %w (account not found)", portholder.ErrUnknownAccount)
	ph.mu.Unlock()

	res := playElection(t, ts.URL, clk, userB, api.PlayRequest{MachineID: "pc-b"})
	if res.status != http.StatusForbidden {
		t.Fatalf("Zuschlag mit unbekanntem Konto: %d, erwartet 403", res.status)
	}
	if st := getStatus(t, ts.URL, userB); st.Host != nil {
		t.Fatalf("Zuschlag blieb trotz gescheitertem Set: %+v", st.Host)
	}
	var up api.PlayResponse
	code := call(t, ts.URL, userB, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-b", UploadOnly: true}, &up)
	if code != http.StatusForbidden {
		t.Fatalf("UploadOnly mit unbekanntem Konto: %d, erwartet 403", code)
	}
}

// T4(2): Die Host-Einstellungen warten höchstens die Frist auf Mojang,
// lösen parallel auf und lassen ungelöste Namen weg – ein hängender
// Resolver verhindert das Hosten nie.
func TestHostSettingsWithHangingResolver(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	var calls atomic.Int64
	res := resolverFunc(func(ctx context.Context, name string) (string, error) {
		calls.Add(1)
		switch name {
		case "Bob":
			return uuidB, nil
		default: // Alice (erstes Mitglied) hängt und kümmert sich nicht um ctx
			<-hang
			return "", fmt.Errorf("zu spät")
		}
	})
	srv, clk, _ := newServerWith(t, "dev", res)
	srv.SetUUIDTimeout(300 * time.Millisecond)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	g := playElection(t, ts.URL, clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if g.resp.Role != api.RoleHost {
		t.Fatalf("Zuschlag: %+v", g.resp)
	}
	start := time.Now()
	var hs api.HostSettings
	if code := call(t, ts.URL, userA, http.MethodGet, "/v1/host/settings", nil, &hs); code != http.StatusOK {
		t.Fatalf("Host-Einstellungen: %d", code)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Host-Einstellungen brauchten %v trotz Frist 300 ms", d)
	}
	// Bob kommt durch, obwohl Alice vor ihm hängt (parallel); Alice (Op)
	// fehlt in Whitelist und Ops.
	if want := []api.Player{{UUID: uuidB, Name: "Bob"}}; !slices.Equal(hs.Whitelist, want) || len(hs.Ops) != 0 {
		t.Fatalf("Whitelist %+v, Ops %+v; erwartet nur Bob, keine Ops", hs.Whitelist, hs.Ops)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d Auflösungen, erwartet 2 (parallel)", n)
	}
}

type resolverFunc func(ctx context.Context, name string) (string, error)

func (f resolverFunc) UUID(ctx context.Context, name string) (string, error) { return f(ctx, name) }

// T4(2): Mojang-Fehlschläge werden 10 Minuten gemerkt; Treffer dauerhaft.
func TestMojangNegativeCache(t *testing.T) {
	var hits atomic.Int64
	mojang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/Alice"):
			fmt.Fprint(w, `{"id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","name":"Alice"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mojang.Close()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	m := server.NewMojangResolverAt(filepath.Join(t.TempDir(), "uuids.json"), mojang.URL+"/", func() time.Time { return now })
	ctx := t.Context()

	for range 3 {
		if _, err := m.UUID(ctx, "Niemand"); err == nil {
			t.Fatal("unbekannter Name ohne Fehler")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d Anfragen für einen unbekannten Namen, erwartet 1 (Negativ-Cache)", n)
	}
	now = now.Add(11 * time.Minute)
	if _, err := m.UUID(ctx, "Niemand"); err == nil {
		t.Fatal("unbekannter Name ohne Fehler")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("nach 11 Minuten %d Anfragen, erwartet 2", n)
	}
	for range 2 {
		if u, err := m.UUID(ctx, "Alice"); err != nil || u != uuidA {
			t.Fatalf("Alice: %q, %v", u, err)
		}
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("%d Anfragen, erwartet 3 (Treffer gemerkt)", n)
	}
	// Abbruch durch den Aufrufer zählt nicht als Fehlschlag.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.UUID(cctx, "Bob"); err == nil {
		t.Fatal("Bob trotz Abbruch aufgelöst")
	}
	if _, err := m.UUID(ctx, "Bob"); err == nil {
		t.Fatal("Bob ohne Fehler")
	}
	if n := hits.Load(); n != 4 {
		t.Fatalf("%d Anfragen, erwartet 4 (abgebrochene Anfrage nicht gemerkt)", n)
	}
}

// Status nennt die Zahl der Gruppenmitglieder (Discord-Party), keine Namen.
func TestStatusMembers(t *testing.T) {
	srv, _, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	st := getStatus(t, ts.URL, userA)
	if st.Members < 2 {
		t.Errorf("Members = %d, erwartet mindestens userA und userB", st.Members)
	}
}
