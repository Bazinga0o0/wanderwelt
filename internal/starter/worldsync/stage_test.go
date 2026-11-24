package worldsync_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// laterCancel ist ein Kontext, dessen Err die ersten free Aufrufe nil
// liefert und danach context.Canceled (Abbruch mitten im Bereitstellen).
type laterCancel struct {
	context.Context
	free  int32
	calls atomic.Int32
}

func (c *laterCancel) Err() error {
	if c.calls.Add(1) > c.free {
		return context.Canceled
	}
	return nil
}

func TestStageContextStops(t *testing.T) {
	world, staging := t.TempDir(), filepath.Join(t.TempDir(), "staging")
	for _, name := range []string{"a.dat", "b.dat", "c.dat"} {
		if err := os.WriteFile(filepath.Join(world, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	countFiles := func() int {
		entries, _ := os.ReadDir(staging)
		return len(entries)
	}

	// Abbruch nach der Prüfung am Anfang: Keine Datei wird mehr kopiert.
	err := worldsync.StageContext(&laterCancel{Context: context.Background(), free: 1}, world, staging)
	if !errors.Is(err, context.Canceled) || countFiles() != 0 {
		t.Fatalf("StageContext = %v mit %d kopierten Dateien, erwartet Abbruch ohne Kopie", err, countFiles())
	}

	// Schon beendet, obwohl nichts zu kopieren wäre: trotzdem Abbruch.
	if err := worldsync.StageContext(context.Background(), world, staging); err != nil || countFiles() != 3 {
		t.Fatalf("StageContext = %v mit %d Dateien", err, countFiles())
	}
	cause := errors.New("Ende verlangt")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	if err := worldsync.StageContext(ctx, world, staging); !errors.Is(err, cause) {
		t.Fatalf("StageContext = %v, erwartet %v", err, cause)
	}
}
