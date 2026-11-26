package group

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Einmalige Migration des alten Einzel-Layouts (alles direkt in Home()) nach
// gruppen\<id>. Sicherheitsregeln:
//   - Es wird nichts gelöscht und nichts kopiert, nur umbenannt (derselbe
//     Ordner, also dasselbe Laufwerk; das geht atomar und schnell, auch für
//     eine große Welt).
//   - Läuft noch ein Minecraft-Server mit server\ als Arbeitsordner (Unter
//     Windows hält er den Ordner fest), scheitert das Umbenennen von server;
//     dann wird alles zurückgelegt und das alte Layout bleibt. Der Aufrufer
//     hält außerdem die Sperre der einzigen Instanz.
//   - gruppe.yaml wird zuletzt umbenannt: Solange es in Home() liegt, gilt
//     das alte Layout (Legacy). Bei jedem Fehler wird zurückgelegt; ein
//     Absturz mitten drin wird beim nächsten Start zurückgelegt und neu
//     versucht.

// legacyItems sind die gruppenspezifischen Einträge des alten Layouts (ohne
// gruppe.yaml und die Muster verworfen-*).
var legacyItems = []string{"state.json", "profil.yaml", "tunnel.yaml", "notfallkopie.json", "server"}

// rename ist im Test austauschbar.
var rename = renameRetry

func legacyEntries() ([]string, error) {
	var out []string
	for _, n := range legacyItems {
		if _, err := os.Lstat(filepath.Join(Home(), n)); err == nil {
			out = append(out, n)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	matches, err := filepath.Glob(filepath.Join(Home(), "verworfen-*"))
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		out = append(out, filepath.Base(m))
	}
	return out, nil
}

// MigrateLegacy verschiebt das alte Einzel-Layout nach gruppen\<id>, wenn es
// vorliegt. Ergebnis: die Kennung der migrierten Gruppe; "" ohne Änderung
// (kein altes Layout). Bei einem Fehler bleibt das alte Layout vollständig
// erhalten (der Fehler sagt, warum).
func MigrateLegacy() (id string, err error) {
	src := filepath.Join(Home(), groupFileName)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	g, err := LoadGroup(src)
	if err != nil {
		return "", fmt.Errorf("Migration: bisherige Gruppendatei unlesbar: %w", err)
	}
	id, err = IDFromCoordinator(g.Coordinator)
	if err != nil {
		return "", fmt.Errorf("Migration: %w", err)
	}
	dir := IDDir(id)
	if err := ensureNotSymlink(GroupsRoot()); err != nil {
		return "", fmt.Errorf("Migration: %w", err)
	}
	if err := os.MkdirAll(GroupsRoot(), 0o755); err != nil {
		return "", fmt.Errorf("Migration: %w", err)
	}
	if err := ensureNotSymlink(dir); err != nil {
		return "", fmt.Errorf("Migration: %w", err)
	}
	if err := prepareTarget(dir); err != nil {
		return "", fmt.Errorf("Migration: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("Migration: %w", err)
	}
	items, err := legacyEntries()
	if err != nil {
		return "", fmt.Errorf("Migration: %w", err)
	}
	items = append(items, groupFileName) // zuletzt
	var moved []string
	for _, n := range items {
		to := filepath.Join(dir, n)
		if n == groupFileName {
			// Schon da? Dann nie überschreiben.
			if _, err := os.Lstat(to); err == nil {
				err = fmt.Errorf("%s gibt es im Zielordner schon", n)
				return "", rollback(dir, moved, err)
			}
		}
		if err := rename(filepath.Join(Home(), n), to); err != nil {
			return "", rollback(dir, moved, fmt.Errorf("%s verschieben: %w", n, err))
		}
		moved = append(moved, n)
	}
	// aktiv.json: nur setzen, wenn noch keine Wahl da ist.
	if _, err := os.Stat(activePath()); err != nil {
		_ = setActiveFile(id) // Fehlschlag: ActiveID nimmt die erste Gruppe
	}
	return id, nil
}

// rollback legt die verschobenen Einträge (rückwärts) zurück und liefert
// den Fehler cause, ergänzt um Probleme beim Zurücklegen.
func rollback(dir string, moved []string, cause error) error {
	var failed []error
	for i := len(moved) - 1; i >= 0; i-- {
		n := moved[i]
		if err := rename(filepath.Join(dir, n), filepath.Join(Home(), n)); err != nil {
			failed = append(failed, fmt.Errorf("%s zurücklegen: %w (liegt jetzt in %s)", n, err, dir))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("Migration gescheitert (%v) und Zurücklegen unvollständig: %w", cause, errors.Join(failed...))
	}
	return fmt.Errorf("Migration gescheitert, bisheriges Layout bleibt: %w", cause)
}

// prepareTarget stellt sicher, dass dir leer ist oder nur Reste eines
// abgebrochenen Versuchs enthält; die legt es nach Home() zurück (nie
// überschreiben: gibt es den Eintrag in Home() auch, ist das ein Fehler).
func prepareTarget(dir string) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(ents) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, groupFileName)); err == nil {
		return fmt.Errorf("der Zielordner %s ist schon eine eingerichtete Gruppe", dir)
	}
	known := map[string]bool{}
	for _, n := range legacyItems {
		known[n] = true
	}
	for _, e := range ents {
		n := e.Name()
		if !known[n] && !isDiscarded(n) {
			return fmt.Errorf("der Zielordner %s ist nicht leer (%s)", dir, n)
		}
	}
	// Reste eines abgebrochenen Versuchs: zurücklegen.
	var back []string
	for _, e := range ents {
		n := e.Name()
		if _, err := os.Lstat(filepath.Join(Home(), n)); err == nil {
			return fmt.Errorf("%s liegt in Home() und in %s", n, dir)
		}
		if err := rename(filepath.Join(dir, n), filepath.Join(Home(), n)); err != nil {
			for i := len(back) - 1; i >= 0; i-- {
				_ = rename(filepath.Join(Home(), back[i]), filepath.Join(dir, back[i]))
			}
			return fmt.Errorf("Reste eines früheren Versuchs zurücklegen: %w", err)
		}
		back = append(back, n)
	}
	return nil
}

func isDiscarded(n string) bool {
	ok, _ := filepath.Match("verworfen-*", n)
	return ok
}
