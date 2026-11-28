package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fehlerbericht"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const (
	berichtTestToken = "tok-GEHEIM-4711xyz"
	berichtTestRcon  = "Rc0nGeheim987"
)

// berichtUmgebung richtet im aktuellen Home Starter-Log, Server-Ordner
// (server.properties mit RCON-Passwort, Host-Logs, Welt-Logs des Vor-Hosts)
// und einen Downloads-Ordner ein.
func berichtUmgebung(t *testing.T) (downloads, serverDir string) {
	t.Helper()
	downloads = filepath.Join(t.TempDir(), "Downloads")
	t.Setenv("WANDERWELT_DOWNLOADS", downloads)
	jetzt := time.Now()
	z := func(m string) string { return jetzt.Format("2006/01/02 15:04:05") + ".000001 " + m + "\n" }
	schreibeDatei(t, LogPath(),
		z("wanderwelt 0.3.1 startet (Home "+group.Home()+", PID 7)")+
			z("host: Stand w1/4 eingereicht")+
			z("Koordinator: Antwort 401 für Authorization: Bearer "+berichtTestToken))
	dir, err := group.ServerDir(group.FirstWorld)
	if err != nil {
		t.Fatal(err)
	}
	schreibeDatei(t, filepath.Join(dir, "server.properties"), "server-port=25565\nrcon.password="+berichtTestRcon+"\n")
	schreibeDatei(t, filepath.Join(dir, "logs", "latest.log"), "[12:00] Thread: RCON mit "+berichtTestRcon+" verbunden\n")
	schreibeDatei(t, filepath.Join(dir, "world", fehlerbericht.WeltLogOrdner, "2026-11-26-abc123.log"), "2026/11/26 10:00:00.000001 host: Stand w1/3 eingereicht\n")
	return downloads, dir
}

func schreibeDatei(t *testing.T, p, inhalt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(inhalt), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Der Fenster-Knopf (POST /v1/fehlerbericht): Zip in Downloads, Ordner im
// Explorer geöffnet, Hinweis, keine Geheimnisse (auch nicht das RCON-
// Passwort aus server.properties), und ein zweiter Aufruf während der
// Erstellung wird abgelehnt.
func TestFehlerberichtAktion(t *testing.T) {
	p := probeAnzeige(t)
	ft := startFenster(t, nil) // setzt ein frisches Home
	ft.a.version = "0.3.1"
	downloads, _ := berichtUmgebung(t)

	if err := ft.fc.Aktion(context.Background(), "fehlerbericht", nil); err != nil {
		t.Fatalf("Aktion: %v", err)
	}
	ents, err := os.ReadDir(downloads)
	if err != nil || len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), fehlerbericht.BerichtPrefix) || !strings.HasSuffix(ents[0].Name(), ".zip") {
		t.Fatalf("Downloads: %v %v", ents, err)
	}
	pfad := filepath.Join(downloads, ents[0].Name())
	namen, err := fehlerbericht.ZipNamen(pfad)
	if err != nil {
		t.Fatal(err)
	}
	for _, soll := range []string{"info.txt", "zustand.json", "manifest.txt", "starter-heute.log", "host-w1/latest.log", "host-w1/wanderwelt-logs/2026-11-26-abc123.log"} {
		if !slices.Contains(namen, soll) {
			t.Errorf("%s fehlt: %v", soll, namen)
		}
	}
	for _, n := range namen {
		b, _ := fehlerbericht.ZipLesen(pfad, n)
		for _, g := range []string{berichtTestToken, berichtTestRcon} {
			if strings.Contains(string(b), g) {
				t.Errorf("%s enthält das Geheimnis %q", n, g)
			}
		}
	}
	if b, _ := fehlerbericht.ZipLesen(pfad, "info.txt"); !strings.Contains(string(b), "Version: 0.3.1") {
		t.Errorf("info.txt: %s", b)
	}
	ft.mu.Lock()
	opened := slices.Clone(ft.opened)
	ft.mu.Unlock()
	if len(opened) != 1 || opened[0] != downloads {
		t.Errorf("Explorer öffnete %v, erwartet %s", opened, downloads)
	}
	if shown, _ := p.got(); len(shown) != 1 || !strings.Contains(shown[0], "Fehlerbericht gespeichert") || !strings.Contains(shown[0], ents[0].Name()) {
		t.Errorf("Hinweis: %v", shown)
	}

	// Läuft schon einer: abgelehnt, nichts Neues in Downloads.
	berichtLaeuft.Store(true)
	err = ft.fc.Aktion(context.Background(), "fehlerbericht", nil)
	berichtLaeuft.Store(false)
	if err == nil || !strings.Contains(err.Error(), "gerade erstellt") {
		t.Errorf("zweiter Bericht: %v", err)
	}
	if ents, _ := os.ReadDir(downloads); len(ents) != 1 {
		t.Errorf("Downloads enthält %d Einträge", len(ents))
	}
}

