package app

// Dauerbetrieb (profil.yaml: dauerbetrieb, Haken im Tray): Dieser PC hostet
// von selbst, sobald niemand hostet – gleich nach dem Start des Starters und
// dann jede Minute, auf demselben Weg wie „Spielen“ –, und seine Sitzung
// endet nicht mangels Spielern (host.Deps.Dauerbetrieb). Das gilt nur,
// solange der PC am Netz hängt: Auf Akku hostet ein Notebook wie ohne
// Dauerbetrieb (Ende nach der Leerlauf-Frist) und startet nichts von selbst.
//
// „Spiel für mich beenden“ pausiert den Dauerbetrieb bis zum nächsten
// „Spielen“ oder Neustart des Starters. Scheitert das Hosten wiederholt,
// wartet der Takt länger (1, 2, 4, 8, dann 15 Minuten) und zeigt je
// Fehlerserie nur einen Hinweis.

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

const (
	// dauerTakt: So oft prüft der Dauerbetrieb, ob er hosten soll.
	dauerTakt = time.Minute
	// dauerLabel ist die laufende Aktion (App.busy) eines Dauerbetrieb-Plays.
	dauerLabel = "Spielen (Dauerbetrieb)"
	// dauerBackoffMax: längste Wartezeit nach Fehlschlägen in Folge.
	dauerBackoffMax = 15 * time.Minute
)

// dauerState ist der Zustand des Dauerbetriebs (App.dauer).
type dauerState struct {
	on atomic.Bool // Haken aus profil.yaml (zuletzt gelesen oder umgeschaltet)
	// manual: Während das Dauerbetrieb-Play lief, kam „Spielen“; es gilt dann
	// als gewöhnliches Spielen (Mitspielen, Launcher, Hinweise).
	manual atomic.Bool
	// frist: Leerlauf-Frist der letzten Host-Sitzung (für den Hinweis beim
	// Abschalten); 0 unbekannt.
	frist atomic.Int64

	// fileMu reiht Lesen und Umschalten von profil.yaml, damit ein Takt
	// einen gerade umgeschalteten Haken nicht mit dem alten Stand überholt.
	fileMu sync.Mutex

	// playCancel und playDone gehören wie App.busy zu App.mu: Abbruch und
	// Ende des laufenden Dauerbetrieb-Plays (nil: keins).
	playCancel context.CancelFunc
	playDone   chan struct{}

	mu       sync.Mutex
	acKnown  bool      // ac gilt (seit dem letzten Umschalten einmal abgefragt)
	ac       bool      // zuletzt gesehener Netzstatus, fürs Log beim Wechsel
	loadErr  string    // zuletzt geloggter Fehler beim Lesen von profil.yaml
	paused   bool      // „Spiel für mich beenden“: bis zum nächsten „Spielen“ oder Neustart
	fails    int       // Fehlschläge in Folge
	nextTry  time.Time // vorher kein neuer Versuch (Null: sofort)
	notified bool      // in dieser Fehlerserie schon ein Hinweis gezeigt
}

// onACPower meldet, ob der PC am Netz hängt; im Test austauschbar.
var onACPower = winsys.OnACPower

// dauerNow ist die Uhr für die Wartezeit nach Fehlschlägen; im Test
// austauschbar.
var dauerNow = time.Now

// dauerPlay ist „Spielen“ für den Dauerbetrieb; im Test austauschbar.
var dauerPlay = func(ctx context.Context, a *App) error { return a.play(ctx, 0, true) }

// dauerbetriebActive: Der Dauerbetrieb ist an, nicht pausiert, und der PC
// hängt am Netz. Wechselt der Netzstatus, während der Haken gesetzt ist,
// steht das im Log. Antwortet sofort (host.Deps.Dauerbetrieb, aus der
// Schleife der Sitzung).
func (a *App) dauerbetriebActive() bool {
	if !a.dauer.on.Load() {
		return false
	}
	a.dauer.mu.Lock()
	paused := a.dauer.paused
	a.dauer.mu.Unlock()
	if paused {
		return false
	}
	ac := onACPower()
	a.dauer.mu.Lock()
	known, was := a.dauer.acKnown, a.dauer.ac
	a.dauer.acKnown, a.dauer.ac = true, ac
	a.dauer.mu.Unlock()
	switch {
	case !ac && (!known || was):
		log.Printf("Dauerbetrieb ruht: Der PC läuft auf Akku – gehostet wird wie ohne Dauerbetrieb, nichts startet von selbst")
	case ac && known && !was:
		log.Printf("Dauerbetrieb wirkt wieder: Der PC hängt am Netz")
	}
	return ac
}

