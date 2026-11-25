package app

// Host-Übergabe: Der Host kann die Welt an den besten Mitspieler-PC
// weitergeben, ohne dass der Server für alle stoppt (host.Deps.Handover).
//
//   - Bereitschaft (presenceLoop): Solange dieser PC mitspielt oder im
//     Dauerbetrieb untätig bereit ist und das Hosten übernehmen darf
//     (Profil: KeineUebernahme aus), meldet er alle 5 s seine Bereitschaft
//     samt Messwert. Bietet der Koordinator ihm die Welt an, wird er Host
//     (takeOver), mit dem Ersatz-Port aus dem Angebot.
//   - Abgeben: „Spiel für mich beenden“ als Host, solange andere Spieler
//     online sind, schickt das Signal an die Sitzung (handoverHosting);
//     klappt es nicht, endet der Server wie gewohnt.

import (
	"cmp"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

const (
	// presenceTakt: So oft meldet sich ein übernahmebereiter PC.
	presenceTakt = 5 * time.Second
	// presenceLogPause: Fehler der Bereitschaft stehen höchstens so oft im
	// Log (ein älterer Koordinator antwortet immer 404).
	presenceLogPause = time.Minute
	// scoreRetry: Wartezeit nach einer gescheiterten Messung. Der Messwert
	// für die Bereitschaft wird nie erneuert (nicht mitten im Spiel messen);
	// „Spielen“ aktualisiert ihn.
	scoreRetry = 5 * time.Minute

	// uebernahmeLabel ist die laufende Aktion (App.busy) bei der Übernahme.
	uebernahmeLabel = "Hosten übernehmen"
	// stateNobody: OnHandover-Stand, wenn niemand übernehmen kann.
	stateNobody = "keiner da"
)

// Im Test austauschbar.
var (
	// hostRun führt die Host-Sitzung aus.
	hostRun = host.Run
	// measureScore misst die Leistung (wie bei „Spielen“).
	measureScore = func(ctx context.Context, c *client.Client, xmxMB int) (api.Score, error) {
		return bench.Measure(ctx, c, xmxMB)
	}
	// takeoverPrepare sammelt Manifest, Profil und Abhängigkeiten.
	takeoverPrepare = (*App).prepare
	// presenceNow ist die Uhr für Zwischenspeicher und Log-Pause.
	presenceNow = time.Now
)

// overtake beschreibt eine Übernahme des Hostens im laufenden Betrieb.
type overtake struct {
	from string // Name des alten Hosts
	port int    // Ersatz-Port bei tunneld
}

// uebergabeState ist der Zustand der Übergabe (App.uebergabe).
type uebergabeState struct {
	mu sync.Mutex

	score     api.Score
	scoreXmx  int
	scoreAt   time.Time
	scoreFail time.Time // letzte gescheiterte Messung (Null: keine)
	lastLog   time.Time // letzter Fehler der Bereitschaft im Log

	ho   chan struct{} // Übergabe-Signal der laufenden Host-Sitzung (nil: keine)
	gave string        // „bereit“ gemeldet: Name des neuen Hosts

	lastOffer    int64 // zuletzt angenommenes Angebot (HandoverID)
	refusedOffer int64 // zuletzt abgewiesenes (fürs Log)
}

// setScore merkt die Messung (von „Spielen“ oder der Bereitschaft).
func (u *uebergabeState) setScore(s api.Score, xmx int) {
	u.mu.Lock()
	u.score, u.scoreXmx, u.scoreAt, u.scoreFail = s, xmx, presenceNow(), time.Time{}
	u.mu.Unlock()
}

// cachedScore liefert den zuletzt gemessenen Wert (auch einen älteren).
func (u *uebergabeState) cachedScore() (api.Score, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.scoreAt.IsZero() {
		return api.Score{}, false
	}
	return u.score, true
}

// newSession richtet das Übergabe-Signal einer neuen Host-Sitzung ein.
func (u *uebergabeState) newSession() <-chan struct{} {
	ch := make(chan struct{}, 1)
	u.mu.Lock()
	u.ho, u.gave = ch, ""
	u.mu.Unlock()
	return ch
}

// endSession schließt die Sitzung ab; Ergebnis: Name des neuen Hosts, wenn
// die Übergabe gelang („bereit“ ohne folgendes „gescheitert“), sonst "".
func (u *uebergabeState) endSession() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	g := u.gave
	u.ho, u.gave = nil, ""
	return g
}

// signal löst die Übergabe der laufenden Sitzung aus; false: keine Sitzung.
func (u *uebergabeState) signal() bool {
	u.mu.Lock()
	ch := u.ho
	u.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- struct{}{}:
	default: // läuft schon
	}
	return true
}

