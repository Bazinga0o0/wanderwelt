package fehlerbericht

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

var testJetzt = time.Date(2026, 11, 27, 14, 30, 0, 0, time.Local)

func zeit(d time.Time, s int) string {
	return d.Add(time.Duration(s)*time.Second).Format("2006/01/02 15:04:05") + ".123456 "
}

func schreibe(t *testing.T, path, inhalt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(inhalt), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lies(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Welche Zeilen des Starter-Logs zum Server-Lebenszyklus gehören.
func TestWanderweltZeileFilter(t *testing.T) {
	ja := []string{
		"host: Stand w1/12 eingereicht",
		"host: Zwischenstand alle 5m0s, Ende nach 10m0s ohne Spieler",
		"host: Zwischenstand: Änderung an region/r.-2.-5.mca beim Kopieren (Versuch 1/3, 1. Änderung in dieser Sitzung), neuer Versuch in 5s",
		"host: Zwischenstand: Versuch 2/3 nach Änderung an level.dat",
		"host: Zwischenstand nach 3 Versuchen nicht möglich: Änderung an level.dat",
		"winsys: Windows-Sitzung soll enden (lParam 0x0), Herunterfahren wird aufgehalten, bis der Schluss-Stand hochgeladen ist",
		"winsys: Hosten beendet, Herunterfahren wird freigegeben",
		"mcserver: Server gestartet (PID 4242) in C:\\x",
		"mcserver: Server sauber gestoppt",
		"worldsync: Datei geändert",
		"Fortsetzen: gemerkt (host, Welt \"\")",
		"Recover: Welt verworfen",
		"Übergabe: bereit (an \"Bob\")",
		"Übergabe-Angebot 7: Hosten von Bob übernehmen",
		"Zurückholen: frage neu",
		"Nachreichen: Welt w2 übersprungen: x",
		"Host-Sitzung beginnt (Zuschlag 5)",
		"Host-Sitzung beendet: <nil>",
		"Hosten beenden: Spiel für mich beenden",
		"Dauerbetrieb an (Netz)",
		"Selbst-Update: übergebe von 0.1.0 an 0.2.0",
		"Selbst-Update auf 0.2.0 gescheitert, wanderwelt läuft weiter",
		"Play: Rolle host (Zuschlag 5, Welt \"\", Head w1/3)",
		"wanderwelt 0.3.1 startet (Home C:\\x, PID 1)",
		"aktualisiert auf 0.3.1",
		"Welt übernehmen: Zuschlag 3 auf Basis \"\"/0",
		"Messung: {RAM:16}",
		"Wartung angekündigt für 03:00", // Stichwort/Präfix
		"Fehlermeldung: kaputt",
		"Etwas ist gescheitert: x",
		"Der Zuschlag wurde verweigert",
	}
	nein := []string{
		"Beitritt angefragt (Minecraft-Name Alice)",
		"Beitritt angenommen",
		"Beitritts-Link: wanderwelt://beitreten?s=w1.abcdefghij",
		"Gruppencode abgelehnt: Fehler",
		"Entdecken: Fehler beim Laden",
		"Minecraft-Name: Alice geändert",
		"Minecraft-Name gemeldet: Alice",
		"Hinweis im Fenster (fehler): Fehler beim Speichern",
		"Fenster: beitritt",
		"Tray bereit",
		"Steuerung: Fehler",
		"Gruppe gewechselt: abc",
		"Profil nicht gemeldet: Fehler",
		"Launcher-Profil: Fehler",
		"Autostart an (Fenster)",
		"Webseite öffnen: Fehler",
		"Symbol x.ico nicht geschrieben: Fehler",
		"Einrichtung wartet auf die Freigabe",
		"Installation gescheitert: x",
		"<Alice> hallo Welt, Fehler im Stand",         // Chat-artig (steht nie im Starter-Log, aber nie aufnehmen)
		"[Server thread/INFO]: Alice joined the game", // Minecraft-Log
		"",
		"irgendetwas anderes",
	}
	for _, m := range ja {
		l := zeit(testJetzt, 0) + m
		if m == "<Alice> hallo Welt, Fehler im Stand" {
			continue
		}
		if !WanderweltZeile(l) {
			t.Errorf("fehlt: %q", l)
		}
	}
	for _, m := range nein {
		l := zeit(testJetzt, 0) + m
		if WanderweltZeile(l) {
			t.Errorf("zu viel: %q", l)
		}
	}
	// Ohne Zeitstempel (Fortsetzungszeile) zählt der Text selbst.
	if !WanderweltZeile("host: Stand 7 eingereicht") || WanderweltZeile("Beitritt angenommen") {
		t.Error("Zeile ohne Zeitstempel")
	}
}

func TestHostKurzname(t *testing.T) {
	a, b := HostKurzname("maschine-1"), HostKurzname("maschine-2")
	if len(a) != 6 || a == b || a != HostKurzname("maschine-1") {
		t.Errorf("Kurznamen %q %q", a, b)
	}
	if strings.Contains(a, "maschine") || strings.Trim(a, "0123456789abcdef") != "" {
		t.Errorf("Kurzname %q ist kein Hash", a)
	}
}

// Aufbau eines Starter-Logs mit Zeilen von heute, gestern, fremden Themen
// und Geheimnissen.
func starterLogMit(t *testing.T, dir string) string {
	t.Helper()
	gestern := testJetzt.AddDate(0, 0, -1)
	var b strings.Builder
	w := func(d time.Time, s int, m string) { b.WriteString(zeit(d, s) + m + "\n") }
	w(gestern, 0, "host: Stand w1/3 gestern eingereicht")
	w(testJetzt, 0, "wanderwelt 0.3.1 startet (Home C:\\Users\\Max Mustermann\\AppData\\Local\\wanderwelt, PID 7)")
	w(testJetzt, 1, "host: Stand w1/4 eingereicht")
	w(testJetzt, 2, "Beitritt angefragt (Minecraft-Name Alice)")
	w(testJetzt, 3, "mcserver: Konsole: Antwort token=GEHEIMES-TOKEN-123 erhalten")
	w(testJetzt, 4, "Hinweis im Fenster (fehler): Fehler irgendwo")
	w(testJetzt, 5, "Fortsetzen: gemerkt (host)")
	b.WriteString("Zeile ohne Zeitstempel\n")
	w(testJetzt, 6, "host: Übergabe an Bob")
	p := filepath.Join(dir, "logs", "wanderwelt.log")
	schreibe(t, p, b.String())
	return p
}

func TestSchreibeWeltLogsInhalt(t *testing.T) {
	root := t.TempDir()
	world := filepath.Join(root, "world")
	schreibe(t, filepath.Join(world, "level.dat"), "daten")
	log := starterLogMit(t, root)
	s := NeuerSaeuberer(`C:\Users\Max Mustermann`, "Max Mustermann", "GEHEIMES-TOKEN-123")

	if err := SchreibeWeltLogs(world, log, "maschine-1", testJetzt, s); err != nil {
		t.Fatal(err)
	}
	name := "2026-11-27-" + HostKurzname("maschine-1") + ".log"
	got := lies(t, filepath.Join(world, WeltLogOrdner, name))
	for _, soll := range []string{"host: Stand w1/4 eingereicht", "Fortsetzen: gemerkt", "host: Übergabe an Bob", "wanderwelt 0.3.1 startet"} {
		if !strings.Contains(got, soll) {
			t.Errorf("fehlt %q:\n%s", soll, got)
		}
	}
	for _, nicht := range []string{"gestern", "Alice", "Beitritt", "GEHEIMES-TOKEN-123", "Max Mustermann", "Hinweis", "Zeile ohne Zeitstempel"} {
		if strings.Contains(got, nicht) {
			t.Errorf("%q steht in den Welt-Logs:\n%s", nicht, got)
		}
	}
	if !strings.Contains(got, "%USERPROFILE%") {
		t.Errorf("Benutzerpfad nicht ersetzt:\n%s", got)
	}
	if strings.Contains(name, "maschine") {
		t.Errorf("Dateiname %q enthält die Maschinen-ID", name)
	}
	// Nur der Log-Ordner ist entstanden; die Weltdatei ist unberührt.
	if lies(t, filepath.Join(world, "level.dat")) != "daten" {
		t.Error("level.dat verändert")
	}
	ents, _ := os.ReadDir(world)
	var namen []string
	for _, e := range ents {
		namen = append(namen, e.Name())
	}
	if !slices.Equal(namen, []string{"level.dat", WeltLogOrdner}) {
		t.Errorf("Weltordner enthält %v", namen)
	}
}

// Ohne Weltordner wird nichts angelegt (die Welt entsteht nie durch die Logs).
func TestSchreibeWeltLogsLegtWeltNichtAn(t *testing.T) {
	root := t.TempDir()
	log := starterLogMit(t, root)
	world := filepath.Join(root, "world")
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, NeuerSaeuberer("", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(world); !os.IsNotExist(err) {
		t.Errorf("Weltordner entstanden (%v)", err)
	}
	// Auch eine Datei an der Stelle des Ordners wird nicht angefasst.
	schreibe(t, world, "ich bin eine Datei")
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, NeuerSaeuberer("", "")); err != nil {
		t.Fatal(err)
	}
	if lies(t, world) != "ich bin eine Datei" {
		t.Error("Datei überschrieben")
	}
}

// Wiederholen ohne neue Zeilen ändert die Datei nicht (Inhalt und Zeit).
func TestSchreibeWeltLogsWiederholbar(t *testing.T) {
	root := t.TempDir()
	world := filepath.Join(root, "world")
	os.MkdirAll(world, 0o755)
	log := starterLogMit(t, root)
	s := NeuerSaeuberer("", "")
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, s); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(world, WeltLogOrdner, "2026-11-27-"+HostKurzname("m")+".log")
	eins := lies(t, p)
	fi1, _ := os.Stat(p)
	time.Sleep(20 * time.Millisecond)
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, s); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Stat(p)
	if lies(t, p) != eins || !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Error("unveränderte Logs wurden neu geschrieben")
	}
	// Kommt eine Zeile dazu, steht sie danach drin, die alten bleiben.
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	fmt.Fprintf(f, "%shost: Stand w1/5 eingereicht\n", zeit(testJetzt, 60))
	f.Close()
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, s); err != nil {
		t.Fatal(err)
	}
	zwei := lies(t, p)
	if !strings.HasPrefix(zwei, eins) || !strings.Contains(zwei, "w1/5") {
		t.Errorf("neue Zeile fehlt oder alte weg:\n%s", zwei)
	}
}

