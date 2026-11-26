package server_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

// startWartung schreibt den Zustand wie „wartung start“ und lässt den
// Koordinator im nächsten Takt davon erfahren.
func (e *restoreEnv) startWartung(in time.Duration, grund string) {
	e.t.Helper()
	z, err := wartung.Starten(wartung.Zustand{}, e.clk.Now(), in, grund)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := wartung.Speichern(e.dir, z); err != nil {
		e.t.Fatal(err)
	}
	e.srv.ExpireTick(e.t.Context())
}

func (e *restoreEnv) bericht() wartung.Bericht {
	e.t.Helper()
	b, ok, err := wartung.BerichtLesen(e.dir)
	if err != nil || !ok {
		e.t.Fatalf("Bericht: %v %v", ok, err)
	}
	return b
}

// Ablehnung beim Spielen: Während der Wartung bekommt jeder „denied“ mit
// dem Grund (auch Hochladen), danach geht es wieder.
func TestWartungLehntSpielenAb(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	e.startWartung(time.Minute, "Update")

	for _, req := range []api.PlayRequest{{MachineID: "pc-b"}, {MachineID: "pc-b", UploadOnly: true}} {
		var resp api.PlayResponse
		if code := call(t, e.base, userB, http.MethodPost, "/v1/play", req, &resp); code != http.StatusOK ||
			resp.Role != api.RoleDenied || !strings.Contains(resp.Reason, "Update") || !strings.Contains(resp.Reason, "Wartung") {
			t.Fatalf("Play %+v in der Wartung: %d %+v", req, code, resp)
		}
	}
	var st api.Status
	if code := call(t, e.base, userB, http.MethodGet, "/v1/status", nil, &st); code != http.StatusOK ||
		st.Maintenance == nil || st.Maintenance.Reason != "Update" || st.Maintenance.RemainingS != 60 {
		t.Fatalf("Status: %d %+v", code, st.Maintenance)
	}

	// „wartung ende“: Datei weg, nächster Takt (oder SIGHUP) liest sie.
	if err := wartung.Speichern(e.dir, wartung.Zustand{}); err != nil {
		t.Fatal(err)
	}
	e.srv.ReloadWartung()
	if _, ok, _ := wartung.BerichtLesen(e.dir); ok {
		t.Error("Bericht nach „ende“ noch da")
	}
	if resp := e.host(userB, "pc-b"); resp == 0 {
		t.Fatal("kein Zuschlag nach der Wartung")
	}
}

// Der Host erfährt die Wartung mit dem Lebenszeichen (mit Rest), stoppt und
// gibt den Zuschlag zurück; erst dann ist der Bericht „bereit“.
func TestWartungBerichtBisBereit(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	l := e.host(userB, "pc-b")
	e.startWartung(30*time.Second, "Update")

	hb := e.beat(userB, "pc-b", l, true)
	if hb.Maintenance == nil || hb.Maintenance.RemainingS != 30 || hb.Maintenance.Reason != "Update" {
		t.Fatalf("Lebenszeichen: %+v", hb.Maintenance)
	}
	if b := e.bericht(); b.Bereit || b.Zuschlag != "Bob" || !strings.Contains(b.Haengt(), "Bob hostet noch") {
		t.Fatalf("Bericht mit Host: %+v", b)
	}

	// Nach der Frist, aber mit Zuschlag: noch nicht bereit.
	e.clk.Advance(31 * time.Second)
	e.beat(userB, "pc-b", l, false)
	e.srv.ExpireTick(t.Context())
	if b := e.bericht(); b.Bereit {
		t.Fatalf("bereit trotz Zuschlag: %+v", b)
	}
	e.release(userB, "pc-b", l)
	e.srv.ExpireTick(t.Context())
	if b := e.bericht(); !b.Bereit || b.Zuschlag != "" {
		t.Fatalf("nicht bereit nach Rückgabe: %+v", b)
	}
}

// Ein Host, der die Ansage ignoriert (älterer Starter), verliert nach Frist
// und Nachfrist den Zuschlag; ein Zuschlag nur zum Hochladen bleibt.
func TestWartungEntziehtZuschlagNachFrist(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	l := e.host(userB, "pc-b")
	e.startWartung(30*time.Second, "Update")

	// Bis kurz vor Frist + 60 s bleibt der Zuschlag.
	e.tickFor(89*time.Second, 1, func() { e.beat(userB, "pc-b", l, true) })
	if _, _, ok := e.lm.Current(); !ok {
		t.Fatal("Zuschlag zu früh entzogen")
	}
	e.clk.Advance(2 * time.Second)
	e.beat(userB, "pc-b", l, true)
	e.srv.ExpireTick(t.Context())
	if _, _, ok := e.lm.Current(); ok {
		t.Fatal("Zuschlag nicht entzogen")
	}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: l, MachineID: "pc-b", Ready: true}, nil); code != http.StatusConflict {
		t.Fatalf("Lebenszeichen nach Entzug: %d, erwartet 409", code)
	}
}

// Die Wartung steht in data_dir: Ein neu gestarteter Koordinator lehnt
// gleich ab, noch vor dem ersten Takt.
func TestWartungUeberlebtNeustart(t *testing.T) {
	dir := t.TempDir()
	z, _ := wartung.Starten(wartung.Zustand{}, time.Now(), time.Hour, "Umzug")
	if err := wartung.Speichern(dir, z); err != nil {
		t.Fatal(err)
	}
	srv, _, _, _ := newServerIn(t, dir, "dev", fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
	hs := httptest.NewServer(srv)
	defer hs.Close()
	ts := hs.URL
	var st api.Status
	if code := call(t, ts, userB, http.MethodGet, "/v1/status", nil, &st); code != http.StatusOK || st.Maintenance == nil || st.Maintenance.Reason != "Umzug" {
		t.Fatalf("Status nach Neustart: %d %+v", code, st.Maintenance)
	}
}

// Eine kaputte Datei lässt den bisherigen Zustand stehen.
func TestWartungKaputteDatei(t *testing.T) {
	e := newRestoreEnv(t)
	e.startWartung(time.Hour, "Update")
	if err := os.WriteFile(wartung.Pfad(e.dir), []byte("{kaputt"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.srv.ExpireTick(t.Context())
	if resp := e.play(userB, "pc-b"); resp.Role != api.RoleDenied {
		t.Fatalf("nach kaputter Datei: %+v", resp)
	}
}
