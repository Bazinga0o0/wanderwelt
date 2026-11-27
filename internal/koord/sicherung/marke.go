package sicherung

// Marke der letzten Sicherung: „wanderwelt-koordinator sicherung“ schreibt
// nach gelungener Sicherung data_dir/sicherung.json {zeit, ziel}; die
// Gesundheit des Koordinators (GET /v1/health) liest sie, um vor einer zu
// alten Sicherung zu warnen. Fehlt die Datei (oder ist sie kaputt), ist die
// letzte Sicherung „unbekannt“.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// MarkeName ist die Datei in data_dir.
const MarkeName = "sicherung.json"

// Marke ist der Inhalt von sicherung.json.
type Marke struct {
	Zeit time.Time `json:"zeit"`
	Ziel string    `json:"ziel"`
}

// MarkePfad liefert den Pfad der Marke in dataDir.
func MarkePfad(dataDir string) string { return filepath.Join(dataDir, MarkeName) }

// MarkeSchreiben legt die Marke atomar an oder aktualisiert sie.
func MarkeSchreiben(dataDir string, m Marke) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dataDir, ".tmp-sicherung-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, MarkePfad(dataDir))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// MarkeLesen liest die Marke. Ohne Datei ist ok false und der Fehler nil;
// eine kaputte oder leere Marke (keine Zeit) ist ein Fehler.
func MarkeLesen(dataDir string) (m Marke, ok bool, err error) {
	data, err := os.ReadFile(MarkePfad(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return Marke{}, false, nil
	}
	if err != nil {
		return Marke{}, false, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return Marke{}, false, fmt.Errorf("%s: %w", MarkeName, err)
	}
	if m.Zeit.IsZero() {
		return Marke{}, false, fmt.Errorf("%s: keine Zeit", MarkeName)
	}
	return m, true, nil
}
