package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func (e *hoEnv) stats(as who) api.Stats {
	e.t.Helper()
	var st api.Stats
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/stats", nil, &st); code != http.StatusOK {
		e.t.Fatalf("GET /v1/stats: %d", code)
	}
	return st
}

func statsOf(t *testing.T, p api.StatsPeriod, name string) api.MemberStats {
	t.Helper()
	for _, m := range p.Members {
		if m.MinecraftName == name {
			return m
		}
	}
	t.Fatalf("%s fehlt in %+v", name, p.Members)
	return api.MemberStats{}
}

// Nur Mitglieder dürfen die Statistik lesen (Mitglied ohne Admin-Recht
// genügt); ein fremdes Konto bekommt 403, ohne Konto 401.
func TestStatsNurMitglieder(t *testing.T) {
	e := newHoEnv(t)
	if code := call(t, e.base, stranger, http.MethodGet, "/v1/stats", nil, nil); code != http.StatusForbidden {
		t.Errorf("Fremder: %d, erwartet 403", code)
	}
	if code := call(t, e.base, nobody, http.MethodGet, "/v1/stats", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("ohne Konto: %d, erwartet 401", code)
	}
	st := e.stats(userB)
	if len(st.Total.Members) != 2 || len(st.Last30.Members) != 2 || !st.Since.IsZero() {
		t.Errorf("leere Statistik: %+v", st)
	}
	for _, m := range st.Total.Members {
		if m.PlayS != 0 || m.HostS != 0 || m.HostShare != 0 {
			t.Errorf("Null erwartet: %+v", m)
		}
	}
}

// Host, Spieler, Übergabe und Ausfall zusammen: Hostzeit geht an den Host
// des Haupt-Zuschlags (der wartende zählt nicht), Spielzeit nur für Namen
// aus der Spielerliste, Mitglieder erscheinen mit Minecraft-Namen.
func TestStatsZaehlungMitUebergabe(t *testing.T) {
	e := newHoEnv(t)
	var lidA int64
	beatA := func() { e.mustHB(userA, pcA1, lidA, true, "Alice", "bob", "Fremder") }
	lidA = e.hostWithPlayers(userA, pcA1) // erstes Lebenszeichen: Sitzung beginnt
	e.advance(60*time.Second, beatA)

	// Übergabe an Bob: Der wartende Zuschlag schickt Lebenszeichen, zählt aber nicht.
	e.present(userB, pcB, true)
	hs := e.mustStart(userA, pcA1, lidA)
	e.mustReady(userA, pcA1, lidA, hs.HandoverID)
	o := e.present(userB, pcB, true).Handover
	if o == nil {
		t.Fatal("kein Angebot")
	}
	e.advance(30*time.Second, func() {
		beatA()
		e.mustHB(userB, pcB, o.LeaseID, true, "Bob")
	})
	st := e.stats(userB)
	if a, b := statsOf(t, st.Total, "Alice"), statsOf(t, st.Total, "Bob"); a.HostS != 90 || b.HostS != 0 || a.HostSessions != 1 || b.HostSessions != 0 {
		t.Fatalf("vor der Beförderung: alice %+v bob %+v", a, b)
	}

	// Bob wird Haupt-Host; Alice hört auf.
	if code := e.release(userA, pcA1, lidA); code != http.StatusNoContent {
		t.Fatalf("Rückgabe: %d", code)
	}
	e.mustHB(userB, pcB, o.LeaseID, true, "Bob")
	e.advance(40*time.Second, func() { e.mustHB(userB, pcB, o.LeaseID, true, "Bob") })

	st = e.stats(userA)
	a, b := statsOf(t, st.Total, "Alice"), statsOf(t, st.Total, "Bob")
	if a.HostS != 90 || a.HostSessions != 1 || b.HostS != 40 || b.HostSessions != 1 {
		t.Errorf("Hostzeit: alice %+v bob %+v", a, b)
	}
	if a.PlayS != 90 || b.PlayS != 90+40 {
		t.Errorf("Spielzeit: alice %d s, bob %d s, erwartet 90 und 130", a.PlayS, b.PlayS)
	}
	if st.Total.HostTotalS != 130 || a.HostShare < 0.69 || a.HostShare > 0.70 || b.HostShare < 0.30 || b.HostShare > 0.31 {
		t.Errorf("Anteil: gesamt %d, alice %.3f bob %.3f", st.Total.HostTotalS, a.HostShare, b.HostShare)
	}
	// Alles liegt in den letzten 30 Tagen.
	if l := statsOf(t, st.Last30, "Alice"); l != a {
		t.Errorf("30 Tage %+v, gesamt %+v", l, a)
	}
	if st.Since.IsZero() {
		t.Error("Since fehlt")
	}

	// Datenschutz: nur Namen und Zahlen, keine E-Mail, kein PC.
	raw, _ := json.Marshal(st)
	for _, bad := range []string{"@", "example.org", pcA1, pcB, "Fremder"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("Antwort enthält %q: %s", bad, raw)
		}
	}
}

