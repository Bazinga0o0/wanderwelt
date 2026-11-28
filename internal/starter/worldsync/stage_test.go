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

// hookCtx ruft bei jedem Err-Aufruf n (ab 1) das Stück hook(n) auf; so läuft
// eine Änderung der Welt genau zwischen zwei Schritten von Stage.
type hookCtx struct {
	context.Context
	n    atomic.Int32
	hook func(n int32)
}

func (c *hookCtx) Err() error {
	c.hook(c.n.Add(1))
	return nil
}

func stageWorld(t *testing.T) (world, staging string) {
	t.Helper()
	world, staging = t.TempDir(), filepath.Join(t.TempDir(), "staging")
	for _, name := range []string{"a.dat", "b.dat", "c.dat"} {
		if err := os.WriteFile(filepath.Join(world, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return world, staging
}

func changedPath(t *testing.T, err error, world, rel string) {
	t.Helper()
	if !errors.Is(err, worldsync.ErrChangedWhileCopying) {
		t.Fatalf("Stage = %v, erwartet ErrChangedWhileCopying", err)
	}
	ce, ok := errors.AsType[*worldsync.ChangedWhileCopyingError](err)
	if !ok || ce.Path != filepath.Join(world, rel) {
		t.Fatalf("Stage = %v, erwartet die Datei %s", err, rel)
	}
}

// Ändert sich eine Datei, nachdem sie schon kopiert war (während Stage die
// nächsten kopiert), meldet Stage das zum Schluss: Der Stand wäre sonst aus
// verschiedenen Zeitpunkten gemischt.
func TestStageDetectsChangeAfterOwnCopy(t *testing.T) {
	world, staging := stageWorld(t)
	// Err-Aufrufe: 1 Anfang, 2–4 vor a, b, c, 5 vor der Schlussprüfung.
	ctx := &hookCtx{Context: context.Background(), hook: func(n int32) {
		if n == 3 { // a.dat ist kopiert, b.dat noch nicht
			if err := os.WriteFile(filepath.Join(world, "a.dat"), []byte("a.dat neu und länger"), 0o644); err != nil {
				t.Error(err)
			}
		}
	}}
	changedPath(t, worldsync.StageContext(ctx, world, staging), world, "a.dat")
}

// Auch eine Datei, die gar nicht kopiert werden musste (schon bereitgestellt),
// und eine neue Datei fallen auf.
func TestStageDetectsChangeOfUncopiedAndNewFile(t *testing.T) {
	world, staging := stageWorld(t)
	if err := worldsync.StageContext(context.Background(), world, staging); err != nil {
		t.Fatal(err)
	}
	// Zweiter Lauf: nichts zu kopieren; Err-Aufrufe: 1 Anfang, 2 Schlussprüfung.
	ctx := &hookCtx{Context: context.Background(), hook: func(n int32) {
		if n == 2 {
			if err := os.WriteFile(filepath.Join(world, "b.dat"), []byte("b.dat neu und länger"), 0o644); err != nil {
				t.Error(err)
			}
		}
	}}
	changedPath(t, worldsync.StageContext(ctx, world, staging), world, "b.dat")

	// Bereitstellen, dann kommt während des Laufs eine Datei dazu.
	if err := worldsync.StageContext(context.Background(), world, staging); err != nil {
		t.Fatal(err)
	}
	ctx = &hookCtx{Context: context.Background(), hook: func(n int32) {
		if n == 2 {
			if err := os.WriteFile(filepath.Join(world, "d.dat"), []byte("d"), 0o644); err != nil {
				t.Error(err)
			}
		}
	}}
	changedPath(t, worldsync.StageContext(ctx, world, staging), world, "d.dat")

	// Und eine verschwundene.
	if err := worldsync.StageContext(context.Background(), world, staging); err != nil {
		t.Fatal(err)
	}
	ctx = &hookCtx{Context: context.Background(), hook: func(n int32) {
		if n == 2 {
			os.Remove(filepath.Join(world, "c.dat"))
		}
	}}
	changedPath(t, worldsync.StageContext(ctx, world, staging), world, "c.dat")
}