// Ist der Starter-Log gedreht worden, bleiben die früheren Zeilen des Tages
// aus der Datei erhalten; die neuen kommen dahinter ohne Doppelungen.
func TestSchreibeWeltLogsZusammenfuehren(t *testing.T) {
	root := t.TempDir()
	world := filepath.Join(root, "world")
	p := filepath.Join(world, WeltLogOrdner, "2026-11-27-"+HostKurzname("m")+".log")
	schreibe(t, p, zeit(testJetzt, -3600)+"host: Stand 1 eingereicht\n"+zeit(testJetzt, 5)+"host: Stand 2 eingereicht\n")
	log := filepath.Join(root, "logs", "wanderwelt.log")
	schreibe(t, log, zeit(testJetzt, 5)+"host: Stand 2 eingereicht\n"+zeit(testJetzt, 30)+"host: Stand 3 eingereicht\n")
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, NeuerSaeuberer("", "")); err != nil {
		t.Fatal(err)
	}
	got := lies(t, p)
	for i, sub := range []string{"Stand 1", "Stand 2", "Stand 3"} {
		if strings.Count(got, sub) != 1 {
			t.Errorf("%s %d×:\n%s", sub, strings.Count(got, sub), got)
		}
		_ = i
	}
	if strings.Index(got, "Stand 1") > strings.Index(got, "Stand 2") || strings.Index(got, "Stand 2") > strings.Index(got, "Stand 3") {
		t.Errorf("Reihenfolge:\n%s", got)
	}
}

