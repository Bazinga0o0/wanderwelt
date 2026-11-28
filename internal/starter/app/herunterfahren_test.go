package app

import (
	"strings"
	"testing"
)

// Beim Herunterfahren von Windows sagt das Fenster, was der PC tut und wie
// lange es höchstens dauert; sonst bleibt die kurze Meldung.
func TestStoppingDetail(t *testing.T) {
	a := &App{}
	if d := a.stoppingDetail(); d != "stoppt den Server und lädt den Stand hoch …" {
		t.Fatalf("normal: %q", d)
	}
	a.ending = true
	d := a.stoppingDetail()
	if !strings.Contains(d, "Windows fährt herunter") || !strings.Contains(d, "bis zu 90 s") {
		t.Fatalf("beim Herunterfahren: %q", d)
	}
}
