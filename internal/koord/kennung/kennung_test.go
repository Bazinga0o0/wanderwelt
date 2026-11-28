package kennung

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIDStabilUndGrossKleinEgal(t *testing.T) {
	k, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	a := k.ID("Max.Muster@Example.org")
	if len(a) != Laenge || !IstID(a) {
		t.Fatalf("ID %q: erwartet %d Hex-Zeichen", a, Laenge)
	}
	if b := k.ID("  max.muster@example.ORG "); a != b {
		t.Errorf("Schreibweise ändert die ID: %q != %q", a, b)
	}
	if k.ID("anders@example.org") == a {
		t.Error("verschiedene Adressen, gleiche ID")
	}
	if k.ID("") != "" {
		t.Error("leere Adresse braucht keine ID")
	}
}

func TestIDVerschiedeneSchluessel(t *testing.T) {
	k1, _ := New(bytes.Repeat([]byte{1}, 32))
	k2, _ := New(bytes.Repeat([]byte{2}, 32))
	if k1.ID("a@b.de") == k2.ID("a@b.de") {
		t.Error("gleiche ID trotz anderem Schlüssel: ohne Schlüssel ließe sich raten")
	}
}

func TestIstID(t *testing.T) {
	for s, want := range map[string]bool{
		"0123456789abcdef":  true,
		"0123456789ABCDEF":  false,
		"0123456789abcde":   false,
		"0123456789abcdefa": false,
		"a@b.de":            false,
		"0123456789abcdeg":  false,
		"":                  false,
	} {
		if IstID(s) != want {
			t.Errorf("IstID(%q) = %v", s, !want)
		}
	}
}

func TestLadenLegtSchluesselAnUndBleibtStabil(t *testing.T) {
	dir := t.TempDir()
	k1, neu, err := Laden(dir)
	if err != nil || !neu {
		t.Fatalf("Laden: neu=%v err=%v", neu, err)
	}
	path := filepath.Join(dir, Datei)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("idkey hat %v, erwartet 0600", fi.Mode().Perm())
	}
	k2, neu, err := Laden(dir)
	if err != nil || neu {
		t.Fatalf("zweites Laden: neu=%v err=%v", neu, err)
	}
	if k1.ID("a@b.de") != k2.ID("a@b.de") {
		t.Error("ID ändert sich über einen Neustart")
	}
	// Keine Hilfsdateien zurücklassen.
	es, _ := os.ReadDir(dir)
	if len(es) != 1 {
		t.Errorf("data_dir enthält %d Dateien, erwartet nur idkey", len(es))
	}
	if k3, ok, err := Lesen(dir); err != nil || !ok || k3.ID("a@b.de") != k1.ID("a@b.de") {
		t.Errorf("Lesen: ok=%v err=%v", ok, err)
	}
}

func TestLesenOhneDateiLegtNichtsAn(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Lesen(dir); ok || err != nil {
		t.Fatalf("Lesen: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, Datei)); !os.IsNotExist(err) {
		t.Error("Lesen hat eine Datei angelegt")
	}
}

func TestKaputteDateiBleibtUndNenntDenInhaltNicht(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Datei)
	geheim := "zz-nicht-hex-GEHEIM"
	if err := os.WriteFile(path, []byte(geheim), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Laden(dir)
	if err == nil {
		t.Fatal("kaputte Datei wurde akzeptiert")
	}
	if strings.Contains(err.Error(), geheim) {
		t.Errorf("Fehlermeldung enthält den Dateiinhalt: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != geheim {
		t.Error("kaputte Datei wurde überschrieben")
	}
}

func TestLadenOhneDataDirIstFluechtig(t *testing.T) {
	a, neu, err := Laden("")
	if err != nil || !neu {
		t.Fatal(neu, err)
	}
	b, _, _ := Laden("")
	if a.ID("x@y.de") == b.ID("x@y.de") {
		t.Error("zwei flüchtige Schlüssel gleich")
	}
}
