package host

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func testMaintenance(remainingS int64) *api.Maintenance {
	return &api.Maintenance{
		Reason:     "Update",
		Since:      t0,
		StopAt:     t0.Add(time.Duration(remainingS) * time.Second),
		RemainingS: remainingS,
	}
}

func checkMaintenanceErr(t *testing.T, err error, m *api.Maintenance) {
	t.Helper()
	if !errors.Is(err, ErrMaintenance) {
		t.Fatalf("Run = %v, erwartet ErrMaintenance", err)
	}
	me, ok := errors.AsType[*MaintenanceError](err)
	if !ok || me.Maintenance.Reason != m.Reason {
		t.Fatalf("Run = %#v, erwartet MaintenanceError mit Grund %q", err, m.Reason)
	}
}

const maintFirst = "Wartung: Server stoppt in 100 Sekunden – Update. Danach ist Spielen gleich wieder möglich."

// Die Notice im Lebenszeichen: sofort Ansage mit Grund, weitere bei 60 und
// 10 s vor dem Stopp, dann sauberer Stopp mit Schluss-Stand, Zuschlag
// zurück, Run liefert ErrMaintenance – und keine Übergabe.
func TestMaintenanceEndsSession(t *testing.T) {
	t.Run("während des Hostens", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		r := e.start(t)
		n := e.head.Number
		m := testMaintenance(100)
		e.drv.advanceTo(55 * time.Second)
		e.co.hbResp.Store(&api.HeartbeatResponse{Maintenance: m})
		e.drv.advanceTo(60 * time.Second)
		first := timedText{60 * time.Second, maintFirst}
		checkAnnouncements(t, e, first)
		e.drv.advanceTo(100 * time.Second)
		w1 := timedText{100 * time.Second, "Wartung: Server stoppt in 60 Sekunden"}
		checkAnnouncements(t, e, first, w1)
		e.drv.advanceTo(150 * time.Second)
		w2 := timedText{150 * time.Second, "Wartung: Server stoppt in 10 Sekunden"}
		checkAnnouncements(t, e, first, w1, w2)
		e.drv.advanceTo(160*time.Second - time.Second)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Server vor dem Stopp-Termin gestoppt: %v", s)
		}
		e.drv.advanceTo(160 * time.Second)
		checkMaintenanceErr(t, r.wait(t), m)
		if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{160 * time.Second}) {
			t.Fatalf("Stop %v, erwartet einmal bei 160 s", s)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		final := e.co.head(t)
		if final.Number != n+1 || final.MachineID != hostMachine {
			t.Fatalf("Head %d von %s, erwartet Schluss-Stand %d vom Host", final.Number, final.MachineID, n+1)
		}
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
		checkUploaded(t, e.checkAfterRun(t), final)
	})

	// Frist 0: Ansage, wenig später (maintMinDelay) der Stopp.
	t.Run("Frist 0: Ansage und gleich Stopp", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		r := e.start(t)
		m := testMaintenance(0)
		e.drv.advanceTo(55 * time.Second)
		e.co.hbResp.Store(&api.HeartbeatResponse{Maintenance: m})
		e.drv.advanceTo(60 * time.Second)
		checkAnnouncements(t, e, timedText{60 * time.Second,
			"Wartung: Server stoppt in 3 Sekunden – Update. Danach ist Spielen gleich wieder möglich."})
		e.drv.advanceTo(63 * time.Second)
		checkMaintenanceErr(t, r.wait(t), m)
		if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{63 * time.Second}) {
			t.Fatalf("Stop %v, erwartet einmal bei 63 s", s)
		}
		checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
	})

	// Der Server läuft noch nicht bereit: sofort sauber enden, ohne Ansage.
	t.Run("Server noch nicht bereit: sofort sauber enden", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		m := testMaintenance(100)
		e.co.hbResp.Store(&api.HeartbeatResponse{Maintenance: m})
		g := newGate(t)
		e.co.hbGate.Store(g)
		r := e.start(t)
		g.waitArrived(t)
		e.drv.allow = 1
		e.drv.advanceTo(0)
		e.co.hbGate.Store(nil)
		e.drv.allow = 0
		e.drv.resync()
		g.release()
		checkMaintenanceErr(t, r.wait(t), m)
		checkAnnouncements(t, e)
		if rd := e.co.heartbeatReadies(); slices.Contains(rd, true) {
			t.Fatalf("ready %v, erwartet nie true", rd)
		}
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
		checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
	})
}

// Ändert der Admin die Frist, gilt die neue (mit Hinweis); dieselbe Notice
// in jedem Lebenszeichen sagt nichts doppelt an.
func TestMaintenanceDeadlineChanged(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	r := e.start(t)
	e.drv.advanceTo(55 * time.Second)
	e.co.hbResp.Store(&api.HeartbeatResponse{Maintenance: testMaintenance(100)})
	e.drv.advanceTo(70 * time.Second) // mehrere Lebenszeichen mit derselben Notice
	checkAnnouncements(t, e, timedText{60 * time.Second, maintFirst})

	m2 := testMaintenance(20) // „wartung start --in 20s“ noch einmal
	m2.StopAt = t0.Add(10 * time.Minute)
	e.co.hbResp.Store(&api.HeartbeatResponse{Maintenance: m2})
	e.drv.advanceTo(80 * time.Second)
	got := e.srv.announcements()
	if len(got) != 2 || got[1].text != "Wartung (Frist geändert): Server stoppt in 20 Sekunden – Update. Danach ist Spielen gleich wieder möglich." {
		t.Fatalf("Ansagen: %+v", got)
	}
	e.drv.advanceTo(100 * time.Second)
	checkMaintenanceErr(t, r.wait(t), m2)
}