// Größengrenze der Datei: höchstens 256 KiB, die ältesten Zeilen gehen
// zuerst, nur ganze Zeilen.
func TestSchreibeWeltLogsDateiGrenze(t *testing.T) {
	root := t.TempDir()
	world := filepath.Join(root, "world")
	os.MkdirAll(world, 0o755)
	var b strings.Builder
	n := 0
	for b.Len() < 3*MaxWeltLogDatei {
		n++
		fmt.Fprintf(&b, "%shost: Stand %06d eingereicht, %s\n", zeit(testJetzt, 0), n, strings.Repeat("x", 100))
	}
	log := filepath.Join(root, "logs", "wanderwelt.log")
	schreibe(t, log, b.String())
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, NeuerSaeuberer("", "")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(world, WeltLogOrdner, "2026-11-27-"+HostKurzname("m")+".log")
	got := lies(t, p)
	if len(got) > 262144 {
		t.Fatalf("Datei %d Bytes, erlaubt %d", len(got), MaxWeltLogDatei)
	}
	if len(got) < 262144-300 {
		t.Errorf("Datei nur %d Bytes: es wird zu viel weggeworfen", len(got))
	}
	zeilen := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if !strings.Contains(zeilen[len(zeilen)-1], fmt.Sprintf("Stand %06d", n)) {
		t.Errorf("jüngste Zeile fehlt: %q", zeilen[len(zeilen)-1])
	}
	if strings.Contains(got, "Stand 000001 ") {
		t.Error("die älteste Zeile ist noch da")
	}
	for _, z := range zeilen {
		if !strings.HasSuffix(z, strings.Repeat("x", 100)) {
			t.Fatalf("abgeschnittene Zeile %q", z)
		}
	}
}

