//go:build linux

package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// serveHelperEnv: Ist sie gesetzt, läuft TestServeHelper als Koordinator mit
// dieser Konfiguration (Helferprozess für TestServeSighupWaehrendDesStarts).
const serveHelperEnv = "WANDERWELT_TEST_SERVE_CONFIG"

func TestServeHelper(t *testing.T) {
	cfgPath := os.Getenv(serveHelperEnv)
	if cfgPath == "" {
		t.Skip("nur als Helferprozess")
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	os.Exit(serve([]string{"--config", cfgPath}))
}

// Ein SIGHUP während des Starts (systemctl reload) darf den Koordinator
// nicht beenden: Ohne Handler endet der Prozess, und systemd startet ihn
// nach einem Ende durch SIGHUP nicht neu (Restart=on-failure). Der Start
// hängt hier beim Lesen des Manifests (eine FIFO) – nach config.Load, vor
// dem Server; genau dann kommt das Signal. Danach muss der Koordinator
// laufen, die Mitglieder einmal neu laden und auf SIGTERM sauber enden.
func TestServeSighupWaehrendDesStarts(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "manifest.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "koordinator.yaml")
	cfg := fmt.Sprintf("listen: 127.0.0.1:0\ndata_dir: %s\nauth:\n  mode: dev\nmanifest_file: %s\n",
		filepath.Join(dir, "data"), fifo)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestServeHelper$")
	cmd.Env = append(os.Environ(), serveHelperEnv+"="+cfgPath)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		lines []string
	)
	logged := make(chan struct{}) // „Mitglieder neu geladen“ steht im Log
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(stderr)
		once := sync.Once{}
		for sc.Scan() {
			mu.Lock()
			lines = append(lines, sc.Text())
			mu.Unlock()
			if strings.Contains(sc.Text(), "Mitglieder neu geladen") {
				once.Do(func() { close(logged) })
			}
		}
	}()
	exited := make(chan struct{}) // geschlossen nach Wait, Ergebnis in waitErr
	var waitErr error
	go func() {
		<-scanned // erst alles lesen, dann Wait (schließt die Pipe)
		waitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
		// Ein noch wartendes Öffnen zum Schreiben freigeben.
		if r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			r.Close()
		}
	})
	log := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}

	// Öffnen zum Schreiben kehrt erst zurück, wenn der Koordinator die FIFO
	// zum Lesen geöffnet hat.
	opened := make(chan *os.File, 1)
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			w = nil
		}
		opened <- w
	}()
	var w *os.File
	select {
	case w = <-opened:
	case <-exited:
		t.Fatalf("Koordinator endet vor dem Lesen des Manifests: %v\n%s", waitErr, log())
	case <-time.After(30 * time.Second):
		t.Fatalf("Koordinator liest das Manifest nicht\n%s", log())
	}
	if w == nil {
		t.FailNow()
	}
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	// Zeit für die Zustellung: Ohne Handler wäre der Prozess danach weg.
	time.Sleep(200 * time.Millisecond)
	_, werr := w.WriteString("minecraft_version: \"26.2\"\n")
	w.Close()
	if werr != nil {
		t.Fatalf("Manifest schreiben (Koordinator beendet?): %v\n%s", werr, log())
	}

	select {
	case <-logged:
	case <-exited:
		t.Fatalf("Koordinator nach SIGHUP im Start beendet: %v\n%s", waitErr, log())
	case <-time.After(30 * time.Second):
		t.Fatalf("Mitglieder nicht neu geladen\n%s", log())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		if waitErr != nil {
			t.Fatalf("Koordinator endet auf SIGTERM nicht sauber: %v\n%s", waitErr, log())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("Koordinator endet auf SIGTERM nicht\n%s", log())
	}
	if !strings.Contains(log(), "Koordinator läuft") {
		t.Errorf("Koordinator lief nie:\n%s", log())
	}
}
