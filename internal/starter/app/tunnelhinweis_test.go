package app

import (
	"strings"
	"sync"
	"testing"
)

// Der Hinweis „Tunnel startet nicht“ kommt einmal je Host-Sitzung, nennt den
// Grund (bei fehlendem Port mit der Lösung) und setzt das Detail im Fenster.
func TestTunnelProblemHinweisEinmalJeSitzung(t *testing.T) {
	var mu sync.Mutex
	var got []string
	done := make(chan struct{}, 4)
	old := notify
	notify = func(text string) {
		mu.Lock()
		got = append(got, text)
		mu.Unlock()
		done <- struct{}{}
	}
	t.Cleanup(func() { notify = old })

	a := busyApp(t, nil)
	a.onTunnelProblem("error: connect to twebl.de: dial: dial tcp: address twebl.de: missing port in address")
	<-done
	a.onTunnelProblem("error: weitere")
	a.mu.Lock()
	detail := a.detail
	a.mu.Unlock()
	if !strings.Contains(detail, "Tunnel startet nicht") {
		t.Errorf("Detail: %q", detail)
	}
	mu.Lock()
	n, first := len(got), ""
	if n > 0 {
		first = got[0]
	}
	mu.Unlock()
	if n != 1 || !strings.Contains(first, "twebl.de:7443") || !strings.Contains(first, "Defender") && !strings.Contains(first, "Windows-Sicherheit") {
		t.Fatalf("Hinweise: %d, erster: %q", n, first)
	}
	// Neue Sitzung: wieder ein Hinweis, mit allgemeinem Grund.
	a.tunnelHinweis.Store(false)
	a.onTunnelProblem("error: connect refused")
	<-done
	mu.Lock()
	second := got[len(got)-1]
	mu.Unlock()
	if !strings.Contains(second, "Grund: error: connect refused") {
		t.Errorf("zweiter Hinweis: %q", second)
	}
}
