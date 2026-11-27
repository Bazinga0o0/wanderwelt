// Package stats zählt pro Mitglied Spielzeit, Hostzeit und Host-Sitzungen
// (Reiter „Statistik“, GET /v1/stats).
//
// Quelle sind allein die Lebenszeichen des Hosts (alle 10 s, mit der Liste
// der Spieler, die gerade auf dem Server sind). Der Koordinator ruft bei
// jedem Lebenszeichen des Haupt-Zuschlags Sample auf; zwischen zwei
// aufeinanderfolgenden Lebenszeichen desselben Zuschlags vergeht die Zeit
// gap, und sie wird so verteilt:
//
//   - Hostzeit: gap für den Host.
//   - Spielzeit: gap für jeden, der in beiden Lebenszeichen als Spieler
//     stand, gap/2 für jeden, der nur in einem stand (Trapezregel: Wer
//     zwischen zwei Lebenszeichen kommt oder geht, wird im Mittel richtig
//     gezählt). Wer zwischen zwei Lebenszeichen kommt und wieder geht, ohne
//     in einem zu stehen, wird nicht gezählt.
//
// Ungenauigkeit also: je Beitritt, Austritt und Hostbeginn bis zu einem
// Lebenszeichen-Abstand (10 s), im Mittel nichts.
//
// Keine Phantomzeiten: Gezählt wird nur Zeit zwischen zwei Lebenszeichen,
// die höchstens MaxGap auseinanderliegen. Stirbt der Host, endet die Zählung
// mit seinem letzten Lebenszeichen (die 90 s bis zum Ablauf des Zuschlags
// zählen nicht). Ein größerer Abstand (Netz weg, Koordinator neu gestartet)
// zählt gar nicht; mit dem nächsten Lebenszeichen beginnt die Zählung neu.
//
// Eine Host-Sitzung ist ein Zuschlag (LeaseID); sie wird mit dem ersten
// Lebenszeichen des Haupt-Zuschlags gezählt, auch nach einem Neustart des
// Koordinators nur einmal. Bei einer Übergabe zählt der wartende Zuschlag
// nicht (der Aufrufer meldet nur den Haupt-Zuschlag), erst der beförderte
// ab seinem ersten Lebenszeichen als Haupt-Zuschlag: nichts doppelt.
//
// Gespeichert wird nach data_dir/stats.json (atomar, höchstens alle
// SaveEvery und bei Flush): Tageswerte je Mitglied für die letzten KeepDays
// Tage (Tage nach UTC; ein Abstand über Mitternacht zählt ganz zum Tag des
// späteren Lebenszeichens), ältere Tage nur als Summe je Mitglied. Die Datei
// bleibt so bei einer Handvoll Mitgliedern unter 100 kB. Mitglieder sind
// über ihre E-Mail-Adresse (klein) geschlüsselt, damit Umbenennen des
// Minecraft-Namens die Zahlen nicht trennt; die Datei verlässt den Server
// nicht. Nur Zeiten und Zähler: keine IP, keine Hardware, keine Chat- oder
// Positionsdaten.
package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// MaxGap: Größter Abstand zweier Lebenszeichen, der noch zählt (drei
	// Lebenszeichen-Abstände; der Host sendet alle 10 s).
	MaxGap = 30 * time.Second
	// SaveEvery: Mindestabstand zwischen zwei Schreibvorgängen.
	SaveEvery = time.Minute
	// KeepDays: So viele Tage (heute eingerechnet) bleiben einzeln; ältere
	// gehen in die Summe. Muss mindestens Window30 sein.
	KeepDays = 45
	// Window30: Länge des Zeitraums „30 Tage“ (heute und die 29 Tage davor).
	Window30 = 30
	// version der Datei.
	version = 1
)

// Totals sind die Summen eines Mitglieds in einem Zeitraum.
type Totals struct {
	PlayMS   int64 `json:"play_ms"`
	HostMS   int64 `json:"host_ms"`
	Sessions int   `json:"sessions"`
}

func (t *Totals) add(o Totals) {
	t.PlayMS += o.PlayMS
	t.HostMS += o.HostMS
	t.Sessions += o.Sessions
}

// file ist der Inhalt von stats.json.
type file struct {
	Version int `json:"version"`
	// Since: erstes gezähltes Lebenszeichen.
	Since time.Time `json:"since,omitzero"`
	// LastLease: Zuschlag der zuletzt gezählten Sitzung (gegen Doppelzählung
	// nach einem Neustart).
	LastLease int64 `json:"last_lease,omitempty"`
	// Days: "2026-11-26" → Mitglied (E-Mail klein) → Werte.
	Days map[string]map[string]Totals `json:"days"`
	// Older: Mitglied → Summe aller Tage, die älter als KeepDays sind.
	Older map[string]Totals `json:"older"`
}

// sample ist das letzte Lebenszeichen der laufenden Zählung.
type sample struct {
	lease   int64
	at      time.Time
	players map[string]bool
}

// Tracker zählt und speichert. Nebenläufig nutzbar.
type Tracker struct {
	path string // "": nicht speichern

	mu       sync.Mutex
	f        file
	last     *sample
	dirty    bool
	lastSave time.Time
}

