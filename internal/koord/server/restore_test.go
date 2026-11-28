package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

// manifestMC ist die Minecraft-Version im Versions-Manifest der Tests.
const manifestMC = "26.2"

// restoreEnv ist ein Koordinator mit Versions-Manifest, dessen Zeit der
// falschen Uhr folgt, und den Blobs für Stände.
type restoreEnv struct {
	t     *testing.T
	srv   *server.Server
	clk   *fakeClock
	lm    *lease.Manager
	ph    *fakePortHolder
	base  string
	dir   string // DataDir
	data  string // Blob "hallo wanderwelt"
	empty string // leerer Blob
}

func newRestoreEnv(t *testing.T) *restoreEnv {
	t.Helper()
	return newRestoreEnvWith(t, nil)
}

// newRestoreEnvWith ist newRestoreEnv; tweak darf die Konfiguration vor dem
// Start ändern.
func newRestoreEnvWith(t *testing.T, tweak func(*config.Config)) *restoreEnv {
	t.Helper()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.yaml")
	writeFile(t, manifest, []byte("minecraft_version: \""+manifestMC+"\"\n"))
	cfg := &config.Config{
		DataDir:          dir,
		MinecraftAddress: "mc.example.org:15565",
		StorageBudget:    1 << 30,
		SnapshotInterval: 5 * time.Minute,
		ManifestFile:     manifest,
		Members: []config.Member{
			{Email: emailA, Minecraft: "Alice", Admin: true, Op: true},
			{Email: emailB, Minecraft: "Bob"},
		},
	}
	cfg.Auth.Mode = "dev"
	cfg.PortHolder.Port = mcPort
	if tweak != nil {
		tweak(cfg)
	}

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
	srv := server.New(cfg, lm, st, ph, fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
	srv.SetNow(clk.Now)
	srv.ExpireTick(t.Context())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	e := &restoreEnv{t: t, srv: srv, clk: clk, lm: lm, ph: ph, base: ts.URL, dir: dir}
	data := []byte("hallo wanderwelt")
	e.data, e.empty = shaHex(data), shaHex(nil)
	if code := putPart(t, ts.URL, userA, e.data, "bytes 0-15/16", data); code != http.StatusCreated {
		t.Fatalf("Blob: %d", code)
	}
	if code := putPart(t, ts.URL, userA, e.empty, "bytes */0", nil); code != http.StatusCreated {
		t.Fatalf("leerer Blob: %d", code)
	}
	return e
}

// files ist der Inhalt von Stand k: level.dat und eine eigene Datei.
func (e *restoreEnv) files(k int) []api.FileEntry {
	return []api.FileEntry{
		{Path: "data/stand-" + fmt.Sprint(k) + ".dat", Size: 0, SHA256: e.empty},
		{Path: "level.dat", Size: 16, SHA256: e.data},
	}
}

// host lässt as hosten und liefert die LeaseID.
func (e *restoreEnv) host(as who, machine string) int64 {
	e.t.Helper()
	r := playElection(e.t, e.base, e.clk, as, api.PlayRequest{MachineID: machine})
	if r.status != http.StatusOK || r.resp.Role != api.RoleHost {
		e.t.Fatalf("%s hostet nicht: %d %+v", as.email, r.status, r.resp)
	}
	return r.resp.LeaseID
}

// commit reicht Stand k auf dem Head ein (newWorld: als neue Welt).
func (e *restoreEnv) commit(as who, machine string, leaseID int64, k int, newWorld bool, version string) api.Generation {
	e.t.Helper()
	st := getStatus(e.t, e.base, as)
	req := api.CommitRequest{
		LeaseID: leaseID, MachineID: machine,
		BaseWorldID: st.WorldID, BaseGeneration: st.HeadGeneration, NewWorld: newWorld,
		MinecraftVersion: version, Files: e.files(k), Players: []string{"Alice", "Bob"},
	}
	var g api.Generation
	if code := call(e.t, e.base, as, http.MethodPost, "/v1/generations", req, &g); code != http.StatusOK {
		e.t.Fatalf("Commit von Stand %d: %d", k, code)
	}
	return g
}

func (e *restoreEnv) release(as who, machine string, leaseID int64) {
	e.t.Helper()
	if code := call(e.t, e.base, as, http.MethodPost, "/v1/lease/release",
		api.ReleaseRequest{LeaseID: leaseID, MachineID: machine}, nil); code != http.StatusNoContent {
		e.t.Fatalf("Rückgabe: %d", code)
	}
}

// seed: A hostet, reicht n Stände ein und gibt den Zuschlag zurück.
func (e *restoreEnv) seed(n int) []api.Generation {
	e.t.Helper()
	l := e.host(userA, "pc-a")
	var gens []api.Generation
	for k := 1; k <= n; k++ {
		gens = append(gens, e.commit(userA, "pc-a", l, k, false, manifestMC))
	}
	e.release(userA, "pc-a", l)
	return gens
}

func (e *restoreEnv) restore(as who, target api.GenRef) (int, api.RestoreResponse) {
	e.t.Helper()
	var resp api.RestoreResponse
	code := call(e.t, e.base, as, http.MethodPost, "/v1/restore", api.RestoreRequest{WorldID: target.WorldID, Number: target.Number}, &resp)
	return code, resp
}

// restoreError schickt das Zurückholen und liefert Status und Fehlertext.
func (e *restoreEnv) restoreError(as who, target api.GenRef) (int, string) {
	e.t.Helper()
	code, body := e.raw(as, http.MethodPost, "/v1/restore", api.RestoreRequest{WorldID: target.WorldID, Number: target.Number})
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		e.t.Fatalf("Antwort %q: %v", body, err)
	}
	return code, out.Error
}

