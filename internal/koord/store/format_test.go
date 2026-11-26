package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFormatRaw(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FormatPath(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Ein neuer Speicher bekommt gleich die aktuelle Version; ein zweites Open
// ändert nichts.
func TestFormatNeuerSpeicher(t *testing.T) {
	dir := t.TempDir()
	openStore(t, dir, 1<<30)
	v, found, err := ReadFormat(dir)
	if err != nil || !found || v != FormatVersion() {
		t.Fatalf("ReadFormat: %d %v %v", v, found, err)
	}
	openStore(t, dir, 1<<30)
}

// Ein Speicher aus der Zeit vor der Versionierung (Daten, aber ohne
// format.json) wird migriert, seine Stände und Blobs bleiben unberührt.
func TestFormatAltSpeicherWirdMigriert(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	f := putBlob(t, s, "level.dat", []byte("alt"))
	commit(t, s, 0, true, t0, f)
	if err := os.Remove(FormatPath(dir)); err != nil {
		t.Fatal(err)
	}
	s2 := openStore(t, dir, 1<<30)
	if v, found, err := ReadFormat(dir); err != nil || !found || v != FormatVersion() {
		t.Fatalf("nach Migration: %d %v %v", v, found, err)
	}
	if _, n := head(t, s2); n != 1 || !blobOK(t, s2, f) {
		t.Fatalf("Inhalt nach Migration verändert (Head %d)", n)
	}
}

// Eine unbekannte neuere Version startet nicht und ändert nichts.
func TestFormatNeuereVersionStartetNicht(t *testing.T) {
	dir := t.TempDir()
	writeFormatRaw(t, dir, `{"version": 99}`)
	_, err := Open(dir, 1<<30)
	if !errors.Is(err, ErrFormatTooNew) {
		t.Fatalf("Open: %v, erwartet ErrFormatTooNew", err)
	}
	if data, _ := os.ReadFile(FormatPath(dir)); string(data) != `{"version": 99}` {
		t.Fatalf("format.json verändert: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs")); err == nil {
		t.Error("ein zu neuer Speicher wurde angefasst (blobs/ angelegt)")
	}
}

func TestFormatKaputt(t *testing.T) {
	dir := t.TempDir()
	writeFormatRaw(t, dir, `kein json`)
	if _, err := Open(dir, 1<<30); err == nil {
		t.Fatal("kaputte format.json angenommen")
	}
	writeFormatRaw(t, dir, `{"version": 0}`)
	if _, err := Open(dir, 1<<30); err == nil {
		t.Fatal("Version 0 in format.json angenommen")
	}
}

// Die Migrationen laufen nacheinander von der gefundenen Version aus; jede
// Stufe wird festgehalten, ein Fehler stoppt Open.
func TestFormatMigrationenVorwaerts(t *testing.T) {
	oldM := migrations
	defer func() { migrations = oldM }()
	var ran []int
	migrations = []func(string) error{
		func(string) error { ran = append(ran, 0); return nil },
		func(string) error { ran = append(ran, 1); return nil },
		func(string) error { ran = append(ran, 2); return errors.New("kaputt") },
	}
	dir := t.TempDir()
	writeFormatRaw(t, dir, `{"version": 1}`)
	if _, err := Open(dir, 1<<30); err == nil {
		t.Fatal("Fehler der Migration nicht gemeldet")
	}
	if len(ran) != 2 || ran[0] != 1 || ran[1] != 2 {
		t.Fatalf("Migrationen liefen: %v, erwartet [1 2]", ran)
	}
	// Stufe 1 → 2 ist festgehalten, 2 → 3 nicht.
	if v, _, _ := ReadFormat(dir); v != 2 {
		t.Fatalf("Version nach Abbruch: %d, erwartet 2", v)
	}
}

// Prune als Probe meldet, was Prune löschen würde, und löscht nichts.
func TestPruneProbe(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	old := putBlob(t, s, "a.dat", []byte("verwaist"))
	_ = old // nie in einem Stand genannt → Prune würde ihn löschen, sobald er nicht mehr „frisch“ ist
	keep := putBlob(t, s, "level.dat", []byte("behalten"))
	g := commit(t, s, 0, true, t0, keep) // der Commit lässt „frisch“ leer
	if g.Number != 1 {
		t.Fatal(g.Number)
	}
	rep, err := s.PruneProbe(t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blobs != 1 || rep.Bytes != int64(len("verwaist")) || rep.Generations != 0 {
		t.Fatalf("Probe: %+v", rep)
	}
	if !blobOK(t, s, old) {
		t.Fatal("die Probe hat den Blob gelöscht")
	}
	if err := s.Prune(t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if blobOK(t, s, old) {
		t.Fatal("Prune hat den verwaisten Blob nicht gelöscht – die Probe war also kein Abbild")
	}
	if rep, _ := s.PruneProbe(t0.Add(time.Minute)); !rep.Empty() {
		t.Fatalf("Probe nach Prune: %+v", rep)
	}
}
