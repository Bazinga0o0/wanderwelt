package app

// Hosten nach einer Wartung fortsetzen (Koordinator: wartung.go, „wartung
// ende“ öffnet ein Fenster in Status.Resume).
//
// Der Starter merkt sich, dass er bei Wartungsbeginn Host war (er endete
// wegen der Wartung) oder mitspielte (sein Status nannte die Wartung, solange
// er mitspielte): <Gruppenordner>\fortsetzen.json, damit der Merker auch einen
// Neustart des Starters (Selbst-Update) übersteht. Meldet der Status nach der
// Wartung ein Fenster zu derselben Wartung, bereitet er seine Bewerbung vor
// (Manifest, Abhängigkeiten, Messung), wartet auf den Beginn des Fensters und
// bewirbt sich dann wie bei „Spielen“ (play, Zuschlag, Stand holen,
// Hochladen: derselbe Weg), nur mit PlayRequest.Resume. Weil sich alle zum
// selben Zeitpunkt bewerben, entscheidet das gewöhnliche Sammelfenster des
// Koordinators über den besten PC; die anderen bekommen „join“ und den
// Beitrittshinweis (ohne den Launcher von selbst zu öffnen).
//
// Der Merker gilt einmal: Nach der Bewerbung oder einem Abbruch ist er weg.
// Er verfällt auch durch „Fortsetzen abbrechen“, „Spielen“, „Spiel für mich
// beenden“ / „Nicht mehr mitspielen“, Beenden des Starters, Herunterfahren
// von Windows, ein beendetes Minecraft (best effort) und wenn schon jemand
// hostet.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// fortLabel ist die laufende Aktion (App.busy) der Bewerbung.
const fortLabel = fensterapi.FortsetzenBusy

// fortFileName ist der Merker im Gruppenordner.
const fortFileName = "fortsetzen.json"

// Im Test austauschbar.
var (
	fortNow   = time.Now
	fortAfter = time.After
	// minecraftLaeuft fragt (nur lesend) die Prozessliste: läuft ein
	// Minecraft-Client? known false: nicht feststellbar.
	minecraftLaeuft = minecraftClientLaeuft
	// fortPlayFn ist die Bewerbung (playWith).
	fortPlayFn func(a *App, ctx context.Context, auto int, dauer bool, fp *fortPlay) error
)

// Erst in init gesetzt: playWith führt über startJoin, watchJoin und fortNote
// hierher (sonst ein Initialisierungszyklus).
func init() { fortPlayFn = (*App).playWith }

// merker ist, was dieser PC über das Fortsetzen weiß (fortsetzen.json).
type merker struct {
	// Since: die Wartung (api.Maintenance.Since); nur ein Fenster zu
	// derselben Kennung zählt.
	Since time.Time `json:"since"`
	World string    `json:"world,omitempty"`
	Rolle string    `json:"rolle"` // fensterapi.RolleHost | RolleMitspieler
	// SpielLief: Beim Merken lief ein Minecraft-Client; er muss bei der
	// Bewerbung noch laufen (sonst hat der Nutzer es beendet).
	SpielLief bool      `json:"spiel_lief,omitempty"`
	Gemerkt   time.Time `json:"gemerkt"`
}

// fortFenster ist das zuletzt gesehene Fortsetzen-Fenster nach der Uhr dieses
// PCs (die Zeiten kommen als Restsekunden des Koordinators).
type fortFenster struct {
	since      time.Time
	opens, bis time.Time
	gesehenUm  time.Time
}

// fortState ist der Zustand des Fortsetzens (App.fort).
type fortState struct {
	mu     sync.Mutex
	dir    string // Gruppenordner, zu dem m gehört
	loaded bool
	m      *merker
	win    *fortFenster

	// cancel und done: eine laufende Bewerbung (nil: keine).
	cancel context.CancelFunc
	done   chan struct{}
}

func fortPath() string { return filepath.Join(group.Dir(), fortFileName) }

// loadLocked liest den Merker der aktiven Gruppe (nach einem Gruppenwechsel
// neu). Eine unlesbare Datei gilt als kein Merker.
func (f *fortState) loadLocked() {
	dir := group.Dir()
	if f.loaded && f.dir == dir {
		return
	}
	f.dir, f.loaded, f.m, f.win = dir, true, nil, nil
	data, err := os.ReadFile(fortPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("Fortsetzen: Merker nicht lesbar: %v", err)
		}
		return
	}
	var m merker
	if err := json.Unmarshal(data, &m); err != nil || m.Since.IsZero() {
		log.Printf("Fortsetzen: Merker unbrauchbar, wird verworfen (%v)", err)
		_ = os.Remove(fortPath())
		return
	}
	f.m = &m
}

