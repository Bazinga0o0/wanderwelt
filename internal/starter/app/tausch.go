package app

// Tausch der installierten Kopie Home()/wanderwelt.exe (Selbst-Update und
// Übergabe aus update\) und Selbstheilung nach einem abgebrochenen Tausch.
//
// Der Tausch ist eine feste Folge, bei der zu jedem Zeitpunkt (auch wenn der
// Prozess hart endet: Aufgabenplanung beendet Kindprozesse, Absturz,
// Stromausfall, Virenscanner) mindestens eine vollständige Kopie unter einem
// der Namen wanderwelt.exe, .neu oder .alt liegt:
//
//  1. kopieren: neue exe nach wanderwelt.exe.neu.tmp (Sync)
//  2. prüfen:   Größe und SHA-256 der Temp-Datei gegen die Quelle
//  3. neu:      .neu.tmp → .neu (ab hier ist .neu vollständig)
//  4. alt:      wanderwelt.exe → wanderwelt.exe.alt (Umbenennen geht auch bei
//     einer laufenden exe)
//  5. einsetzen: .neu → wanderwelt.exe
//
// Scheitert 4 oder 5, wird zurückgerollt (.alt → wanderwelt.exe). Zwischen 4
// und 5 fehlt wanderwelt.exe kurz; wird der Prozess genau dort beendet,
// stellt der nächste Start (healInstall) sie aus .neu her. .alt, .neu und die
// Temp-Datei löscht erst confirmInstall, wenn die neue Version nachweislich
// gestartet ist.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// newCopySuffix: vollständige neue Kopie neben der installierten.
	newCopySuffix = ".neu"
	// newTmpSuffix: die neue Kopie, solange sie geschrieben und geprüft wird.
	newTmpSuffix = ".neu.tmp"
	// oldCopySuffix: die bisherige Kopie, beiseitegestellt (Rückfall).
	oldCopySuffix = ".alt"
	// oldCopyMark: Ausweichname, wenn .alt belegt ist (eine noch laufende
	// Kopie): wanderwelt.exe.alt-<Zeit>.
	oldCopyMark = ".alt-"
)

// Im Test austauschbar.
var (
	// replaceWait: So lange wird versucht, die installierte Kopie
	// beiseitezustellen bzw. einzusetzen (die alte Instanz kann gerade noch
	// enden, ein Virenscanner die Datei offen haben).
	replaceWait = 30 * time.Second
	// swapRename ersetzt os.Rename in der Tauschfolge.
	swapRename = os.Rename
	// swapStep läuft vor jedem Schritt der Tauschfolge mit dessen Namen
	// (kopieren, pruefen, neu, alt, einsetzen). Ein Fehler lässt den Schritt
	// scheitern; im Test beendet ein panic den Prozess nach.
	swapStep = func(step string) error { return nil }
)

// copySelf stellt die exe src als dst bereit (Tauschfolge oben). Bei einem
// Fehler liegt dst danach wie vorher da (bzw. fehlt, wenn sie vorher fehlte).
func copySelf(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	neu, tmp, alt := dst+newCopySuffix, dst+newTmpSuffix, dst+oldCopySuffix

	if err := swapStep("kopieren"); err != nil {
		return err
	}
	if err := writeSynced(src, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("kopieren: %w", err)
	}
	if err := swapStep("pruefen"); err != nil {
		os.Remove(tmp)
		return err
	}
	if same, err := sameContent(src, tmp); err != nil || !same {
		os.Remove(tmp)
		if err == nil {
			err = errors.New("Kopie weicht von der Quelle ab")
		}
		return fmt.Errorf("prüfen: %w", err)
	}
	if err := swapStep("neu"); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := renameWithin(tmp, neu, replaceWait); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s: %w", filepath.Base(neu), err)
	}

	if err := swapStep("alt"); err != nil {
		os.Remove(neu)
		return err
	}
	aside, err := setAside(dst, alt, replaceWait)
	if err != nil {
		os.Remove(neu)
		return fmt.Errorf("alte Kopie beiseitestellen: %w", err)
	}
	if aside != "" && aside != alt {
		suLogf("Installation: %s lief noch; die alte Kopie liegt jetzt unter %s", filepath.Base(alt), filepath.Base(aside))
	}

	err = swapStep("einsetzen")
	if err == nil {
		err = renameWithin(neu, dst, replaceWait)
	}
	if err != nil {
		// Zurückrollen: die alte Kopie an ihren Platz.
		if aside != "" {
			if rerr := swapRename(aside, dst); rerr != nil {
				// .neu und .alt bleiben liegen; der nächste Start heilt (healInstall).
				suLogf("Installation: alte Kopie nicht zurückgestellt (%v); der nächste Start stellt %s her", rerr, ExeName)
				return fmt.Errorf("einsetzen: %w", err)
			}
		}
		os.Remove(neu)
		return fmt.Errorf("einsetzen: %w", err)
	}
	return nil
}