func TestKuerzeZeile(t *testing.T) {
	if got := kuerzeZeile("abc", 10); got != "abc" {
		t.Errorf("%q", got)
	}
	got := kuerzeZeile(strings.Repeat("ä", 50), 11)
	if !strings.HasSuffix(got, "…") || len(got) > 11+len("…") {
		t.Errorf("%q", got)
	}
	for _, r := range got {
		if r == '�' {
			t.Errorf("zerschnittenes Zeichen: %q", got)
		}
	}
}

// Aufräumen: Tage, Gesamtgröße, Temp-Reste; Fremdes bleibt.
func TestAufraeumenTageUndGroesse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), WeltLogOrdner)
	os.MkdirAll(dir, 0o755)
	tag := func(d int) string { return testJetzt.AddDate(0, 0, -d).Format("2006-01-02") }
	eigene := tag(0) + "-aaaaaa.log"
	schreibe(t, filepath.Join(dir, eigene), "eigene\n")
	schreibe(t, filepath.Join(dir, tag(3)+"-bbbbbb.log"), "drei Tage alt\n") // bleibt
	schreibe(t, filepath.Join(dir, tag(4)+"-bbbbbb.log"), "vier Tage alt\n") // weg
	schreibe(t, filepath.Join(dir, tag(20)+"-cccccc.log"), "alt\n")          // weg
	schreibe(t, filepath.Join(dir, "notizen.txt"), "fremd")                  // bleibt
	schreibe(t, filepath.Join(dir, tag(40)+"-xyz.log"), "fremd")             // Name passt nicht: bleibt
	schreibe(t, filepath.Join(dir, ".wanderwelt-log-123.tmp"), "rest")       // weg
	if err := aufraeumen(dir, eigene, testJetzt); err != nil {
		t.Fatal(err)
	}
	var rest []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		rest = append(rest, e.Name())
	}
	slices.Sort(rest)
	want := []string{tag(0) + "-aaaaaa.log", tag(3) + "-bbbbbb.log", tag(40) + "-xyz.log", "notizen.txt"}
	slices.Sort(want)
	if !slices.Equal(rest, want) {
		t.Errorf("übrig %v, erwartet %v", rest, want)
	}
}

// Gesamtgrenze: Über 1 MiB gehen die ältesten Dateien zuerst; die eigene
// aktuelle bleibt immer.
func TestAufraeumenGesamtGrenze(t *testing.T) {
	dir := filepath.Join(t.TempDir(), WeltLogOrdner)
	os.MkdirAll(dir, 0o755)
	tag := func(d int) string { return testJetzt.AddDate(0, 0, -d).Format("2006-01-02") }
	block := strings.Repeat("y", 200<<10)
	var namen []string
	for d := 3; d >= 0; d-- { // 4 Tage, je Tag zwei Hosts: 8 × 200 KiB = 1,6 MiB
		for _, h := range []string{"aaaaaa", "bbbbbb"} {
			n := tag(d) + "-" + h + ".log"
			schreibe(t, filepath.Join(dir, n), block)
			namen = append(namen, n)
		}
	}
	eigene := tag(0) + "-bbbbbb.log"
	if err := aufraeumen(dir, eigene, testJetzt); err != nil {
		t.Fatal(err)
	}
	var total int64
	var rest []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		fi, _ := e.Info()
		total += fi.Size()
		rest = append(rest, e.Name())
	}
	if total > 1048576 {
		t.Errorf("Ordner %d Bytes, erlaubt %d", total, MaxWeltLogGesamt)
	}
	if len(rest) != 5 { // 5 × 200 KiB = 1000 KiB ≤ 1024 KiB, 6 wären zu viel
		t.Errorf("%d Dateien übrig, erwartet 5: %v", len(rest), rest)
	}
	// Die ältesten sind weg, die jüngsten und die eigene da.
	if !slices.Contains(rest, eigene) || !slices.Contains(rest, tag(0)+"-aaaaaa.log") {
		t.Errorf("neueste fehlen: %v", rest)
	}
	if slices.Contains(rest, namen[0]) || slices.Contains(rest, namen[1]) {
		t.Errorf("älteste noch da: %v", rest)
	}
	// Nur die eigene Datei da und zu groß ist: sie bleibt trotzdem (nie die eigene löschen).
	solo := filepath.Join(t.TempDir(), WeltLogOrdner)
	schreibe(t, filepath.Join(solo, tag(0)+"-aaaaaa.log"), strings.Repeat("z", 2<<20))
	if err := aufraeumen(solo, tag(0)+"-aaaaaa.log", testJetzt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(solo, tag(0)+"-aaaaaa.log")); err != nil {
		t.Errorf("eigene Datei gelöscht: %v", err)
	}
}

