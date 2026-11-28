// Package karte erzeugt und liefert die Karte der Oberwelt (Karten-MVP,
// Kartenvertrag: Kachelraster, Manifest, Endpunkte).
//
// Der Dienst ruft das Programm „karte“ (Rust) als Prozess auf: Aus dem
// Speicher setzt er die Regionsdateien des Head-Stands jeder Welt zusammen
// (nur geänderte Regionen samt Nachbarn, der erste Lauf alle), das Programm
// rendert Kacheln und Manifest in einen Staging-Ordner, und der wird gegen
// data_dir/karte/<welt>/ getauscht. Es gibt immer nur den neuesten Stand;
// nichts davon liegt in blobs/ (das Aufräumen des Speichers fasst die Karte
// nie an). Die Karte einer Welt heißt nach der Kennung der Welt der Gruppe
// (w1, w2, …), nicht nach der Weltkennung des Speichers: So überlebt sie
// Zurückholen und „Welt übernehmen“.
//
// Gerendert wird in einer eigenen Goroutine (Lauf), nie auf dem Pfad von
// Lebenszeichen oder Commit; Fehler werden gefangen und stehen im Log und in
// Zustand und Gesundheit. Während einer Wartung läuft nichts (ein laufender
// Lauf wird abgebrochen): Die Sicherung soll einen ruhigen Ordner sehen.
//
// Ausgeliefert wird auch ohne Programm (Neustart ohne Binary schaltet nur
// die Erzeugung aus).
package karte

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

// Quelle ist der Speicher, aus dem gerendert wird (*store.Store).
type Quelle interface {
	Blobs
	// GroupWorlds: die Welten der Gruppe; HeadRefOf und HeadGenOf: Weltkennung
	// des Speichers und Head-Nummer bzw. -Stand der Welt mit der Kennung der
	// Gruppe (ohne Stand: ""/0 bzw. nil).
	GroupWorlds() []store.WorldEntry
	HeadRefOf(world string) (worldID string, n int64)
	HeadGenOf(world string) (worldID string, gen *api.Generation, err error)
}

// Optionen bestimmt einen Dienst. Programm leer: Die Erzeugung ist aus,
// vorhandene Karten werden trotzdem ausgeliefert.
type Optionen struct {
	// Verzeichnis ist data_dir/karte.
	Verzeichnis string
	Programm    string        // absoluter Pfad von „karte“
	Farben      string        // Farbtabelle (leer: die eingebaute)
	Nice        int           // 0: ohne nice
	Threads     int           // 0: Standard des Programms
	MinAbstand  time.Duration // Mindestabstand zweier Läufe derselben Welt
	MaxBytes    int64         // höchste Größe der Karte einer Welt
	Quelle      Quelle
	// Gesperrt meldet eine Wartung (nil: nie).
	Gesperrt func() bool
	Log      *slog.Logger
	Jetzt    func() time.Time

	// Zeitlimit: Höchstdauer eines Laufs (Standard 30 min). Takt: Abstand der
	// Prüfung auf neue Stände (Standard 20 s). Wiederholung: Wartezeit nach
	// einem Fehler (Standard 10 min). Umgebung: zusätzliche Einträge für die
	// Umgebung des Programms (nur Tests).
	Zeitlimit    time.Duration
	Takt         time.Duration
	Wiederholung time.Duration
	Umgebung     []string
}

// weltStatus ist, was der Dienst über eine Welt zur Laufzeit merkt.
type weltStatus struct {
	lastStart time.Time // Beginn des letzten Laufs (Mindestabstand)
	notBefore time.Time // nach einem Fehler: nicht vor
	final     bool      // ein Host hat geschlossen: nächster Lauf ohne Mindestabstand
	running   bool
	lastErr   string
	lastErrAt time.Time
	lastRun   time.Time // Ende des letzten erfolgreichen Laufs
	lastDur   time.Duration
}

// Dienst erzeugt und liefert die Karten. Alle Methoden sind für
// nebenläufigen Gebrauch.
type Dienst struct {
	o    Optionen
	log  *slog.Logger
	wake chan struct{}
	lim  *begrenzer

	// mu: die ausgelieferten Stände. Leser halten es nur zum Nachschlagen und
	// Öffnen der Datei, der Austausch hält es für die beiden Umbenennungen.
	mu      sync.RWMutex
	staende map[string]*stand

	// sm: Laufzeitstatus je Welt.
	sm sync.Mutex
	ws map[string]*weltStatus
}

