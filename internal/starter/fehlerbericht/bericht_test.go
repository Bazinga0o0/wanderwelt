package fehlerbericht

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Geheimnisse, die in den präparierten Logs stehen und im Zip nirgends
// vorkommen dürfen.
var berichtGeheimnisse = []string{
	"tok-GEHEIM-1234abcd",                     // Token der Gruppendatei
	"rconPASS99",                              // RCON-Passwort
	"BearerSecretValue123",                    // Bearer-Header
	"https://discord.com/api/webhooks/123456", // Webhook
	"WebhookTokenXyz_abc-DEF",
	"nutzer:pw123@",                 // URL-Zugangsdaten
	"Max Mustermann",                // Windows-Benutzername
	"w1.aHR0cHM6Ly9leGFtcGxlLm9yZw", // Gruppencode
	"203.0.113.77",                  // Spieler-IP
	"SuperSecretPassword!",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.SflKxwRJSMeKKF2QT4fw",
}

// präparierte Quellen: Starter-Log, Host-Logs, Welt-Logs des Vor-Hosts, Mods.
func berichtQuellen(t *testing.T) (Eingabe, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	gestern := testJetzt.AddDate(0, 0, -1)
	var sl strings.Builder
	w := func(d time.Time, m string) { sl.WriteString(zeit(d, 0) + m + "\n") }
	w(gestern.Add(-48*time.Hour), "host: Stand vorgestern")
	w(gestern, "host: Stand w1/3 gestern eingereicht")
	w(gestern, `mcserver: Pfad C:\Users\Max Mustermann\AppData\Local\wanderwelt\server`)
	w(testJetzt, "wanderwelt 0.3.1 startet (Home C:\\Users\\Max Mustermann\\AppData, PID 7)")
	w(testJetzt, "Koordinator: Antwort 401, Authorization: Bearer BearerSecretValue123")
	w(testJetzt, "Beitritts-Link: wanderwelt://beitreten?s=w1.aHR0cHM6Ly9leGFtcGxlLm9yZw")
	w(testJetzt, "Discord: post https://discord.com/api/webhooks/123456789/WebhookTokenXyz_abc-DEF fehlgeschlagen")
	w(testJetzt, "Update: lade https://nutzer:pw123@example.org/x.exe")
	w(testJetzt, "Fehler: Token tok-GEHEIM-1234abcd abgelehnt")
	sl.WriteString("fortgesetzte Zeile ohne Datum gehört zu heute\n")
	slog := filepath.Join(home, "logs", "wanderwelt.log")
	schreibe(t, slog, sl.String())
	schreibe(t, slog+".1", zeit(gestern, 0)+"host: Stand aus dem alten Log\n")

	srv := filepath.Join(home, "server")
	// Host-Log größer als 2 MiB: nur das Ende kommt mit.
	var hl strings.Builder
	for i := 0; hl.Len() < 3<<20; i++ {
		fmt.Fprintf(&hl, "[12:00:00] [Server thread/INFO]: Zeile %07d\n", i)
	}
	hl.WriteString("[12:00:01] [Server thread/INFO]: rcon.password=rconPASS99\n")
	hl.WriteString("[12:00:02] [Server thread/INFO]: Alice[/203.0.113.77:54321] logged in\n")
	schreibe(t, filepath.Join(srv, "logs", "wanderwelt-server.log"), hl.String())
	schreibe(t, filepath.Join(srv, "logs", "latest.log"), "[12:00:00] [Server thread/INFO]: Starting\npassword = SuperSecretPassword!\n")
	// Logs des Vor-Hosts, mit der Welt gewandert.
	schreibe(t, filepath.Join(srv, "world", WeltLogOrdner, "2026-11-26-abc123.log"), zeit(gestern, 0)+"host: Stand 9 vom Vor-Host, token=tok-GEHEIM-1234abcd\n")
	schreibe(t, filepath.Join(srv, "world", WeltLogOrdner, "notizen.txt"), "fremde Datei im Log-Ordner")
	schreibe(t, filepath.Join(srv, "world", "level.dat"), "WELTDATEN DÜRFEN NICHT MIT")
	schreibe(t, filepath.Join(srv, "world", "playerdata", "abc.dat"), "SPIELERDATEN")
	schreibe(t, filepath.Join(srv, "mods", "fabric-api-0.161.0+26.2.jar"), "jar")
	schreibe(t, filepath.Join(srv, "mods", "lithium.jar"), "jar")

	return Eingabe{
		Version:    "0.3.1",
		System:     "Windows: 10.0.26300 (amd64)\nProzessor: Test-CPU, 8 logische Kerne\n",
		StarterLog: slog,
		Welten:     []Welt{{ID: "", ServerDir: srv}, {ID: "w2", ServerDir: filepath.Join(root, "gibtsnicht")}},
		Zustand:    []byte(`{"Phase":"Hostet","LastErr":"Token tok-GEHEIM-1234abcd abgelehnt","Pfad":"C:\\Users\\Max Mustermann\\x"}`),
		Manifest:   "minecraft_version: 26.2\nmods:\n  - name: lithium\n    hash: sha512:abcdef0123456789\n",
		Jetzt:      testJetzt,
		San:        NeuerSaeuberer(`C:\Users\Max Mustermann`, "Max Mustermann", "tok-GEHEIM-1234abcd", "rconPASS99"),
	}, root
}