// raw schickt in als JSON und liefert Status und Körper.
func (e *restoreEnv) raw(as who, method, path string, in any) (int, []byte) {
	e.t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(method, e.base+path, strings.NewReader(string(b)))
	if err != nil {
		e.t.Fatal(err)
	}
	setIdentity(req, as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func (e *restoreEnv) heartbeat(as who, machine string, leaseID int64) api.HeartbeatResponse {
	e.t.Helper()
	return e.beat(as, machine, leaseID, false)
}

// beat schickt ein Lebenszeichen mit Bereitschaft ready (Server läuft).
func (e *restoreEnv) beat(as who, machine string, leaseID int64, ready bool) api.HeartbeatResponse {
	e.t.Helper()
	var hb api.HeartbeatResponse
	if code := call(e.t, e.base, as, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: leaseID, MachineID: machine, Ready: ready}, &hb); code != http.StatusOK {
		e.t.Fatalf("Lebenszeichen: %d", code)
	}
	return hb
}

func (e *restoreEnv) head() api.Generation {
	e.t.Helper()
	var g api.Generation
	if code := call(e.t, e.base, userB, http.MethodGet, "/v1/generations/head", nil, &g); code != http.StatusOK {
		e.t.Fatalf("Head: %d", code)
	}
	return g
}

// play: as spielt ohne Sammelfenster (es gibt einen Host oder es wird
// abgelehnt). Öffnet die Anfrage doch ein Sammelfenster, schließt play es
// über die Uhr (sonst hinge der Server beim Beenden des Tests) und schlägt
// fehl.
func (e *restoreEnv) play(as who, machine string) api.PlayResponse {
	e.t.Helper()
	ch := make(chan playResult, 1)
	go func() {
		var r playResult
		r.status = call(e.t, e.base, as, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: machine}, &r.resp)
		ch <- r
	}()
	select {
	case r := <-ch:
		if r.status != http.StatusOK {
			e.t.Fatalf("Play: %d", r.status)
		}
		return r.resp
	case <-time.After(5 * time.Second):
		e.clk.Advance(lease.Window)
		<-ch
		e.t.Fatalf("Play von %s ging in ein Sammelfenster statt sofort zu antworten", as.email)
		return api.PlayResponse{}
	}
}

func ref(g api.Generation) api.GenRef { return api.GenRef{WorldID: g.WorldID, Number: g.Number} }

