package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	tauschAlt = []byte("MZ alte Version")
	tauschNeu = []byte("MZ neue Version")
)

// tauschAufbau legt ein leeres Home an, darin die „alte“ installierte Kopie, und
// eine „neue“ exe (wie update\wanderwelt-0.5.0.exe) außerhalb davon.
func tauschAufbau(t *testing.T) (src string) {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir()) // nie die echte Installation
	oldRename, oldStep, oldWait := swapRename, swapStep, replaceWait
	t.Cleanup(func() { swapRename, swapStep, replaceWait = oldRename, oldStep, oldWait })
	replaceWait = 50 * time.Millisecond
	writeInstalled(t, tauschAlt)
	src = filepath.Join(t.TempDir(), "wanderwelt-0.5.0.exe")
	if err := os.WriteFile(src, tauschNeu, 0o755); err != nil {
		t.Fatal(err)
	}
	return src
}

func vorhanden(p string) bool { _, err := os.Stat(p); return err == nil }

func inhalt(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return string(b)
}

// abgebrochen ruft copySelf auf und meldet, ob der Hook den Prozess an step
// „beendet“ hat (panic: nichts läuft danach mehr, auch kein Zurückrollen).
type abbruch struct{}

func copySelfBisAbbruch(src, dst, step string) (beendet bool, err error) {
	swapStep = func(s string) error {
		if s == step {
			panic(abbruch{})
		}
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(abbruch); !ok {
				panic(r)
			}
			beendet = true
		}
	}()
	err = copySelf(src, dst)
	return false, err
}

var tauschSchritte = []string{"kopieren", "pruefen", "neu", "alt", "einsetzen"}

// (a) Der Prozess endet vor jedem Schritt der Tauschfolge: Es liegt immer eine
// vollständige Kopie unter wanderwelt.exe, .neu oder .alt; nach der Heilung
// steht wanderwelt.exe da, ganz alt oder ganz neu. Eine vorhandene
// wanderwelt.exe ändert die Heilung nicht.
func TestTauschAbbruchNachJedemSchritt(t *testing.T) {
	for _, step := range tauschSchritte {
		t.Run(step, func(t *testing.T) {
			src := tauschAufbau(t)
			dst := installedExe()
			if beendet, err := copySelfBisAbbruch(src, dst, step); !beendet {
				t.Fatalf("nicht beendet: %v", err)
			}
			// Nie keine Kopie.
			var da []string
			for _, s := range []string{"", newCopySuffix, oldCopySuffix} {
				if vorhanden(dst + s) {
					da = append(da, dst+s)
				}
			}
			if len(da) == 0 {
				t.Fatal("keine der Dateien wanderwelt.exe, .neu, .alt vorhanden")
			}
			vorher := vorhanden(dst)
			var vorherInhalt string
			if vorher {
				vorherInhalt = inhalt(t, dst)
			}
			healInstall()
			if !vorhanden(dst) {
				t.Fatalf("wanderwelt.exe fehlt nach der Heilung (vorhanden: %v)", da)
			}
			got := inhalt(t, dst)
			if got != string(tauschAlt) && got != string(tauschNeu) {
				t.Fatalf("Inhalt weder ganz alt noch ganz neu: %q", got)
			}
			if vorher && got != vorherInhalt {
				t.Fatalf("Heilung hat eine vorhandene Installation verändert: %q → %q", vorherInhalt, got)
			}
			if step != "einsetzen" && !vorher {
				t.Fatalf("wanderwelt.exe fehlte schon vor der Heilung (Abbruch vor %s)", step)
			}
			// Erwartung je Stelle: vor „einsetzen“ steht alt da, danach (Lücke) heilt .neu.
			want := string(tauschAlt)
			if step == "einsetzen" {
				want = string(tauschNeu)
				if vorher {
					t.Fatal("zwischen alt und einsetzen sollte wanderwelt.exe fehlen")
				}
			}
			if got != want {
				t.Fatalf("nach Abbruch bei %s: %q, erwartet %q", step, got, want)
			}
		})
	}
}

// (a) Scheitert ein Schritt mit einem Fehler (statt Abbruch), bleibt die alte
// Kopie unverändert an ihrem Platz, ohne Reste der neuen.
func TestTauschFehlerNachJedemSchrittRolltZurueck(t *testing.T) {
	for _, step := range tauschSchritte {
		t.Run(step, func(t *testing.T) {
			src := tauschAufbau(t)
			dst := installedExe()
			boom := errors.New("Fehler eingespielt")
			swapStep = func(s string) error {
				if s == step {
					return boom
				}
				return nil
			}
			if err := copySelf(src, dst); !errors.Is(err, boom) {
				t.Fatalf("Fehler: %v", err)
			}
			if got := inhalt(t, dst); got != string(tauschAlt) {
				t.Fatalf("installierte Kopie: %q", got)
			}
			for _, s := range []string{newCopySuffix, newTmpSuffix} {
				if vorhanden(dst + s) {
					t.Errorf("Rest %s", dst+s)
				}
			}
		})
	}
}

