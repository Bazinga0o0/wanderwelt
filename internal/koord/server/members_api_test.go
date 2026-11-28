package server_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

const (
	emailC = "carol@example.org" // ohne Minecraft-Namen in der Konfiguration
	uuidC  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	gib    = int64(1) << 30
)

var userC = who{emailC, 303}

// mojangFake kennt die Namen aus known; „Stoerung“ ist eine gestörte API,
// alles andere kennt Mojang nicht.
type mojangFake map[string]string

func (f mojangFake) UUID(_ context.Context, name string) (string, error) {
	if u, ok := f[name]; ok {
		return u, nil
	}
	if strings.HasPrefix(name, "Gast") || name == "Dora" || name == "Emil" {
		return "dddddddd-dddd-dddd-dddd-dddddddddddd", nil
	}
	if name == "Stoerung" {
		return "", errors.New("Mojang-API: 503 Service Unavailable")
	}
	return "", fmt.Errorf("Mojang kennt %q nicht: %w", name, server.ErrUnknownName)
}

// memberEnv ist ein Koordinator mit drei Mitgliedern: Alice (Admin, Op),
// Bob und Carol (ohne Minecraft-Namen).
type memberEnv struct {
	t    *testing.T
	srv  *server.Server
	st   *store.Store
	clk  *fakeClock
	base string
	dir  string // data_dir
}

// newMemberEnv baut den Koordinator in dir (Überlagerungen, die dort schon
// liegen, gelten) mit Speicherbudget budget aus der Konfiguration.
func newMemberEnv(t *testing.T, dir string, budget int64) *memberEnv {
	t.Helper()
	cfg := &config.Config{
		DataDir:          dir,
		MinecraftAddress: "mc.example.org:15565",
		StorageBudget:    budget,
		Members: []config.Member{
			{Email: emailA, Minecraft: "Alice", Admin: true, Op: true},
			{Email: emailB, Minecraft: "Bob"},
			{Email: emailC},
		},
	}
	cfg.Auth.Mode = "dev"
	cfg.PortHolder.Port = mcPort
	clk := &fakeClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	lm, err := lease.New(filepath.Join(dir, "lease.json"), clk)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), budget)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, lm, st, &fakePortHolder{}, mojangFake{"Alice": uuidA, "Bob": uuidB, "Carol": uuidC, "Caro": uuidC})
	srv.SetNow(clk.Now)
	srv.ExpireTick(t.Context())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	t.Cleanup(srv.Wait) // vor dem Löschen von dir
	return &memberEnv{t: t, srv: srv, st: st, clk: clk, base: ts.URL, dir: dir}
}

func (e *memberEnv) members(as who) []api.MemberInfo {
	e.t.Helper()
	var out []api.MemberInfo
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/members", nil, &out); code != http.StatusOK {
		e.t.Fatalf("Mitgliederliste: %d", code)
	}
	return out
}

func (e *memberEnv) me(as who) api.Me {
	e.t.Helper()
	var me api.Me
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/me", nil, &me); code != http.StatusOK {
		e.t.Fatalf("/v1/me: %d", code)
	}
	return me
}

func (e *memberEnv) setRole(as who, email string, admin, op bool) int {
	e.t.Helper()
	return call(e.t, e.base, as, http.MethodPut, "/v1/members/"+e.key(email), api.RoleRequest{Admin: admin, Op: op}, nil)
}

// hostSettings lässt as hosten (falls noch nicht) und liefert die
// Host-Einstellungen.
func (e *memberEnv) hostSettings(as who) api.HostSettings {
	e.t.Helper()
	var hs api.HostSettings
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/host/settings", nil, &hs); code != http.StatusOK {
		e.t.Fatalf("Host-Einstellungen: %d", code)
	}
	return hs
}

// heartbeat schickt ein Lebenszeichen für den Zuschlag l von A auf pc-a.
func (e *memberEnv) heartbeat(l int64, players ...string) api.HeartbeatResponse {
	e.t.Helper()
	var hb api.HeartbeatResponse
	req := api.HeartbeatRequest{LeaseID: l, MachineID: "pc-a", Players: players, Ready: true}
	if code := call(e.t, e.base, userA, http.MethodPost, "/v1/lease/heartbeat", req, &hb); code != http.StatusOK {
		e.t.Fatalf("Lebenszeichen: %d", code)
	}
	return hb
}

