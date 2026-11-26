// Package sicherung legt die Sicherung eines data_dir vor einem Update an,
// ohne den Platz zu verdoppeln.
//
// Blobs (store/blobs/…) sind inhaltsadressiert, werden nur über Temp-Datei
// und Rename angelegt und nie verändert: Die Sicherung bekommt sie als
// Hardlink (praktisch kein Platz, gleiches Dateisystem nötig). Löscht ein
// neueres Programm später Blobs (Prune), bleiben sie in der Sicherung; wird
// die Sicherung gelöscht, ist der Platz frei. Alles andere – Metadaten
// (current.json, Generationen, lease.json, Überlagerungen, format.json) und
// Konfiguration – wird echt kopiert, denn das ändert sich.
//
// Nicht gesichert werden angefangene Uploads (store/tmp), Temp-Dateien und
// der Wartungsbericht.
//
// Rückweg: Dienst stoppen, altes Programm zurück, data_dir wegsichern und
// die Sicherung als data_dir einsetzen (mv). Die Hardlinks bleiben dabei
// erhalten.
package sicherung

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

// Ergebnis zählt, was die Sicherung getan hat.
type Ergebnis struct {
	Verlinkt    int   // Blobs als Hardlink
	Kopiert     int   // echt kopierte Dateien
	KopiertByte int64 // deren Größe
}

// Anlegen sichert den Ordner src (data_dir) nach dst. dst darf nicht
// existieren oder muss ein leerer Ordner sein, und es darf nicht in src
// liegen. Scheitert ein Hardlink (anderes Dateisystem, kein Hardlink-
// Support), bricht die Sicherung ab: Ein stilles Kopieren würde den Platz
// verdoppeln.
func Anlegen(src, dst string) (Ergebnis, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return Ergebnis{}, err
	}
	dst, err = filepath.Abs(dst)
	if err != nil {
		return Ergebnis{}, err
	}
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return Ergebnis{}, fmt.Errorf("%s ist kein Ordner", src)
	}
	if rel, err := filepath.Rel(src, dst); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Ergebnis{}, fmt.Errorf("das Ziel %s liegt im gesicherten Ordner %s", dst, src)
	}
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		return Ergebnis{}, fmt.Errorf("das Ziel %s existiert und ist nicht leer", dst)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Ergebnis{}, err
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return Ergebnis{}, err
	}

	var res Ergebnis
	blobRoot := filepath.Join(src, "store", "blobs")
	tmpRoot := filepath.Join(src, "store", "tmp")
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir() && path == tmpRoot:
			// Angefangene Uploads gehören nicht in die Sicherung; der Ordner
			// selbst schon (Open legt ihn sonst an).
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(target, 0o750)
		case !d.Type().IsRegular():
			return nil // Sockets, Links u. Ä. gibt es in data_dir nicht
		case strings.HasPrefix(d.Name(), ".tmp-") || (filepath.Dir(path) == src && d.Name() == wartung.BerichtName):
			return nil // Temp-Dateien und der (veraltende) Wartungsbericht
		case strings.HasPrefix(path, blobRoot+string(filepath.Separator)):
			if err := os.Link(path, target); err != nil {
				return fmt.Errorf("Hardlink für %s: %w (Sicherung auf demselben Dateisystem anlegen)", rel, err)
			}
			res.Verlinkt++
			return nil
		}
		n, err := kopieren(path, target)
		res.Kopiert++
		res.KopiertByte += n
		return err
	})
	return res, err
}

// Datei kopiert eine Datei samt Rechten und Änderungszeit; Aufruf für
// Konfigurationsdateien außerhalb von data_dir.
func Datei(src, dst string) error {
	_, err := kopieren(src, dst)
	return err
}

func kopieren(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chtimes(dst, info.ModTime(), info.ModTime())
	}
	return n, err
}