// Tray: Fehler beim Erstellen erscheinen als Dialog.
func TestFehlerberichtTrayFehler(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	berichtUmgebung(t)
	datei := filepath.Join(t.TempDir(), "keinordner")
	schreibeDatei(t, datei, "x")
	t.Setenv("WANDERWELT_DOWNLOADS", filepath.Join(datei, "unter"))
	p := probeAnzeige(t)
	a := busyApp(t, nil)
	a.berichtTray()
	shown, _ := p.got()
	if len(shown) != 1 || !strings.HasPrefix(shown[0], "fehler: Der Fehlerbericht konnte nicht erstellt werden") {
		t.Errorf("angezeigt: %v", shown)
	}
	if berichtLaeuft.Load() {
		t.Error("berichtLaeuft bleibt gesetzt")
	}
}

// Der Haken der Host-Sitzung schreibt die Logs in den Weltordner, gesäubert.
func TestWeltLogsHaken(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	_, dir := berichtUmgebung(t)
	world := filepath.Join(dir, "world")
	weltLogsHaken("maschine-1")(world)
	name := time.Now().Format("2006-01-02") + "-" + fehlerbericht.HostKurzname("maschine-1") + ".log"
	b, err := os.ReadFile(filepath.Join(world, fehlerbericht.WeltLogOrdner, name))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "host: Stand w1/4 eingereicht") || strings.Contains(s, berichtTestToken) {
		t.Errorf("Inhalt:\n%s", s)
	}
}

func TestRconPasswort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.properties")
	schreibeDatei(t, p, "a=b\n  rcon.password = x\nrcon.password=geheim123\n")
	// Die erste Zeile mit rcon.password= (ohne Leerzeichen um das =) zählt.
	if got := rconPasswort(p); got != "geheim123" {
		t.Errorf("%q", got)
	}
	if got := rconPasswort(filepath.Join(t.TempDir(), "gibtsnicht")); got != "" {
		t.Errorf("%q", got)
	}
}

// Der Zustand im Fehlerbericht enthält nur den Minecraft-Namen des Hosts:
// keine E-Mail-Adresse (die Host-Struktur trägt sie, Fund im ersten Bericht).
func TestBerichtZustandOhneEmailDesHosts(t *testing.T) {
	a := busyApp(t, nil)
	a.mu.Lock()
	a.status = &api.Status{Host: &api.HostInfo{Email: "geheim.person@example.org", MinecraftName: "Steve_1", MachineID: "pc-xyz"}, Players: []string{"A", "B"}}
	a.mu.Unlock()
	z := a.berichtZustand()
	data, err := json.Marshal(z)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, verboten := range []string{"geheim.person", "example.org", "@", "pc-xyz"} {
		if strings.Contains(s, verboten) {
			t.Errorf("Zustand enthält %q: %s", verboten, s)
		}
	}
	if z.Status == nil || z.Status.HostName != "Steve_1" || z.Status.Spieler != 2 {
		t.Errorf("Status: %+v", z.Status)
	}
}
