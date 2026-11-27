package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
)

const (
	uuidGast  = "99999999-9999-4999-8999-999999999999"
	uuidTroll = "77777777-7777-4777-8777-777777777777"
)

// listResolver kennt Alice, Bob, Gast1 und Troll; alles andere ist bei
// Mojang unbekannt.
var listResolver = resolverFunc(func(_ context.Context, name string) (string, error) {
	switch name {
	case "Alice":
		return uuidA, nil
	case "Bob":
		return uuidB, nil
	case "Gast1":
		return uuidGast, nil
	case "Troll":
		return uuidTroll, nil
	}
	return "", server.ErrUnknownName
})

type listEnv struct {
	t    *testing.T
	srv  *server.Server
	clk  *fakeClock
	base string
	dir  string
}

func newListEnv(t *testing.T) *listEnv {
	t.Helper()
	srv, clk, _, dir := newServerIn(t, t.TempDir(), "dev", listResolver)
	srv.ExpireTick(t.Context())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &listEnv{t: t, srv: srv, clk: clk, base: ts.URL, dir: dir}
}

func (e *listEnv) change(as who, list, action, name string) (int, api.ServerListResult) {
	e.t.Helper()
	var res api.ServerListResult
	code := call(e.t, e.base, as, http.MethodPost, "/v1/server/lists", api.ServerListChange{List: list, Action: action, Name: name}, &res)
	return code, res
}

func (e *listEnv) mustChange(list, action, name string) api.ServerListResult {
	e.t.Helper()
	code, res := e.change(userA, list, action, name)
	if code != http.StatusOK {
		e.t.Fatalf("%s %s %s: %d", list, action, name, code)
	}
	return res
}

// hostUp: A hostet auf pc-a und meldet „bereit“; Ergebnis ist der Zuschlag.
func (e *listEnv) hostUp() int64 {
	e.t.Helper()
	res := playElection(e.t, e.base, e.clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if res.resp.Role != api.RoleHost {
		e.t.Fatalf("A ist nicht Host: %+v", res.resp)
	}
	if code := call(e.t, e.base, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: res.resp.LeaseID, MachineID: "pc-a", Ready: true}, nil); code != http.StatusOK {
		e.t.Fatalf("Lebenszeichen: %d", code)
	}
	return res.resp.LeaseID
}

func names(ps []api.Player) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	slices.Sort(out)
	return out
}

func TestServerReiterNurAdmins(t *testing.T) {
	e := newListEnv(t)
	lease := e.hostUp()
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/server/lists", nil},
		{http.MethodPost, "/v1/server/lists", api.ServerListChange{List: api.ListWhitelist, Action: api.ListAdd, Name: "Gast1"}},
		{http.MethodPost, "/v1/server/lists", api.ServerListChange{List: api.ListBans, Action: api.ListAdd, Name: "Alice"}},
		{http.MethodGet, "/v1/server/gamerules", nil},
		{http.MethodPost, "/v1/server/gamerules", api.GameruleSet{Name: "keep_inventory", Value: "true"}},
		{http.MethodPost, "/v1/server/command", api.ServerCommand{Command: "say hi"}},
	} {
		for _, as := range []who{userB, stranger} {
			if code := call(t, e.base, as, c.method, c.path, c.body, nil); code != http.StatusForbidden {
				t.Errorf("%s %s als %s: %d, erwartet 403", c.method, c.path, as.email, code)
			}
		}
	}
	// Nichts davon ist angekommen.
	if l := e.srv; l != nil {
		var lists api.ServerLists
		call(t, e.base, userA, http.MethodGet, "/v1/server/lists", nil, &lists)
		if len(lists.Bans) != 0 || len(lists.Whitelist) != 2 {
			t.Fatalf("Listen nach abgelehnten Änderungen: %+v", lists)
		}
	}
	// Aufträge holen und Ergebnisse melden darf nur der Host mit seinem PC.
	for _, c := range []struct {
		as      who
		lease   int64
		machine string
	}{{userB, lease, "pc-a"}, {userA, lease, "pc-b"}, {userA, lease + 1, "pc-a"}} {
		if code := call(t, e.base, c.as, http.MethodPost, "/v1/host/jobs", api.HostJobsRequest{LeaseID: c.lease, MachineID: c.machine}, nil); code != http.StatusConflict {
			t.Errorf("Aufträge holen als %s/%d/%s: %d, erwartet 409", c.as.email, c.lease, c.machine, code)
		}
		if code := call(t, e.base, c.as, http.MethodPost, "/v1/host/jobs/result", api.HostJobResult{LeaseID: c.lease, MachineID: c.machine, ID: 1}, nil); code != http.StatusConflict {
			t.Errorf("Ergebnis als %s/%d/%s: %d, erwartet 409", c.as.email, c.lease, c.machine, code)
		}
	}
}