func (e *memberEnv) host() int64 {
	e.t.Helper()
	r := playElection(e.t, e.base, e.clk, userA, api.PlayRequest{MachineID: "pc-a"})
	if r.status != http.StatusOK || r.resp.Role != api.RoleHost {
		e.t.Fatalf("A hostet nicht: %d %+v", r.status, r.resp)
	}
	return r.resp.LeaseID
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "<fehlt>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// checkMode600 prüft unter Unix die Rechte einer Überlagerung.
func checkMode600(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: Rechte %v, want 0600", path, fi.Mode().Perm())
	}
}

// logBuffer nimmt das Log des Servers auf (slog.Default während des Tests).
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureLog(t *testing.T) *logBuffer {
	t.Helper()
	lb := &logBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(lb, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return lb
}

// Die Mitgliederliste zeigt wirksame Namen und Rollen (Überlagerungen beim
// Start) und wer gerade spielt.
func TestMembersList(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, config.RolesFile), []byte(`{"Bob@Example.org": {"admin": false, "op": true}, "weg@example.org": {"admin": true}}`))
	writeFile(t, filepath.Join(dir, config.NamesFile), []byte(`{"carol@example.org": {"minecraft": "Carol"}}`))
	e := newMemberEnv(t, dir, gib)
	l := e.host()
	e.heartbeat(l, "alice", "Carol")

	// Datenschutz: nur die eigene Zeile (Alice) trägt die Adresse; in fremden
	// Zeilen steht die Kennung (auch in email, für ältere Starter).
	want := []api.MemberInfo{
		{Email: emailA, ID: e.key(emailA), MinecraftName: "Alice", Admin: true, Op: true, Online: true},
		{Email: e.key(emailB), ID: e.key(emailB), MinecraftName: "Bob", Op: true},
		{Email: e.key(emailC), ID: e.key(emailC), MinecraftName: "Carol", Online: true},
	}
	if got := e.members(userA); !slices.Equal(got, want) {
		t.Errorf("Mitglieder %+v\nwant %+v", got, want)
	}
	if code := call(t, e.base, who{"weg@example.org", 999}, http.MethodGet, "/v1/me", nil, nil); code != http.StatusForbidden {
		t.Errorf("Eintrag in roles.json macht kein Mitglied: %d", code)
	}
}

// Das einzige Admin-Recht lässt sich nicht wegnehmen (409), roles.json
// bleibt dann, wie sie war.
func TestPutRoleLastAdmin(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	rolesPath := filepath.Join(e.dir, config.RolesFile)

	if code := e.setRole(userA, emailA, false, true); code != http.StatusConflict {
		t.Fatalf("einziger Admin nimmt sich das Recht: %d, want 409", code)
	}
	if got := readFile(t, rolesPath); got != "<fehlt>" {
		t.Fatalf("roles.json nach 409: %q", got)
	}
	if !e.me(userA).Admin {
		t.Fatal("Alice ist nach 409 kein Admin mehr")
	}

	if code := e.setRole(userA, "BOB@example.org", true, false); code != http.StatusOK {
		t.Fatalf("Bob zum Admin: %d", code)
	}
	checkMode600(t, rolesPath)
	if code := e.setRole(userB, emailA, false, true); code != http.StatusOK {
		t.Fatalf("Bob nimmt Alice das Admin-Recht: %d", code)
	}
	before := readFile(t, rolesPath)
	if code := e.setRole(userB, emailB, false, false); code != http.StatusConflict {
		t.Fatalf("Bob, jetzt einziger Admin, nimmt sich das Recht: %d, want 409", code)
	}
	if after := readFile(t, rolesPath); after != before {
		t.Fatalf("roles.json nach 409 geändert:\n%s\nvorher\n%s", after, before)
	}
	if me := e.me(userA); me.Admin || !me.Op {
		t.Errorf("Alice: %+v, want Op ohne Admin", me)
	}
	if code := e.setRole(userB, "niemand@example.org", true, true); code != http.StatusNotFound {
		t.Errorf("unbekanntes Mitglied: %d, want 404", code)
	}
}

