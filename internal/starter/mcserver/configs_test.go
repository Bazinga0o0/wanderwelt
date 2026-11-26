package mcserver

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
)

func cfgFor(t *testing.T, path, content, mode string) deps.ConfigPath {
	t.Helper()
	src := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return deps.ConfigPath{Path: path, Src: src, Hash: "sha256:" + hex.EncodeToString(sum[:]), Mode: mode}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyConfigsEnforce(t *testing.T) {
	dir := t.TempDir()
	c := cfgFor(t, "config/sub/v.json", `{"x":1}`, "")
	target := filepath.Join(dir, "config", "sub", "v.json")

	applyConfigs(dir, []deps.ConfigPath{c}) // fehlt: angelegt (Standard enforce)
	if got := readFile(t, target); got != `{"x":1}` {
		t.Fatalf("angelegt: %q", got)
	}
	// Lokal geändert: enforce stellt wieder her.
	if err := os.WriteFile(target, []byte("lokal"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyConfigs(dir, []deps.ConfigPath{c})
	if got := readFile(t, target); got != `{"x":1}` {
		t.Fatalf("nicht wiederhergestellt: %q", got)
	}
	// Hash passt: nicht neu geschrieben (Änderungszeit bleibt).
	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(target, past, past); err != nil {
		t.Fatal(err)
	}
	applyConfigs(dir, []deps.ConfigPath{c})
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(past) {
		t.Error("Datei mit passendem Hash wurde neu geschrieben")
	}
	// Keine Reste (Temp-Dateien) im Ordner.
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Errorf("Reste: %v", entries)
	}
}

func TestApplyConfigsDefault(t *testing.T) {
	dir := t.TempDir()
	c := cfgFor(t, "config/d.toml", "vorgabe", "default")
	target := filepath.Join(dir, "config", "d.toml")

	applyConfigs(dir, []deps.ConfigPath{c})
	if got := readFile(t, target); got != "vorgabe" {
		t.Fatalf("angelegt: %q", got)
	}
	if err := os.WriteFile(target, []byte("lokal geändert"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyConfigs(dir, []deps.ConfigPath{c})
	if got := readFile(t, target); got != "lokal geändert" {
		t.Fatalf("default hat überschrieben: %q", got)
	}
}

// Hash-Abweichung der Cache-Datei: nichts wird geschrieben, eine vorhandene
// lokale Datei bleibt unverändert.
func TestApplyConfigsHashMismatch(t *testing.T) {
	dir := t.TempDir()
	c := cfgFor(t, "config/a.json", "inhalt", "enforce")
	c.Hash = "sha256:" + hex.EncodeToString(make([]byte, 32)) // passt nicht zur Cache-Datei
	target := filepath.Join(dir, "config", "a.json")

	applyConfigs(dir, []deps.ConfigPath{c})
	if _, err := os.Stat(target); err == nil {
		t.Fatal("Datei mit falschem Hash geschrieben")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("alt"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyConfigs(dir, []deps.ConfigPath{c})
	if got := readFile(t, target); got != "alt" {
		t.Fatalf("vorhandene Datei verändert: %q", got)
	}
}

// Böse Pfade (die deps nie liefern würde) schreiben nichts.
func TestApplyConfigsEvilPaths(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../evil.json", "config/../../evil.json", "/abs/evil.json", "server.properties",
		"mods/x.json", `config\..\..\evil.json`, "C:/evil.json", "config/a.json:ads", "config/a.exe"} {
		applyConfigs(dir, []deps.ConfigPath{cfgFor(t, p, "böse", "enforce")})
	}
	var found []string
	filepath.WalkDir(base, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Fatalf("Dateien geschrieben: %v", found)
	}
}

// Ein Link als config-Ordner, der aus dem Server-Ordner hinauszeigt, wird
// nicht benutzt.
func TestApplyConfigsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Symlinks brauchen unter Windows Rechte")
	}
	base := t.TempDir()
	dir, outside := filepath.Join(base, "server"), filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	applyConfigs(dir, []deps.ConfigPath{cfgFor(t, "config/a.json", "x", "enforce")})
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("außerhalb geschrieben: %v", entries)
	}
}

// Ein Fehler bei einer Konfigdatei hält die anderen nicht auf.
func TestApplyConfigsContinuesAfterError(t *testing.T) {
	dir := t.TempDir()
	bad := cfgFor(t, "config/bad.json", "x", "enforce")
	bad.Src = filepath.Join(dir, "gibt-es-nicht")
	good := cfgFor(t, "config/good.json", "ok", "enforce")
	applyConfigs(dir, []deps.ConfigPath{bad, good})
	if got := readFile(t, filepath.Join(dir, "config", "good.json")); got != "ok" {
		t.Fatalf("zweite Datei nicht angewendet: %q", got)
	}
}

// Prepare schlägt wegen einer kaputten Konfigdatei nicht fehl.
func TestPrepareWithBrokenConfigStillSucceeds(t *testing.T) {
	root := t.TempDir()
	java := filepath.Join(root, "java.exe")
	jar := filepath.Join(root, "fabric.jar")
	for _, p := range []string{java, jar} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bad := cfgFor(t, "config/bad.json", "x", "enforce")
	bad.Src = filepath.Join(root, "weg")
	good := cfgFor(t, "config/ok.json", "ok", "enforce")

	srv := filepath.Join(root, "srv")
	f := &fabric{}
	err := f.Prepare(t.Context(), PrepareInput{
		Dir:     srv,
		Paths:   &deps.Paths{Java: java, FabricServerJar: jar, Configs: []deps.ConfigPath{bad, good}},
		Profile: api.Profile{XmxMB: minXmxMB},
	})
	if err != nil {
		t.Fatalf("Prepare scheiterte an kaputter Konfigdatei: %v", err)
	}
	if got := readFile(t, filepath.Join(srv, "config", "ok.json")); got != "ok" {
		t.Fatalf("ok.json: %q", got)
	}
}
