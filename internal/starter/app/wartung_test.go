package app

import (
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

// Endet die Host-Sitzung wegen Wartung, kommt ein Hinweis mit dem Grund,
// und es wird nicht neu gefragt (anders als beim Zurückholen).
func TestMaintenanceEndNotifiesWithoutReplay(t *testing.T) {
	for _, dauer := range []bool{false, true} {
		notes := countNotify(t)
		f := installFakeDauerPlay(t, nil)
		a := &App{ctx: t.Context(), c: replayCoord(t)}
		a.hostEnded(&host.MaintenanceError{Maintenance: api.Maintenance{Reason: "Update 0.2"}}, false, true, dauer)
		waitFor(t, "Hinweis", func() bool { return len(notes()) == 1 })
		if n := notes()[0]; !strings.Contains(n, "Wartung am Server (Update 0.2)") || !strings.Contains(n, "gleich wieder möglich") {
			t.Fatalf("Hinweis %q", n)
		}
		time.Sleep(50 * time.Millisecond)
		if b := busyOf(a); b != "" || f.calls.Load() != 0 {
			t.Fatalf("dauer=%v: nach der Wartung-Sitzung läuft %q, %d× Play – erwartet nichts", dauer, b, f.calls.Load())
		}
		if len(notes()) != 1 {
			t.Fatalf("weitere Hinweise: %q", notes())
		}
	}
}

// Läuft die Wartung ab, meldet der nächste Status es einmal.
func TestMaintenanceOverNotifiesOnce(t *testing.T) {
	notes := countNotify(t)
	a := &App{ctx: t.Context(), c: replayCoord(t), status: &api.Status{Maintenance: &api.Maintenance{Reason: "x"}}}
	a.pollStatus(false) // replayCoord antwortet mit {}: keine Wartung mehr
	waitFor(t, "Hinweis", func() bool { return len(notes()) == 1 })
	if n := notes()[0]; n != "Wartung vorbei, ihr könnt wieder spielen." {
		t.Fatalf("Hinweis %q", n)
	}
	a.pollStatus(false)
	time.Sleep(50 * time.Millisecond)
	if len(notes()) != 1 {
		t.Fatalf("zweiter Hinweis: %q", notes())
	}
}
