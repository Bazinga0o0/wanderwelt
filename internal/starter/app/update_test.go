package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// I2: Eine andere wanderwelt.exe als die installierte (andere Bytes) ist
// ein Update; dieselben Bytes oder die installierte selbst nicht.
func TestDiffersFromInstalled(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if d, err := differsFromInstalled(); err != nil || !d {
		t.Fatalf("ohne installierte Kopie: %v, %v; erwartet anders", d, err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedExe(), data, 0o755); err != nil {
		t.Fatal(err)
	}
	if d, err := differsFromInstalled(); err != nil || d {
		t.Fatalf("gleiche Bytes: %v, %v; erwartet gleich", d, err)
	}
	if err := os.WriteFile(installedExe(), append(data, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	if d, err := differsFromInstalled(); err != nil || !d {
		t.Fatalf("andere Bytes: %v, %v; erwartet anders", d, err)
	}
}

// I2: Die installierte Kopie wird atomar ersetzt, auch wenn sie gerade
// noch offen ist (die alte Instanz endet noch, ein Virenscanner liest).
func TestCopySelfReplacesFileInUse(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("nur unter Windows verhindert eine offene Datei das Ersetzen")
	}
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "neu.exe"), filepath.Join(dir, "home", ExeName)
	if err := os.WriteFile(src, []byte("NEU"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("ALT"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		time.Sleep(3 * time.Second)
		f.Close()
	})
	err = copySelf(src, dst)
	wg.Wait()
	if err != nil {
		t.Fatalf("copySelf: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "NEU" {
		t.Fatalf("installiert: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), "*.tmp")); len(left) != 0 {
		t.Fatalf("Reste: %v", left)
	}
}

// Ein offenes Fenster („wanderwelt.exe fenster“) läuft aus der
// installierten Kopie; die lässt sich nicht ersetzen, aber beiseiteschieben.
// Das Update gelingt sofort, die alte Kopie verschwindet beim nächsten
// Start, sobald sie nicht mehr läuft.
func TestCopySelfReplacesRunningExe(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("nur unter Windows ist eine laufende exe gesperrt")
	}
	home := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)
	dst := filepath.Join(home, ExeName)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
	// Die „alte Kopie“ läuft (dieser Test-Binary, schlafend).
	cmd := exec.Command(dst, "-test.run=^TestHilfsprozessSchlaeft$")
	cmd.Env = append(os.Environ(), "WANDERWELT_HILFE_SCHLAEFT=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	beendet := false
	beenden := func() {
		if !beendet {
			cmd.Process.Kill()
			cmd.Wait()
			beendet = true
		}
	}
	defer beenden()
	time.Sleep(500 * time.Millisecond)
	if err := os.WriteFile(dst+".probe", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dst+".probe", dst); err == nil {
		t.Fatal("Vorbedingung: eine laufende exe ließ sich ersetzen")
	}

	src := filepath.Join(t.TempDir(), "neu.exe")
	if err := os.WriteFile(src, []byte("NEU"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := copySelf(src, dst); err != nil {
		t.Fatalf("copySelf bei laufender alter Kopie: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("copySelf brauchte %v", d)
	}
	if b, _ := os.ReadFile(dst); string(b) != "NEU" {
		t.Fatalf("installiert: %.10q", b)
	}
	olds, _ := filepath.Glob(filepath.Join(home, ExeName+oldCopyMark+"*"))
	if len(olds) != 1 {
		t.Fatalf("alte Kopien: %v", olds)
	}
	removeOldCopies() // läuft noch: bleibt
	if _, err := os.Stat(olds[0]); err != nil {
		t.Fatalf("laufende alte Kopie verschwunden: %v", err)
	}
	beenden()
	removeOldCopies()
	if _, err := os.Stat(olds[0]); err == nil {
		t.Fatal("alte Kopie nach dem Ende nicht gelöscht")
	}
	if left, _ := filepath.Glob(filepath.Join(home, "*.tmp")); len(left) != 0 {
		t.Fatalf("Reste: %v", left)
	}
}

// TestHilfsprozessSchlaeft ist der laufende Prozess für
// TestCopySelfReplacesRunningExe (sonst übersprungen).
func TestHilfsprozessSchlaeft(t *testing.T) {
	if os.Getenv("WANDERWELT_HILFE_SCHLAEFT") == "" {
		t.Skip("nur als Hilfsprozess")
	}
	time.Sleep(time.Minute)
}

// I2: Die Steuerung nimmt „update“ an; eine ältere Instanz, die es nicht
// kennt (404), führt sofort zu errUnknownAction statt zu 20 s Warten.
func TestControlUpdateAction(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	got := make(chan string, 1)
	cs, err := startControl(func(action string, _ []byte) { got <- action }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendToRunning(actionUpdate); err != nil {
		t.Fatalf("update: %v", err)
	}
	if a := <-got; a != actionUpdate {
		t.Fatalf("Aktion %q", a)
	}
	cs.Close()

	// Ältere Instanz: kennt update nicht.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unbekannte Aktion", http.StatusNotFound)
	}))
	defer old.Close()
	u, _ := url.Parse(old.URL)
	port, _ := strconv.Atoi(u.Port())
	data, _ := json.Marshal(fensterapi.ControlInfo{Port: port, Token: "x", PID: 1})
	if err := os.WriteFile(controlPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = sendToRunning(actionUpdate)
	if !errors.Is(err, errUnknownAction) || time.Since(start) > 5*time.Second {
		t.Fatalf("ältere Instanz: %v nach %v", err, time.Since(start))
	}
	if _, err := updateRunning(); err == nil {
		t.Fatal("updateRunning bei älterer Instanz ohne Fehler")
	}
}

// I2: Die laufende Instanz beendet für das Update erst das Hosten sauber
// (Schluss-Stand eingeschlossen), dann sich selbst – ohne Rückfrage.
func TestQuitForUpdateEndsHostingFirst(t *testing.T) {
	old := notify
	t.Cleanup(func() { notify = old })
	var notes atomic.Int64
	notify = func(string) { notes.Add(1) }

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var ended atomic.Bool
	a := &App{ctx: ctx, cancel: cancel, quitCh: make(chan struct{})}
	a.hostDone = done
	a.hostCancel = func() {
		time.AfterFunc(100*time.Millisecond, func() {
			ended.Store(true)
			close(done)
		})
	}
	a.quitForUpdate()
	if !ended.Load() {
		t.Fatal("beendet, bevor das Hosten zu Ende war")
	}
	if !a.isQuitting() || ctx.Err() == nil {
		t.Fatal("Programm endet nicht")
	}
	select {
	case <-a.quitCh:
	default:
		t.Fatal("Tray-Schleife nicht beendet")
	}
	// Ein zweites update tut nichts mehr.
	a.quitNow()
}
