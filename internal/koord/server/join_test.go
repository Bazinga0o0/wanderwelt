package server_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

var (
	guestD = who{"dora@example.org", 404}
	guestE = who{"emil@example.org", 405}
)

func (e *memberEnv) join(as who, name string) int {
	e.t.Helper()
	return call(e.t, e.base, as, http.MethodPost, "/v1/join", api.JoinRequest{MinecraftName: name}, nil)
}

func (e *memberEnv) joinState(as who) api.JoinStatus {
	e.t.Helper()
	var st api.JoinStatus
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/join", nil, &st); code != http.StatusOK {
		e.t.Fatalf("GET /v1/join: %d", code)
	}
	return st
}

func (e *memberEnv) joinRequests() []api.JoinRequestInfo {
	e.t.Helper()
	var out []api.JoinRequestInfo
	if code := call(e.t, e.base, userA, http.MethodGet, "/v1/join-requests", nil, &out); code != http.StatusOK {
		e.t.Fatalf("GET /v1/join-requests: %d", code)
	}
	return out
}

func (e *memberEnv) decide(as who, email, verb string) int {
	e.t.Helper()
	return call(e.t, e.base, as, http.MethodPost, "/v1/join-requests/"+email+"/"+verb, nil, nil)
}

// Ein Nicht-Mitglied darf nur POST und GET /v1/join; jede andere Gruppe von
// Endpunkten antwortet 403 (nicht 404, nicht 400).
func TestJoinGuestOnlyJoin(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	e.host()
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/me"},
		{"GET", "/v1/status"},
		{"GET", "/v1/manifest"},
		{"GET", "/v1/generations"},
		{"GET", "/v1/generations/head"},
		{"GET", "/v1/artifacts/server.jar"},
		{"POST", "/v1/play"},
		{"POST", "/v1/presence"},
		{"POST", "/v1/lease/heartbeat"},
		{"POST", "/v1/lease/release"},
		{"POST", "/v1/handover/start"},
		{"POST", "/v1/handover/ready"},
		{"GET", "/v1/host/settings"},
		{"POST", "/v1/blobs/missing"},
		{"GET", "/v1/blobs/abc"},
		{"POST", "/v1/restore"},
		{"POST", "/v1/me/minecraft"},
		{"GET", "/v1/members"},
		{"PUT", "/v1/storage/budget"},
		{"PUT", "/v1/join"},
		{"DELETE", "/v1/join"},
		{"GET", "/v1/join-requests"},
		{"POST", "/v1/join-requests/" + emailB + "/accept"},
		{"POST", "/v1/join-requests/" + guestD.email + "/accept"},
		{"POST", "/v1/join-requests/" + guestD.email + "/decline"},
		{"GET", "/v1/gibt-es-nicht"},
	} {
		if code := call(t, e.base, guestD, c.method, c.path, nil, nil); code != http.StatusForbidden {
			t.Errorf("Nicht-Mitglied: %s %s: %d, want 403", c.method, c.path, code)
		}
	}
	// Ohne Identität bleibt es 401.
	if code := call(t, e.base, nobody, http.MethodPost, "/v1/join", api.JoinRequest{MinecraftName: "Dora"}, nil); code != http.StatusUnauthorized {
		t.Errorf("ohne Identität: %d, want 401", code)
	}
	// Die Beitritts-Endpunkte gehen.
	if st := e.joinState(guestD); st.State != api.JoinNone {
		t.Errorf("Stand vor der Anfrage: %+v", st)
	}
	if code := e.join(guestD, "Dora"); code != http.StatusCreated {
		t.Fatalf("Anfrage: %d", code)
	}
	if st := e.joinState(guestD); st.State != api.JoinOpen || st.Since.IsZero() {
		t.Errorf("Stand nach der Anfrage: %+v", st)
	}
	// Ein Mitglied hat keine Anfrage zu stellen, GET sagt „angenommen“.
	if code := e.join(userB, "Bob"); code != http.StatusConflict {
		t.Errorf("Mitglied fragt an: %d, want 409", code)
	}
	if st := e.joinState(userB); st.State != api.JoinAccepted {
		t.Errorf("Stand eines Mitglieds: %+v", st)
	}
	// Admin-Endpunkte für Nicht-Admins: 403.
	if code := call(t, e.base, userB, http.MethodGet, "/v1/join-requests", nil, nil); code != http.StatusForbidden {
		t.Errorf("Mitglied sieht Anfragen: %d", code)
	}
	if code := e.decide(userB, guestD.email, "accept"); code != http.StatusForbidden {
		t.Errorf("Mitglied nimmt an: %d", code)
	}
	if got := e.joinRequests(); len(got) != 1 || got[0].Email != guestD.email || got[0].MinecraftName != "Dora" {
		t.Errorf("Anfragen: %+v", got)
	}
}