// logPresence loggt einen Fehler der Bereitschaft, höchstens einmal je
// presenceLogPause.
func (u *uebergabeState) logPresence(what string, err error) {
	now := presenceNow()
	u.mu.Lock()
	skip := !u.lastLog.IsZero() && now.Sub(u.lastLog) < presenceLogPause
	if !skip {
		u.lastLog = now
	}
	u.mu.Unlock()
	if !skip {
		log.Printf("Bereitschaft: %s: %v", what, err)
	}
}

// ---- Bereitschaft und Übernahme ----

// presenceLoop meldet alle presenceTakt die Bereitschaft, bis das Programm
// endet.
func (a *App) presenceLoop() {
	t := time.NewTicker(presenceTakt)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
		a.presenceTick(a.ctx)
	}
}

// presenceTick: Ist dieser PC übernahmebereit, Bereitschaft melden und ein
// Angebot annehmen.
func (a *App) presenceTick(ctx context.Context) {
	a.mu.Lock()
	idle := !a.quitting && !a.ending && a.busy == "" && a.hostCancel == nil && a.c != nil && a.st != nil
	joined := a.phase == phJoined
	c, st := a.c, a.st
	a.mu.Unlock()
	if !idle {
		return
	}
	// Mitspielen oder – ohne Mitspielen – untätig bereit im Dauerbetrieb.
	if !joined && !a.dauerbetriebActive() {
		return
	}
	prof, err := bench.LoadProfile()
	if err != nil || prof.KeineUebernahme {
		return
	}
	score, ok := a.presenceScore(ctx, c, prof.XmxMB)
	if !ok {
		return
	}
	resp, err := c.Presence(ctx, api.PresenceRequest{MachineID: st.MachineID, Score: score, Joined: joined})
	if err != nil {
		if ctx.Err() == nil {
			a.uebergabe.logPresence("Presence", err)
		}
		return
	}
	if resp.Handover != nil {
		a.takeOver(resp.Handover)
	}
}

// presenceScore liefert den Messwert für die Bereitschaft: den von „Spielen“
// gemerkten, sonst einmal gemessen (und gemerkt). false: nicht verfügbar.
func (a *App) presenceScore(ctx context.Context, c *client.Client, xmx int) (api.Score, bool) {
	if s, ok := a.uebergabe.cachedScore(); ok {
		return s, true
	}
	a.uebergabe.mu.Lock()
	wait := !a.uebergabe.scoreFail.IsZero() && presenceNow().Sub(a.uebergabe.scoreFail) < scoreRetry
	a.uebergabe.mu.Unlock()
	if wait {
		return api.Score{}, false
	}
	s, err := retry401(ctx, func() (api.Score, error) { return measureScore(ctx, c, xmx) })
	if err != nil {
		if ctx.Err() == nil {
			a.uebergabe.mu.Lock()
			a.uebergabe.scoreFail = presenceNow()
			a.uebergabe.mu.Unlock()
			a.uebergabe.logPresence("Messung", err)
		}
		return api.Score{}, false
	}
	log.Printf("Messung für die Bereitschaft: %+v", s)
	a.uebergabe.setScore(s, xmx)
	return s, true
}

// takeOver nimmt ein Angebot an: Nur wenn gerade keine andere Aktion und
// keine Host-Sitzung läuft. Die Übernahme läuft im Hintergrund.
func (a *App) takeOver(o *api.HandoverOffer) {
	a.mu.Lock()
	ok := !a.quitting && !a.ending && a.busy == "" && a.hostCancel == nil
	if ok {
		a.busy = uebernahmeLabel
	}
	a.mu.Unlock()
	if !ok {
		a.uebergabe.mu.Lock()
		again := a.uebergabe.refusedOffer == o.HandoverID
		a.uebergabe.refusedOffer = o.HandoverID
		a.uebergabe.mu.Unlock()
		if !again { // ein Logeintrag je Angebot
			log.Printf("Übergabe-Angebot %d nicht angenommen: es läuft schon etwas anderes", o.HandoverID)
		}
		return
	}
	a.uebergabe.mu.Lock()
	seen := a.uebergabe.lastOffer == o.HandoverID
	a.uebergabe.lastOffer = o.HandoverID
	a.uebergabe.mu.Unlock()
	if seen {
		// Schon einmal angenommen (und gescheitert): nicht noch einmal.
		a.end()
		return
	}
	a.stopJoinWatch()
	from := cmp.Or(o.From.MinecraftName, "dem bisherigen Host")
	log.Printf("Übergabe-Angebot %d: Hosten von %s übernehmen (Zuschlag %d, Head %s/%d, Port %d)",
		o.HandoverID, from, o.LeaseID, o.WorldID, o.HeadGeneration, o.Port)
	go notify("Du übernimmst das Hosten von " + from + " …")
	go func() {
		defer a.end()
		if err := a.startTakeover(a.ctx, o, from); err != nil {
			if !errors.Is(err, errLeaseGone) {
				a.releaseOffer(o)
			}
			a.fail("Hosten übernehmen", err)
		}
	}()
}