func TestErstellenZipOhneGeheimnisse(t *testing.T) {
	in, root := berichtQuellen(t)
	ziel := filepath.Join(root, "Downloads")
	pfad, err := Erstellen(in, ziel)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ziel, "wanderwelt-fehlerbericht-20261127-143000.zip"); pfad != want {
		t.Errorf("Pfad %q, erwartet %q", pfad, want)
	}
	namen, err := ZipNamen(pfad)
	if err != nil {
		t.Fatal(err)
	}
	for _, soll := range []string{
		"info.txt", "zustand.json", "manifest.txt", "starter-heute.log", "starter-gestern.log",
		"host-haupt/wanderwelt-server.log", "host-haupt/latest.log",
		"host-haupt/wanderwelt-logs/2026-11-26-abc123.log",
	} {
		if !slices.Contains(namen, soll) {
			t.Errorf("%s fehlt im Zip (%v)", soll, namen)
		}
	}
	for _, nicht := range []string{"level.dat", "abc.dat", "notizen.txt"} {
		for _, n := range namen {
			if strings.Contains(n, nicht) {
				t.Errorf("%s gehört nicht in den Bericht: %s", nicht, n)
			}
		}
	}
	// Kein Geheimnis in irgendeinem Eintrag, auch nicht in den Namen.
	var alles strings.Builder
	for _, n := range namen {
		alles.WriteString(n + "\n")
		b, err := ZipLesen(pfad, n)
		if err != nil {
			t.Fatal(err)
		}
		alles.Write(b)
		alles.WriteString("\n")
	}
	for _, g := range berichtGeheimnisse {
		if strings.Contains(alles.String(), g) {
			t.Errorf("%q steht im Zip", g)
		}
	}
	// Inhalt: heute, gestern und die Reihenfolge stimmen.
	heute, _ := ZipLesen(pfad, "starter-heute.log")
	gest, _ := ZipLesen(pfad, "starter-gestern.log")
	if !strings.Contains(string(heute), "startet") || !strings.Contains(string(heute), "fortgesetzte Zeile ohne Datum") || strings.Contains(string(heute), "gestern") {
		t.Errorf("starter-heute.log:\n%s", heute)
	}
	if !strings.Contains(string(gest), "gestern eingereicht") || !strings.Contains(string(gest), "alten Log") || strings.Contains(string(gest), "vorgestern") || strings.Contains(string(gest), "startet") {
		t.Errorf("starter-gestern.log:\n%s", gest)
	}
	if !strings.Contains(string(heute), "%USERPROFILE%") {
		t.Errorf("Benutzerpfad nicht ersetzt:\n%s", heute)
	}
	// Host-Log gekürzt auf das Ende (höchstens 2 MiB), mit der letzten Zeile.
	srv, _ := ZipLesen(pfad, "host-haupt/wanderwelt-server.log")
	if len(srv) > MaxLogStueck || !strings.Contains(string(srv), "rcon.password=[entfernt]") || strings.Contains(string(srv), "Zeile 0000000") {
		t.Errorf("Host-Log: %d Bytes, Ende/Anfang falsch", len(srv))
	}
	if !strings.Contains(string(srv), "[/[IP entfernt]]") {
		t.Errorf("Spieler-IP nicht ersetzt")
	}
	// Welt-Logs des Vor-Hosts mit Inhalt, gesäubert.
	vor, _ := ZipLesen(pfad, "host-haupt/wanderwelt-logs/2026-11-26-abc123.log")
	if !strings.Contains(string(vor), "Stand 9 vom Vor-Host") {
		t.Errorf("Welt-Log des Vor-Hosts: %q", vor)
	}
	// Manifest und Mods.
	man, _ := ZipLesen(pfad, "manifest.txt")
	for _, s := range []string{"minecraft_version: 26.2", "sha512:abcdef", "fabric-api-0.161.0+26.2.jar", "lithium.jar"} {
		if !strings.Contains(string(man), s) {
			t.Errorf("manifest.txt ohne %q:\n%s", s, man)
		}
	}
	// info.txt: Version, System, kein Rechner-/Benutzername; Zustand als JSON.
	info, _ := ZipLesen(pfad, "info.txt")
	for _, s := range []string{"Version: 0.3.1", "Windows: 10.0.26300", "Test-CPU", "Weitergeben"} {
		if !strings.Contains(string(info), s) {
			t.Errorf("info.txt ohne %q:\n%s", s, info)
		}
	}
	zu, _ := ZipLesen(pfad, "zustand.json")
	if !strings.Contains(string(zu), `"Phase":"Hostet"`) || !strings.Contains(string(zu), "%USERPROFILE%") {
		t.Errorf("zustand.json: %s", zu)
	}
	// Kein Temp-Rest im Zielordner.
	ents, _ := os.ReadDir(ziel)
	if len(ents) != 1 {
		t.Errorf("Zielordner enthält %d Einträge, erwartet nur das Zip", len(ents))
	}
}