// Höchstens eine offene Anfrage je Konto, 50 offene insgesamt, nach einer
// Ablehnung 24 h Wartezeit.
func TestJoinLimits(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	if code := e.join(guestD, "Dora"); code != http.StatusCreated {
		t.Fatalf("erste Anfrage: %d", code)
	}
	if code := e.join(guestD, "Gast77"); code != http.StatusTooManyRequests {
		t.Errorf("zweite offene Anfrage desselben Kontos: %d, want 429", code)
	}

	// 24 h nach einer Ablehnung.
	if code := e.decide(userA, guestD.email, "decline"); code != http.StatusNoContent {
		t.Fatalf("ablehnen: %d", code)
	}
	if st := e.joinState(guestD); st.State != api.JoinDeclined {
		t.Errorf("Stand nach Ablehnung: %+v", st)
	}
	if code := e.join(guestD, "Dora"); code != http.StatusTooManyRequests {
		t.Errorf("gleich nach der Ablehnung: %d, want 429", code)
	}
	e.clk.Advance(23*time.Hour + 59*time.Minute)
	if code := e.join(guestD, "Dora"); code != http.StatusTooManyRequests {
		t.Errorf("nach 23:59 h: %d, want 429", code)
	}
	e.clk.Advance(2 * time.Minute)
	if code := e.join(guestD, "Dora"); code != http.StatusCreated {
		t.Errorf("nach 24 h: %d, want 201", code)
	}
	if code := e.decide(userA, guestD.email, "decline"); code != http.StatusNoContent {
		t.Fatalf("ablehnen: %d", code)
	}
	if code := e.decide(userA, guestD.email, "decline"); code != http.StatusNotFound {
		t.Errorf("zweites Ablehnen: %d, want 404", code)
	}

	// 50 offene insgesamt.
	for i := 0; i < api.MaxOpenJoins; i++ {
		g := who{fmt.Sprintf("gast%02d@example.org", i), int64(1000 + i)}
		if code := e.join(g, fmt.Sprintf("Gast%02d", i)); code != http.StatusCreated {
			t.Fatalf("Anfrage %d: %d", i, code)
		}
	}
	if n := len(e.joinRequests()); n != api.MaxOpenJoins {
		t.Fatalf("offene Anfragen: %d", n)
	}
	if code := e.join(guestE, "Emil"); code != http.StatusTooManyRequests {
		t.Errorf("51. offene Anfrage: %d, want 429", code)
	}
	if got := e.me(userA).PendingJoins; got != api.MaxOpenJoins {
		t.Errorf("PendingJoins des Admins: %d", got)
	}
	// Eine Ablehnung schafft Platz.
	if code := e.decide(userA, "gast00@example.org", "decline"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if code := e.join(guestE, "Emil"); code != http.StatusCreated {
		t.Errorf("nach Ablehnung: %d, want 201", code)
	}
}

// Ein Minecraft-Name, der einem Mitglied gehört oder schon angefragt ist,
// wird abgelehnt; ungültige und bei Mojang unbekannte Namen auch.
func TestJoinNameChecks(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	for _, c := range []struct {
		name string
		want int
	}{
		{"Alice", http.StatusConflict}, // Mitglied
		{"alice", http.StatusConflict}, // Groß/Klein egal
		{"Ung ültig", http.StatusBadRequest},
		{"", http.StatusBadRequest},
		{"Unbekannt", http.StatusBadRequest}, // Mojang kennt ihn nicht
		{"Stoerung", http.StatusServiceUnavailable},
	} {
		if code := e.join(guestD, c.name); code != c.want {
			t.Errorf("Name %q: %d, want %d", c.name, code, c.want)
		}
	}
	if st := e.joinState(guestD); st.State != api.JoinNone {
		t.Errorf("abgelehnte Anfragen dürfen nichts anlegen: %+v", st)
	}
	if code := e.join(guestD, "Dora"); code != http.StatusCreated {
		t.Fatalf("Dora: %d", code)
	}
	if code := e.join(guestE, "dora"); code != http.StatusConflict {
		t.Errorf("derselbe Name in zweiter Anfrage: %d, want 409", code)
	}
	// Nach einer Ablehnung ist der Name wieder frei.
	e.decide(userA, guestD.email, "decline")
	if code := e.join(guestE, "Dora"); code != http.StatusCreated {
		t.Errorf("Name nach Ablehnung: %d, want 201", code)
	}
}

// Annehmen macht das Konto zum Mitglied: Anmeldung, Whitelist des laufenden
// Hosts (MembersVersion), Rollen. Ablehnen nicht.
func TestJoinAcceptDecline(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	l := e.host()
	before := e.heartbeat(l).MembersVersion
	e.join(guestD, "Dora")
	e.join(guestE, "Emil")
	if got := e.me(userA).PendingJoins; got != 2 {
		t.Errorf("PendingJoins: %d, want 2", got)
	}
	if got := e.me(userB).PendingJoins; got != 0 {
		t.Errorf("PendingJoins eines Nicht-Admins: %d, want 0", got)
	}
	if code := e.decide(userA, "niemand@example.org", "accept"); code != http.StatusNotFound {
		t.Errorf("unbekannte Anfrage: %d, want 404", code)
	}
	if code := e.decide(userA, guestE.email, "decline"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if e.heartbeat(l).MembersVersion != before {
		t.Error("Ablehnen darf MembersVersion nicht ändern")
	}
	if code := e.decide(userA, guestD.email, "accept"); code != http.StatusNoContent {
		t.Fatalf("annehmen: %d", code)
	}
	if e.heartbeat(l).MembersVersion == before {
		t.Error("Annehmen muss MembersVersion erhöhen")
	}
	var names []string
	for _, p := range e.hostSettings(userA).Whitelist {
		names = append(names, p.Name)
	}
	if !slices.Contains(names, "Dora") || slices.Contains(names, "Emil") {
		t.Errorf("Whitelist: %v", names)
	}
	if me := e.me(guestD); me.Email != guestD.email || me.MinecraftName != "Dora" || me.Admin {
		t.Errorf("Dora nach Annahme: %+v", me)
	}
	if st := e.joinState(guestD); st.State != api.JoinAccepted || st.Since.IsZero() {
		t.Errorf("Stand: %+v", st)
	}
	if code := call(t, e.base, guestE, http.MethodGet, "/v1/me", nil, nil); code != http.StatusForbidden {
		t.Errorf("abgelehnter Emil: %d, want 403", code)
	}
	if got := e.joinRequests(); len(got) != 0 {
		t.Errorf("offene Anfragen: %+v", got)
	}
	if got := e.me(userA).PendingJoins; got != 0 {
		t.Errorf("PendingJoins danach: %d", got)
	}
	// Als Mitglied steht Dora in der Mitgliederliste; ein Admin kann ihre Rolle setzen.
	found := false
	for _, m := range e.members(userA) {
		found = found || m.Email == guestD.email && m.MinecraftName == "Dora" && !m.Admin
	}
	if !found {
		t.Error("Dora fehlt in der Mitgliederliste")
	}
	if code := e.setRole(userA, guestD.email, false, true); code != http.StatusOK {
		t.Errorf("Rolle für Dora: %d", code)
	}
	if code := e.decide(userA, guestD.email, "accept"); code != http.StatusNotFound {
		t.Errorf("zweites Annehmen: %d, want 404", code)
	}
}

// Anfragen, Ablehnungen und angenommene Mitglieder überstehen einen Neustart.
func TestJoinSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	e := newMemberEnv(t, dir, gib)
	e.join(guestD, "Dora")
	e.join(guestE, "Emil")
	e.join(who{"fritz@example.org", 406}, "Gast01")
	e.decide(userA, guestD.email, "accept")
	e.decide(userA, guestE.email, "decline")
	if code := e.setRole(userA, guestD.email, false, true); code != http.StatusOK {
		t.Fatal(code)
	}
	checkMode600(t, filepath.Join(dir, "members.json"))
	checkMode600(t, filepath.Join(dir, "joins.json"))

	e2 := newMemberEnv(t, dir, gib)
	if me := e2.me(guestD); me.MinecraftName != "Dora" || !me.Op {
		t.Errorf("Dora nach Neustart: %+v", me)
	}
	if st := e2.joinState(guestE); st.State != api.JoinDeclined {
		t.Errorf("Emil nach Neustart: %+v", st)
	}
	if code := e2.join(guestE, "Emil"); code != http.StatusTooManyRequests {
		t.Errorf("Wartezeit nach Neustart: %d, want 429", code)
	}
	got := e2.joinRequests()
	if len(got) != 1 || got[0].Email != "fritz@example.org" || got[0].MinecraftName != "Gast01" {
		t.Errorf("offene Anfragen nach Neustart: %+v", got)
	}
	if n := e2.me(userA).PendingJoins; n != 1 {
		t.Errorf("PendingJoins nach Neustart: %d", n)
	}
	// Der Name von Dora ist weiter belegt.
	if code := e2.join(who{"gina@example.org", 407}, "dora"); code != http.StatusConflict {
		t.Errorf("Name eines angenommenen Mitglieds: %d, want 409", code)
	}
}

// HEAD, OPTIONS und Pfadvarianten von /v1/join sind für Nicht-Mitglieder 403.
func TestJoinGuestMethodAndPathVariants(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	for _, c := range []struct{ method, path string }{
		{"HEAD", "/v1/join"},
		{"OPTIONS", "/v1/join"},
		{"GET", "/v1/join/"},
		{"POST", "/v1/join/"},
		{"GET", "//v1/join"},
		{"GET", "/v1/x/../join"},
		{"GET", "/v1/JOIN"},
	} {
		req, err := http.NewRequest(c.method, e.base+c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		setIdentity(req, guestD)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
}

// Eine kaputte members.json beim Neuladen sperrt die angenommenen
// Mitglieder nicht aus; abgelehnte Anfragen verschwinden nach 24 h aus
// joins.json.
func TestJoinBrokenMembersFileAndPrune(t *testing.T) {
	dir := t.TempDir()
	e := newMemberEnv(t, dir, gib)
	e.join(guestD, "Dora")
	e.join(guestE, "Emil")
	e.decide(userA, guestD.email, "accept")
	e.decide(userA, guestE.email, "decline")

	if err := os.WriteFile(filepath.Join(dir, "members.json"), []byte("{kaputt"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "k.yaml")
	cfg := "auth: {mode: dev}\nmembers:\n  - {email: alice@example.org, minecraft: Alice, admin: true, op: true}\n  - {email: bob@example.org, minecraft: Bob}\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.ReloadMembers(t.Context(), cfgPath); err != nil {
		t.Fatal(err)
	}
	if me := e.me(guestD); me.MinecraftName != "Dora" {
		t.Errorf("Dora nach Neuladen mit kaputter members.json: %+v", me)
	}

	// Emils Ablehnung ist nach 24 h aus joins.json geräumt (beim nächsten Schreiben).
	e.clk.Advance(25 * time.Hour)
	e.join(who{"fritz@example.org", 406}, "Gast01")
	data, err := os.ReadFile(filepath.Join(dir, "joins.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "emil@example.org") || !strings.Contains(string(data), "fritz@example.org") {
		t.Errorf("joins.json:\n%s", data)
	}
	if st := e.joinState(guestE); st.State != api.JoinNone {
		t.Errorf("Emil nach 24 h: %+v", st)
	}
}