// saveLocked schreibt den Merker (nil: Datei löschen).
func (f *fortState) saveLocked() {
	if f.m == nil {
		if err := os.Remove(fortPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("Fortsetzen: Merker nicht gelöscht: %v", err)
		}
		return
	}
	data, err := json.MarshalIndent(f.m, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(fortPath()), 0o700)
	}
	if err == nil {
		err = writeFileAtomic(fortPath(), data, 0o600)
	}
	if err != nil {
		log.Printf("Fortsetzen: Merker nicht gespeichert (er gilt bis zum Beenden im Speicher): %v", err)
	}
}

// fortMerken merkt sich, dass dieser PC bei Beginn der Wartung since Host
// (RolleHost) oder Mitspieler war. Ein vorhandener Merker zur selben Wartung
// bleibt, außer der Host löst den Mitspieler ab.
func (a *App) fortMerken(rolle, world string, since time.Time) {
	if since.IsZero() {
		return
	}
	a.fort.mu.Lock()
	a.fort.loadLocked()
	if m := a.fort.m; m != nil && m.Since.Equal(since) && (m.Rolle == rolle || rolle == fensterapi.RolleMitspieler) {
		a.fort.mu.Unlock()
		return
	}
	lief, known := minecraftLaeuft()
	a.fort.m = &merker{Since: since, World: world, Rolle: rolle, SpielLief: known && lief, Gemerkt: fortNow().UTC()}
	a.fort.saveLocked()
	a.fort.mu.Unlock()
	log.Printf("Fortsetzen: gemerkt (%s, Welt %q, Minecraft lief: %v) – nach der Wartung setzt wanderwelt das Hosten fort", rolle, world, known && lief)
	a.refresh()
}

// fortVerwerfen löscht den Merker und bricht eine laufende Bewerbung ab
// (why steht im Log). true: es gab etwas zu verwerfen.
func (a *App) fortVerwerfen(why string) bool {
	a.fort.mu.Lock()
	a.fort.loadLocked()
	had := a.fort.m != nil
	cancel := a.fort.cancel
	a.fort.m = nil
	if had {
		a.fort.saveLocked()
	}
	a.fort.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if had {
		log.Printf("Fortsetzen: verworfen (%s)", why)
		a.refresh()
	}
	return had || cancel != nil
}

// fortAbort bricht eine laufende Bewerbung ab und wartet auf ihr Ende.
func (a *App) fortAbort(why string) {
	a.fort.mu.Lock()
	cancel, done := a.fort.cancel, a.fort.done
	a.fort.mu.Unlock()
	if cancel == nil {
		return
	}
	log.Printf("Fortsetzen: Bewerbung wird abgebrochen (%s)", why)
	cancel()
	<-done
}

// fortRunning: Eine Bewerbung läuft gerade.
func (a *App) fortRunning() bool {
	a.fort.mu.Lock()
	defer a.fort.mu.Unlock()
	return a.fort.cancel != nil
}

// fortPending: Es gibt einen Merker (für Tray und Fenster).
func (a *App) fortPending() bool {
	a.fort.mu.Lock()
	defer a.fort.mu.Unlock()
	a.fort.loadLocked()
	return a.fort.m != nil
}

// fortBlocksAuto: Der Beginn eines Fensters steht noch aus – der Dauerbetrieb
// startet dann nicht von selbst, sonst gewänne der erste Takt das
// Sammelfenster allein, noch bevor sich alle bewerben (autoPlay).
func (a *App) fortBlocksAuto() bool {
	a.fort.mu.Lock()
	defer a.fort.mu.Unlock()
	w := a.fort.win
	return w != nil && fortNow().Before(w.opens)
}

// fortZustand ist der Merker fürs Fenster (nil ohne).
func (a *App) fortZustand() *fensterapi.Fortsetzen {
	a.fort.mu.Lock()
	defer a.fort.mu.Unlock()
	a.fort.loadLocked()
	if a.fort.m == nil {
		return nil
	}
	z := &fensterapi.Fortsetzen{Rolle: a.fort.m.Rolle, Laeuft: a.fort.cancel != nil}
	if w := a.fort.win; w != nil && w.since.Equal(a.fort.m.Since) {
		z.Ab, z.Bis = w.opens, w.bis
	}
	return z
}

// fortAbbrechen: „Fortsetzen abbrechen“ (Tray, Fenster).
func (a *App) fortAbbrechen() {
	if a.fortVerwerfen("vom Nutzer abgebrochen") {
		go notify("Okay, wanderwelt setzt das Hosten nach der Wartung nicht fort. Mit „Spielen“ geht es jederzeit.")
	}
}

// fortAktion ist die Entscheidung von fortPruefe.
type fortAktion int