// Open lädt path (leer: nur im Speicher). Fehlt die Datei, beginnt die
// Zählung leer. Eine kaputte Datei wird als <Datei>.kaputt-<Zeit> beiseite-
// gelegt (und geloggt), die Zählung beginnt leer; eine nicht lesbare Datei
// ist ein Fehler.
func Open(path string, now time.Time) (*Tracker, error) {
	t := &Tracker{path: path, f: leer(), lastSave: now}
	if path == "" {
		return t, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stats: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil || f.Version != version {
		why := "Version " + fmt.Sprint(f.Version)
		if err != nil {
			why = err.Error()
		}
		kaputt := path + ".kaputt-" + now.UTC().Format("20060102T150405Z")
		if rerr := os.Rename(path, kaputt); rerr != nil {
			return nil, fmt.Errorf("stats: kaputte Datei beiseitelegen: %w", rerr)
		}
		slog.Error("Statistik: stats.json unbrauchbar, beiseitegelegt, Zählung beginnt neu", "grund", why, "kopie", kaputt)
		return t, nil
	}
	if f.Days == nil {
		f.Days = map[string]map[string]Totals{}
	}
	if f.Older == nil {
		f.Older = map[string]Totals{}
	}
	t.f = f
	t.compactLocked(now)
	return t, nil
}

func leer() file {
	return file{Version: version, Days: map[string]map[string]Totals{}, Older: map[string]Totals{}}
}

func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Sample meldet ein Lebenszeichen des Haupt-Zuschlags lease (nicht eines
// nur zum Hochladen und nicht des wartenden einer Übergabe) um now: host ist
// das Mitglied, das hostet, players sind die Mitglieder auf dem Server (alle
// als Schlüssel, also E-Mail; Doppelte und Leere zählen einmal bzw. nicht).
func (t *Tracker) Sample(now time.Time, lease int64, host string, players []string) {
	host = strings.ToLower(host)
	cur := &sample{lease: lease, at: now, players: map[string]bool{}}
	for _, p := range players {
		if p = strings.ToLower(p); p != "" {
			cur.players[p] = true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.last
	t.last = cur
	if t.f.Since.IsZero() {
		t.f.Since = now
		t.dirty = true
	}
	if prev == nil || prev.lease != lease {
		// Neue Sitzung (oder erstes Lebenszeichen nach einem Neustart).
		if t.f.LastLease != lease {
			t.f.LastLease = lease
			if host != "" {
				t.dayLocked(now, host, func(x *Totals) { x.Sessions++ })
			}
		}
		t.maybeSaveLocked(now)
		return
	}
	gap := now.Sub(prev.at)
	if gap > 0 && gap <= MaxGap {
		ms := gap.Milliseconds()
		if host != "" {
			t.dayLocked(now, host, func(x *Totals) { x.HostMS += ms })
		}
		for p := range cur.players {
			share := ms / 2
			if prev.players[p] {
				share = ms
			}
			t.dayLocked(now, p, func(x *Totals) { x.PlayMS += share })
		}
		for p := range prev.players {
			if !cur.players[p] {
				t.dayLocked(now, p, func(x *Totals) { x.PlayMS += ms / 2 })
			}
		}
	}
	t.compactLocked(now)
	t.maybeSaveLocked(now)
}

// dayLocked ändert die Werte von member am Tag von now.
func (t *Tracker) dayLocked(now time.Time, member string, f func(*Totals)) {
	k := dayKey(now)
	d := t.f.Days[k]
	if d == nil {
		d = map[string]Totals{}
		t.f.Days[k] = d
	}
	x := d[member]
	f(&x)
	d[member] = x
	t.dirty = true
}

// compactLocked faltet Tage, die älter als KeepDays sind, in die Summe.
func (t *Tracker) compactLocked(now time.Time) {
	cut := dayKey(now.AddDate(0, 0, -(KeepDays - 1)))
	for k, d := range t.f.Days {
		if k >= cut {
			continue
		}
		for m, x := range d {
			o := t.f.Older[m]
			o.add(x)
			t.f.Older[m] = o
		}
		delete(t.f.Days, k)
		t.dirty = true
	}
}

// Flush schreibt ungespeicherte Änderungen sofort (beim Beenden).
func (t *Tracker) Flush(now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.saveLocked(now)
}

// Tick schreibt ungespeicherte Änderungen, wenn der letzte Schreibvorgang
// länger als SaveEvery her ist (aus dem regelmäßigen Takt des Koordinators).
func (t *Tracker) Tick(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maybeSaveLocked(now)
}

func (t *Tracker) maybeSaveLocked(now time.Time) {
	if !t.dirty || now.Sub(t.lastSave) < SaveEvery {
		return
	}
	if err := t.saveLocked(now); err != nil {
		slog.Error("Statistik: stats.json nicht geschrieben", "err", err)
		t.lastSave = now // nicht bei jedem Lebenszeichen neu versuchen
	}
}

func (t *Tracker) saveLocked(now time.Time) error {
	if !t.dirty || t.path == "" {
		return nil
	}
	data, err := json.Marshal(t.f)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(t.path, data); err != nil {
		return err
	}
	t.dirty, t.lastSave = false, now
	return nil
}

// Report ist die Auswertung zum Zeitpunkt now.
type Report struct {
	// Since: seit wann gezählt wird (Null: noch nichts gezählt).
	Since time.Time
	// Total: alles seit Beginn; Last30: heute und die 29 Tage davor.
	// Schlüssel ist das Mitglied (E-Mail klein); Mitglieder ohne Werte fehlen.
	Total, Last30 map[string]Totals
}

// Report liefert die Summen je Mitglied.
func (t *Tracker) Report(now time.Time) Report {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := Report{Since: t.f.Since, Total: map[string]Totals{}, Last30: map[string]Totals{}}
	for m, x := range t.f.Older {
		r.Total[m] = x
	}
	cut := dayKey(now.AddDate(0, 0, -(Window30 - 1)))
	for k, d := range t.f.Days {
		for m, x := range d {
			a := r.Total[m]
			a.add(x)
			r.Total[m] = a
			if k >= cut {
				b := r.Last30[m]
				b.add(x)
				r.Last30[m] = b
			}
		}
	}
	return r
}

// writeFileAtomic schreibt data über eine Temp-Datei im selben Ordner.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
