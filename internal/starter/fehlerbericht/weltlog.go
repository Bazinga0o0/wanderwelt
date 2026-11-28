package fehlerbericht

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Logs, die mit der Welt wandern: Der Host legt vor jedem Stand die
// wanderwelt-Logzeilen des Tages in <Welt>/wanderwelt-logs/ (siehe README,
// Abschnitt „Fehlerbericht und Logs der Welt“).
// worldsync überträgt den ganzen Weltordner; der nächste Host bekommt die
// Dateien also mit dem Stand, der Admin sieht sie im Weltordner.
const (
	// WeltLogOrdner ist der Ordner im Weltordner.
	WeltLogOrdner = "wanderwelt-logs"
	// MaxWeltLogDatei: so groß wird eine Tagesdatei höchstens (älteste Zeilen
	// zuerst weg).
	MaxWeltLogDatei = 256 << 10
	// MaxWeltLogGesamt: so groß wird der Ordner höchstens (älteste Dateien
	// zuerst weg).
	MaxWeltLogGesamt = 1 << 20
	// MaxWeltLogTage: Dateien mit einem Datum, das mehr Tage zurückliegt,
	// werden gelöscht (heute und die 3 Tage davor bleiben).
	MaxWeltLogTage = 3

	// maxZeile: längere Zeilen werden gekürzt.
	maxZeile = 2000
	// maxStarterLogLesen: So viel vom Ende jeder Starter-Logdatei wird
	// gelesen.
	maxStarterLogLesen = 2 << 20

	tmpPrefix = ".wanderwelt-log-"
)