// (b) Scheitert das letzte Umbenennen (.neu → wanderwelt.exe) wirklich,
// stellt der Tausch die alte Kopie zurück und räumt .neu weg.
func TestTauschRollbackLetzterSchritt(t *testing.T) {
	src := tauschAufbau(t)
	dst := installedExe()
	swapRename = func(from, to string) error {
		if from == dst+newCopySuffix && to == dst {
			return errors.New("gesperrt")
		}
		return os.Rename(from, to)
	}
	if err := copySelf(src, dst); err == nil {
		t.Fatal("kein Fehler")
	}
	if got := inhalt(t, dst); got != string(tauschAlt) {
		t.Fatalf("installierte Kopie: %q", got)
	}
	if vorhanden(dst+newCopySuffix) || vorhanden(dst+newTmpSuffix) {
		t.Fatal("Reste der neuen Kopie")
	}
}

// (b) Scheitert auch das Zurückstellen, bleiben .neu und .alt; die Heilung
// beim nächsten Start stellt die neue Kopie her.
func TestTauschRollbackScheitertHeilungRettet(t *testing.T) {
	src := tauschAufbau(t)
	dst := installedExe()
	swapRename = func(from, to string) error {
		if to == dst {
			return errors.New("gesperrt")
		}
		return os.Rename(from, to)
	}
	if err := copySelf(src, dst); err == nil {
		t.Fatal("kein Fehler")
	}
	if vorhanden(dst) {
		t.Fatal("Vorbedingung: wanderwelt.exe sollte fehlen")
	}
	swapRename = os.Rename
	healInstall()
	if got := inhalt(t, dst); got != string(tauschNeu) {
		t.Fatalf("nach Heilung: %q", got)
	}
}

