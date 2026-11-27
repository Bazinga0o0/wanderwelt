package sicherung

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMarkeSchreibenLesen(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := MarkeLesen(dir); ok || err != nil {
		t.Fatalf("ohne Datei: ok=%v err=%v", ok, err)
	}
	zeit := time.Date(2026, 11, 26, 10, 30, 0, 0, time.UTC)
	if err := MarkeSchreiben(dir, Marke{Zeit: zeit, Ziel: "/var/backup/a"}); err != nil {
		t.Fatal(err)
	}
	m, ok, err := MarkeLesen(dir)
	if err != nil || !ok || !m.Zeit.Equal(zeit) || m.Ziel != "/var/backup/a" {
		t.Fatalf("gelesen: %+v ok=%v err=%v", m, ok, err)
	}
	// Aktualisieren ersetzt die Marke und lässt keine Temp-Dateien liegen.
	zeit2 := zeit.Add(24 * time.Hour)
	if err := MarkeSchreiben(dir, Marke{Zeit: zeit2, Ziel: "/var/backup/b"}); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := MarkeLesen(dir); !m.Zeit.Equal(zeit2) || m.Ziel != "/var/backup/b" {
		t.Fatalf("aktualisiert: %+v", m)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != MarkeName {
		t.Fatalf("Ordner: %v", entries)
	}
	// Das Dateiformat steht fest: {zeit, ziel}.
	data, _ := os.ReadFile(MarkePfad(dir))
	if !strings.Contains(string(data), `"zeit": "2026-11-27T10:30:00Z"`) || !strings.Contains(string(data), `"ziel": "/var/backup/b"`) {
		t.Errorf("Inhalt: %s", data)
	}
}

// Eine kaputte oder leere Marke ist ein Fehler (die Gesundheit zeigt
// „unbekannt“), kein Absturz.
func TestMarkeKaputt(t *testing.T) {
	dir := t.TempDir()
	for _, inhalt := range []string{"kein json", `{"ziel": "x"}`, `{}`} {
		if err := os.WriteFile(filepath.Join(dir, MarkeName), []byte(inhalt), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := MarkeLesen(dir); ok || err == nil {
			t.Errorf("%q: ok=%v err=%v", inhalt, ok, err)
		}
	}
	// Ein nicht vorhandener Ordner scheitert beim Schreiben.
	if err := MarkeSchreiben(filepath.Join(dir, "fehlt"), Marke{Zeit: time.Now()}); err == nil {
		t.Error("Schreiben in fehlenden Ordner ohne Fehler")
	}
}