// checkRestored prüft, dass der Head ein Zurückholen von target auf parent
// ist: neue Nummer parent+1, Inhalt von target, RestoredFrom, Admin A.
func checkRestored(t *testing.T, head api.Generation, target api.Generation, parent int64) {
	t.Helper()
	if head.WorldID != target.WorldID || head.Number != parent+1 || head.Parent != parent ||
		head.RestoredFrom == nil || *head.RestoredFrom != ref(target) ||
		// Der Head geht an Bob: Alices Adresse fehlt, ihre Kennung steht da.
		!slices.Equal(head.Files, target.Files) || head.HostEmail != "" || head.HostID == "" {
		t.Fatalf("Head nach dem Zurückholen: %+v (RestoredFrom %+v), erwartet Inhalt von %s/%d auf %d",
			head, head.RestoredFrom, target.WorldID, target.Number, parent)
	}
}

// Ohne Zuschlag wird sofort zurückgeholt: neuer Head mit dem Inhalt des
// Ziels; der Status nennt Zeit des Heads und Abstand der Zwischenstände.
func TestRestoreWithoutHost(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(3)

	code, resp := e.restore(userA, ref(gens[0]))
	if code != http.StatusOK || !resp.Applied || resp.Pending || resp.New == nil ||
		*resp.New != (api.GenRef{WorldID: gens[0].WorldID, Number: 4}) {
		t.Fatalf("Zurückholen: %d %+v (New %+v)", code, resp, resp.New)
	}
	head := e.head()
	checkRestored(t, head, gens[0], 3)

	st := getStatus(t, e.base, userB)
	if st.HeadGeneration != 4 || !st.HeadCreated.Equal(head.Created) || st.HeadCreated.IsZero() ||
		st.SnapshotIntervalS != 300 || st.RestorePending != nil {
		t.Fatalf("Status: %+v", st)
	}
}

// Mit Host steht das Zurückholen aus: Das Lebenszeichen des Hosts trägt die
// Ansage, sein Stand auf dem Head wird noch angenommen, und erst nach der
// Rückgabe wird zurückgeholt – auf seinem Schluss-Stand.
func TestRestoreWithHost(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	l := e.host(userB, "pc-b")
	requested := e.clk.Now()

	code, resp := e.restore(userA, ref(gens[0]))
	if code != http.StatusOK || resp.Applied || !resp.Pending || resp.New != nil {
		t.Fatalf("Zurückholen mit Host: %d %+v", code, resp)
	}
	if h := e.head(); h.Number != 2 {
		t.Fatalf("Head vor der Rückgabe: %d", h.Number)
	}
	want := api.RestoreNotice{By: "Alice", Target: ref(gens[0]), TargetCreated: gens[0].Created, RequestedAt: requested, DelayS: 30}
	if st := getStatus(t, e.base, userB); st.RestorePending == nil || !sameNotice(*st.RestorePending, want) {
		t.Fatalf("Status: ausstehend %+v, erwartet %+v", st.RestorePending, want)
	}
	hb := e.heartbeat(userB, "pc-b", l)
	if hb.Restore == nil || !sameNotice(*hb.Restore, want) {
		t.Fatalf("Lebenszeichen: %+v, erwartet %+v", hb.Restore, want)
	}

	// Der Schluss-Stand des Hosts auf dem Head wird angenommen.
	final := e.commit(userB, "pc-b", l, 3, false, manifestMC)
	if final.Number != 3 {
		t.Fatalf("Schluss-Stand: %+v", final)
	}
	e.release(userB, "pc-b", l)

	checkRestored(t, e.head(), gens[0], 3)
	if st := getStatus(t, e.base, userB); st.RestorePending != nil {
		t.Fatalf("nach der Rückgabe noch ausstehend: %+v", st.RestorePending)
	}
}

func sameNotice(a, b api.RestoreNotice) bool {
	return a.By == b.By && a.Target == b.Target && a.TargetCreated.Equal(b.TargetCreated) &&
		a.RequestedAt.Equal(b.RequestedAt) && a.DelayS == b.DelayS
}

