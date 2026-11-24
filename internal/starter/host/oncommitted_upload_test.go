package host

import (
	"testing"
	"time"
)

// Scheitert der Schluss-Upload, meldet die Sitzung den bereitgestellten,
// nicht eingereichten Stand (die Basis als Generation).
func TestOnCommittedNotUploadedWhenFinalFails(t *testing.T) {
	e := newHostEnv(t, []string{})
	e.co.failBlobPuts.Store(true)
	var l committedLog
	e.d.OnCommitted = l.hook
	r := e.start(t)
	e.drv.advanceTo(10 * time.Minute)
	if err := r.wait(t); err == nil {
		t.Fatal("Run ohne Fehler trotz gescheitertem Schluss-Upload")
	}
	if len(l.calls) != 1 {
		t.Fatalf("%d Aufrufe von OnCommitted, erwartet einen", len(l.calls))
	}
	c := l.calls[0]
	if !c.NotUploaded || c.Generation != e.head.Number || c.WorldID != e.head.WorldID {
		t.Fatalf("Committed %+v, erwartet NotUploaded auf der Basis %s/%d", c, e.head.WorldID, e.head.Number)
	}
	if l.level[0] != e.srv.saved() {
		t.Fatalf("level.dat im Stand %q, erwartet der Stand nach Stop %q", l.level[0], e.srv.saved())
	}
}