func TestServerListenUndHostEinstellungen(t *testing.T) {
	e := newListEnv(t)

	// Ohne laufenden Host: gespeichert für den nächsten Start.
	res := e.mustChange(api.ListWhitelist, api.ListAdd, "Gast1")
	if !strings.Contains(res.Text, "gespeichert für den nächsten Start") {
		t.Errorf("Text ohne Host: %q", res.Text)
	}
	e.mustChange(api.ListOps, api.ListAdd, "Gast1")
	e.mustChange(api.ListOps, api.ListAdd, "Bob") // Mitglied: Op-Recht in roles.json
	res = e.mustChange(api.ListBans, api.ListAdd, "Troll")
	if len(res.Lists.Bans) != 1 || res.Lists.Bans[0].UUID != uuidTroll || res.Lists.Bans[0].By != "Alice" {
		t.Fatalf("Banns: %+v", res.Lists.Bans)
	}

	// Abgelehnt: Mitglied von der Whitelist, Op ohne Whitelist, sich selbst
	// bannen, ungültige und unbekannte Namen, Gebannte als Gast, doppelt.
	for _, c := range []struct {
		list, action, name string
		want               int
	}{
		{api.ListWhitelist, api.ListRemove, "Bob", http.StatusConflict},
		{api.ListWhitelist, api.ListAdd, "Bob", http.StatusConflict},
		{api.ListOps, api.ListAdd, "Niemand", http.StatusConflict},
		{api.ListBans, api.ListAdd, "alice", http.StatusBadRequest},
		{api.ListWhitelist, api.ListAdd, "Troll", http.StatusConflict},
		{api.ListWhitelist, api.ListAdd, "Gast1", http.StatusConflict},
		{api.ListWhitelist, api.ListAdd, "ab", http.StatusBadRequest},
		{api.ListWhitelist, api.ListAdd, "Gast 1", http.StatusBadRequest},
		{api.ListWhitelist, api.ListAdd, "Gast;op", http.StatusBadRequest},
		{api.ListWhitelist, api.ListAdd, "Unbekannt", http.StatusBadRequest},
		{api.ListBans, api.ListRemove, "Niemand", http.StatusNotFound},
		{"irgendwas", api.ListAdd, "Gast1", http.StatusBadRequest},
	} {
		if code, _ := e.change(userA, c.list, c.action, c.name); code != c.want {
			t.Errorf("%s %s %q: %d, erwartet %d", c.list, c.action, c.name, code, c.want)
		}
	}

	// Der nächste Host startet mit allem (A bekommt den Zuschlag).
	lease := e.hostUp()
	var hs api.HostSettings
	if code := call(t, e.base, userA, http.MethodGet, "/v1/host/settings", nil, &hs); code != http.StatusOK {
		t.Fatalf("Host-Einstellungen: %d", code)
	}
	if got := names(hs.Whitelist); !slices.Equal(got, []string{"Alice", "Bob", "Gast1"}) {
		t.Errorf("Whitelist %v", got)
	}
	if got := names(hs.Ops); !slices.Equal(got, []string{"Alice", "Bob", "Gast1"}) {
		t.Errorf("Ops %v", got)
	}
	if got := names(hs.Bans); !slices.Equal(got, []string{"Troll"}) {
		t.Errorf("Banns %v", got)
	}

	// Jede Änderung ändert MembersVersion (der laufende Host gleicht an).
	beat := func() int64 {
		var r api.HeartbeatResponse
		call(t, e.base, userA, http.MethodPost, "/v1/lease/heartbeat", api.HeartbeatRequest{LeaseID: lease, MachineID: "pc-a", Ready: true}, &r)
		return r.MembersVersion
	}
	v0 := beat()
	res = e.mustChange(api.ListBans, api.ListAdd, "Gast1") // Bann nimmt den Gast von Whitelist und Ops
	if !strings.Contains(res.Text, "übernimmt das in wenigen Sekunden") {
		t.Errorf("Text mit Host: %q", res.Text)
	}
	if v1 := beat(); v1 == v0 {
		t.Errorf("MembersVersion unverändert nach Bann")
	}
	call(t, e.base, userA, http.MethodGet, "/v1/host/settings", nil, &hs)
	if got := names(hs.Whitelist); !slices.Equal(got, []string{"Alice", "Bob"}) {
		t.Errorf("Whitelist nach Bann %v", got)
	}
	if got := names(hs.Bans); !slices.Equal(got, []string{"Gast1", "Troll"}) {
		t.Errorf("Banns nach Bann %v", got)
	}

	// Ein Mitglied bannen: es bleibt Mitglied (Whitelist), der Bann geht vor.
	res = e.mustChange(api.ListBans, api.ListAdd, "Bob")
	if !strings.Contains(res.Text, "Bleibt Mitglied") {
		t.Errorf("Text Bann eines Mitglieds: %q", res.Text)
	}
	e.mustChange(api.ListBans, api.ListRemove, "Bob")

	// Die Listen überstehen einen Neustart des Koordinators.
	srv2, _, _, _ := newServerIn(t, e.dir, "dev", listResolver)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	var lists api.ServerLists
	call(t, ts2.URL, userA, http.MethodGet, "/v1/server/lists", nil, &lists)
	if len(lists.Bans) != 2 || len(lists.Whitelist) != 2 || len(lists.Ops) != 2 {
		t.Errorf("nach Neustart: %+v", lists)
	}
}