// releaseOffer gibt den wartenden Zuschlag aus einem gescheiterten Angebot
// sofort zurück; der Koordinator meldet dem alten Host dann gleich
// „gescheitert“, statt bis zur Zeitgrenze zu warten.
func (a *App) releaseOffer(o *api.HandoverOffer) {
	c := a.client()
	a.mu.Lock()
	st := a.st
	a.mu.Unlock()
	if c == nil || st == nil || o.LeaseID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := c.Release(ctx, api.ReleaseRequest{LeaseID: o.LeaseID, MachineID: st.MachineID})
	if err != nil {
		log.Printf("Übergabe-Angebot %d: Zuschlag zurückgeben: %v", o.HandoverID, err)
		return
	}
	log.Printf("Übergabe-Angebot %d: Zuschlag %d zurückgegeben", o.HandoverID, o.LeaseID)
}

// putProfile meldet das Leistungsprofil. Lehnt ein älterer Koordinator es
// wegen „keine_uebernahme“ ab, geht es noch einmal ohne dieses Feld (die
// Einstellung bleibt lokal gültig).
func putProfile(ctx context.Context, c *client.Client, machine string, p api.Profile) error {
	err := retry0(ctx, func() error { return c.PutProfile(ctx, machine, p) })
	if err != nil && p.KeineUebernahme {
		log.Printf("Profil mit „keine_uebernahme“ nicht angenommen (%v) – melde es ohne", err)
		p.KeineUebernahme = false
		return retry0(ctx, func() error { return c.PutProfile(ctx, machine, p) })
	}
	return err
}

// reportProfile meldet p im Hintergrund (Einstellung „Darf das Hosten
// übernehmen“ geändert).
func (a *App) reportProfile(p api.Profile) {
	c := a.client()
	a.mu.Lock()
	st := a.st
	a.mu.Unlock()
	if c == nil || st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if err := putProfile(ctx, c, st.MachineID, p); err != nil {
		log.Printf("Profil nicht gemeldet: %v", err)
	}
}

// takeoverBeat ist der Abstand der Überbrückungs-Lebenszeichen; im Test
// austauschbar.
var takeoverBeat = 10 * time.Second