// Fehlende Quellen sind ein Hinweis, kein Fehler; zwei Berichte in einer
// Sekunde überschreiben einander nicht.
func TestErstellenFehlendeQuellen(t *testing.T) {
	root := t.TempDir()
	in := Eingabe{
		Version:    "dev",
		StarterLog: filepath.Join(root, "gibt", "es", "nicht.log"),
		Welten:     []Welt{{ID: "w9", ServerDir: filepath.Join(root, "kein-server")}},
		Jetzt:      testJetzt,
		San:        NeuerSaeuberer("", ""),
	}
	p1, err := Erstellen(in, filepath.Join(root, "dl"))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Erstellen(in, filepath.Join(root, "dl"))
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatal("zweiter Bericht überschreibt den ersten")
	}
	for _, p := range []string{p1, p2} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	info, _ := ZipLesen(p1, "info.txt")
	for _, s := range []string{"Starter-Log nicht gefunden", "Zustand der Instanz nicht verfügbar", "Hinweise:"} {
		if !strings.Contains(string(info), s) {
			t.Errorf("info.txt ohne %q:\n%s", s, info)
		}
	}
	man, _ := ZipLesen(p1, "manifest.txt")
	if !strings.Contains(string(man), "nicht verfügbar") {
		t.Errorf("manifest.txt: %s", man)
	}
}

// Ein nicht schreibbares Ziel ist ein Fehler und hinterlässt nichts.
func TestErstellenZielNichtSchreibbar(t *testing.T) {
	root := t.TempDir()
	datei := filepath.Join(root, "ich-bin-eine-datei")
	schreibe(t, datei, "x")
	_, err := Erstellen(Eingabe{Jetzt: testJetzt, San: NeuerSaeuberer("", "")}, filepath.Join(datei, "unter"))
	if err == nil {
		t.Fatal("kein Fehler bei unmöglichem Ziel")
	}
}

// Weltnamen im Zip sind harmlos, auch bei einer seltsamen Kennung.
func TestWeltOrdnerName(t *testing.T) {
	cases := map[string]string{"": "host-haupt", "w2": "host-w2", "../x": "host-welt", "a b": "host-welt", `a\b`: "host-welt"}
	for in, want := range cases {
		if got := weltOrdnerName(in); got != want {
			t.Errorf("%q -> %q, erwartet %q", in, got, want)
		}
	}
}