// Stürzt der Host ab, wird beim Ablauf seines Zuschlags zurückgeholt – im
// Takt oder spätestens bei der nächsten Spielanfrage –, ebenso, wenn sein
// neu gestarteter Starter vom selben PC wieder spielen will. Danach kann
// wieder jemand hosten, und es wird genau einmal zurückgeholt.
func TestRestoreHostExpires(t *testing.T) {
	for _, mode := range []string{"Takt", "Spielanfrage", "selber PC"} {
		t.Run(mode, func(t *testing.T) {
			e := newRestoreEnv(t)
			gens := e.seed(2)
			e.host(userB, "pc-b")
			if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
				t.Fatalf("Zurückholen: %d %+v", code, resp)
			}

			next := userA
			if mode == "selber PC" {
				next = userB // Zuschlag noch gültig; releaseStale gibt ihn frei
			} else {
				e.clk.Advance(lease.LeaseTTL + time.Second)
			}
			if mode == "Takt" {
				e.srv.ExpireTick(t.Context())
				checkRestored(t, e.head(), gens[0], 2)
				if st := getStatus(t, e.base, userB); st.RestorePending != nil {
					t.Fatalf("nach dem Ablauf noch ausstehend: %+v", st.RestorePending)
				}
			}

			machine := map[who]string{userA: "pc-a", userB: "pc-b"}[next]
			r := playElection(t, e.base, e.clk, next, api.PlayRequest{MachineID: machine})
			if r.status != http.StatusOK || r.resp.Role != api.RoleHost || r.resp.HeadGeneration != 3 {
				t.Fatalf("Play danach: %d %+v", r.status, r.resp)
			}
			checkRestored(t, e.head(), gens[0], 2)
			e.srv.ExpireTick(t.Context())
			if h := e.head(); h.Number != 3 {
				t.Fatalf("doppelt zurückgeholt: Head %d", h.Number)
			}
		})
	}
}

// Solange ein Zurückholen aussteht, bekommen Spielanfragen anderer
// „denied“, und ein zweites Zurückholen wird abgelehnt.
func TestRestorePendingBlocksPlay(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	l := e.host(userA, "pc-a")
	if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}

	for _, as := range []who{userB, userA} {
		resp := e.play(as, "pc-x")
		if resp.Role != api.RoleDenied || resp.Reason != "Stand wird gerade zurückgeholt, gleich nochmal" {
			t.Fatalf("Play von %s: %+v", as.email, resp)
		}
	}
	if code, msg := e.restoreError(userA, ref(gens[1])); code != http.StatusConflict || msg == "" {
		t.Fatalf("zweites Zurückholen: %d %q", code, msg)
	}

	e.release(userA, "pc-a", l)
	checkRestored(t, e.head(), gens[0], 2)
	if r := playElection(t, e.base, e.clk, userB, api.PlayRequest{MachineID: "pc-b"}); r.status != http.StatusOK || r.resp.Role != api.RoleHost {
		t.Fatalf("Play nach dem Zurückholen: %d %+v", r.status, r.resp)
	}
}

func TestRestoreNotAdmin(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	if code, msg := e.restoreError(userB, ref(gens[0])); code != http.StatusForbidden || msg == "" {
		t.Fatalf("Zurückholen durch Nicht-Admin: %d %q", code, msg)
	}
	if h := e.head(); h.Number != 2 || h.RestoredFrom != nil {
		t.Fatalf("Head: %+v", h)
	}
}

