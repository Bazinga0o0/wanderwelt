package sicherung

import (
	"os"
	"path/filepath"
	"testing"
)

func schreibe(t *testing.T, path, inhalt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(inhalt), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lies(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Blobs sind Hardlinks (gleiche Datei, kein zweiter Platz), Metadaten und
// Konfiguration echte Kopien; angefangene Uploads fehlen.
func TestAnlegenHardlinksFuerBlobsKopienFuerMetadaten(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	dst := filepath.Join(t.TempDir(), "sicherung")
	blob := filepath.Join(src, "store", "blobs", "ab", "ab12")
	schreibe(t, blob, "blob-inhalt")
	schreibe(t, filepath.Join(src, "store", "current.json"), `{"head":1}`)
	schreibe(t, filepath.Join(src, "store", "worlds", "w1", "generations", "1.json"), `{}`)
	schreibe(t, filepath.Join(src, "store", "format.json"), `{"version":1}`)
	schreibe(t, filepath.Join(src, "lease.json"), `{}`)
	schreibe(t, filepath.Join(src, "wartung-bericht.json"), `{}`)
	schreibe(t, filepath.Join(src, "store", "tmp", "cd34.part"), "halb")

	res, err := Anlegen(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verlinkt != 1 || res.Kopiert != 4 {
		t.Fatalf("Ergebnis %+v, erwartet 1 verlinkt und 4 kopiert", res)
	}

	a, _ := os.Stat(blob)
	b, err := os.Stat(filepath.Join(dst, "store", "blobs", "ab", "ab12"))
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("Blob ist kein Hardlink: %v", err)
	}
	// Ein Blob, den ein neueres Programm löscht, bleibt in der Sicherung.
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	if got := lies(t, filepath.Join(dst, "store", "blobs", "ab", "ab12")); got != "blob-inhalt" {
		t.Fatalf("Blob der Sicherung: %q", got)
	}

	// Metadaten sind echte Kopien: Ändern im Original fasst die Sicherung nicht an.
	cur := filepath.Join(src, "store", "current.json")
	schreibe(t, cur, `{"head":2}`)
	if got := lies(t, filepath.Join(dst, "store", "current.json")); got != `{"head":1}` {
		t.Fatalf("current.json der Sicherung folgt dem Original: %q", got)
	}
	for _, rel := range []string{"store/worlds/w1/generations/1.json", "store/format.json", "lease.json"} {
		if lies(t, filepath.Join(dst, filepath.FromSlash(rel))) == "" {
			t.Errorf("%s fehlt oder ist leer", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "store", "tmp", "cd34.part")); err == nil {
		t.Error("angefangener Upload gesichert")
	}
	if _, err := os.Stat(filepath.Join(dst, "store", "tmp")); err != nil {
		t.Error("store/tmp fehlt in der Sicherung")
	}
	if _, err := os.Stat(filepath.Join(dst, "wartung-bericht.json")); err == nil {
		t.Error("Wartungsbericht gesichert")
	}
}

func TestAnlegenLehntUngeeigneteZieleAb(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	schreibe(t, filepath.Join(src, "lease.json"), `{}`)

	if _, err := Anlegen(src, filepath.Join(src, "innen")); err == nil {
		t.Error("Ziel in der Quelle angenommen")
	}
	voll := t.TempDir()
	schreibe(t, filepath.Join(voll, "x"), "x")
	if _, err := Anlegen(src, voll); err == nil {
		t.Error("nicht leeres Ziel angenommen")
	}
	if _, err := Anlegen(filepath.Join(src, "gibt-es-nicht"), filepath.Join(t.TempDir(), "z")); err == nil {
		t.Error("fehlende Quelle angenommen")
	}
	// Ein leeres vorhandenes Ziel ist in Ordnung.
	if _, err := Anlegen(src, t.TempDir()); err != nil {
		t.Errorf("leeres Ziel: %v", err)
	}
}