// leaseBridge schickt ab jetzt für den wartenden Zuschlag aus dem Angebot
// alle takeoverBeat ein Lebenszeichen (ohne Spieler, nicht bereit), solange
// die Übernahme noch vorbereitet (Download, Welt-Ordner): Der Zuschlag
// verfiele sonst nach 90 s. Die zurückgegebene Funktion beendet es und
// wartet, bis nichts mehr unterwegs ist (danach schickt host.Run seine
// eigenen); mehrfach aufrufbar.
func (a *App) leaseBridge(o *api.HandoverOffer, lost func()) (stop func()) {
	c := a.client()
	a.mu.Lock()
	st := a.st
	a.mu.Unlock()
	if c == nil || st == nil || o.LeaseID == 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(takeoverBeat)
		defer t.Stop()
		for {
			_, err := c.Heartbeat(ctx, api.HeartbeatRequest{LeaseID: o.LeaseID, MachineID: st.MachineID, Players: []string{}})
			if err != nil && ctx.Err() == nil {
				log.Printf("Übergabe-Angebot %d: Lebenszeichen für den wartenden Zuschlag: %v", o.HandoverID, err)
				if errors.Is(err, client.ErrLeaseLost) {
					lost() // der Zuschlag ist weg: weitermachen hätte keinen Sinn
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// errLeaseGone: Der wartende Zuschlag ist verloren (409 auf das Lebenszeichen).
var errLeaseGone = errors.New("der wartende Zuschlag ist abgelaufen oder zurückgegeben")

// takeoverErr nennt als Fehler den Grund des Abbruchs, wenn die Übernahme
// abgebrochen wurde (errLeaseGone).
func takeoverErr(ctx context.Context, err error) error {
	if c := context.Cause(ctx); errors.Is(c, errLeaseGone) {
		return c
	}
	return err
}

// startTakeover wie „Spielen“ mit Rolle host, aber mit Zuschlag, Stand und
// Ersatz-Port aus dem Angebot.
func (a *App) startTakeover(ctx context.Context, o *api.HandoverOffer, from string) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := a.leaseBridge(o, func() { cancel(errLeaseGone) })
	defer stop()
	in, err := takeoverPrepare(a, ctx)
	if err != nil {
		return takeoverErr(ctx, err)
	}
	in.score, _ = a.uebergabe.cachedScore()
	if err := a.acquireWorld(ctx, "host"); err != nil {
		return takeoverErr(ctx, err)
	}
	a.mu.Lock()
	gone := a.quitting || a.ending
	a.mu.Unlock()
	if ctx.Err() != nil {
		a.releaseWorld()
		return takeoverErr(ctx, ctx.Err())
	}
	if gone {
		a.releaseWorld()
		return errors.New("wanderwelt wird gerade beendet")
	}
	resp := &api.PlayResponse{
		Role:             api.RoleHost,
		LeaseID:          o.LeaseID,
		HeadGeneration:   o.HeadGeneration,
		WorldID:          o.WorldID,
		MinecraftAddress: o.MinecraftAddress,
	}
	stop() // ab jetzt schickt host.Run die Lebenszeichen
	a.startHost(in, resp, false, &overtake{from: from, port: o.Port})
	return nil
}

// ---- Abgeben ----

// onHandover bekommt die Stände der Übergabe aus der Host-Sitzung (darf
// nicht blockieren).
func (a *App) onHandover(state, to string) {
	name := cmp.Or(to, "einem Mitspieler")
	log.Printf("Übergabe: %s (an %q)", state, to)
	switch state {
	case api.HandoverPreparing, api.HandoverStarting:
		a.handoverDetail("Übergabe an " + name + " …")
		if state == api.HandoverPreparing {
			go notify("Übergabe an " + name + " …")
		}
	case api.HandoverReady:
		a.uebergabe.mu.Lock()
		a.uebergabe.gave = name
		a.uebergabe.mu.Unlock()
		a.handoverDetail("Übergabe an " + name + ": die Spieler wechseln …")
	case api.HandoverFailed:
		a.uebergabe.mu.Lock()
		a.uebergabe.gave = ""
		a.uebergabe.mu.Unlock()
		a.handoverDetail("")
		go notify("Übergabe gescheitert.")
	case stateNobody:
		a.handoverDetail("")
		go notify("Niemand kann übernehmen – Server stoppt normal.")
	}
}

// handoverDetail zeigt text im Zustand, solange der Server noch als Host
// läuft (danach gehört der Zustand dem Stopp).
func (a *App) handoverDetail(text string) {
	a.mu.Lock()
	if a.phase == phHosting {
		a.detail = text
	}
	a.mu.Unlock()
	a.refresh()
}

// andereSpieler: die online sind, ohne den eigenen Namen.
func (a *App) andereSpieler() []string {
	a.mu.Lock()
	players := ownPlayers(a.status, a.me)
	own := ""
	if a.me != nil {
		own = a.me.MinecraftName
	}
	a.mu.Unlock()
	return fensterapi.AndereSpieler(players, own)
}

// handoverHosting: „Spiel für mich beenden“ als Host mit Mitspielern (nach
// der Rückfrage): Die Sitzung gibt die Welt weiter; klappt es nicht, endet
// sie wie gewohnt. Ist der Dauerbetrieb an, pausiert er wie sonst.
func (a *App) handoverHosting() {
	a.pauseDauerbetrieb()
	log.Printf("Hosten beenden: Spiel für mich beenden – mit Übergabe")
	if !a.uebergabe.signal() {
		a.endHosting("Spiel für mich beenden")
		return
	}
	a.handoverDetail("Übergabe wird vorbereitet …")
}

// stopHostingPlain: „Hosten ohne Weitergabe beenden“ (Tray und Fenster):
// wie früher, der Server stoppt für alle.
func (a *App) stopHostingPlain(ask bool) {
	a.endPlayingMode(ask, false)
}

// handoverActive: Der Status zeigt eine Übergabe, die nicht gescheitert ist.
func handoverActive(st *api.Status) bool {
	return st != nil && st.Handover != nil && st.Handover.State != api.HandoverFailed
}

// isTarget: h ist das Ziel der Übergabe (E-Mail, sonst Minecraft-Name).
func isTarget(h *api.HostInfo, to api.HostInfo) bool {
	return to.Email != "" && strings.EqualFold(h.Email, to.Email) ||
		to.Email == "" && to.MinecraftName != "" && strings.EqualFold(h.MinecraftName, to.MinecraftName)
}
