package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/sicherung"
)

// Eine gelungene Sicherung schreibt data_dir/sicherung.json {zeit, ziel} und
// aktualisiert sie bei der nächsten; eine gescheiterte lässt sie, wie sie ist.
func TestSicherungSchreibtMarke(t *testing.T) {
	cfgPath, _, dir := setupWithData(t, nil)
	old := dienstLaeuft
	defer func() { dienstLaeuft = old }()
	dienstLaeuft = func(string) bool { return false }

	f := &fakeZeit{now: zeit0}
	run := func(ziel string) (int, string) {
		var out, errOut bytes.Buffer
		code := sicherungMain([]string{ziel, "--config", cfgPath}, f.env(), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	if _, ok, _ := sicherung.MarkeLesen(dir); ok {
		t.Fatal("Marke schon vor der ersten Sicherung")
	}
	ziel1 := filepath.Join(t.TempDir(), "s1")
	if code, msg := run(ziel1); code != 0 {
		t.Fatalf("Sicherung: %d %s", code, msg)
	}
	m, ok, err := sicherung.MarkeLesen(dir)
	if err != nil || !ok || !m.Zeit.Equal(zeit0) || m.Ziel != ziel1 {
		t.Fatalf("Marke: %+v ok=%v err=%v (Ziel %s)", m, ok, err, ziel1)
	}

	// Zweite Sicherung später: Marke aktualisiert.
	f.now = zeit0.Add(48 * time.Hour)
	ziel2 := filepath.Join(t.TempDir(), "s2")
	if code, msg := run(ziel2); code != 0 {
		t.Fatalf("zweite Sicherung: %d %s", code, msg)
	}
	if m, _, _ := sicherung.MarkeLesen(dir); !m.Zeit.Equal(zeit0.Add(48*time.Hour)) || m.Ziel != ziel2 {
		t.Fatalf("Marke nach der zweiten: %+v", m)
	}

	// Gescheitert (Ziel nicht leer): Marke bleibt.
	f.now = zeit0.Add(72 * time.Hour)
	if code, msg := run(ziel2); code != 1 || !strings.Contains(msg, "nicht leer") {
		t.Fatalf("Sicherung in belegtes Ziel: %d %s", code, msg)
	}
	if m, _, _ := sicherung.MarkeLesen(dir); !m.Zeit.Equal(zeit0.Add(48 * time.Hour)) {
		t.Fatalf("Marke nach Fehlschlag verändert: %+v", m)
	}
}