const (
	fortNichts   fortAktion = iota // nichts tun, Merker bleibt
	fortStarten                    // Bewerbung vorbereiten und senden
	fortWeg                        // Merker verwerfen
	fortMitspiel                   // Mitspieler-Merker anlegen
)

// fortPruefe entscheidet zu einem frischen Status: m der Merker (nil ohne),
// mitspielt: dieser PC spielt gerade mit. Der Grund steht im Log.
//
//   - Der Status nennt eine Wartung: wer mitspielt, merkt sich die Wartung; ein
//     Merker zu einer anderen Wartung ohne Mitspielen ist veraltet.
//   - Ohne Wartung, mit Fenster zur Kennung des Merkers: Bewerbung.
//   - Ohne Wartung und ohne passendes Fenster (älterer Koordinator, Fenster
//     verfallen, andere Wartung): Merker weg.
func fortPruefe(m *merker, st *api.Status, mitspielt bool) (fortAktion, string) {
	if st == nil {
		return fortNichts, ""
	}
	if mt := st.Maintenance; mt != nil {
		switch {
		case mitspielt && !mt.Since.IsZero() && (m == nil || !m.Since.Equal(mt.Since)):
			return fortMitspiel, "Mitspieler bei Beginn der Wartung"
		case m != nil && !m.Since.Equal(mt.Since) && !mitspielt:
			return fortWeg, "der Merker gehört zu einer früheren Wartung"
		}
		return fortNichts, ""
	}
	if m == nil {
		return fortNichts, ""
	}
	switch {
	case st.Resume == nil:
		return fortWeg, "der Koordinator bietet kein Fortsetzen (mehr) an"
	case !st.Resume.Since.Equal(m.Since):
		return fortWeg, "das Fenster gehört zu einer anderen Wartung"
	}
	return fortStarten, ""
}

// fortNote wertet einen frischen Status aus (pollStatus, watchJoin). true:
// ein Fortsetzen steht an oder läuft (für den Hinweis „Wartung vorbei“).
func (a *App) fortNote(st *api.Status) bool {
	if st == nil {
		return false
	}
	now := fortNow()
	a.mu.Lock()
	mitspielt := a.phase == phJoined || a.joinCancel != nil
	world := a.joinWorld
	hosting := a.hostCancel != nil
	a.mu.Unlock()

	a.fort.mu.Lock()
	a.fort.loadLocked()
	if r := st.Resume; r != nil && st.Maintenance == nil {
		a.fort.win = &fortFenster{
			since: r.Since, gesehenUm: now,
			opens: now.Add(time.Duration(r.OpensInS) * time.Second),
			bis:   now.Add(time.Duration(r.UntilInS) * time.Second),
		}
	} else {
		a.fort.win = nil
	}
	var m *merker
	if a.fort.m != nil {
		c := *a.fort.m
		m = &c
	}
	running := a.fort.cancel != nil
	win := a.fort.win
	a.fort.mu.Unlock()

	if running {
		return true
	}
	if hosting && m != nil && st.Maintenance == nil {
		// Dieser PC hostet schon (etwa per „Spielen“): nichts mehr fortzusetzen.
		a.fortVerwerfen("dieser PC hostet schon")
		return false
	}
	act, why := fortPruefe(m, st, mitspielt && !hosting)
	switch act {
	case fortMitspiel:
		a.fortMerken(fensterapi.RolleMitspieler, world, st.Maintenance.Since)
		return true
	case fortWeg:
		a.fortVerwerfen(why)
		return false
	case fortStarten:
		a.fortStart(*m, *win)
		return true
	}
	return m != nil
}