// Wie beim Hosten: Ein Stand mit neuerer oder nicht vergleichbarer
// Minecraft-Version (Snapshot) als im Manifest wird abgelehnt, ebenso ein
// unbekannter; der Speicher bleibt unverändert. Ein älterer Stand (vor einem
// Update) geht – hier aus einer archivierten Welt, die dabei wieder aktiv
// wird.
func TestRestoreVersionMismatch(t *testing.T) {
	e := newRestoreEnv(t)
	l := e.host(userA, "pc-a")
	older := e.commit(userA, "pc-a", l, 1, false, "26.1")
	newer := e.commit(userA, "pc-a", l, 2, true, "26.3")
	snapshot := e.commit(userA, "pc-a", l, 3, true, "26w14a")
	cur := e.commit(userA, "pc-a", l, 4, true, manifestMC)
	e.release(userA, "pc-a", l)
	before := e.generations(userA)

	for _, tc := range []struct {
		g      api.Generation
		reason string
	}{{newer, "beschädigen"}, {snapshot, "nicht vergleichen"}} {
		code, msg := e.restoreError(userA, ref(tc.g))
		if code != http.StatusConflict || !strings.Contains(msg, tc.g.MinecraftVersion) || !strings.Contains(msg, manifestMC) ||
			!strings.Contains(msg, tc.reason) {
			t.Fatalf("Stand mit %s: %d %q", tc.g.MinecraftVersion, code, msg)
		}
	}
	if code, msg := e.restoreError(userA, api.GenRef{WorldID: cur.WorldID, Number: 9}); code != http.StatusNotFound || msg == "" {
		t.Fatalf("unbekannter Stand: %d %q", code, msg)
	}
	if code, _ := e.restoreError(userA, api.GenRef{WorldID: "../x", Number: 1}); code != http.StatusBadRequest {
		t.Fatalf("ungültige Welt: %d", code)
	}
	if h := e.head(); h.WorldID != cur.WorldID || h.Number != 1 {
		t.Fatalf("Head: %s/%d", h.WorldID, h.Number)
	}
	after := e.generations(userA)
	after.Storage, before.Storage = nil, nil
	if a, b := mustJSON(t, after), mustJSON(t, before); a != b {
		t.Fatalf("Stände verändert:\nvorher  %s\nnachher %s", b, a)
	}

	// Älterer Stand aus dem Archiv: angewandt, seine Welt ist wieder aktiv.
	code, resp := e.restore(userA, ref(older))
	if code != http.StatusOK || !resp.Applied || resp.New == nil || *resp.New != (api.GenRef{WorldID: older.WorldID, Number: 2}) {
		t.Fatalf("älterer Stand: %d %+v (New %+v)", code, resp, resp.New)
	}
	checkRestored(t, e.head(), older, 1)
	if ws := e.generations(userB).Worlds; len(ws) != 4 || ws[0].WorldID != older.WorldID || !ws[0].Active {
		t.Fatalf("aktive Welt nach dem Zurückholen: %+v", ws)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *restoreEnv) generations(as who) api.GenerationsResponse {
	e.t.Helper()
	var resp api.GenerationsResponse
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/generations", nil, &resp); code != http.StatusOK {
		e.t.Fatalf("Stände-Liste: %d", code)
	}
	return resp
}

// Die Stände-Liste nennt jedem Mitglied alle Welten (aktive zuerst) mit
// Host als Minecraft-Name und die Belegung des Speichers.
func TestGenerationsList(t *testing.T) {
	e := newRestoreEnv(t)
	la := e.host(userA, "pc-a")
	a1 := e.commit(userA, "pc-a", la, 1, false, manifestMC)
	e.clk.Advance(time.Minute)
	a2 := e.commit(userA, "pc-a", la, 2, true, manifestMC)
	e.release(userA, "pc-a", la)
	// Bobs E-Mail steht jetzt anders geschrieben in der Konfiguration (neu
	// geladen); sein Stand trägt sie so, die Liste nennt ihn trotzdem.
	cfgPath := filepath.Join(t.TempDir(), "koordinator.yaml")
	writeFile(t, cfgPath, []byte(`
auth: {mode: dev}
members:
  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}
  - {email: Bob@Example.ORG, minecraft: Bob}
`))
	if err := e.srv.ReloadMembers(t.Context(), cfgPath); err != nil {
		t.Fatal(err)
	}
	lb := e.host(userB, "pc-b")
	b3 := e.commit(userB, "pc-b", lb, 3, false, manifestMC)
	if b3.HostEmail != "Bob@Example.ORG" {
		t.Fatalf("Testannahme: Stand mit E-Mail %q", b3.HostEmail)
	}
	e.release(userB, "pc-b", lb)

	e.srv.PruneTick(t.Context())
	resp := e.generations(userB)
	info := func(g api.Generation, host string) api.GenerationInfo {
		return api.GenerationInfo{WorldID: g.WorldID, Number: g.Number, Created: g.Created, HostName: host,
			Players: []string{"Alice", "Bob"}, Size: 16, MinecraftVersion: manifestMC}
	}
	want := []api.WorldInfo{
		{WorldID: a2.WorldID, Active: true, GroupWorld: "w1", GroupName: "Welt", Generations: []api.GenerationInfo{info(b3, "Bob"), info(a2, "Alice")}},
		{WorldID: a1.WorldID, GroupWorld: "w1", GroupName: "Welt", Generations: []api.GenerationInfo{info(a1, "Alice")}},
	}
	if got, w := mustJSON(t, resp.Worlds), mustJSON(t, want); got != w {
		t.Fatalf("Welten:\n%s\nerwartet\n%s", got, w)
	}
	wantFree := runtime.GOOS == "linux"
	if s := resp.Storage; s == nil || s.UsedBytes != 16 || s.BudgetBytes != 1<<30 || s.OverBudget || (s.DiskFreeBytes > 0) != wantFree {
		t.Fatalf("Speicher: %+v", resp.Storage)
	}
	if code := call(t, e.base, stranger, http.MethodGet, "/v1/generations", nil, nil); code != http.StatusForbidden {
		t.Fatalf("Nicht-Mitglied: %d", code)
	}

}