// Rollen wirken sofort: Ops in den Host-Einstellungen, Admin in /v1/me und
// beim Zugriff auf die Mitgliederliste; auch nach dem Neuladen (SIGHUP) und
// einem Neustart.
func TestPutRoleAppliesToHostSettings(t *testing.T) {
	dir := t.TempDir()
	e := newMemberEnv(t, dir, gib)
	e.host()
	ops := func() []string {
		var names []string
		for _, p := range e.hostSettings(userA).Ops {
			names = append(names, p.Name)
		}
		return names
	}
	if got := ops(); !slices.Equal(got, []string{"Alice"}) {
		t.Fatalf("Ops vorher: %v", got)
	}
	if code := e.setRole(userA, emailB, false, true); code != http.StatusOK {
		t.Fatalf("Bob zum Op: %d", code)
	}
	if code := e.setRole(userA, emailA, true, false); code != http.StatusOK {
		t.Fatalf("Alice ohne Op: %d", code)
	}
	if got := ops(); !slices.Equal(got, []string{"Bob"}) {
		t.Fatalf("Ops nach dem Umschalten: %v, want [Bob]", got)
	}
	if code := call(t, e.base, userB, http.MethodGet, "/v1/members", nil, nil); code != http.StatusForbidden {
		t.Fatalf("Bob vor dem Admin-Recht: %d, want 403", code)
	}
	var info api.MemberInfo
	if code := call(t, e.base, userA, http.MethodPut, "/v1/members/"+e.key(emailB), api.RoleRequest{Admin: true, Op: true}, &info); code != http.StatusOK {
		t.Fatalf("Bob zum Admin: %d", code)
	}
	if want := (api.MemberInfo{Email: e.key(emailB), ID: e.key(emailB), MinecraftName: "Bob", Admin: true, Op: true}); info != want {
		t.Errorf("Antwort %+v, want %+v", info, want)
	}
	if me := e.me(userB); !me.Admin || !me.Op {
		t.Errorf("/v1/me von Bob: %+v", me)
	}
	if code := call(t, e.base, userB, http.MethodGet, "/v1/members", nil, nil); code != http.StatusOK {
		t.Errorf("Bob als Admin: %d", code)
	}

	// Neuladen: dieselben Mitglieder, die Rollen gelten weiter.
	cfgPath := filepath.Join(t.TempDir(), "koordinator.yaml")
	writeFile(t, cfgPath, []byte(`
auth: {mode: dev}
members:
  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}
  - {email: bob@example.org, minecraft: Bob}
  - {email: carol@example.org}
`))
	if err := e.srv.ReloadMembers(t.Context(), cfgPath); err != nil {
		t.Fatal(err)
	}
	if got := ops(); !slices.Equal(got, []string{"Bob"}) {
		t.Errorf("Ops nach dem Neuladen: %v, want [Bob]", got)
	}
	if !e.me(userB).Admin {
		t.Error("Bob nach dem Neuladen kein Admin")
	}

	// Neustart auf demselben data_dir.
	e2 := newMemberEnv(t, dir, gib)
	if me := e2.me(userB); !me.Admin || !me.Op {
		t.Errorf("Bob nach dem Neustart: %+v", me)
	}
	if me := e2.me(userA); !me.Admin || me.Op {
		t.Errorf("Alice nach dem Neustart: %+v", me)
	}
}

// Wer beim Neuladen nicht mehr Mitglied ist, verliert seine Einträge in den
// Überlagerungen: Wird er später wieder aufgenommen, gelten die Werte aus
// der Konfiguration.
func TestReloadDropsRemovedMembersFromOverlays(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	if code := e.setRole(userA, emailB, true, true); code != http.StatusOK {
		t.Fatalf("Bob zum Admin: %d", code)
	}
	cfgPath := filepath.Join(t.TempDir(), "koordinator.yaml")
	write := func(members string) {
		t.Helper()
		writeFile(t, cfgPath, []byte("auth: {mode: dev}\nmembers:\n"+members))
		if err := e.srv.ReloadMembers(t.Context(), cfgPath); err != nil {
			t.Fatal(err)
		}
	}
	write("  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}\n")
	if got := readFile(t, filepath.Join(e.dir, config.RolesFile)); strings.Contains(got, "bob") {
		t.Fatalf("roles.json nennt Bob nach dem Entfernen noch: %s", got)
	}
	write("  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}\n  - {email: bob@example.org, minecraft: Bob}\n")
	if me := e.me(userB); me.Admin || me.Op {
		t.Errorf("Bob nach Wiederaufnahme: %+v, want ohne Rollen", me)
	}
}