// Neu baut den Dienst: legt das Verzeichnis an, räumt Reste eines
// unterbrochenen Austauschs oder Laufs weg und lädt die vorhandenen Karten.
// Ein Fehler beim Laden einer einzelnen Karte (kaputt) wird geloggt, die Karte
// gilt dann als nicht vorhanden und wird beim nächsten Lauf neu erzeugt.
func Neu(o Optionen) (*Dienst, error) {
	if o.Verzeichnis == "" {
		return nil, errors.New("karte: Verzeichnis fehlt")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Jetzt == nil {
		o.Jetzt = time.Now
	}
	if o.Zeitlimit <= 0 {
		o.Zeitlimit = 30 * time.Minute
	}
	if o.Takt <= 0 {
		o.Takt = 20 * time.Second
	}
	if o.Wiederholung <= 0 {
		o.Wiederholung = 10 * time.Minute
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 1 << 30
	}
	d := &Dienst{o: o, log: o.Log, wake: make(chan struct{}, 1), lim: newBegrenzer(o.Jetzt),
		staende: map[string]*stand{}, ws: map[string]*weltStatus{}}
	if err := os.MkdirAll(o.Verzeichnis, 0o750); err != nil {
		return nil, fmt.Errorf("karte: %w", err)
	}
	d.aufraeumen()
	d.ladeAlle()
	return d, nil
}

// Erzeugung meldet, ob der Dienst Karten erzeugt (Programm eingetragen).
func (d *Dienst) Erzeugung() bool { return d.o.Programm != "" }

func (d *Dienst) weltDir(welt string) string { return filepath.Join(d.o.Verzeichnis, welt) }

// validWelt: Kennung einer Welt der Gruppe (w<Ziffern>), nie ein Pfad.
func validWelt(s string) bool {
	if len(s) < 2 || len(s) > 12 || s[0] != 'w' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// aufraeumen: Reste eines unterbrochenen Laufs oder Austauschs. Fehlt ein
// Kartenordner, aber „<welt>.alt“ liegt noch da (Absturz zwischen den beiden
// Umbenennungen), kommt der alte Stand zurück.
func (d *Dienst) aufraeumen() {
	entries, err := os.ReadDir(d.o.Verzeichnis)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(d.o.Verzeichnis, name)
		switch {
		case strings.HasPrefix(name, ".staging-") || strings.HasPrefix(name, ".work-"):
			os.RemoveAll(full)
		case strings.HasSuffix(name, ".alt") && validWelt(strings.TrimSuffix(name, ".alt")):
			live := d.weltDir(strings.TrimSuffix(name, ".alt"))
			if _, err := os.Stat(live); errors.Is(err, fs.ErrNotExist) {
				if err := os.Rename(full, live); err != nil {
					d.log.Error("Karte: alter Stand nicht zurückgeholt", "welt", name, "err", err)
				} else {
					d.log.Warn("Karte: unterbrochener Austausch zurückgenommen", "welt", strings.TrimSuffix(name, ".alt"))
				}
				continue
			}
			os.RemoveAll(full)
		}
	}
}

// ladeAlle liest die vorhandenen Kartenstände.
func (d *Dienst) ladeAlle() {
	entries, err := os.ReadDir(d.o.Verzeichnis)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !validWelt(e.Name()) {
			continue
		}
		s, err := loadStand(d.weltDir(e.Name()), true)
		if err != nil {
			d.log.Error("Karte: vorhandener Stand nicht lesbar, er wird beim nächsten Lauf neu erzeugt", "welt", e.Name(), "err", err)
			continue
		}
		d.staende[e.Name()] = s
		w := d.status(e.Name())
		w.lastRun, w.lastStart = s.st.LastRun, s.st.LastRun
		w.lastDur = time.Duration(s.st.DurationMs) * time.Millisecond
	}
}

func (d *Dienst) status(welt string) *weltStatus {
	d.sm.Lock()
	defer d.sm.Unlock()
	return d.statusLocked(welt)
}

func (d *Dienst) statusLocked(welt string) *weltStatus {
	w := d.ws[welt]
	if w == nil {
		w = &weltStatus{}
		d.ws[welt] = w
	}
	return w
}

func (d *Dienst) stand(welt string) *stand {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.staende[welt]
}

// Info liefert den ausgelieferten Kartenstand der Welt für den Status (nil:
// keiner).
func (d *Dienst) Info(welt string) *api.KarteInfo {
	s := d.stand(welt)
	if s == nil {
		return nil
	}
	i := s.info
	i.Levels = slices.Clone(i.Levels)
	if i.Bounds != nil {
		b := *i.Bounds
		i.Bounds = &b
	}
	return &i
}

