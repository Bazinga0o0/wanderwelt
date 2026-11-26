package store

// Format-Version des Speichers (store/format.json).
//
// Jeder Speicher trägt seine Format-Version. Open führt die Migrationen von
// der gefundenen bis zur aktuellen Version nacheinander aus (nur vorwärts)
// und schreibt danach die neue Version. Eine Version, die dieses Programm
// nicht kennt (neuer als FormatVersion), startet nicht: Ein altes Programm
// soll nie an Daten arbeiten, die ein neueres angelegt hat.
//
// Ein Speicher ohne format.json, der schon Daten hat (current.json oder
// Welten), stammt aus der Zeit vor der Versionierung und gilt als Version 0.
// Ein leerer neuer Speicher bekommt gleich FormatVersion.
//
// Migrationen dürfen nur Metadaten umschreiben (current.json, Generations-
// dateien), nie Blobs: Blobs sind unveränderlich und per Hardlink mit
// Sicherungen geteilt (sicherung.go).
//
// Eine neue Version einführen: an migrations eine
// Funktion anhängen (FormatVersion folgt von selbst), die von der alten Version auf die neue hebt.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// FormatVersion ist die Format-Version, die dieses Programm schreibt: so
// viele, wie es Migrationen gibt.
func FormatVersion() int { return len(migrations) }

// formatFile ist der Name der Datei in store/.
const formatFile = "format.json"

// formatInfo ist der Inhalt von format.json.
type formatInfo struct {
	Version int `json:"version"`
}

// migrations[i] hebt einen Speicher von Version i auf Version i+1. dir ist der Speicherordner.
var migrations = []func(dir string) error{
	// 0 → 1: Der Speicher bekommt format.json; am Inhalt ändert sich nichts.
	func(string) error { return nil },
}

// ErrFormatTooNew: Der Speicher hat ein neueres Format, als dieses Programm
// kennt.
var ErrFormatTooNew = errors.New("Speicher-Format ist neuer als dieses Programm")

// FormatPath liefert den Pfad von format.json für den Speicherordner dir.
func FormatPath(dir string) string { return filepath.Join(dir, formatFile) }

// ReadFormat liest die Format-Version des Speichers in dir. found ist
// false, wenn format.json fehlt (dann ist version 0).
func ReadFormat(dir string) (version int, found bool, err error) {
	data, err := os.ReadFile(FormatPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var fi formatInfo
	if err := json.Unmarshal(data, &fi); err != nil {
		return 0, true, fmt.Errorf("%s: %w", formatFile, err)
	}
	if fi.Version < 1 {
		return 0, true, fmt.Errorf("%s: ungültige Version %d", formatFile, fi.Version)
	}
	return fi.Version, true, nil
}

// hasData: Der Speicher enthält schon Welten oder einen Head.
func hasData(dir string) bool {
	if isFile(filepath.Join(dir, "current.json")) {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(dir, "worlds"))
	return err == nil && len(entries) > 0
}

// migrate prüft die Format-Version und führt ausstehende Migrationen aus.
// Aufruf in Open, vor allem anderen; die Ordner existieren schon.
func migrate(dir string) error {
	version, found, err := ReadFormat(dir)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if version > FormatVersion() {
		return fmt.Errorf("store: %w (Format %d, dieses Programm kennt bis %d) – bitte das neuere Programm oder die Sicherung davor benutzen",
			ErrFormatTooNew, version, FormatVersion())
	}
	if !found && !hasData(dir) {
		version = FormatVersion() // neuer, leerer Speicher
	}
	if found && version == FormatVersion() {
		return nil
	}
	for v := version; v < FormatVersion(); v++ {
		if err := migrations[v](dir); err != nil {
			return fmt.Errorf("store: Migration von Format %d auf %d fehlgeschlagen: %w", v, v+1, err)
		}
		slog.Info("Speicher migriert", "von_format", v, "auf_format", v+1)
		// Jede Stufe ist sofort festgehalten: Bricht eine spätere ab, beginnt
		// der nächste Start dort.
		if err := writeFormat(dir, v+1); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	if !found && version == FormatVersion() {
		return writeFormat(dir, FormatVersion())
	}
	return nil
}

func writeFormat(dir string, version int) error {
	data, err := json.Marshal(formatInfo{Version: version})
	if err != nil {
		return err
	}
	return writeAtomic(FormatPath(dir), append(data, '\n'))
}