// writeSynced kopiert src nach dst (neu angelegt oder überschrieben) und
// schreibt sie auf die Platte.
func writeSynced(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// renameWithin benennt from zu to um und wiederholt es höchstens wait lang.
func renameWithin(from, to string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for i := 1; ; i++ {
		err := swapRename(from, to)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(min(time.Duration(i)*50*time.Millisecond, 500*time.Millisecond))
	}
}

// setAside stellt dst unter alt beiseite (ein vorhandenes alt wird dabei
// ersetzt; ist es von einer laufenden Kopie belegt, unter alt-<Zeit>). aside
// ist der Name, unter dem die alte Kopie jetzt liegt; leer, wenn dst fehlt.
func setAside(dst, alt string, wait time.Duration) (aside string, err error) {
	deadline := time.Now().Add(wait)
	for i := 1; ; i++ {
		if _, serr := os.Stat(dst); errors.Is(serr, fs.ErrNotExist) {
			return "", nil
		}
		if err = swapRename(dst, alt); err == nil {
			return alt, nil
		}
		unique := dst + oldCopyMark + strconv.FormatInt(time.Now().UnixNano(), 36)
		if swapRename(dst, unique) == nil {
			return unique, nil
		}
		if time.Now().After(deadline) {
			return "", err
		}
		time.Sleep(min(time.Duration(i)*50*time.Millisecond, 500*time.Millisecond))
	}
}

// plausibleExe: Die Datei ist eine nichtleere exe (PE-Kopf „MZ“). Eine
// abgeschnittene oder leere Datei stellt die Heilung nicht her.
func plausibleExe(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, []byte("MZ"))
}

// healInstall stellt Home()/wanderwelt.exe wieder her, wenn sie fehlt (ein
// abgebrochener Tausch). Reihenfolge: .neu, .alt, .alt-* (neueste), dann die
// neueste update\wanderwelt-*.exe; jede wird auf den PE-Kopf geprüft. Eine
// vorhandene wanderwelt.exe wird nie angefasst. Aufruf unter der
// Instanz-Sperre, damit sie nicht neben einem laufenden Tausch läuft.
func healInstall() {
	target := installedExe()
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		return // vorhanden (oder nicht prüfbar): nichts anfassen
	}
	home := filepath.Dir(target)
	suLogf("Installation: %s fehlt (ein Tausch wurde wohl abgebrochen) – stelle sie wieder her", target)

	type cand struct {
		path string
		move bool // umbenennen statt kopieren
		what string
	}
	cands := []cand{
		{target + newCopySuffix, true, "die fertige neue Kopie"},
		{target + oldCopySuffix, true, "die beiseitegestellte alte Kopie"},
	}
	olds, _ := filepath.Glob(target + oldCopyMark + "*")
	slices.SortFunc(olds, func(a, b string) int { return modTime(b).Compare(modTime(a)) })
	for _, o := range olds {
		cands = append(cands, cand{o, true, "eine beiseitegestellte alte Kopie"})
	}
	ups, _ := filepath.Glob(filepath.Join(updateDir(), "wanderwelt-*.exe"))
	slices.SortFunc(ups, func(a, b string) int { return -compareUpdateName(filepath.Base(a), filepath.Base(b)) })
	for _, u := range ups {
		cands = append(cands, cand{u, false, "die geladene neue Version"})
	}

	for _, c := range cands {
		if _, err := os.Stat(c.path); err != nil {
			continue
		}
		if !plausibleExe(c.path) {
			suLogf("Installation: %s ist keine vollständige exe, übersprungen", c.path)
			continue
		}
		var err error
		if c.move {
			if _, serr := os.Stat(target); !errors.Is(serr, fs.ErrNotExist) {
				return // inzwischen da: nie überschreiben
			}
			err = os.Rename(c.path, target)
		} else {
			err = copySelf(c.path, target)
		}
		if err != nil {
			suLogf("Installation: %s aus %s nicht hergestellt: %v", ExeName, c.path, err)
			continue
		}
		suLogf("Installation: %s aus %s (%s) hergestellt", ExeName, c.path, c.what)
		return
	}
	suLogf("Installation: nichts gefunden, woraus %s in %s herzustellen wäre", ExeName, home)
}

func modTime(p string) time.Time {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// compareUpdateName vergleicht zwei Namen wanderwelt-X.Y.Z.exe nach der
// Version; nicht lesbare Namen gelten als kleiner.
func compareUpdateName(a, b string) int {
	ver := func(n string) string {
		return strings.TrimSuffix(strings.TrimPrefix(n, "wanderwelt-"), ".exe")
	}
	c, ok := compareSemVer(ver(a), ver(b))
	if ok {
		return c
	}
	_, okA := compareSemVer(ver(a), ver(a))
	_, okB := compareSemVer(ver(b), ver(b))
	switch {
	case okA && !okB:
		return 1
	case !okA && okB:
		return -1
	}
	return strings.Compare(a, b)
}

// swapOpen: Es liegt ein unfertiger Tausch vor (wanderwelt.exe fehlt oder
// .neu bzw. die Temp-Datei liegt); update\ darf dann nicht aufgeräumt werden,
// die geladene exe ist die letzte Rückfallebene.
func swapOpen() bool {
	target := installedExe()
	if _, err := os.Stat(target); err != nil {
		return true
	}
	for _, s := range []string{newCopySuffix, newTmpSuffix} {
		if _, err := os.Stat(target + s); err == nil {
			return true
		}
	}
	return false
}

// confirmInstall: Läuft self aus der installierten Kopie und ist sie
// nachweislich gestartet, räumt es die Zwischendateien des Tauschs weg.
func confirmInstall(self string) {
	if !samePath(self, installedExe()) {
		return
	}
	removeOldCopies()
}

// removeOldCopies löscht die Zwischendateien des Tauschs in Home(): .alt,
// .alt-*, .neu und die Temp-Datei. Läuft eine noch (ein Fenster der alten
// Version), bleibt sie bis zum nächsten Start liegen. Nur aufrufen, wenn
// die installierte Kopie läuft.
func removeOldCopies() {
	target := installedExe()
	files := []string{target + oldCopySuffix, target + newCopySuffix, target + newTmpSuffix}
	olds, _ := filepath.Glob(target + oldCopyMark + "*")
	for _, f := range append(files, olds...) {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		if err := os.Remove(f); err == nil {
			suLogf("Installation: %s gelöscht", filepath.Base(f))
		}
	}
}