// Eine kaputte roles.json gilt nicht: Fehler im Log, die Rollen aus der
// Konfiguration gelten, die Datei bleibt liegen – bis zur nächsten
// Änderung, die sie als .kaputt-<Zeit> beiseitelegt.
func TestRolesCorrupt(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"kein JSON", `{"bob@example.org": {"admin": tr`},
		{"unbekanntes Feld", `{"bob@example.org": {"admin": true, "farbe": "blau"}}`},
		{"doppelte E-Mail", `{"bob@example.org": {"admin": true}, "Bob@example.org": {"op": true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rolesPath := filepath.Join(dir, config.RolesFile)
			writeFile(t, rolesPath, []byte(tc.content))
			logs := captureLog(t)
			e := newMemberEnv(t, dir, gib)

			if l := logs.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "ÜBERLAGERUNG KAPUTT") || !strings.Contains(l, "roles.json") {
				t.Errorf("kein Fehler im Log:\n%s", l)
			}
			if me := e.me(userB); me.Admin {
				t.Errorf("Bob ist Admin aus kaputter roles.json: %+v", me)
			}
			if me := e.me(userA); !me.Admin || !me.Op {
				t.Errorf("Alice: %+v, want Admin und Op aus der Konfiguration", me)
			}
			if got := readFile(t, rolesPath); got != tc.content {
				t.Fatalf("kaputte roles.json verändert: %q", got)
			}

			// Neuladen ändert daran nichts.
			cfgPath := filepath.Join(t.TempDir(), "koordinator.yaml")
			writeFile(t, cfgPath, []byte("auth: {mode: dev}\nmembers:\n  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}\n  - {email: bob@example.org, minecraft: Bob}\n  - {email: carol@example.org}\n"))
			if err := e.srv.ReloadMembers(t.Context(), cfgPath); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, rolesPath); got != tc.content {
				t.Fatalf("kaputte roles.json beim Neuladen verändert: %q", got)
			}

			// Die nächste Änderung legt sie beiseite.
			if code := e.setRole(userA, emailB, false, true); code != http.StatusOK {
				t.Fatalf("Bob zum Op: %d", code)
			}
			kaputt := rolesPath + ".kaputt-20261122T120000Z"
			if got := readFile(t, kaputt); got != tc.content {
				t.Errorf("Kopie %s: %q", kaputt, got)
			}
			if _, err := config.LoadRoles(rolesPath); err != nil {
				t.Errorf("neue roles.json: %v", err)
			}
			if me := e.me(userB); !me.Op || me.Admin {
				t.Errorf("Bob: %+v, want Op", me)
			}
		})
	}
}

// Nur Admins sehen die Mitgliederliste und ändern Rollen, fremde Namen und
// das Speicherbudget.
func TestMembersNotAdmin(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/members", nil},
		{http.MethodPut, "/v1/members/" + e.key(emailB), api.RoleRequest{Admin: true, Op: true}},
		{http.MethodPut, "/v1/members/" + e.key(emailC) + "/minecraft", api.MinecraftNameRequest{MinecraftName: "Carol"}},
		{http.MethodPut, "/v1/storage/budget", api.BudgetRequest{BudgetBytes: 2 * gib}},
	} {
		if code := call(t, e.base, userB, c.method, c.path, c.body, nil); code != http.StatusForbidden {
			t.Errorf("Bob %s %s: %d, want 403", c.method, c.path, code)
		}
		if code := call(t, e.base, stranger, c.method, c.path, c.body, nil); code != http.StatusForbidden {
			t.Errorf("Fremder %s %s: %d, want 403", c.method, c.path, code)
		}
	}
	for _, f := range []string{config.RolesFile, config.NamesFile, config.SettingsFile} {
		if got := readFile(t, filepath.Join(e.dir, f)); got != "<fehlt>" {
			t.Errorf("%s entstanden: %s", f, got)
		}
	}
}

// MembersVersion steht in jedem Lebenszeichen: beim Start die Startzeit in
// Nanosekunden, +1 bei jedem erfolgreichen Neuladen, Rollen- oder
// Namenswechsel; Ablehnungen und gescheitertes Neuladen ändern sie nicht.
func TestMembersVersionChanges(t *testing.T) {
	start := time.Now().UnixNano()
	e := newMemberEnv(t, t.TempDir(), gib)
	end := time.Now().UnixNano()
	l := e.host()
	v := e.heartbeat(l).MembersVersion
	if v < start || v > end {
		t.Fatalf("Version %d nicht die Startzeit (%d…%d)", v, start, end)
	}
	step := func(what string, wantDelta int64) {
		t.Helper()
		got := e.heartbeat(l).MembersVersion
		if got != v+wantDelta {
			t.Fatalf("%s: Version %d, want %d", what, got, v+wantDelta)
		}
		v = got
	}
	step("nichts geändert", 0)

	cfgPath := filepath.Join(t.TempDir(), "koordinator.yaml")
	writeFile(t, cfgPath, []byte("auth: {mode: dev}\nmembers:\n  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}\n  - {email: bob@example.org, minecraft: Bob}\n  - {email: carol@example.org}\n"))
	if err := e.srv.ReloadMembers(t.Context(), cfgPath); err != nil {
		t.Fatal(err)
	}
	step("Neuladen", 1)
	writeFile(t, cfgPath, []byte("auth: {mode: dev}\nfarbe: blau\n"))
	if err := e.srv.ReloadMembers(t.Context(), cfgPath); err == nil {
		t.Fatal("ungültige Datei geladen")
	}
	step("gescheitertes Neuladen", 0)

	if code := e.setRole(userA, emailB, false, true); code != http.StatusOK {
		t.Fatal(code)
	}
	step("Op für Bob", 1)
	if code := e.setRole(userA, emailB, false, true); code != http.StatusOK {
		t.Fatal(code)
	}
	step("dieselben Rollen noch einmal", 0)
	if code := e.setRole(userA, emailA, false, false); code != http.StatusConflict {
		t.Fatal(code)
	}
	step("abgelehnter Rollenwechsel", 0)

	if code := call(t, e.base, userC, http.MethodPost, "/v1/me/minecraft", api.MinecraftNameRequest{MinecraftName: "Carol"}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	step("Carol meldet ihren Namen", 1)
	if code := call(t, e.base, userA, http.MethodPut, "/v1/members/"+e.key(emailB)+"/minecraft", api.MinecraftNameRequest{MinecraftName: "Caro"}, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	step("Admin setzt Bobs Namen", 1)
}

// Ein Mitglied ohne Namen meldet seinen Minecraft-Namen genau einmal; danach
// steht er in /v1/me, in der Whitelist und in der Mitgliederliste. Ein
// zweites Mal: 409.
func TestMeMinecraftOnce(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	e.host()
	whitelist := func() []string {
		var names []string
		for _, p := range e.hostSettings(userA).Whitelist {
			names = append(names, p.Name)
		}
		return names
	}
	if got := whitelist(); !slices.Equal(got, []string{"Alice", "Bob"}) {
		t.Fatalf("Whitelist ohne Carol: %v", got)
	}
	if me := e.me(userC); me.MinecraftName != "" {
		t.Fatalf("Carol hat schon einen Namen: %+v", me)
	}

	var me api.Me
	if code := call(t, e.base, userC, http.MethodPost, "/v1/me/minecraft", api.MinecraftNameRequest{MinecraftName: " Carol "}, &me); code != http.StatusOK {
		t.Fatalf("Name melden: %d", code)
	}
	if me.MinecraftName != "Carol" || me.Email != emailC {
		t.Errorf("Antwort %+v", me)
	}
	if got := e.me(userC).MinecraftName; got != "Carol" {
		t.Errorf("/v1/me: %q", got)
	}
	if got := whitelist(); !slices.Equal(got, []string{"Alice", "Bob", "Carol"}) {
		t.Errorf("Whitelist: %v", got)
	}
	namesPath := filepath.Join(e.dir, config.NamesFile)
	checkMode600(t, namesPath)
	before := readFile(t, namesPath)

	if code := call(t, e.base, userC, http.MethodPost, "/v1/me/minecraft", api.MinecraftNameRequest{MinecraftName: "Caro"}, nil); code != http.StatusConflict {
		t.Errorf("zweites Melden: %d, want 409", code)
	}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/me/minecraft", api.MinecraftNameRequest{MinecraftName: "Caro"}, nil); code != http.StatusConflict {
		t.Errorf("Bob mit Namen aus der Konfiguration: %d, want 409", code)
	}
	if after := readFile(t, namesPath); after != before {
		t.Errorf("names.json nach 409 geändert: %s", after)
	}
	// Ein Neustart behält den Namen.
	e2 := newMemberEnv(t, e.dir, gib)
	if got := e2.me(userC).MinecraftName; got != "Carol" {
		t.Errorf("nach dem Neustart: %q", got)
	}
}

// Abgelehnt mit 400: ein Name, den schon ein anderes Mitglied hat (Groß/Klein
// egal), den Mojang nicht kennt oder der ungültig ist; 503, wenn Mojang
// nicht antwortet. Nichts wird geschrieben.
func TestMeMinecraftRejectsTaken(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	for _, c := range []struct {
		name string
		want int
	}{
		{"bob", http.StatusBadRequest},
		{"Alice", http.StatusBadRequest},
		{"Unbekannt", http.StatusBadRequest},
		{"Carol-1", http.StatusBadRequest},
		{"", http.StatusBadRequest},
		{"Stoerung", http.StatusServiceUnavailable},
	} {
		if code := call(t, e.base, userC, http.MethodPost, "/v1/me/minecraft", api.MinecraftNameRequest{MinecraftName: c.name}, nil); code != c.want {
			t.Errorf("%q: %d, want %d", c.name, code, c.want)
		}
	}
	if got := readFile(t, filepath.Join(e.dir, config.NamesFile)); got != "<fehlt>" {
		t.Errorf("names.json entstanden: %s", got)
	}
	if got := e.me(userC).MinecraftName; got != "" {
		t.Errorf("Carol hat jetzt %q", got)
	}
}

// Ein Admin setzt Namen jederzeit, auch über einen aus der Konfiguration;
// ein vom Mitglied selbst gemeldeter Name weicht dagegen einem Namen, der
// später in der Konfiguration steht.
func TestAdminSetsMinecraft(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	var info api.MemberInfo
	if code := call(t, e.base, userA, http.MethodPut, "/v1/members/"+e.key("BOB@example.org")+"/minecraft", api.MinecraftNameRequest{MinecraftName: "Caro"}, &info); code != http.StatusOK {
		t.Fatalf("Admin setzt Bobs Namen: %d", code)
	}
	if info.ID != e.key(emailB) || info.Email != info.ID || info.MinecraftName != "Caro" {
		t.Errorf("Antwort %+v", info)
	}
	if got := e.me(userB).MinecraftName; got != "Caro" {
		t.Errorf("Bob heißt %q, want Caro", got)
	}
	if code := call(t, e.base, userA, http.MethodPut, "/v1/members/"+e.key(emailC)+"/minecraft", api.MinecraftNameRequest{MinecraftName: "caro"}, nil); code != http.StatusBadRequest {
		t.Errorf("Name von Bob für Carol: %d, want 400", code)
	}
	if code := call(t, e.base, userA, http.MethodPut, "/v1/members/"+e.key(emailC)+"/minecraft", api.MinecraftNameRequest{MinecraftName: "Carol"}, nil); code != http.StatusOK {
		t.Errorf("Carols Name: %d", code)
	}
	if code := call(t, e.base, userA, http.MethodPut, "/v1/members/"+e.key("niemand@example.org")+"/minecraft", api.MinecraftNameRequest{MinecraftName: "Bob"}, nil); code != http.StatusNotFound {
		t.Errorf("unbekanntes Mitglied: %d, want 404", code)
	}
	// Zurück auf den Namen aus der Konfiguration: Bobs Eintrag fällt weg.
	if code := call(t, e.base, userA, http.MethodPut, "/v1/members/"+e.key(emailB)+"/minecraft", api.MinecraftNameRequest{MinecraftName: "Bob"}, nil); code != http.StatusOK {
		t.Errorf("Bob zurück: %d", code)
	}
	names, err := config.LoadNames(filepath.Join(e.dir, config.NamesFile))
	if err != nil {
		t.Fatal(err)
	}
	want := config.Names{emailC: {Minecraft: "Carol", ByAdmin: true}}
	if len(names) != 1 || names[emailC] != want[emailC] {
		t.Errorf("names.json: %+v, want %+v", names, want)
	}

	// Der Admin-Name gilt vor der Konfiguration, ein selbst gemeldeter nicht.
	if got := config.ApplyNames([]config.Member{{Email: emailC, Minecraft: "AusDerKonfig"}}, names); got[0].Minecraft != "Carol" {
		t.Errorf("Admin-Name vor der Konfiguration: %q", got[0].Minecraft)
	}
	self := config.Names{emailC: {Minecraft: "Carol"}}
	if got := config.ApplyNames([]config.Member{{Email: emailC, Minecraft: "AusDerKonfig"}}, self); got[0].Minecraft != "AusDerKonfig" {
		t.Errorf("selbst gemeldeter Name vor der Konfiguration: %q", got[0].Minecraft)
	}
}

// Ein neues Budget landet in settings.json, gilt für storageInfo und das
// Aufräumen (gleich danach im Hintergrund) und übersteht einen Neustart.
func TestBudgetOverlay(t *testing.T) {
	dir := t.TempDir()
	e := newMemberEnv(t, dir, gib)
	e.srv.SetDiskFree(func(string) int64 { return 100 * gib })

	// Ein Stand und ein Blob, den keiner nennt: Das Aufräumen nach dem
	// neuen Budget entfernt ihn.
	data := []byte("hallo wanderwelt")
	orphan := []byte("niemand braucht mich")
	if code := putPart(t, e.base, userA, shaHex(data), "bytes 0-15/16", data); code != http.StatusCreated {
		t.Fatalf("Blob: %d", code)
	}
	if code := putPart(t, e.base, userA, shaHex(orphan), fmt.Sprintf("bytes 0-%d/%d", len(orphan)-1, len(orphan)), orphan); code != http.StatusCreated {
		t.Fatalf("Waise: %d", code)
	}
	l := e.host()
	req := api.CommitRequest{LeaseID: l, MachineID: "pc-a", MinecraftVersion: "26.2",
		Files: []api.FileEntry{{Path: "level.dat", Size: 16, SHA256: shaHex(data)}}}
	if code := call(t, e.base, userA, http.MethodPost, "/v1/generations", req, nil); code != http.StatusOK {
		t.Fatalf("Commit: %d", code)
	}

	// Solange die Belegung nicht berechnet ist (beim Start: RefreshUsage):
	// 503, nichts geschrieben.
	settings := filepath.Join(dir, config.SettingsFile)
	if code := call(t, e.base, userA, http.MethodPut, "/v1/storage/budget", api.BudgetRequest{BudgetBytes: 20 * gib}, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("Budget vor der Belegung: %d, want 503", code)
	}
	if got := readFile(t, settings); got != "<fehlt>" {
		t.Fatalf("settings.json trotz 503: %s", got)
	}
	if err := e.st.RefreshUsage(e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	var info api.StorageInfo
	if code := call(t, e.base, userA, http.MethodPut, "/v1/storage/budget", api.BudgetRequest{BudgetBytes: 20 * gib}, &info); code != http.StatusOK {
		t.Fatalf("Budget setzen: %d", code)
	}
	if info.BudgetBytes != 20*gib || info.DiskFreeBytes != 100*gib || info.OverBudget {
		t.Errorf("Antwort %+v", info)
	}
	if got := readFile(t, settings); !strings.Contains(got, `"storage_budget_bytes": 21474836480`) {
		t.Errorf("settings.json: %s", got)
	}
	checkMode600(t, settings)
	e.srv.Wait()
	if b := e.st.Budget(); b != 20*gib {
		t.Errorf("Budget des Speichers: %d", b)
	}
	if code := call(t, e.base, userA, http.MethodGet, "/v1/blobs/"+shaHex(orphan), nil, nil); code != http.StatusNotFound {
		t.Errorf("Waise nach dem Aufräumen: %d, want 404 (kein Aufräumen nach dem neuen Budget?)", code)
	}
	var gens api.GenerationsResponse
	if code := call(t, e.base, userB, http.MethodGet, "/v1/generations", nil, &gens); code != http.StatusOK {
		t.Fatal(code)
	}
	if s := gens.Storage; s == nil || s.BudgetBytes != 20*gib || s.UsedBytes != 16 || s.OverBudget {
		t.Errorf("Stände-Liste: %+v", gens.Storage)
	}

	// Neustart: Die Konfiguration sagt 1 GiB, settings.json gilt.
	e2 := newMemberEnv(t, dir, gib)
	if b := e2.st.Budget(); b != 20*gib {
		t.Errorf("Budget nach dem Neustart: %d", b)
	}
	if code := call(t, e2.base, userB, http.MethodGet, "/v1/generations", nil, &gens); code != http.StatusOK || gens.Storage.BudgetBytes != 20*gib {
		t.Errorf("Stände-Liste nach dem Neustart: %d %+v", code, gens.Storage)
	}
}

// Unter 1 GiB oder über belegt + frei: 400, settings.json bleibt; ein
// kaputtes settings.json gilt nicht und wird beim nächsten Setzen
// beiseitegelegt.
func TestBudgetRejects(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, config.SettingsFile)
	writeFile(t, settings, []byte(`{"storage_budget_bytes": "viel"}`))
	logs := captureLog(t)
	e := newMemberEnv(t, dir, gib)
	if !strings.Contains(logs.String(), "settings.json") {
		t.Errorf("kaputtes settings.json nicht im Log:\n%s", logs.String())
	}
	if b := e.st.Budget(); b != gib {
		t.Fatalf("Budget aus kaputtem settings.json: %d", b)
	}
	e.srv.SetDiskFree(func(string) int64 { return 5 * gib })
	if err := e.st.RefreshUsage(e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	for _, b := range []int64{gib / 2, 0, -1, 5*gib + 1} {
		if code := call(t, e.base, userA, http.MethodPut, "/v1/storage/budget", api.BudgetRequest{BudgetBytes: b}, nil); code != http.StatusBadRequest {
			t.Errorf("Budget %d: %d, want 400", b, code)
		}
	}
	if code := call(t, e.base, userB, http.MethodPut, "/v1/storage/budget", api.BudgetRequest{BudgetBytes: 2 * gib}, nil); code != http.StatusForbidden {
		t.Errorf("Nicht-Admin: %d, want 403", code)
	}
	if got := readFile(t, settings); got != `{"storage_budget_bytes": "viel"}` {
		t.Fatalf("settings.json nach Ablehnungen: %s", got)
	}
	if code := call(t, e.base, userA, http.MethodPut, "/v1/storage/budget", api.BudgetRequest{BudgetBytes: 5 * gib}, nil); code != http.StatusOK {
		t.Fatalf("Budget = belegt + frei: %d", code)
	}
	if got := readFile(t, settings+".kaputt-20261122T120000Z"); got != `{"storage_budget_bytes": "viel"}` {
		t.Errorf("Kopie des kaputten settings.json: %s", got)
	}
	if s, err := config.LoadSettings(settings); err != nil || s.StorageBudgetBytes != 5*gib {
		t.Errorf("settings.json: %+v %v", s, err)
	}
}

// Bliebe mit roles.json kein Admin übrig – nach dem Neuladen oder beim
// Start –, gilt roles.json nicht: Fehler im Log, die Rollen aus der
// Konfiguration gelten, die Datei bleibt liegen.
func TestRolesNeverWithoutAdmin(t *testing.T) {
	dir := t.TempDir()
	logs := captureLog(t) // der Server nimmt das Log beim Bauen
	e := newMemberEnv(t, dir, gib)
	rolesPath := filepath.Join(dir, config.RolesFile)
	// Bob wird Admin, Alice (einzige Admin laut Konfiguration) nicht mehr.
	if code := e.setRole(userA, emailB, true, false); code != http.StatusOK {
		t.Fatal(code)
	}
	if code := e.setRole(userB, emailA, false, true); code != http.StatusOK {
		t.Fatal(code)
	}
	// Bob fliegt aus der Konfiguration: Sein Eintrag fällt weg, und
	// roles.json ließe keinen Admin übrig.
	if strings.Contains(logs.String(), "KEINEN ADMIN") {
		t.Fatalf("Fehler schon vor dem Neuladen:\n%s", logs.String())
	}
	cfgPath := filepath.Join(t.TempDir(), "koordinator.yaml")
	writeFile(t, cfgPath, []byte("auth: {mode: dev}\nmembers:\n  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}\n  - {email: carol@example.org}\n"))
	if err := e.srv.ReloadMembers(t.Context(), cfgPath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "KEINEN ADMIN") {
		t.Errorf("kein Fehler im Log:\n%s", logs.String())
	}
	if me := e.me(userA); !me.Admin || !me.Op {
		t.Errorf("Alice nach dem Neuladen: %+v, want Admin und Op aus der Konfiguration", me)
	}
	stale := readFile(t, rolesPath)
	if !strings.Contains(stale, "alice@example.org") {
		t.Fatalf("roles.json: %s", stale)
	}

	// Beim Start ebenso (von Hand geänderte Datei).
	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, config.RolesFile), []byte(`{"alice@example.org": {"admin": false, "op": true}}`))
	logs2 := captureLog(t)
	e2 := newMemberEnv(t, dir2, gib)
	if !strings.Contains(logs2.String(), "KEINEN ADMIN") {
		t.Errorf("kein Fehler im Log beim Start:\n%s", logs2.String())
	}
	if me := e2.me(userA); !me.Admin {
		t.Errorf("Alice beim Start: %+v, want Admin", me)
	}
	if got := readFile(t, filepath.Join(dir2, config.RolesFile)); got != `{"alice@example.org": {"admin": false, "op": true}}` {
		t.Errorf("roles.json beim Start verändert: %s", got)
	}
	// Die nächste Rollenänderung legt die Datei beiseite.
	if code := e2.setRole(userA, emailB, false, true); code != http.StatusOK {
		t.Fatal(code)
	}
	if got := readFile(t, filepath.Join(dir2, config.RolesFile)+".kaputt-20261122T120000Z"); !strings.Contains(got, "alice") {
		t.Errorf("Kopie: %s", got)
	}
	if me := e2.me(userA); !me.Admin {
		t.Errorf("Alice nach der Änderung: %+v", me)
	}
}

// key ist der Schlüssel für Pfade der Admin-Aktionen: die Kennung (eine
// Adresse nimmt die API nur für die eigene an).
func (e *memberEnv) key(email string) string { return e.srv.IDOf(email) }
