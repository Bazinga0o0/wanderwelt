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
	"time"
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
	// 1 → 2: current.json wird die Weltliste (migrateWorldList).
	migrateWorldList,
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

// currentBackup: Name der Kopie der alten current.json (Format 1), die die
// Migration 1 → 2 daneben legt.
const currentBackup = "current.json.format1"

// migrateWorldList hebt current.json von Format 1 (eine Welt: world_id und
// head) auf Format 2 (Weltliste, welten.go).
//
// Die bisherige aktive Welt wird Welt Nr. 1: Kennung w1, Name „Welt“,
// Spielmodus und Schwierigkeit leer (es gilt weiter world_settings), mit
// unveränderter Weltkennung und unverändertem Head. Alle anderen Ordner
// unter worlds/ (Archive) gehören zu dieser Welt (vor der Weltliste gab es
// nur eine). Fehlt current.json, gibt es aber Welten (Archive ohne aktive
// Welt), entsteht w1 ohne Stand mit diesen Archiven. Stände und Blobs
// werden nicht angefasst.
//
// Die alte Datei bleibt als current.json.format1 liegen. Die neue wird
// atomar geschrieben, bevor format.json steigt; eine schon umgeschriebene
// current.json (Absturz dazwischen) erkennt die Migration und lässt sie
// stehen.
func migrateWorldList(dir string) error {
	path := filepath.Join(dir, "current.json")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var old struct {
		WorldID string          `json:"world_id"`
		Head    int64           `json:"head"`
		Worlds  json.RawMessage `json:"worlds"`
	}
	if err == nil {
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("current.json: %w", err)
		}
		if old.Worlds != nil {
			return nil // schon Format 2
		}
		if !validWorldID(old.WorldID) || old.Head < 1 {
			return fmt.Errorf("current.json ungültig: Welt %q, Head %d", old.WorldID, old.Head)
		}
		if _, err := os.Stat(filepath.Join(dir, "worlds", old.WorldID, "generations", fmt.Sprintf("%d.json", old.Head))); err != nil {
			return fmt.Errorf("Head %s/%d fehlt: %w", old.WorldID, old.Head, err)
		}
	}
	var archives []string
	if entries, err := os.ReadDir(filepath.Join(dir, "worlds")); err == nil {
		for _, e := range entries {
			if e.IsDir() && validWorldID(e.Name()) && e.Name() != old.WorldID {
				archives = append(archives, e.Name())
			}
		}
	}
	if old.WorldID == "" && len(archives) == 0 {
		return nil // nichts zu migrieren
	}
	w1 := WorldEntry{
		ID: "w1", Name: DefaultWorldName,
		Created:    time.Now().UTC(),
		StoreWorld: old.WorldID, Head: old.Head,
		Previous: archives,
	}
	next, err := json.Marshal(current{Next: 2, Worlds: []WorldEntry{w1}})
	if err != nil {
		return err
	}
	if data != nil {
		if err := writeAtomic(filepath.Join(dir, currentBackup), data); err != nil {
			return fmt.Errorf("Kopie der alten current.json: %w", err)
		}
	}
	if err := writeAtomic(path, next); err != nil {
		return err
	}
	slog.Info("Weltliste angelegt: bisherige Welt ist jetzt Welt w1 „"+DefaultWorldName+"“ (Format 2); die alte current.json liegt als "+currentBackup+" daneben",
		"weltkennung", old.WorldID, "head", old.Head, "archive", len(archives))
	return nil
}