// Gesundheit liefert den Zustand für GET /v1/health; nil, wenn nichts
// eingerichtet ist (keine Erzeugung und noch nie eine Karte).
func (d *Dienst) Gesundheit() *api.HealthKarte {
	h := &api.HealthKarte{Enabled: d.Erzeugung()}
	d.mu.RLock()
	for _, s := range d.staende {
		h.SizeBytes += s.bytes
		h.Tiles += len(s.tiles)
	}
	d.mu.RUnlock()
	d.sm.Lock()
	for welt, w := range d.ws {
		h.Running = h.Running || w.running
		if w.lastRun.After(h.LastRun) {
			h.LastRun, h.LastDurationMs = w.lastRun, w.lastDur.Milliseconds()
		}
		if w.lastErr != "" && w.lastErrAt.After(h.ErrorTime) {
			h.Error, h.ErrorTime = welt+": "+w.lastErr, w.lastErrAt
		}
	}
	d.sm.Unlock()
	if !h.Enabled && h.Tiles == 0 && h.Error == "" {
		return nil
	}
	return h
}

// Melde weckt den Dienst: Ein Stand wurde angenommen. Der Dienst rendert
// höchstens alle min_interval; die Meldung blockiert nie.
func (d *Dienst) Melde() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Schluss meldet, dass ein Host mit seinem Schluss-Stand fertig ist: Der
// nächste Lauf jeder Welt mit neuem Stand läuft ohne den Mindestabstand.
func (d *Dienst) Schluss() {
	d.sm.Lock()
	for _, e := range d.o.Quelle.GroupWorlds() {
		d.statusLocked(e.ID).final = true
	}
	d.sm.Unlock()
	d.Melde()
}

// Lauf ist die Arbeits-Goroutine: Sie prüft im Takt und auf Meldung, ob eine
// Welt einen neuen Stand hat, und rendert. Er blockiert bis ctx endet; ohne
// Programm kehrt er sofort zurück.
func (d *Dienst) Lauf(ctx context.Context) {
	if !d.Erzeugung() {
		return
	}
	if err := d.programmPruefen(); err != nil {
		d.log.Error("KARTE: Erzeugung aus", "err", err)
		return
	}
	t := time.NewTicker(d.o.Takt)
	defer t.Stop()
	for {
		d.pruefe(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.wake:
		}
	}
}

// programmPruefen: Das Programm ist eine ausführbare reguläre Datei.
func (d *Dienst) programmPruefen() error {
	fi, err := os.Stat(d.o.Programm)
	switch {
	case err != nil:
		return fmt.Errorf("karte.binary %s: %w – es wird keine Karte erzeugt, vorhandene Karten werden weiter ausgeliefert", d.o.Programm, err)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("karte.binary %s ist keine reguläre Datei", d.o.Programm)
	}
	return nil
}

// pruefe geht alle Welten durch und rendert, was fällig ist. Panics werden
// gefangen.
func (d *Dienst) pruefe(ctx context.Context) {
	if d.gesperrt() {
		return
	}
	for _, e := range d.o.Quelle.GroupWorlds() {
		if ctx.Err() != nil {
			return
		}
		if e.Empty() || !validWelt(e.ID) {
			continue
		}
		d.versuche(ctx, e.ID)
	}
}

func (d *Dienst) gesperrt() bool { return d.o.Gesperrt != nil && d.o.Gesperrt() }

// versuche rendert die Welt, wenn ihr Head neu und der Lauf fällig ist.
func (d *Dienst) versuche(ctx context.Context, welt string) {
	storeWorld, n := d.o.Quelle.HeadRefOf(welt)
	if storeWorld == "" || n < 1 {
		return
	}
	source := fmt.Sprintf("%s/%d", storeWorld, n)
	cur := d.stand(welt)

	now := d.o.Jetzt()
	d.sm.Lock()
	w := d.statusLocked(welt)
	if cur != nil && cur.st.Source == source {
		w.final = false // nichts Neues
		d.sm.Unlock()
		return
	}
	due := w.final || w.lastStart.IsZero() || now.Sub(w.lastStart) >= d.o.MinAbstand
	if !due || now.Before(w.notBefore) {
		d.sm.Unlock()
		return
	}
	w.final, w.running, w.lastStart = false, true, now
	d.sm.Unlock()

	start := time.Now()
	err := d.lauf(ctx, welt)
	dur := time.Since(start)

	d.sm.Lock()
	defer d.sm.Unlock()
	w.running = false
	switch {
	case err == nil:
		w.lastErr, w.lastErrAt = "", time.Time{}
		w.lastRun, w.lastDur = d.o.Jetzt(), dur
	case errors.Is(err, errWartung):
		w.lastStart = time.Time{} // nach der Wartung gleich wieder
		d.log.Info("Karte: Lauf wegen Wartung abgebrochen", "welt", welt)
	case ctx.Err() != nil:
		w.lastStart = time.Time{}
	default:
		w.lastErr, w.lastErrAt = err.Error(), d.o.Jetzt()
		w.notBefore, w.lastStart = d.o.Jetzt().Add(d.o.Wiederholung), time.Time{} // neuer Versuch nach der Frist, nicht erst nach min_interval
		d.log.Error("KARTE: LAUF FEHLGESCHLAGEN", "welt", welt, "dauer", dur.Round(time.Millisecond), "err", err)
	}
}