// (c) Eine vorhandene Installation überschreibt die Heilung nie, auch wenn
// .neu, .alt und eine geladene exe daliegen.
func TestHeilungUeberschreibtGuteInstallationNicht(t *testing.T) {
	tauschAufbau(t)
	dst := installedExe()
	for s, b := range map[string]string{newCopySuffix: "MZ neu", oldCopySuffix: "MZ alt"} {
		if err := os.WriteFile(dst+s, []byte(b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	up := filepath.Join(updateDir(), "wanderwelt-0.9.0.exe")
	if err := os.MkdirAll(updateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(up, []byte("MZ update"), 0o755); err != nil {
		t.Fatal(err)
	}
	healInstall()
	if got := inhalt(t, dst); got != string(tauschAlt) {
		t.Fatalf("Installation überschrieben: %q", got)
	}
	for s, b := range map[string]string{newCopySuffix: "MZ neu", oldCopySuffix: "MZ alt"} {
		if inhalt(t, dst+s) != b {
			t.Errorf("%s verändert", s)
		}
	}
	if !vorhanden(up) {
		t.Error("update-Datei verschwunden")
	}
}

// Reihenfolge der Heilung: .neu, .alt, geladene exe (neueste Version); eine
// abgeschnittene Datei wird übersprungen.
func TestHeilungReihenfolge(t *testing.T) {
	tauschAufbau(t)
	dst := installedExe()
	schreibe := func(p, b string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	neuerStand := func() {
		t.Helper()
		os.Remove(dst)
		for _, s := range []string{newCopySuffix, oldCopySuffix} {
			os.Remove(dst + s)
		}
		os.RemoveAll(updateDir())
	}
	neuerStand()
	schreibe(dst+newCopySuffix, "MZ neu")
	schreibe(dst+oldCopySuffix, "MZ alt")
	schreibe(filepath.Join(updateDir(), "wanderwelt-0.5.0.exe"), "MZ up")
	healInstall()
	if got := inhalt(t, dst); got != "MZ neu" || !vorhanden(dst+oldCopySuffix) {
		t.Fatalf("erst .neu: %q", got)
	}

	neuerStand()
	schreibe(dst+newCopySuffix, "M") // abgeschnitten
	schreibe(dst+oldCopySuffix, "MZ alt")
	schreibe(filepath.Join(updateDir(), "wanderwelt-0.5.0.exe"), "MZ up")
	healInstall()
	if got := inhalt(t, dst); got != "MZ alt" {
		t.Fatalf("abgeschnittenes .neu, dann .alt: %q", got)
	}

	neuerStand()
	schreibe(filepath.Join(updateDir(), "wanderwelt-0.5.0.exe"), "MZ up 0.5.0")
	schreibe(filepath.Join(updateDir(), "wanderwelt-0.10.0.exe"), "MZ up 0.10.0")
	schreibe(filepath.Join(updateDir(), "wanderwelt-0.9.0.exe"), "MZ up 0.9.0")
	healInstall()
	if got := inhalt(t, dst); got != "MZ up 0.10.0" {
		t.Fatalf("neueste geladene exe: %q", got)
	}
	if !vorhanden(filepath.Join(updateDir(), "wanderwelt-0.10.0.exe")) {
		t.Fatal("die geladene exe muss liegen bleiben")
	}

	neuerStand()
	healInstall() // nichts da: kein Absturz, nichts entsteht
	if vorhanden(dst) {
		t.Fatal("aus nichts entstanden")
	}
}

// (d) .alt bleibt, bis die neue Version nachweislich gestartet ist
// (confirmInstall aus der installierten Kopie); ein anderer Prozess räumt
// nichts weg.
func TestAlteKopieErstNachErfolgreichemStartGeloescht(t *testing.T) {
	src := tauschAufbau(t)
	dst := installedExe()
	if err := copySelf(src, dst); err != nil {
		t.Fatal(err)
	}
	if got := inhalt(t, dst); got != string(tauschNeu) {
		t.Fatalf("installiert: %q", got)
	}
	if got := inhalt(t, dst+oldCopySuffix); got != string(tauschAlt) {
		t.Fatalf(".alt: %q", got)
	}
	if vorhanden(dst+newCopySuffix) || vorhanden(dst+newTmpSuffix) {
		t.Fatal("Reste nach dem Tausch")
	}
	confirmInstall(src) // läuft nicht aus Home(): nichts löschen
	if !vorhanden(dst + oldCopySuffix) {
		t.Fatal(".alt gelöscht, obwohl die installierte Kopie nicht lief")
	}
	// Ein zweites Update ersetzt .alt (aber nur nach vollständigem Tausch).
	if err := os.WriteFile(src, []byte("MZ noch neuer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copySelf(src, dst); err != nil {
		t.Fatal(err)
	}
	if got := inhalt(t, dst+oldCopySuffix); got != string(tauschNeu) {
		t.Fatalf(".alt nach zweitem Update: %q", got)
	}
	confirmInstall(dst)
	for _, s := range []string{oldCopySuffix, newCopySuffix, newTmpSuffix} {
		if vorhanden(dst + s) {
			t.Errorf("%s nach erfolgreichem Start nicht gelöscht", s)
		}
	}
	if got := inhalt(t, dst); got != "MZ noch neuer" {
		t.Fatalf("installiert: %q", got)
	}
}

// (e) cleanUpdateDir löscht die geladene exe nicht zu früh: nicht, solange
// wanderwelt.exe fehlt oder ein Tausch offen ist; die Zwischendateien in
// Home() fasst es nie an.
func TestCleanUpdateDirLoeschtNichtZuFrueh(t *testing.T) {
	tauschAufbau(t)
	dst := installedExe()
	up := filepath.Join(updateDir(), "wanderwelt-0.5.0.exe")
	if err := os.MkdirAll(updateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(up, tauschNeu, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(updateDir(), updateStateFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// wanderwelt.exe fehlt (Tausch abgebrochen): bleibt.
	if err := os.Rename(dst, dst+oldCopySuffix); err != nil {
		t.Fatal(err)
	}
	cleanUpdateDir("")
	if !vorhanden(up) {
		t.Fatal("geladene exe gelöscht, obwohl wanderwelt.exe fehlt")
	}
	if err := os.Rename(dst+oldCopySuffix, dst); err != nil {
		t.Fatal(err)
	}

	// .neu liegt (Tausch offen): bleibt.
	if err := os.WriteFile(dst+newCopySuffix, tauschNeu, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanUpdateDir("")
	if !vorhanden(up) {
		t.Fatal("geladene exe gelöscht, obwohl ein Tausch offen ist")
	}
	os.Remove(dst + newCopySuffix)

	// Alles in Ordnung (nur .alt als Rückfall): jetzt wird aufgeräumt, .alt bleibt.
	if err := os.WriteFile(dst+oldCopySuffix, tauschAlt, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanUpdateDir("")
	if vorhanden(up) {
		t.Fatal("geladene exe nicht aufgeräumt")
	}
	if !vorhanden(filepath.Join(updateDir(), updateStateFile)) {
		t.Fatal("zustand.json gelöscht")
	}
	if !vorhanden(dst + oldCopySuffix) {
		t.Fatal("cleanUpdateDir hat .alt in Home() gelöscht")
	}
}
