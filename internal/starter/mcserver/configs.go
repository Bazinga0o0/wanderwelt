package mcserver

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
)

// maxConfigSize begrenzt eine Konfigdatei beim Schreiben.
const maxConfigSize = 1 << 20

// applyConfigs legt die Konfigdateien aus dem Manifest im Server-Ordner dir
// ab (deps hat sie schon geladen und geprüft). enforce: schreibt neu, wenn
// die Datei fehlt oder der Hash abweicht; default: nur, wenn sie fehlt.
// Geschrieben wird atomar (temp + rename). Fehler werden nur geloggt – der
// Server startet in jedem Fall weiter.
func applyConfigs(dir string, cfgs []deps.ConfigPath) {
	for _, c := range cfgs {
		if err := applyConfig(dir, c); err != nil {
			log.Printf("mcserver: Konfigdatei %s nicht angewendet, der Server startet trotzdem: %v", c.Path, err)
		}
	}
}

func applyConfig(dir string, c deps.ConfigPath) error {
	// Noch einmal prüfen: Nur Pfade unterhalb von config/ im Server-Ordner.
	if err := api.ValidateConfigPath(c.Path); err != nil {
		return err
	}
	mode := c.Mode
	if mode == "" {
		mode = api.ConfigEnforce
	}
	if mode != api.ConfigEnforce && mode != api.ConfigDefault {
		return fmt.Errorf("unbekannter Modus %q", mode)
	}
	target := filepath.Join(dir, filepath.FromSlash(c.Path))

	fi, err := os.Lstat(target)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if exists && !fi.Mode().IsRegular() {
		return errors.New("Ziel ist keine normale Datei (Ordner oder Link) – bleibt unberührt")
	}
	if exists && mode == api.ConfigDefault {
		return nil // Host darf lokal ändern
	}
	if exists && deps.FileHashMatches(target, c.Hash) {
		return nil
	}

	data, err := readLimited(c.Src)
	if err != nil {
		return err
	}
	// Nach dem Laden muss der Inhalt zum Manifest passen; schützt auch
	// vor einem verfälschten Cache.
	if !deps.FileHashMatches(c.Src, c.Hash) {
		return errors.New("geladene Datei passt nicht mehr zum Hash aus dem Manifest")
	}

	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if err := insideDir(dir, parent); err != nil {
		return err
	}
	if err := writeFileAtomic(target, data, 0o644); err != nil {
		return err
	}
	if exists {
		log.Printf("mcserver: %s neu geschrieben (Vorgabe aus dem Manifest)", c.Path)
	} else {
		log.Printf("mcserver: %s angelegt (%s)", c.Path, mode)
	}
	return nil
}

// readLimited liest die Quelldatei, höchstens maxConfigSize Bytes.
func readLimited(p string) ([]byte, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxConfigSize {
		return nil, fmt.Errorf("%s ist keine normale Datei oder größer als %d Bytes", p, maxConfigSize)
	}
	return os.ReadFile(p)
}

// insideDir prüft nach Auflösen von Links, dass sub unter dir liegt.
func insideDir(dir, sub string) error {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(sub)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("Zielordner liegt außerhalb des Server-Ordners")
	}
	return nil
}