// Das Lebenszeichen antwortet 200 mit JSON; alte Starter prüfen nur den
// Status und lesen den Körper nicht.
func TestHeartbeatOldClient(t *testing.T) {
	e := newRestoreEnv(t)
	l := e.host(userA, "pc-a")
	req, err := http.NewRequest(http.MethodPost, e.base+"/v1/lease/heartbeat",
		strings.NewReader(mustJSON(t, api.HeartbeatRequest{LeaseID: l, MachineID: "pc-a"})))
	if err != nil {
		t.Fatal(err)
	}
	setIdentity(req, userA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		t.Fatalf("Status %d: alter Starter hielte das für einen Fehler", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" || !json.Valid(body) {
		t.Fatalf("Antwort %d %q: %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	// Ohne Zurückholen steht nur die Mitglieder-Version darin.
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 1 || fields["members_version"] == nil {
		t.Fatalf("ohne Zurückholen erwartet nur members_version: %q (%v)", body, err)
	}
}

// genExists meldet, ob es Generation n der Welt worldID gibt.
func (e *restoreEnv) genExists(worldID string, n int64) bool {
	e.t.Helper()
	code := call(e.t, e.base, userB, http.MethodGet, fmt.Sprintf("/v1/generations/%s/%d", worldID, n), nil, nil)
	if code != http.StatusOK && code != http.StatusNotFound {
		e.t.Fatalf("Generation %d: %d", n, code)
	}
	return code == http.StatusOK
}

// Das Ziel eines ausstehenden Zurückholens übersteht das Aufräumen; nach
// einer Ablehnung und nach dem Anwenden hält es nichts mehr. Alle Stände
// entstehen in derselben Stunde: Es bleiben jeweils nur die letzten 24.
func TestRestoreTargetPinned(t *testing.T) {
	e := newRestoreEnv(t)
	l := e.host(userA, "pc-a")
	var gens []api.Generation
	commit := func(k int) {
		version := manifestMC
		if k == 1 {
			version = "26.3"
		}
		gens = append(gens, e.commit(userA, "pc-a", l, k, false, version))
	}
	exist := func(phase string, want map[int64]bool) {
		t.Helper()
		for n, w := range want {
			if got := e.genExists(gens[0].WorldID, n); got != w {
				t.Errorf("%s: Stand %d vorhanden = %v, erwartet %v", phase, n, got, w)
			}
		}
	}
	for k := 1; k <= 25; k++ {
		commit(k)
	}

	// Abgelehnt (neuere Version): Stand 1 ist danach nicht gepinnt.
	if code, _ := e.restoreError(userA, ref(gens[0])); code != http.StatusConflict {
		t.Fatalf("Stand 1 mit neuerer Version: %d", code)
	}
	e.srv.PruneTick(t.Context())
	exist("nach Ablehnung", map[int64]bool{1: false, 2: true})

	// Ausstehend: Stand 2 übersteht das Aufräumen, Stand 3 nicht.
	commit(26)
	commit(27)
	if code, resp := e.restore(userA, ref(gens[1])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	e.srv.PruneTick(t.Context())
	exist("ausstehend", map[int64]bool{2: true, 3: false, 4: true})

	// Angewandt: Head 28; danach hält Stand 2 nichts mehr.
	e.release(userA, "pc-a", l)
	checkRestored(t, e.head(), gens[1], 27)
	e.srv.PruneTick(t.Context())
	exist("angewandt", map[int64]bool{2: false, 4: false, 5: true})
}

// Fehlt das Ziel beim Anwenden trotzdem, wird das Zurückholen verworfen,
// und Spielen geht wieder.
func TestRestoreTargetGone(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	l := e.host(userB, "pc-b")
	if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	if err := os.Remove(filepath.Join(e.dir, "store", "worlds", gens[0].WorldID, "generations", "1.json")); err != nil {
		t.Fatal(err)
	}
	e.release(userB, "pc-b", l)
	if h := e.head(); h.Number != 2 || h.RestoredFrom != nil {
		t.Fatalf("Head: %d (RestoredFrom %+v)", h.Number, h.RestoredFrom)
	}
	if st := getStatus(t, e.base, userB); st.RestorePending != nil {
		t.Fatalf("noch ausstehend: %+v", st.RestorePending)
	}
	if r := playElection(t, e.base, e.clk, userA, api.PlayRequest{MachineID: "pc-a"}); r.status != http.StatusOK || r.resp.Role != api.RoleHost {
		t.Fatalf("Play danach: %d %+v", r.status, r.resp)
	}
}

// Endet der Zuschlag, auf den ein Zurückholen wartet, und gilt schon ein
// neuer (Wettlauf mit dem Ablauf; hier ein Zuschlag nur zum Hochladen), wird
// nicht unter dem neuen Host zurückgeholt: Das Zurückholen wartet auf dessen
// Ende, und dessen Lebenszeichen trägt die Ansage.
func TestRestoreWaitsForNewLease(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	e.host(userB, "pc-b")
	if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	e.clk.Advance(lease.LeaseTTL + time.Second)
	g, err := e.lm.Acquire(lease.Candidate{Email: emailA, MinecraftName: "Alice", MachineID: "pc-a", AccountID: idA})
	if err != nil {
		t.Fatal(err)
	}

	// Schon vor dem nächsten Takt erfährt der neue Host davon.
	if hb := e.heartbeat(userA, "pc-a", g.LeaseID); hb.Restore == nil || hb.Restore.Target != ref(gens[0]) {
		t.Fatalf("Lebenszeichen des neuen Zuschlags vor dem Takt: %+v", hb.Restore)
	}
	e.srv.ExpireTick(t.Context())
	if h := e.head(); h.Number != 2 {
		t.Fatalf("unter dem neuen Zuschlag zurückgeholt: Head %d", h.Number)
	}
	if hb := e.heartbeat(userA, "pc-a", g.LeaseID); hb.Restore == nil || hb.Restore.Target != ref(gens[0]) {
		t.Fatalf("Lebenszeichen des neuen Zuschlags: %+v", hb.Restore)
	}
	if resp := e.play(userB, "pc-b"); resp.Role != api.RoleDenied {
		t.Fatalf("Play während des neuen Zuschlags: %+v", resp)
	}
	e.release(userA, "pc-a", g.LeaseID)
	checkRestored(t, e.head(), gens[0], 2)
}

// tickFor lässt d in Schritten von 10 s vergehen; vor jedem Takt meldet
// sich der Host mit ready (beat), sofern beat nicht nil ist. Der Head darf
// sich dabei nicht ändern.
func (e *restoreEnv) tickFor(d time.Duration, wantHead int64, beat func()) {
	e.t.Helper()
	for step := 10 * time.Second; d > 0; d -= step {
		e.clk.Advance(min(step, d))
		if beat != nil {
			beat()
		}
		e.srv.ExpireTick(e.t.Context())
		if h := e.head(); h.Number != wantHead {
			e.t.Fatalf("zu früh zurückgeholt: Head %d", h.Number)
		}
	}
}

// (a) Ein Host, der die Ansage nicht umsetzt und weiter einen laufenden
// Server meldet (alter Starter), blockiert nicht unbegrenzt: 30 + 60 s nach
// der Anforderung entzieht der Koordinator ihm den Zuschlag und holt
// zurück.
func TestRestoreRevokesIgnoringHost(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	l := e.host(userB, "pc-b")
	if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	e.ph.take()

	// Der Host meldet sich weiter als bereit, stoppt aber nicht: 89 s.
	e.tickFor(89*time.Second, 2, func() { e.beat(userB, "pc-b", l, true) })
	if hb := e.beat(userB, "pc-b", l, true); hb.Restore == nil {
		t.Fatal("nach 89 s keine Ansage mehr")
	}

	e.clk.Advance(time.Second) // 90 s
	e.srv.ExpireTick(t.Context())
	checkRestored(t, e.head(), gens[0], 2)
	if got := e.ph.take(); !slices.Equal(got, []string{"clear 15565"}) {
		t.Fatalf("Port-Halter nach dem Entzug: %q", got)
	}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: l, MachineID: "pc-b"}, nil); code != http.StatusConflict {
		t.Fatalf("Lebenszeichen nach dem Entzug: %d, erwartet 409", code)
	}
	if st := getStatus(t, e.base, userA); st.RestorePending != nil || st.Host != nil {
		t.Fatalf("Status nach dem Entzug: ausstehend %+v, Host %+v", st.RestorePending, st.Host)
	}
	if r := playElection(t, e.base, e.clk, userA, api.PlayRequest{MachineID: "pc-a"}); r.status != http.StatusOK || r.resp.Role != api.RoleHost {
		t.Fatalf("Play danach: %d %+v", r.status, r.resp)
	}
}

// (b) Ein Host, der die Ansage umsetzt, stoppt (ready false) und lädt seinen
// Schluss-Stand 5 Minuten lang hoch: kein Entzug; zurückgeholt wird nach
// seiner Rückgabe, auf seinem Schluss-Stand.
func TestRestoreWaitsForSlowFinalUpload(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	l := e.host(userB, "pc-b")
	if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	e.tickFor(30*time.Second, 2, func() { e.beat(userB, "pc-b", l, true) })
	e.tickFor(5*time.Minute, 2, func() { e.beat(userB, "pc-b", l, false) })

	final := e.commit(userB, "pc-b", l, 3, false, manifestMC)
	e.release(userB, "pc-b", l)
	checkRestored(t, e.head(), gens[0], final.Number)
}

// (c) Ein Host stoppt (ready false) und stürzt dann ab: kein Entzug, das
// Zurückholen gilt beim Ablauf seines Zuschlags (90 s nach dem letzten
// Lebenszeichen).
func TestRestoreHostStopsThenCrashes(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	l := e.host(userB, "pc-b")
	if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	e.tickFor(30*time.Second, 2, func() { e.beat(userB, "pc-b", l, true) })
	e.tickFor(10*time.Second, 2, func() { e.beat(userB, "pc-b", l, false) }) // letztes Lebenszeichen bei 40 s
	e.tickFor(lease.LeaseTTL-time.Second, 2, nil)                            // 129 s: Zuschlag gilt noch
	e.clk.Advance(time.Second)
	e.srv.ExpireTick(t.Context())
	checkRestored(t, e.head(), gens[0], 2)
}

// Ein Zuschlag nur zum Hochladen (Nachreichen, Welt übernehmen) wird nie
// entzogen, auch wenn sein Lebenszeichen „bereit“ meldet.
func TestRestoreNeverRevokesUpload(t *testing.T) {
	e := newRestoreEnv(t)
	gens := e.seed(2)
	g, err := e.lm.Acquire(lease.Candidate{Email: emailA, MinecraftName: "Alice", MachineID: "pc-a", AccountID: idA})
	if err != nil {
		t.Fatal(err)
	}
	if code, resp := e.restore(userA, ref(gens[0])); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	e.tickFor(3*time.Minute, 2, func() { e.beat(userA, "pc-a", g.LeaseID, true) })
	e.release(userA, "pc-a", g.LeaseID)
	checkRestored(t, e.head(), gens[0], 2)
}