// weltLogName: <JJJJ-MM-TT>-<6 Hex>.log
var weltLogName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-([0-9a-f]{6})\.log$`)

// HostKurzname ist der Kurzname eines PCs in den Dateinamen: die ersten
// 6 Hex-Zeichen von SHA-256 der Maschinen-ID. Kein Rechnername, je PC (und
// Gruppe) stabil.
func HostKurzname(maschinenID string) string {
	sum := sha256.Sum256([]byte("wanderwelt-log\x00" + maschinenID))
	return hex.EncodeToString(sum[:3])
}

// Zeilenanfang des Starter-Logs (log.LstdFlags|log.Lmicroseconds):
// "2026/11/27 12:34:56.123456 ".
const zeitLaenge = len("2026/11/27 12:34:56.123456 ")

// praefixeJa: Nachrichten des Starters mit Bezug zum Server-Lebenszyklus.
var praefixeJa = []string{
	"host:", "mcserver:", "worldsync:", "Fortsetzen:", "Recover:", "Übergabe", "Zurückholen:",
	"Nachreichen:", "Host-Sitzung", "Hosten ", "Welt übernehmen:", "Welt:", "Welt gewählt:",
	"Play:", "Spielen:", "Dauerbetrieb", "Selbst-Update", "Update:", "Koordinator:",
	"winsys:", "Notfallkopie:", "Bereitschaft:", "Messung", "Mitspielen:", "Wartefenster:", "Wartung",
	"wanderwelt ", "aktualisiert auf", "Fehlermeldung:", "Zustand nicht gespeichert:",
}

// praefixeNein: Nachrichten, die nie in die Welt-Logs kommen (Beitritte,
// Gruppencodes, Namen, Oberfläche), auch wenn ein Stichwort darin steht.
var praefixeNein = []string{
	"Beitritt", "Entdecken:", "Gruppencode", "Beitritts-Link", "Minecraft-Name", "Hinweis", "Meldung:",
	"Tray", "Steuerung", "Fenster:", "Gruppe ", "Gruppen-Layout", "installierte Gruppendatei",
	"Profil", "Launcher-Profil", "Einrichtung", "Installation", "Webseite öffnen", "Symbol", "Autostart",
}

// stichwoerter: Auch ohne bekanntes Präfix gehören Zeilen mit diesen Wörtern
// (Groß-/Kleinschreibung egal) dazu.
var stichwoerter = []string{
	"zuschlag", "wartung", "selbst-update", "übergabe", "fortsetzen", "fehler", "gescheitert", "panic",
	"fencing", "stand ",
}

// WanderweltZeile meldet, ob eine Zeile des Starter-Logs (mit Zeitstempel)
// zum Server-Lebenszyklus gehört: host, mcserver, Stand, Zuschlag, Übergabe,
// Wartung, Fortsetzen, Selbst-Update, Fehler. Nie der Serverlog von Minecraft
// (der steht nicht im Starter-Log) und nichts zu Beitritten oder Chat.
func WanderweltZeile(zeile string) bool {
	msg := zeile
	if len(zeile) > zeitLaenge && zeile[4] == '/' && zeile[7] == '/' {
		msg = zeile[zeitLaenge:]
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return false
	}
	// Chat- und Minecraft-Logzeilen ("<Name> Text", "[Server thread/INFO]: …")
	// gehören nie hinein, auch wenn sie sich einmal in den Starter-Log
	// verirren.
	if msg[0] == '<' || msg[0] == '[' {
		return false
	}
	for _, p := range praefixeNein {
		if strings.HasPrefix(msg, p) {
			return false
		}
	}
	for _, p := range praefixeJa {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	lo := strings.ToLower(msg)
	for _, k := range stichwoerter {
		if strings.Contains(lo, k) {
			return true
		}
	}
	return false
}

// SchreibeWeltLogs legt die wanderwelt-Logzeilen von heute (Ortszeit) aus dem
// Starter-Log starterLog (und dessen .1) in
// worldDir/wanderwelt-logs/<JJJJ-MM-TT>-<kurz>.log, gesäubert mit s, hält die
// Grenzen ein (Datei 256 KiB, Ordner 1 MiB, 3 Tage) und räumt auf.
//
// Der Weltordner muss schon da sein (er wird nie angelegt). Geschrieben wird
// über eine Temp-Datei, nur wenn sich der Inhalt ändert. Aufrufen darf man
// das nur, solange kein Stand des Weltordners gebildet wird (der Host tut es
// vor save-off bzw. nach dem Stop); es berührt nichts außerhalb von
// wanderwelt-logs/.
func SchreibeWeltLogs(worldDir, starterLog, maschinenID string, jetzt time.Time, s *Saeuberer) error {
	if fi, err := os.Stat(worldDir); err != nil || !fi.IsDir() {
		return nil // noch keine Welt: nichts zu tun, nichts anlegen
	}
	dir := filepath.Join(worldDir, WeltLogOrdner)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	jetzt = jetzt.Local()
	name := jetzt.Format("2006-01-02") + "-" + HostKurzname(maschinenID) + ".log"
	path := filepath.Join(dir, name)
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	frisch := heutigeZeilen(starterLog, jetzt, s)
	alt, _ := os.ReadFile(path)
	neu := zusammenfuehren(zeilenVon(alt), frisch)
	neu = kuerzenAufBytes(neu, MaxWeltLogDatei)
	if len(neu) > 0 {
		inhalt := []byte(strings.Join(neu, "\n") + "\n")
		if !bytes.Equal(inhalt, alt) {
			keep(schreibeAtomar(dir, path, inhalt))
		}
	}
	keep(aufraeumen(dir, name, jetzt))
	return firstErr
}

// zeilenVon teilt Dateiinhalt in Zeilen (ohne leere am Ende).
func zeilenVon(b []byte) []string {
	t := strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if t == "" {
		return nil
	}
	return strings.Split(t, "\n")
}

// heutigeZeilen liest das Ende von starterLog.1 und starterLog und liefert
// die gesäuberten wanderwelt-Zeilen des Tages von jetzt.
func heutigeZeilen(starterLog string, jetzt time.Time, s *Saeuberer) []string {
	tag := jetzt.Format("2006/01/02") + " "
	var out []string
	for _, p := range []string{starterLog + ".1", starterLog} {
		b, err := leseEnde(p, maxStarterLogLesen)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimRight(l, "\r")
			if !strings.HasPrefix(l, tag) || !WanderweltZeile(l) {
				continue
			}
			out = append(out, kuerzeZeile(s.Zeile(l), maxZeile))
		}
	}
	return out
}

// kuerzeZeile kürzt auf höchstens n Bytes an einer Zeichengrenze.
func kuerzeZeile(l string, n int) string {
	if len(l) <= n {
		return l
	}
	for n > 0 && !utf8.RuneStart(l[n]) {
		n--
	}
	return l[:n] + "…"
}

// zusammenfuehren: Zeilen der alten Datei, die vor der ersten frischen Zeile
// liegen (nach Zeitstempel), bleiben erhalten – der Starter-Log kann
// inzwischen gedreht worden sein –, danach gilt der Starter-Log.
func zusammenfuehren(alt, frisch []string) []string {
	if len(frisch) == 0 {
		return alt
	}
	if len(alt) == 0 {
		return frisch
	}
	grenze := frisch[0]
	if len(grenze) > zeitLaenge {
		grenze = grenze[:zeitLaenge]
	}
	var out []string
	for _, l := range alt {
		k := l
		if len(k) > zeitLaenge {
			k = k[:zeitLaenge]
		}
		if k >= grenze {
			break
		}
		out = append(out, l)
	}
	return append(out, frisch...)
}

// kuerzenAufBytes wirft die ältesten Zeilen weg, bis der Text (mit je einem
// Zeilenumbruch) höchstens max Bytes hat.
func kuerzenAufBytes(zeilen []string, max int) []string {
	total := 0
	for _, l := range zeilen {
		total += len(l) + 1
	}
	i := 0
	for total > max && i < len(zeilen) {
		total -= len(zeilen[i]) + 1
		i++
	}
	return zeilen[i:]
}

// leseEnde liest höchstens die letzten n Bytes; beginnt das Stück mitten in
// einer Zeile, wird diese angefangene Zeile verworfen.
func leseEnde(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if fi.Size() > n {
		start = fi.Size() - n
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			b = nil
		}
	}
	return b, nil
}

// LeseEnde liest höchstens die letzten n Bytes einer Datei (an einer
// Zeilengrenze).
func LeseEnde(path string, n int64) ([]byte, error) { return leseEnde(path, n) }

// schreibeAtomar schreibt über eine Temp-Datei im selben Ordner (Muster
// .wanderwelt-*.tmp, das worldsync nie überträgt) und benennt um.
func schreibeAtomar(dir, path string, inhalt []byte) error {
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(inhalt)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// aufraeumen löscht Temp-Reste, Dateien älter als MaxWeltLogTage und – bis
// der Ordner unter MaxWeltLogGesamt liegt – die ältesten, nie die Datei
// eigene. Fremde Dateien (Namen, die nicht dem Muster entsprechen) bleiben
// unberührt.
func aufraeumen(dir, eigene string, jetzt time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	heute := time.Date(jetzt.Year(), jetzt.Month(), jetzt.Day(), 0, 0, 0, 0, time.Local)
	grenze := heute.AddDate(0, 0, -MaxWeltLogTage)
	type datei struct {
		name string
		tag  time.Time
		size int64
	}
	var rest []datei
	var total int64
	var firstErr error
	rm := func(name string) {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, tmpPrefix) && strings.HasSuffix(n, ".tmp") {
			rm(n)
			continue
		}
		m := weltLogName.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		tag, err := time.ParseInLocation("2006-01-02", m[1], time.Local)
		if err != nil {
			continue
		}
		if tag.Before(grenze) && n != eigene {
			rm(n)
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		rest = append(rest, datei{n, tag, info.Size()})
	}
	slices.SortFunc(rest, func(a, b datei) int {
		if c := a.tag.Compare(b.tag); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	for _, d := range rest {
		if total <= MaxWeltLogGesamt {
			break
		}
		if d.name == eigene {
			continue
		}
		rm(d.name)
		total -= d.size
	}
	if firstErr != nil {
		return fmt.Errorf("wanderwelt-logs aufräumen: %w", firstErr)
	}
	return nil
}
