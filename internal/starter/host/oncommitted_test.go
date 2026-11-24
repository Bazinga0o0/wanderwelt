package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// committedLog sammelt die Aufrufe von Deps.OnCommitted und liest dabei den
// Bereitstellungsordner, solange er noch gilt.
type committedLog struct {
	mu    sync.Mutex
	calls []Committed
	level []string // Inhalt von level.dat im Bereitstellungsordner beim Aufruf
}

func (l *committedLog) hook(c Committed) {
	data, _ := os.ReadFile(filepath.Join(c.Dir, "level.dat"))
	l.mu.Lock()
	l.calls = append(l.calls, c)
	l.level = append(l.level, string(data))
	l.mu.Unlock()
}

func TestOnCommittedAfterFinalStand(t *testing.T) {
	e := newHostEnv(t, []string{})
	var l committedLog
	e.d.OnCommitted = l.hook
	r := e.start(t)
	e.drv.advanceTo(2 * time.Minute)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	final := e.co.head(t)
	if len(l.calls) != 1 {
		t.Fatalf("%d Aufrufe von OnCommitted, erwartet genau einen", len(l.calls))
	}
	c := l.calls[0]
	if c.Generation != final.Number || c.WorldID != final.WorldID || c.Handover || c.MinecraftVersion != mcVersion {
		t.Fatalf("Committed %+v, erwartet Schluss-Stand %s/%d", c, final.WorldID, final.Number)
	}
	if c.Dir != filepath.Join(e.dir, StagingName) {
		t.Fatalf("Dir %q, erwartet der Bereitstellungsordner", c.Dir)
	}
	if l.level[0] != e.srv.saved() {
		t.Fatalf("level.dat im Stand %q, erwartet der Stand nach Stop %q", l.level[0], e.srv.saved())
	}
}

func TestOnCommittedHandoverStand(t *testing.T) {
	e, _, ch := newHandoverEnv(t, map[string]string{"Alex": "1", "Bob": "0"}, "Alex", "Bob")
	var l committedLog
	e.d.OnCommitted = l.hook
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
	if len(l.calls) != 1 {
		t.Fatalf("%d Aufrufe von OnCommitted, erwartet einen", len(l.calls))
	}
	c := l.calls[0]
	if !c.Handover || c.Generation != head.Number || c.WorldID != head.WorldID {
		t.Fatalf("Committed %+v, erwartet Übergabe-Stand %s/%d", c, head.WorldID, head.Number)
	}
	if l.level[0] != "flushed" {
		t.Fatalf("level.dat im Stand %q, erwartet den Übergabe-Stand (flushed)", l.level[0])
	}
}

// Nach einem neuen restage enthält der Bereitstellungsordner nicht mehr den
// eingereichten Stand: kein Aufruf, auch wenn das Bilden scheitert.
func TestRestageInvalidatesCommitted(t *testing.T) {
	fail := errors.New("kaputt")
	var err error
	s := &Session{
		clk:     SystemClock{},
		staging: "stage",
		stage: func(context.Context, string, string, *worldsync.HashCache) error {
			return err
		},
	}
	s.noteCommitted(&api.Generation{WorldID: "w", Number: 3}, false)
	if c := s.lastCommitted(); c == nil || c.Generation != 3 || c.Dir != "stage" {
		t.Fatalf("lastCommitted = %+v", c)
	}
	err = fail
	if got := s.restage(context.Background()); got != fail {
		t.Fatalf("restage = %v", got)
	}
	if c := s.lastCommitted(); c != nil {
		t.Fatalf("nach restage noch %+v", c)
	}
}
