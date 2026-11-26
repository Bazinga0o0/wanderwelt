package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

// fakeZeit ist eine von Hand gestellte Uhr; hook läuft bei jeder Pause
// (dort spielt der Test den Koordinator).
type fakeZeit struct {
	now  time.Time
	hook func(now time.Time)
}

func (f *fakeZeit) env() wartungEnv {
	return wartungEnv{
		now: func() time.Time { return f.now },
		sleep: func(d time.Duration) {
			f.now = f.now.Add(d)
			if f.hook != nil {
				f.hook(f.now)
			}
		},
	}
}

func runWartung(t *testing.T, env wartungEnv, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = wartungMain(args, env, &out, &errOut)
	return code, out.String(), errOut.String()
}

var zeit0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// „start“ schreibt den Zustand, wartet auf den Bericht des Koordinators und
// meldet „bereit“, sobald der Host weg ist und die Frist um.
func TestWartungStartWartetBisBereit(t *testing.T) {
	cfgPath, _, dir := setupWithData(t, nil)
	f := &fakeZeit{now: zeit0}
	f.hook = func(now time.Time) {
		b := wartung.Bericht{Time: now, Zuschlag: "Alice"}
		if now.After(zeit0.Add(40 * time.Second)) {
			b = wartung.Bericht{Time: now, Bereit: true}
		}
		if err := wartung.BerichtSchreiben(dir, b); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := runWartung(t, f.env(), "start", "--config", cfgPath, "--in", "30s", "--grund", "Update 0.2")
	if code != 0 {
		t.Fatalf("Exit %d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Update 0.2") || !strings.Contains(stdout, "Alice hostet noch") || !strings.Contains(stdout, "Bereit für das Update") {
		t.Fatalf("Ausgabe:\n%s", stdout)
	}
	z, err := wartung.Laden(dir)
	if err != nil || !z.Aktiv || z.Grund != "Update 0.2" || z.StopAt.Sub(z.Since) != 30*time.Second {
		t.Fatalf("Zustand: %+v %v", z, err)
	}

	// status zeigt dasselbe; ende beendet und löscht Zustand und Bericht.
	if code, out, _ := runWartung(t, f.env(), "status", "--config", cfgPath); code != 0 || !strings.Contains(out, "Bereit für das Update") {
		t.Fatalf("status: %d %s", code, out)
	}
	if code, out, errOut := runWartung(t, f.env(), "ende", "--config", cfgPath); code != 0 || !strings.Contains(out, "Wartung beendet") {
		t.Fatalf("ende: %d %s %s", code, out, errOut)
	}
	if z, _ := wartung.Laden(dir); z.Aktiv {
		t.Fatal("Zustand nach ende noch aktiv")
	}
	if _, ok, _ := wartung.BerichtLesen(dir); ok {
		t.Fatal("Bericht nach ende noch da")
	}
	if code, out, _ := runWartung(t, f.env(), "ende", "--config", cfgPath); code != 0 || !strings.Contains(out, "keine Wartung") {
		t.Fatalf("zweites ende: %d %s", code, out)
	}
}

// Hängt jemand über die Frist hinaus, endet „start“ mit Ausgang 3 und nennt, wer.
func TestWartungStartMeldetWerHaengt(t *testing.T) {
	cfgPath, _, dir := setupWithData(t, nil)
	f := &fakeZeit{now: zeit0}
	f.hook = func(now time.Time) {
		_ = wartung.BerichtSchreiben(dir, wartung.Bericht{Time: now, Zuschlag: "Bob", NurUpload: true, Uploads: 1})
	}
	code, _, stderr := runWartung(t, f.env(), "start", "--config", cfgPath, "--in", "0s", "--frist", "10s")
	if code != 3 || !strings.Contains(stderr, "Bob lädt noch einen Stand hoch") {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
}

// Ohne Bericht des Koordinators gibt es einen Hinweis statt endlosen Wartens.
func TestWartungStartOhneKoordinator(t *testing.T) {
	cfgPath, _, _ := setupWithData(t, nil)
	f := &fakeZeit{now: zeit0}
	code, _, stderr := runWartung(t, f.env(), "start", "--config", cfgPath, "--in", "0s", "--frist", "1m")
	if code != 3 || !strings.Contains(stderr, "läuft er?") || !strings.Contains(stderr, "kein aktueller Bericht") {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
}

func TestWartungStartPruefteEingaben(t *testing.T) {
	cfgPath, _, dir := setupWithData(t, nil)
	f := &fakeZeit{now: zeit0}
	if code, _, _ := runWartung(t, f.env(), "start", "--config", cfgPath, "--in", "-5s", "--nicht-warten"); code != 2 {
		t.Errorf("negative Frist: Exit %d", code)
	}
	if _, err := os.Stat(wartung.Pfad(dir)); err == nil {
		t.Error("bei falscher Eingabe wurde Wartung geschrieben")
	}
	if code, _, _ := runWartung(t, f.env(), "nix"); code != 2 {
		t.Errorf("unbekannter Unterbefehl: Exit %d", code)
	}
}

// „sicherung“ bei laufendem Dienst nur in der Wartung nach „bereit“; bei
// gestopptem Dienst immer. Blobs kommen als Hardlink.
func TestSicherungNurBeiStillstand(t *testing.T) {
	cfgPath, _, dir := setupWithData(t, nil)
	blob := filepath.Join(dir, "store", "blobs", "ab", "ab01")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeZeit{now: zeit0}
	run := func(ziel string) (int, string) {
		var out, errOut bytes.Buffer
		code := sicherungMain([]string{ziel, "--config", cfgPath}, f.env(), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	old := dienstLaeuft
	defer func() { dienstLaeuft = old }()

	dienstLaeuft = func(string) bool { return true }
	ziel := filepath.Join(t.TempDir(), "s1")
	if code, msg := run(ziel); code != 1 || !strings.Contains(msg, "wartung start") {
		t.Fatalf("laufender Dienst ohne Wartung: %d %s", code, msg)
	}
	z, _ := wartung.Starten(wartung.Zustand{}, f.now, 0, "x")
	_ = wartung.Speichern(dir, z)
	_ = wartung.BerichtSchreiben(dir, wartung.Bericht{Time: f.now, Zuschlag: "Alice"})
	if code, msg := run(ziel); code != 1 || !strings.Contains(msg, "nicht bereit") {
		t.Fatalf("Wartung, aber Host hängt: %d %s", code, msg)
	}
	_ = wartung.BerichtSchreiben(dir, wartung.Bericht{Time: f.now, Bereit: true})
	if code, msg := run(ziel); code != 0 {
		t.Fatalf("Wartung bereit: %d %s", code, msg)
	}
	a, _ := os.Stat(blob)
	b, err := os.Stat(filepath.Join(ziel, "store", "blobs", "ab", "ab01"))
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("Blob nicht verlinkt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ziel, "konfiguration", "koordinator.yaml")); err != nil {
		t.Errorf("Konfiguration nicht gesichert: %v", err)
	}

	dienstLaeuft = func(string) bool { return false }
	_ = wartung.Speichern(dir, wartung.Zustand{})
	if code, msg := run(filepath.Join(t.TempDir(), "s2")); code != 0 {
		t.Fatalf("gestoppter Dienst: %d %s", code, msg)
	}
}
