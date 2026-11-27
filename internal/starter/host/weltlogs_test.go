package host

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// weltLogsLog hält fest, wann Deps.WeltLogs gerufen wurde: nie, während der
// Server die Welt für einen Stand angehalten hat (zwischen SaveBarrier und
// release), und jedes Mal mit dem Weltordner. Der Haken legt wie der echte
// eine Datei in wanderwelt-logs/ ab.
type weltLogsLog struct {
	mu      sync.Mutex
	dirs    []string
	holding int // Aufrufe zwischen SaveBarrier und release
	saveOff int // Aufrufe, nachdem der Befehl save-off schon gegeben war
	e       *hostEnv
	paniken bool
	zaehler int
}

func (l *weltLogsLog) hook(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dirs = append(l.dirs, dir)
	if l.e.srv.isHolding() {
		l.holding++
	}
	l.zaehler++
	for _, c := range l.e.srv.commands() {
		if c == "save-off" {
			l.saveOff++
			break
		}
	}
	os.MkdirAll(filepath.Join(dir, "wanderwelt-logs"), 0o755)
	os.WriteFile(filepath.Join(dir, "wanderwelt-logs", "2026-11-27-abc123.log"), []byte("Zeile "+time.Duration(l.zaehler).String()+"\n"), 0o644)
	if l.paniken {
		panic("kaputter Haken")
	}
}

// Zwischenstände und Schluss-Stand: Der Haken läuft vor jedem Stand, nie
// zwischen save-off und save-on, und die Datei steht im eingereichten Stand.
func TestWeltLogsVorJedemStand(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	l := &weltLogsLog{e: e}
	e.d.WeltLogs = l.hook
	r := e.start(t)
	e.drv.advanceTo(5*time.Minute + time.Second) // 1. Zwischenstand
	if c := len(e.co.hostCommits()); c != 1 {
		t.Fatalf("%d Commits nach 5 min, erwartet 1", c)
	}
	e.drv.advanceTo(10*time.Minute + time.Second) // 2. Zwischenstand
	e.srv.setPlayers()
	e.drv.advanceTo(30 * time.Minute) // Leerlauf, Ende, Schluss-Stand
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	commits := e.co.hostCommits()
	if len(commits) < 3 {
		t.Fatalf("%d Commits, erwartet mindestens 3 (2 Zwischenstände, Schluss-Stand)", len(commits))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.dirs) != len(commits) {
		t.Fatalf("Haken %d×, Commits %d×: je Stand genau einmal erwartet", len(l.dirs), len(commits))
	}
	for _, d := range l.dirs {
		if d != e.srv.world {
			t.Errorf("Haken bekam %q, erwartet der Weltordner %q", d, e.srv.world)
		}
	}
	if l.holding != 0 {
		t.Errorf("Haken lief %d× zwischen SaveBarrier und release", l.holding)
	}
	// Die Datei ist in jedem eingereichten Stand (also im Weltordner, bevor
	// bereitgestellt wurde).
	for i, c := range commits {
		if fileSHA(t, c, "wanderwelt-logs/2026-11-27-abc123.log") == "" {
			t.Errorf("Stand %d ohne die Log-Datei", i+1)
		}
	}
}

// Ein Absturz im Haken verhindert keinen Stand.
func TestWeltLogsAbsturzStoertNicht(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	l := &weltLogsLog{e: e, paniken: true}
	e.d.WeltLogs = l.hook
	r := e.start(t)
	e.drv.advanceTo(5*time.Minute + time.Second)
	if c := len(e.co.hostCommits()); c != 1 {
		t.Fatalf("%d Commits nach 5 min trotz Absturz im Haken, erwartet 1", c)
	}
	e.srv.setPlayers()
	e.drv.advanceTo(30 * time.Minute)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h := e.co.head(t); h.Number < e.head.Number+2 {
		t.Fatalf("Head %d: der Schluss-Stand fehlt", h.Number)
	}
}

// Übergabe: Der Haken läuft vor dem Einfrieren (vor save-off), die Datei
// steht im Übergabe-Stand.
func TestWeltLogsVorUebergabe(t *testing.T) {
	e, _, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	l := &weltLogsLog{e: e}
	e.d.WeltLogs = l.hook
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	e.trigger(ch)
	e.drv.advanceTo(31 * time.Second)
	e.co.hbResp.Store(&api.HeartbeatResponse{Handover: &api.HandoverState{
		HandoverID: hoID, State: api.HandoverReady, Address: "neu.example.org:15566",
	}})
	e.drv.advanceTo(120 * time.Second)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	head := e.co.head(t)
	if genSHA(t, head, "wanderwelt-logs/2026-11-27-abc123.log") == "" {
		t.Fatal("Log-Datei fehlt im Übergabe-Stand")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.dirs) != 1 || l.holding != 0 || l.saveOff != 0 {
		t.Fatalf("Haken %d× (davon %d× nach save-off), erwartet 1× und vor save-off", len(l.dirs), l.saveOff)
	}
}
