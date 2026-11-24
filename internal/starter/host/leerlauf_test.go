package host

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
)

// Die Leerlauf-Frist gibt der Koordinator in den Host-Einstellungen vor
// (empty_timeout_s); fehlt sie (älterer Koordinator) oder liegt sie außerhalb
// von 60–3600 s, gelten 10 Minuten. Gemessen wird ab der ersten Abfrage ohne
// Spieler, hier gleich beim Start. Ohne Dauerbetrieb gibt es Zwischenstände
// wie immer, auch wenn niemand spielt.
func TestEmptyTimeoutFromHostSettings(t *testing.T) {
	for _, tt := range []struct {
		name    string
		timeout time.Duration // empty_timeout des Koordinators (0: fehlt)
		want    time.Duration
	}{
		{"3 min vom Koordinator", 3 * time.Minute, 3 * time.Minute},
		{"fehlt → 10 min", 0, 10 * time.Minute},
		{"untere Grenze 1 min", time.Minute, time.Minute},
		{"obere Grenze 60 min", 60 * time.Minute, 60 * time.Minute},
		{"zu klein → 10 min", 59 * time.Second, 10 * time.Minute},
		{"zu groß → 10 min", 61 * time.Minute, 10 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			co := newCoord(t, func(c *config.Config) { c.EmptyTimeout = tt.timeout })
			e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{})
			r := e.start(t)
			e.drv.advanceTo(tt.want - time.Second)
			if s := e.srv.stopTimes(); len(s) != 0 {
				t.Fatalf("Stop schon vor %v: %v", tt.want, s)
			}
			// Zwischenstände alle 5 min wie ohne Leerlauf.
			if c, want := len(e.co.hostCommits()), int((tt.want-time.Second)/(5*time.Minute)); c != want {
				t.Fatalf("bei %v: %d Commits, erwartet %d", tt.want-time.Second, c, want)
			}
			e.drv.advanceTo(tt.want)
			if err := r.wait(t); err != nil {
				t.Fatalf("Run = %v", err)
			}
			if s := e.srv.stopTimes(); len(s) != 1 || s[0] != tt.want {
				t.Fatalf("Stop bei %v, erwartet genau einmal bei %v", s, tt.want)
			}
		})
	}
	if DefaultEmptyTimeout != 10*time.Minute {
		t.Fatalf("DefaultEmptyTimeout = %v, erwartet 10m", DefaultEmptyTimeout)
	}
}

// Dauerbetrieb (Deps.Dauerbetrieb, bei jeder Entscheidung neu gefragt): kein
// Ende mangels Spielern, und ein Zwischenstand nur, wenn seit dem letzten
// Stand jemand online war – leere Stände verdrängen sonst die Aufbewahrung.
func TestDauerbetriebNoEndNoEmptySnapshots(t *testing.T) {
	co := newCoord(t, func(c *config.Config) { c.EmptyTimeout = 3 * time.Minute })
	e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{})
	var on atomic.Bool
	on.Store(true)
	e.d.Dauerbetrieb = on.Load
	r := e.start(t)
	n := e.head.Number

	// 2 h niemand: kein Ende, kein Zwischenstand (24 übersprungen).
	e.drv.advanceTo(2 * time.Hour)
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt, obwohl Dauerbetrieb an ist: %v", s)
	}
	if _, _, barriers, _ := e.srv.counts(); barriers != 0 || len(e.co.hostCommits()) != 0 {
		t.Fatalf("SaveBarrier %d×, %d Commits in 2 h ohne Spieler, erwartet keine", barriers, len(e.co.hostCommits()))
	}

	// Alex kommt (erste Abfrage mit ihm um 2:01:15) und geht um 2:04:50
	// wieder (die Abfrage um 2:05:00 sieht niemanden). Stand um 2:05: er hat
	// gespielt. Stand um 2:10: Beim Stand um 2:05 stand er noch in der
	// (bis zu 15 s alten) Liste, er kann danach noch gespielt haben. Danach
	// keiner mehr; enden würde es ohne Dauerbetrieb um 2:08:00.
	e.drv.advanceTo(2*time.Hour + time.Minute + 5*time.Second)
	e.srv.setPlayers("Alex")
	e.drv.advanceTo(2*time.Hour + 4*time.Minute + 50*time.Second)
	e.srv.setPlayers()
	for _, s := range []struct {
		at      time.Duration
		commits int
	}{
		{2*time.Hour + 5*time.Minute - time.Second, 0},
		{2*time.Hour + 5*time.Minute, 1},
		{2*time.Hour + 10*time.Minute - time.Second, 1},
		{2*time.Hour + 10*time.Minute, 2},
		{2*time.Hour + 30*time.Minute, 2},
	} {
		e.drv.advanceTo(s.at)
		if c := len(e.co.hostCommits()); c != s.commits {
			t.Fatalf("bei %v: %d Commits, erwartet %d", s.at, c, s.commits)
		}
	}
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server gestoppt, obwohl Dauerbetrieb an ist: %v", s)
	}

	// Ende wie bisher, wenn die Sitzung beendet wird: Schluss-Stand.
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n, n+1, n+2)
	checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
}

