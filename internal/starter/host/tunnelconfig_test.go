package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Ein Tunnel-Server ohne Port in der Gruppendatei (Einrichtung über
// „Entdecken“ mit „twebl.de“) ergab „missing port in address“, der tunnel-Client
// beendete sich sofort. Die Konfiguration trägt jetzt immer host:port.
func TestWriteTunnelConfigErgaenztStandardPort(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	for server, want := range map[string]string{
		"twebl.de":      "server: twebl.de:7443",
		"twebl.de:7443": "server: twebl.de:7443",
		"t.example:99":  "server: t.example:99",
	} {
		g := &group.Group{Coordinator: "https://c.example.org", TunnelServer: server, Token: "geheim-token-1234567890"}
		if err := writeTunnelConfig(g); err != nil {
			t.Fatalf("%s: %v", server, err)
		}
		data, err := os.ReadFile(tunnelConfigPath())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want+"\n") {
			t.Errorf("Server %q: Datei enthält nicht %q:\n%s", server, want, strings.ReplaceAll(string(data), "geheim-token-1234567890", "<token>"))
		}
	}
}

func TestTunnelFehlergrundLiestLetzteFehlerzeile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)
	if g := tunnelFehlergrund(); g != "" {
		t.Errorf("ohne Log: %q", g)
	}
	dir := filepath.Join(home, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := "Usage:\n  tunnel tcp LOCAL_PORT [flags]\n\nerror: erster Fehler\nUsage:\n\nerror: connect to twebl.de: dial: dial tcp: address twebl.de: missing port in address\n"
	if err := os.WriteFile(filepath.Join(dir, "tunnel.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if g := tunnelFehlergrund(); g != "error: connect to twebl.de: dial: dial tcp: address twebl.de: missing port in address" {
		t.Errorf("Grund: %q", g)
	}
	// Ohne Fehlerzeile und mit sehr langer Zeile.
	if err := os.WriteFile(filepath.Join(dir, "tunnel.log"), []byte("nur Text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g := tunnelFehlergrund(); g != "" {
		t.Errorf("ohne Fehlerzeile: %q", g)
	}
	if err := os.WriteFile(filepath.Join(dir, "tunnel.log"), []byte("error: "+strings.Repeat("ä", 400)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g := tunnelFehlergrund(); len([]rune(g)) != 161 || !strings.HasSuffix(g, "…") {
		t.Errorf("Kürzung: %d Zeichen", len([]rune(g)))
	}
}