// fortStart startet die Bewerbung, sofern der Starter frei ist: Läuft noch
// etwas, geht es beim nächsten Status weiter (das Fenster dauert 10 min).
func (a *App) fortStart(m merker, w fortFenster) {
	c := a.client()
	if c == nil {
		return
	}
	a.mu.Lock()
	switch {
	case a.quitting || a.ending || a.busy != "" || a.joinCancel != nil:
		a.mu.Unlock()
		return
	case a.hostCancel != nil:
		a.mu.Unlock()
		a.fortVerwerfen("dieser PC hostet schon")
		return
	}
	a.fort.mu.Lock()
	if a.fort.cancel != nil {
		a.fort.mu.Unlock()
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	a.fort.cancel, a.fort.done = cancel, done
	a.fort.mu.Unlock()
	a.busy = fortLabel
	a.mu.Unlock()
	a.refresh()

	log.Printf("Fortsetzen: Bewerbung nach der Wartung wird vorbereitet (%s, Beginn in %v)", m.Rolle, max(0, w.opens.Sub(fortNow())).Round(time.Second))
	go func() {
		err := fortPlayFn(a, ctx, 0, false, &fortPlay{m: m, opens: w.opens, bis: w.bis})
		canceled := ctx.Err() != nil
		cancel()
		a.fort.mu.Lock()
		a.fort.cancel, a.fort.done = nil, nil
		// Einmal verbraucht: Wer sich beworben hat (oder abbrach), versucht es
		// nicht noch einmal; ein neuerer Merker (neue Wartung) bleibt.
		if cur := a.fort.m; cur != nil && cur.Since.Equal(m.Since) {
			a.fort.m = nil
			a.fort.saveLocked()
		}
		a.fort.mu.Unlock()
		switch {
		case err == nil:
		case errors.Is(err, errFortsetzenEntfaellt):
			log.Printf("Fortsetzen: %v", err)
			a.leaveStarting()
		case canceled:
			log.Printf("Fortsetzen: abgebrochen: %v", err)
			a.leaveStarting()
		default:
			a.fail("Fortsetzen nach der Wartung", err)
		}
		a.end()
		close(done)
	}()
}

// errFortsetzenEntfaellt: Die Bewerbung findet nicht statt, weil eine
// Bedingung nicht (mehr) gilt; kein Fehler, nur ein Logeintrag.
var errFortsetzenEntfaellt = errors.New("das Fortsetzen entfällt")

func entfaellt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errFortsetzenEntfaellt, fmt.Sprintf(format, args...))
}

// fortPlay beschreibt eine Bewerbung nach der Wartung für play (playWith).
type fortPlay struct {
	m          merker
	opens, bis time.Time // Beginn und Verfall nach der Uhr dieses PCs
}

// fortWelt baut den Weltkontext der Welt des Merkers; gibt es sie nicht mehr,
// entfällt das Fortsetzen.
func (a *App) fortWelt(ctx context.Context, c *client.Client, m merker) (worldCtx, error) {
	st := a.statusFresh(ctx, c)
	aware := st != nil && len(st.Worlds) > 0
	id := m.World
	if aware {
		if id == "" {
			id, _ = chooseWorld(st, "")
		} else if !hasWorld(st.Worlds, id) {
			return worldCtx{}, entfaellt("die Welt %q gibt es nicht mehr", id)
		}
	}
	return a.worldFor(id, c, aware)
}

// fortBedingungen prüft kurz vor der Bewerbung (frischer Status): Das Fenster
// gilt noch zur selben Wartung, niemand hostet, keine Übergabe läuft, und der
// Nutzer hat Minecraft nicht beendet. nil: bewerben.
func fortBedingungen(m merker, st *api.Status, bis, now time.Time, mcWeg bool) error {
	switch {
	case st == nil:
		return entfaellt("der Status ist unbekannt")
	case st.Maintenance != nil:
		return entfaellt("die Wartung läuft wieder")
	case st.Resume == nil || !st.Resume.Since.Equal(m.Since):
		return entfaellt("das Fenster gilt nicht (mehr)")
	case !now.Before(bis):
		return entfaellt("das Fenster ist verfallen")
	case st.Host != nil || handoverActive(st):
		return entfaellt("ein anderer PC hostet schon")
	case mcWeg:
		return entfaellt("Minecraft wurde beendet")
	}
	return nil
}

// mcBeendet: Beim Merken lief ein Minecraft-Client, jetzt nachweislich nicht.
func mcBeendet(m merker) bool {
	if !m.SpielLief {
		return false
	}
	lief, known := minecraftLaeuft()
	return known && !lief
}

// fortWarten wartet auf den Beginn des Fensters und prüft dann die
// Bedingungen mit einem frischen Status.
func (a *App) fortWarten(ctx context.Context, c *client.Client, fp *fortPlay) error {
	if d := fp.opens.Sub(fortNow()); d > 0 {
		a.setPhase(phStarting, "wartet auf das Fortsetzen nach der Wartung …")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-fortAfter(d):
		}
	}
	st := a.statusFresh(ctx, c)
	return fortBedingungen(fp.m, st, fp.bis, fortNow(), mcBeendet(fp.m))
}

// minecraftClientLaeuft sucht (nur lesend) einen Minecraft-Client in der
// Prozessliste: Java mit net.minecraft.client oder KnotClient in der
// Kommandozeile. Ein Server (KnotServer) zählt nicht.
func minecraftClientLaeuft() (running, known bool) {
	ps, err := process.Processes()
	if err != nil {
		return false, false
	}
	for _, p := range ps {
		name, err := p.Name()
		if err != nil || !strings.HasPrefix(strings.ToLower(name), "java") {
			continue
		}
		cl, err := p.Cmdline()
		if err != nil {
			continue
		}
		l := strings.ToLower(cl)
		if strings.Contains(l, "net.minecraft.client") || strings.Contains(l, "knotclient") {
			return true, true
		}
	}
	return false, true
}