// Geht der Dauerbetrieb während des Leerlaufs aus (Haken weg, Notebook auf
// Akku), gilt wieder die Leerlauf-Frist – gezählt ab dem letzten Spieler
// oder ab dem Moment, in dem die Sitzung das Abschalten bemerkt (nächste
// Spielerabfrage), je nachdem, was später war. Ein kurz abgezogenes
// Notebook beendet die Sitzung also nicht.
func TestDauerbetriebOffEndsAfterTimeout(t *testing.T) {
	for _, tt := range []struct {
		name   string
		offAt  time.Duration // Dauerbetrieb aus (Alex geht um 0:30:05, zuletzt gesehen 0:30:00)
		onAt   time.Duration // wieder an (0: bleibt aus)
		stopAt time.Duration // 0: kein Ende bis 50:00
	}{
		// Frist seit 33:15 um; aus um 40:05, bemerkt 40:15 → Ende 43:15.
		{"lange leer, dann aus", 40*time.Minute + 5*time.Second, 0, 43*time.Minute + 15*time.Second},
		// Leer seit 30:15; aus um 30:20, bemerkt 30:30 → Ende 33:30, nicht 33:15.
		{"kurz nach dem letzten Spieler aus", 30*time.Minute + 20*time.Second, 0, 33*time.Minute + 30*time.Second},
		// 30 s vom Netz (bemerkt 40:15, wieder an 40:35): kein Ende.
		{"kurz abgezogen", 40*time.Minute + 5*time.Second, 40*time.Minute + 35*time.Second, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			co := newCoord(t, func(c *config.Config) { c.EmptyTimeout = 3 * time.Minute })
			e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
			var on atomic.Bool
			on.Store(true)
			e.d.Dauerbetrieb = on.Load
			r := e.start(t)

			e.drv.advanceTo(30*time.Minute + 5*time.Second)
			e.srv.setPlayers()
			e.drv.advanceTo(tt.offAt)
			if s := e.srv.stopTimes(); len(s) != 0 {
				t.Fatalf("Server gestoppt, obwohl Dauerbetrieb an ist: %v", s)
			}
			on.Store(false)
			if tt.onAt != 0 {
				e.drv.advanceTo(tt.onAt)
				on.Store(true)
			}
			if tt.stopAt == 0 {
				e.drv.advanceTo(50 * time.Minute)
				if s := e.srv.stopTimes(); len(s) != 0 {
					t.Fatalf("Server gestoppt, obwohl der Dauerbetrieb nur kurz aus war: %v", s)
				}
				r.cancel()
				if err := r.wait(t); err != nil {
					t.Fatalf("Run = %v", err)
				}
				return
			}
			e.drv.advanceTo(tt.stopAt - time.Second)
			if s := e.srv.stopTimes(); len(s) != 0 {
				t.Fatalf("Stop schon vor %v: %v", tt.stopAt, s)
			}
			e.drv.advanceTo(tt.stopAt)
			if err := r.wait(t); err != nil {
				t.Fatalf("Run = %v", err)
			}
			if s := e.srv.stopTimes(); len(s) != 1 || s[0] != tt.stopAt {
				t.Fatalf("Stop bei %v, erwartet genau einmal bei %v", s, tt.stopAt)
			}
			checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
		})
	}
}

// Scheitert im Dauerbetrieb ein Zwischenstand, kommt der nächste auf jeden
// Fall, auch wenn inzwischen niemand mehr spielt: Was gespielt wurde, ist
// noch nicht eingereicht.
func TestDauerbetriebRetriesFailedSnapshot(t *testing.T) {
	co := newCoord(t, func(c *config.Config) { c.EmptyTimeout = 3 * time.Minute })
	e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
	var on atomic.Bool
	on.Store(true)
	e.d.Dauerbetrieb = on.Load
	r := e.start(t)

	// Alex geht um 4:40 (die Abfrage um 4:45 sieht niemanden); beim Stand
	// um 5:00 ist also niemand online. Er scheitert beim Hochladen.
	e.drv.advanceTo(4*time.Minute + 40*time.Second)
	e.srv.setPlayers()
	e.co.failBlobPuts.Store(true)
	e.drv.advanceTo(5*time.Minute + 5*time.Second)
	e.co.failBlobPuts.Store(false)
	if _, _, barriers, _ := e.srv.counts(); barriers != 1 || len(e.co.hostCommits()) != 0 {
		t.Fatalf("um 5:05: SaveBarrier %d×, %d Commits, erwartet 1 und 0", barriers, len(e.co.hostCommits()))
	}
	// 10:00: der nachgeholte Stand; 15:00 und 20:00: niemand seitdem.
	for _, s := range []struct {
		at       time.Duration
		barriers int
		commits  int
	}{
		{10 * time.Minute, 2, 1},
		{20 * time.Minute, 2, 1},
	} {
		e.drv.advanceTo(s.at)
		if _, _, barriers, _ := e.srv.counts(); barriers != s.barriers || len(e.co.hostCommits()) != s.commits {
			t.Fatalf("bei %v: SaveBarrier %d×, %d Commits, erwartet %d und %d", s.at, barriers, len(e.co.hostCommits()), s.barriers, s.commits)
		}
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
}