// setDauerbetrieb übernimmt den Haken (why: woher, fürs Log).
func (a *App) setDauerbetrieb(on bool, why string) {
	if a.dauer.on.Swap(on) == on {
		return
	}
	a.dauer.mu.Lock()
	a.dauer.acKnown = false
	a.dauer.mu.Unlock()
	log.Printf("Dauerbetrieb %s (%s)", onOff(on), why)
	a.refresh()
}

// loadDauerbetrieb liest den Haken aus profil.yaml (auch von Hand
// geändert). Ist das Profil unlesbar, bleibt es beim bisherigen Wert; der
// Fehler steht einmal im Log.
func (a *App) loadDauerbetrieb() {
	a.dauer.fileMu.Lock()
	defer a.dauer.fileMu.Unlock()
	p, err := bench.LoadProfile()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	a.dauer.mu.Lock()
	report := msg != a.dauer.loadErr
	a.dauer.loadErr = msg
	a.dauer.mu.Unlock()
	if err != nil {
		if report {
			log.Printf("Dauerbetrieb: Leistungsprofil unlesbar, es bleibt bei %s: %v", onOff(a.dauer.on.Load()), err)
		}
		return
	}
	a.setDauerbetrieb(p.Dauerbetrieb, "profil.yaml")
}

// dauerbetriebLoop ist der Takt des Dauerbetriebs: gleich nach dem Start,
// dann jede Minute, bis das Programm endet.
func (a *App) dauerbetriebLoop() {
	t := time.NewTicker(dauerTakt)
	defer t.Stop()
	for {
		a.dauerbetriebTick()
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// dauerbetriebTick: Ist der Dauerbetrieb wirksam und hostet dieser PC
// nicht schon, frischen Status holen und gegebenenfalls „Spielen“
// anfordern (autoPlay).
func (a *App) dauerbetriebTick() {
	a.loadDauerbetrieb()
	if !a.dauerbetriebActive() || a.isHosting() {
		return
	}
	a.pollStatus(false)
	a.autoPlay()
}

// autoPlay fordert „Spielen“ für den Dauerbetrieb an – nur, wenn der
// zuletzt geholte Status sicher niemanden als Host zeigt, dieser PC weder
// hostet noch als Mitspieler einen Host beobachtet, keine Aktion läuft,
// weder das Programm noch die Windows-Sitzung endet, der Dauerbetrieb nicht
// pausiert ist und die Wartezeit nach einem Fehlschlag um ist. Prüfen und
// Belegen der Aktion geschehen in einem Schritt: Nie laufen zwei „Spielen“
// nebeneinander. true: angefordert.
//
// Hostet inzwischen doch ein anderer PC (er war schneller), antwortet der
// Koordinator „join“; im Dauerbetrieb passiert dann nichts (play).
func (a *App) autoPlay() bool {
	if a.fortBlocksAuto() {
		return false // erst bewerben sich alle gemeinsam nach der Wartung (fortsetzen.go)
	}
	a.dauer.mu.Lock()
	paused, wait := a.dauer.paused, a.dauer.nextTry
	a.dauer.mu.Unlock()
	if paused || dauerNow().Before(wait) {
		return false
	}
	a.mu.Lock()
	ok := !a.quitting && a.busy == "" && a.hostCancel == nil && a.joinCancel == nil &&
		a.statusErr == nil && a.status != nil && a.status.Host == nil &&
		a.status.Maintenance == nil // während der Wartung startet kein PC von selbst
	ending := a.ending
	var (
		ctx    context.Context
		cancel context.CancelFunc
		done   chan struct{}
	)
	if ok && !ending {
		a.busy = dauerLabel
		a.dauer.manual.Store(false)
		ctx, cancel = context.WithCancel(a.ctx)
		done = make(chan struct{})
		a.dauer.playCancel, a.dauer.playDone = cancel, done
	}
	a.mu.Unlock()
	switch {
	case ok && ending:
		log.Printf("Dauerbetrieb: niemand hostet, aber die Windows-Sitzung endet – kein automatisches Hosten")
		return false
	case !ok:
		return false
	}
	log.Printf("Dauerbetrieb: niemand hostet – dieser PC übernimmt")
	play := dauerPlay
	go func() {
		err := play(ctx, a)
		canceled := ctx.Err() != nil
		cancel()
		manual := a.dauer.manual.Swap(false)
		// Das Ergebnis zählt, bevor die Aktion frei wird: Sonst könnte ein
		// Takt dazwischen die Wartezeit nach dem Fehlschlag überholen.
		switch {
		case err == nil:
		case canceled:
			// Herunterfahren, Programmende oder „Spiel für mich beenden“:
			// kein Hinweis, kein Fehlschlag.
			log.Printf("Dauerbetrieb: automatisches Hosten abgebrochen: %v", err)
			a.leaveStarting()
		case manual:
			a.fail("Spielen", err)
		default:
			a.dauerFailed(err)
		}
		a.mu.Lock()
		a.dauer.playCancel, a.dauer.playDone = nil, nil
		a.mu.Unlock()
		a.end()
		close(done)
	}()
	return true
}

// leaveStarting: Ein abgebrochenes „Spielen“ hat keine Sitzung begonnen –
// der Tray soll nicht auf „startet“ stehen bleiben.
func (a *App) leaveStarting() {
	a.mu.Lock()
	if a.phase == phStarting && a.hostCancel == nil {
		a.phase, a.detail = phReady, ""
	}
	a.mu.Unlock()
	a.refresh()
}

// joinDauerPlayLocked: „Spielen“ kam, während das Dauerbetrieb-Play läuft.
// Es gilt ab jetzt als gewöhnliches Spielen (play fragt manual bei der
// Antwort des Koordinators). Aufruf mit gehaltenem a.mu; true: übernommen.
func (a *App) joinDauerPlayLocked() bool {
	if a.busy != dauerLabel || a.hostCancel != nil {
		return false
	}
	a.dauer.manual.Store(true)
	return true
}

// dauerBackoff ist die Wartezeit nach fails Fehlschlägen in Folge: 1, 2, 4,
// 8, dann 15 Minuten.
func dauerBackoff(fails int) time.Duration {
	d := time.Minute
	for i := 1; i < fails && d < dauerBackoffMax; i++ {
		d *= 2
	}
	return min(d, dauerBackoffMax)
}

// dauerFailed: Hosten im Dauerbetrieb gescheitert (Play oder Sitzung).
// Tray-Zustand Fehler und Log; ein Hinweis nur beim ersten Fehlschlag einer
// Serie. Der nächste automatische Versuch kommt frühestens nach
// dauerBackoff – aber nur, solange der Dauerbetrieb wirkt (an, nicht
// pausiert, am Netz). Sonst kommt kein Versuch von selbst: Der Hinweis nennt
// dann keinen und erscheint immer (eine Serie gibt es so nicht).
func (a *App) dauerFailed(err error) {
	now := dauerNow()
	a.dauer.mu.Lock()
	a.dauer.fails++
	n := a.dauer.fails
	wait := dauerBackoff(n)
	a.dauer.nextTry = now.Add(wait)
	a.dauer.mu.Unlock()
	retry := a.dauerbetriebActive()
	a.dauer.mu.Lock()
	first := retry && !a.dauer.notified
	a.dauer.notified = a.dauer.notified || retry
	a.dauer.mu.Unlock()
	msg := "Dauerbetrieb: " + explain(err)
	if retry {
		msg += " – neuer Versuch in " + minutes(wait)
		log.Printf("Dauerbetrieb: %d. Fehlschlag in Folge, neuer Versuch frühestens in %v", n, wait)
	} else {
		log.Printf("Dauerbetrieb: %d. Fehlschlag in Folge, kein neuer Versuch von selbst (Dauerbetrieb aus, pausiert oder auf Akku)", n)
	}
	a.showFailed(msg, err)
	switch {
	case !retry:
		go notify(msg + ".")
	case first:
		go notify(msg + ". Weitere Fehlschläge stehen nur im Log, bis der Server wieder läuft.")
	}
}

// dauerReady: Eine Host-Sitzung ist bereit – Fehlerserie und Wartezeit
// beginnen von vorn.
func (a *App) dauerReady() {
	a.dauer.mu.Lock()
	had := a.dauer.fails
	a.dauer.fails, a.dauer.nextTry, a.dauer.notified = 0, time.Time{}, false
	a.dauer.mu.Unlock()
	if had > 0 {
		log.Printf("Dauerbetrieb: Server läuft wieder (nach %d Fehlschlägen)", had)
	}
}

// stopHosting: „Spiel für mich beenden“ als Host (nach der Rückfrage). Ist
// der Dauerbetrieb an, pausiert er bis zum nächsten „Spielen“ oder Neustart.
func (a *App) stopHosting() {
	a.pauseDauerbetrieb()
	a.endHosting("Spiel für mich beenden")
}

// pauseDauerbetrieb: „Spiel für mich beenden“. Ist der Dauerbetrieb an,
// pausiert er bis zum nächsten „Spielen“ oder Neustart; true: pausiert.
func (a *App) pauseDauerbetrieb() bool {
	if !a.dauer.on.Load() {
		return false
	}
	a.dauer.mu.Lock()
	a.dauer.paused = true
	a.dauer.mu.Unlock()
	log.Printf("Dauerbetrieb pausiert („Spiel für mich beenden“) – bis zum nächsten „Spielen“ oder Neustart")
	return true
}

// cancelDauerPlay: „Spiel für mich beenden“, während das automatische
// Hosten des Dauerbetriebs noch startet (Messung, Zuschlag, …). Das Play
// wird abgebrochen und der Dauerbetrieb pausiert wie nach „Spiel für mich
// beenden“ als Host. Kam der Zuschlag noch vor dem Abbruch, endet die eben
// begonnene Sitzung sauber.
func (a *App) cancelDauerPlay() {
	paused := a.pauseDauerbetrieb()
	a.abortDauerPlay("„Spiel für mich beenden“")
	a.endHosting("Spiel für mich beenden")
	text := "Automatisches Hosten abgebrochen."
	if paused {
		text += " Der Dauerbetrieb pausiert bis zum nächsten „Spielen“ oder Neustart."
	}
	go notify(text)
}

// resumeDauerbetrieb hebt eine Pause auf (why: fürs Log).
func (a *App) resumeDauerbetrieb(why string) {
	a.dauer.mu.Lock()
	was := a.dauer.paused
	a.dauer.paused = false
	a.dauer.mu.Unlock()
	if was {
		log.Printf("Dauerbetrieb läuft wieder (%s)", why)
	}
}

// shutdownHosting ist der Hosting-Haken fürs Herunterfahren: Der PC hostet,
// oder ein Dauerbetrieb-Play läuft (es kann gleich hosten).
func (a *App) shutdownHosting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hostCancel != nil || a.busy == dauerLabel
}

// stopDauerPlay: Windows fährt herunter – ein laufendes Dauerbetrieb-Play
// abbrechen (abortDauerPlay).
func (a *App) stopDauerPlay() { a.abortDauerPlay("Windows fährt herunter") }

// abortDauerPlay bricht ein laufendes Dauerbetrieb-Play ab und wartet auf
// sein Ende (danach hostet der PC entweder oder nicht); why steht im Log.
func (a *App) abortDauerPlay(why string) {
	a.mu.Lock()
	cancel, done := a.dauer.playCancel, a.dauer.playDone
	a.mu.Unlock()
	if cancel == nil {
		return
	}
	log.Printf("Dauerbetrieb: automatisches Hosten wird abgebrochen (%s)", why)
	cancel()
	<-done
}

// toggleDauerbetrieb: Haken „Dauerbetrieb“ im Tray. Schreibt profil.yaml;
// eingeschaltet und am Netz hostet der PC gleich, wenn niemand hostet.
func (a *App) toggleDauerbetrieb() {
	a.dauer.fileMu.Lock()
	p, err := bench.LoadProfile()
	if err == nil {
		p.Dauerbetrieb = !p.Dauerbetrieb
		err = bench.SaveProfile(p)
	}
	if err == nil {
		a.setDauerbetrieb(p.Dauerbetrieb, "Tray")
	}
	a.dauer.fileMu.Unlock()
	if err != nil {
		a.refresh() // Haken zurück auf den gespeicherten Stand
		showError("Dauerbetrieb lässt sich nicht umschalten, das Leistungsprofil ist fehlerhaft (Tray → Profil öffnen):\n\n" + err.Error())
		return
	}
	a.dauerSwitched(p.Dauerbetrieb)
}

// saveProfile speichert das Leistungsprofil p (Fenster; ungültige Werte:
// Fehler der Profilprüfung, profil.yaml bleibt unverändert). Schaltet es den
// Dauerbetrieb um, geht es danach weiter wie beim Haken im Tray
// (dauerSwitched, im Hintergrund).
func (a *App) saveProfile(p api.Profile) error {
	a.dauer.fileMu.Lock()
	old, _ := bench.LoadProfile()
	err := bench.SaveProfile(p)
	switched := err == nil && a.dauer.on.Load() != p.Dauerbetrieb
	if err == nil {
		a.setDauerbetrieb(p.Dauerbetrieb, "Fenster")
	}
	a.dauer.fileMu.Unlock()
	if switched {
		go a.dauerSwitched(p.Dauerbetrieb)
	}
	if err == nil && old.KeineUebernahme != p.KeineUebernahme {
		// Der Koordinator filtert Kandidaten nach dem gemeldeten Profil.
		go a.reportProfile(p)
	}
	return err
}

// dauerSwitched: Der Dauerbetrieb wurde ausdrücklich umgeschaltet (Tray oder
// Fenster) und gespeichert. Eingeschaltet sind Pause und Wartezeit vorbei,
// und am Netz hostet der PC gleich, wenn niemand hostet; dazu ein Hinweis.
func (a *App) dauerSwitched(on bool) {
	if on {
		// Ausdrücklich eingeschaltet: Pause und Wartezeit sind vorbei.
		a.resumeDauerbetrieb("Haken gesetzt")
		a.dauerReady()
	}
	switch {
	case !on:
		text := "Dauerbetrieb aus."
		if f := time.Duration(a.dauer.frist.Load()); a.isHosting() && f > 0 {
			text += " Dieser PC hostet gerade; der Server endet nach " + minutes(f) + " ohne Spieler."
		}
		go notify(text)
	case !onACPower():
		go notify("Dauerbetrieb an – er wirkt, sobald dieser PC am Netz hängt. Auf Akku hostet er wie bisher.")
	default:
		go notify("Dauerbetrieb an: Dieser PC hostet von selbst, sobald niemand hostet, und der Server läuft auch ohne Spieler weiter.")
		if a.client() != nil { // vor dem Laden (Fenster beim Start) übernimmt der Takt
			a.dauerbetriebTick()
		}
	}
}

// dauerHint ergänzt die Rückfrage „Spiel für mich beenden“, solange der
// Dauerbetrieb an ist.
func (a *App) dauerHint() string {
	if !a.dauer.on.Load() {
		return ""
	}
	return "\n\nDauerbetrieb ist an: Er pausiert danach – dieser PC hostet erst wieder von selbst, " +
		"wenn du „Spielen“ klickst oder wanderwelt neu startest."
}

// minutes: „10 min“ (ganze Minuten), sonst die Go-Dauer.
func minutes(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return d.String()
}

func onOff(on bool) string {
	if on {
		return "an"
	}
	return "aus"
}