// hostJobs holt Aufträge als Host A.
func (e *listEnv) hostJobs(lease int64) []api.HostJob {
	e.t.Helper()
	var r api.HostJobs
	if code := call(e.t, e.base, userA, http.MethodPost, "/v1/host/jobs", api.HostJobsRequest{LeaseID: lease, MachineID: "pc-a"}, &r); code != http.StatusOK {
		e.t.Fatalf("Aufträge holen: %d", code)
	}
	return r.Jobs
}

func TestBefehlskanal(t *testing.T) {
	e := newListEnv(t)
	e.srv.SetJobPoll(50 * time.Millisecond)

	// Ohne laufenden Host: 409.
	if code := call(t, e.base, userA, http.MethodPost, "/v1/server/command", api.ServerCommand{Command: "say hi"}, nil); code != http.StatusConflict {
		t.Fatalf("ohne Host: %d", code)
	}
	lease := e.hostUp()

	// Ein Host, der (noch) nicht „bereit“ meldet (lädt die Welt, startet):
	// ebenfalls 409, sonst warteten Aufträge auf einen Server ohne RCON.
	call(t, e.base, userA, http.MethodPost, "/v1/lease/heartbeat", api.HeartbeatRequest{LeaseID: lease, MachineID: "pc-a", Ready: false}, nil)
	if code := call(t, e.base, userA, http.MethodPost, "/v1/server/command", api.ServerCommand{Command: "say hi"}, nil); code != http.StatusConflict {
		t.Fatalf("Host nicht bereit: %d", code)
	}
	call(t, e.base, userA, http.MethodPost, "/v1/lease/heartbeat", api.HeartbeatRequest{LeaseID: lease, MachineID: "pc-a", Ready: true}, nil)

	// Gesperrte Befehle kommen nie beim Host an.
	for _, cmd := range []string{"stop", "execute run save-all", "whitelist off", "say a\nstop", strings.Repeat("x", 300)} {
		if code := call(t, e.base, userA, http.MethodPost, "/v1/server/command", api.ServerCommand{Command: cmd}, nil); code != http.StatusBadRequest {
			t.Errorf("%q: %d, erwartet 400", cmd, code)
		}
	}
	if jobs := e.hostJobs(lease); len(jobs) != 0 {
		t.Fatalf("Aufträge nach abgelehnten Befehlen: %+v", jobs)
	}

	// Ein erlaubter Befehl: Host holt ihn, meldet das Ergebnis, der Admin
	// bekommt die Antwort (gekürzt, ohne Steuerzeichen).
	done := make(chan api.ServerCommandResult, 1)
	go func() {
		var r api.ServerCommandResult
		call(t, e.base, userA, http.MethodPost, "/v1/server/command", api.ServerCommand{Command: "/say hi"}, &r)
		done <- r
	}()
	var jobs []api.HostJob
	for deadline := time.Now().Add(5 * time.Second); len(jobs) == 0 && time.Now().Before(deadline); {
		jobs = e.hostJobs(lease)
	}
	if len(jobs) != 1 || jobs[0].Kind != api.JobCommand || jobs[0].Command != "say hi" {
		t.Fatalf("Aufträge: %+v", jobs)
	}
	// Ein Ergebnis von B (kein Host) zählt nicht.
	if code := call(t, e.base, userB, http.MethodPost, "/v1/host/jobs/result",
		api.HostJobResult{LeaseID: lease, MachineID: "pc-a", ID: jobs[0].ID, OK: true, Output: "falsch"}, nil); code != http.StatusConflict {
		t.Fatalf("Ergebnis von B: %d", code)
	}
	if code := call(t, e.base, userA, http.MethodPost, "/v1/host/jobs/result",
		api.HostJobResult{LeaseID: lease, MachineID: "pc-a", ID: jobs[0].ID, OK: true, Output: "[Rcon] hi\x1b" + strings.Repeat("y", 5000)}, nil); code != http.StatusNoContent {
		t.Fatalf("Ergebnis: %d", code)
	}
	select {
	case r := <-done:
		if !r.OK || !strings.HasPrefix(r.Output, "[Rcon] hi ") || len(r.Output) > 4096+8 {
			t.Fatalf("Ergebnis beim Admin: ok=%v %d Bytes %q…", r.OK, len(r.Output), r.Output[:min(20, len(r.Output))])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Admin bekommt kein Ergebnis")
	}
	// Ein zweites Ergebnis zum selben Auftrag: unbekannt.
	if code := call(t, e.base, userA, http.MethodPost, "/v1/host/jobs/result",
		api.HostJobResult{LeaseID: lease, MachineID: "pc-a", ID: jobs[0].ID, OK: true}, nil); code != http.StatusNotFound {
		t.Fatalf("zweites Ergebnis: %d", code)
	}

	// Antwortet der Host nicht, bekommt der Admin 504.
	defer server.SetJobWait(100 * time.Millisecond)()
	if code := call(t, e.base, userA, http.MethodPost, "/v1/server/gamerules", api.GameruleSet{Name: "keep_inventory", Value: "true"}, nil); code != http.StatusGatewayTimeout {
		t.Fatalf("ohne Antwort des Hosts: %d", code)
	}
	if code := call(t, e.base, userA, http.MethodPost, "/v1/server/gamerules", api.GameruleSet{Name: "keep_inventory", Value: "true run stop"}, nil); code != http.StatusBadRequest {
		t.Fatalf("Spielregel mit Befehl im Wert: %d", code)
	}
}

func TestSpielregelnUeberDenHost(t *testing.T) {
	e := newListEnv(t)
	e.srv.SetJobPoll(50 * time.Millisecond)
	lease := e.hostUp()
	done := make(chan api.Gamerules, 1)
	go func() {
		var r api.Gamerules
		call(t, e.base, userA, http.MethodGet, "/v1/server/gamerules", nil, &r)
		done <- r
	}()
	var jobs []api.HostJob
	for deadline := time.Now().Add(5 * time.Second); len(jobs) == 0 && time.Now().Before(deadline); {
		jobs = e.hostJobs(lease)
	}
	if len(jobs) != 1 || jobs[0].Kind != api.JobGamerules {
		t.Fatalf("Aufträge: %+v", jobs)
	}
	call(t, e.base, userA, http.MethodPost, "/v1/host/jobs/result", api.HostJobResult{
		LeaseID: lease, MachineID: "pc-a", ID: jobs[0].ID, OK: true,
		Rules: []api.Gamerule{{Name: "keep_inventory", Value: "false"}, {Name: "bad name", Value: "1"}, {Name: "x", Value: "1 run stop"}},
	}, nil)
	select {
	case r := <-done:
		if len(r.Rules) != 1 || r.Rules[0] != (api.Gamerule{Name: "keep_inventory", Value: "false"}) {
			t.Fatalf("Spielregeln: %+v", r.Rules)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("keine Spielregeln")
	}
}