// Der Ordner landet im Stand (Stage + Manifest), alle Pfade sind gültig,
// Temp-Reste gelangen nicht hinein, und Weltdaten wie der Chatverlauf bleiben
// bitgleich.
func TestOrdnerLandetImStand(t *testing.T) {
	root := t.TempDir()
	world := filepath.Join(root, "world")
	daten := map[string]string{
		"level.dat":          "level",
		"region/r.0.0.mca":   strings.Repeat("c", 8192),
		"playerdata/abc.dat": "spieler",
		"wanderwelt-chatverlauf/chat-000001.jsonl": `{"zeit":1,"typ":"chat","name":"Alice","text":"hi"}` + "\n",
		"wanderwelt-chatverlauf/.wanderwelt-x.tmp": "temp im Chatordner",
		"wanderwelt-logs/.wanderwelt-log-9.tmp":    "Absturzrest",
		"wanderwelt-logs/2026-11-26-aaaaaa.log":    "gestern, anderer Host\n",
		"session.lock":                             "lock",
	}
	for p, c := range daten {
		schreibe(t, filepath.Join(world, filepath.FromSlash(p)), c)
	}
	log := starterLogMit(t, root)
	if err := SchreibeWeltLogs(world, log, "m", testJetzt, NeuerSaeuberer("", "")); err != nil {
		t.Fatal(err)
	}
	logName := "wanderwelt-logs/2026-11-27-" + HostKurzname("m") + ".log"

	staging := filepath.Join(root, "staging")
	hc := worldsync.LoadHashCache()
	if err := worldsync.StageCached(world, staging, hc); err != nil {
		t.Fatal(err)
	}
	files, err := worldsync.BuildManifest(staging, hc)
	if err != nil {
		t.Fatal(err)
	}
	var pfade []string
	for _, f := range files {
		if !api.ValidPath(f.Path) {
			t.Errorf("ungültiger Pfad im Stand: %q", f.Path)
		}
		pfade = append(pfade, f.Path)
	}
	for _, soll := range []string{logName, "wanderwelt-logs/2026-11-26-aaaaaa.log", "wanderwelt-chatverlauf/chat-000001.jsonl", "level.dat", "region/r.0.0.mca", "playerdata/abc.dat"} {
		if !slices.Contains(pfade, soll) {
			t.Errorf("%s fehlt im Stand (Manifest: %v)", soll, pfade)
		}
	}
	for _, nicht := range []string{"session.lock", "wanderwelt-logs/.wanderwelt-log-9.tmp", "wanderwelt-chatverlauf/.wanderwelt-x.tmp"} {
		if slices.Contains(pfade, nicht) {
			t.Errorf("%s gehört nicht in den Stand", nicht)
		}
	}
	// Weltdaten im Stand bitgleich, Chat unverändert.
	for _, p := range []string{"level.dat", "region/r.0.0.mca", "playerdata/abc.dat", "wanderwelt-chatverlauf/chat-000001.jsonl"} {
		if got := lies(t, filepath.Join(staging, filepath.FromSlash(p))); got != daten[p] {
			t.Errorf("%s im Stand verändert", p)
		}
		if got := lies(t, filepath.Join(world, filepath.FromSlash(p))); got != daten[p] {
			t.Errorf("%s in der Welt verändert", p)
		}
	}
	// Das Aufräumen löscht keine fremden Dateien und nichts außerhalb des
	// Log-Ordners.
	if _, err := os.Stat(filepath.Join(world, "wanderwelt-chatverlauf", "chat-000001.jsonl")); err != nil {
		t.Error(err)
	}
}

// Die Grenzen stehen fest (README und Datenschutz-Hinweis nennen sie): Datei
// 256 KiB, Ordner 1 MiB, 3 Tage.
func TestWeltLogGrenzenFest(t *testing.T) {
	if MaxWeltLogDatei != 256*1024 || MaxWeltLogGesamt != 1024*1024 || MaxWeltLogTage != 3 {
		t.Fatalf("Grenzen %d / %d / %d Tage, erwartet 262144 / 1048576 / 3", MaxWeltLogDatei, MaxWeltLogGesamt, MaxWeltLogTage)
	}
}