// Stirbt der Host, wächst nichts mehr: Der Zuschlag läuft nach 90 s ab, aber
// gezählt wird nur bis zum letzten Lebenszeichen.
func TestStatsHostAusfall(t *testing.T) {
	e := newHoEnv(t)
	lid := e.hostWithPlayers(userA, pcA1)
	e.advance(30*time.Second, func() { e.mustHB(userA, pcA1, lid, true, "Alice", "Bob") })
	e.advance(5*time.Minute, nil) // keine Lebenszeichen mehr; Zuschlag läuft ab
	if s := getStatus(t, e.base, userB); s.Host != nil {
		t.Fatalf("Host nach Ausfall: %+v", s.Host)
	}
	st := e.stats(userB)
	if a, b := statsOf(t, st.Total, "Alice"), statsOf(t, st.Total, "Bob"); a.HostS != 30 || a.PlayS != 30 || b.PlayS != 30 {
		t.Errorf("alice %+v bob %+v, erwartet je 30 s", a, b)
	}
}

// Ein Zuschlag nur zum Hochladen hat keinen Server: nichts wird gezählt.
func TestStatsUploadOnlyZaehltNicht(t *testing.T) {
	e := newHoEnv(t)
	var up api.PlayResponse
	if code := call(t, e.base, userA, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: pcA1, UploadOnly: true}, &up); code != http.StatusOK || up.Role != api.RoleHost {
		t.Fatalf("Hochladen-Zuschlag: %d %+v", code, up)
	}
	e.advance(30*time.Second, func() { e.mustHB(userA, pcA1, up.LeaseID, false) })
	st := e.stats(userA)
	if a := statsOf(t, st.Total, "Alice"); a.HostS != 0 || a.HostSessions != 0 {
		t.Errorf("alice %+v", a)
	}
	if !st.Since.IsZero() {
		t.Errorf("Since gesetzt: %v", st.Since)
	}
}

// Neustart des Koordinators: Zahlen kommen aus stats.json zurück, dieselbe
// Sitzung zählt nicht noch einmal; die Zeit ohne Koordinator zählt nicht.
func TestStatsNeustart(t *testing.T) {
	e := newHoEnv(t)
	lid := e.hostWithPlayers(userA, pcA1)
	e.advance(40*time.Second, func() { e.mustHB(userA, pcA1, lid, true, "Alice", "Bob") })
	e.srv.FlushStats()

	srv2, _, _, _ := newServerIn(t, e.dir, "dev", fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	var st api.Stats
	if code := call(t, ts2.URL, userB, http.MethodGet, "/v1/stats", nil, &st); code != http.StatusOK {
		t.Fatalf("Stats nach Neustart: %d", code)
	}
	if a, b := statsOf(t, st.Total, "Alice"), statsOf(t, st.Total, "Bob"); a.HostS != 40 || a.HostSessions != 1 || b.PlayS != 40 {
		t.Errorf("nach Neustart: alice %+v bob %+v", a, b)
	}
	// Der neue Server kennt den Zuschlag aus lease.json; das Lebenszeichen
	// desselben Zuschlags beginnt neu und zählt keine zweite Sitzung.
	if code := call(t, ts2.URL, userA, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: lid, MachineID: pcA1, Ready: true, Players: []string{"Alice"}}, nil); code != http.StatusOK && code != http.StatusConflict {
		t.Fatalf("Lebenszeichen nach Neustart: %d", code)
	}
	if code := call(t, ts2.URL, userB, http.MethodGet, "/v1/stats", nil, &st); code != http.StatusOK {
		t.Fatal(code)
	}
	if a := statsOf(t, st.Total, "Alice"); a.HostSessions != 1 {
		t.Errorf("Sitzungen nach Neustart: %d", a.HostSessions)
	}
}
