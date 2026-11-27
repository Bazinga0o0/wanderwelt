package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/mcserver"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// launcherHint steht in jedem Hinweis, der zum Verbinden auffordert: Ein
// schon offener Launcher liest das Profil „wanderwelt“ erst nach einem
// Neustart.
const launcherHint = " War der Minecraft-Launcher schon offen, starte ihn einmal neu, damit er das Profil sieht."

// openLauncher öffnet den Minecraft-Launcher und liefert den Schluss des
// Hinweises: geöffnet → launcherHint; sonst die Bitte, ihn selbst zu
// starten – in der Testumgebung ohne launcherHint (der echte Launcher sieht
// sie ohnehin nicht).
func openLauncher() string {
	const self = " Minecraft-Launcher bitte selbst starten."
	err := deps.OpenLauncher()
	if err != nil {
		log.Printf("Launcher: %v", err)
	}
	switch {
	case err == nil:
		return launcherHint
	case errors.Is(err, deps.ErrTestEnvironment):
		return self
	default:
		return self + launcherHint
	}
}

// Play ist „Spielen“ (Menü, Desktop-Verknüpfung). Kehrt sofort zurück.
// Ein pausierter Dauerbetrieb läuft danach wieder.
func (a *App) Play() {
	a.resumeDauerbetrieb("Spielen")
	// Ein ausdrückliches „Spielen“ ersetzt das automatische Fortsetzen nach
	// einer Wartung.
	a.fortVerwerfen("Spielen")
	a.startPlay(0)
}

// startPlay startet „Spielen“; auto zählt, wie oft es schon von selbst neu
// gefragt hat (Host verschwunden).
func (a *App) startPlay(auto int) {
	if a.fortRunning() {
		// Die Bewerbung nach der Wartung wird abgebrochen (Play hat den Merker
		// schon verworfen); das Warten darauf blockiert nicht den Tray.
		go func() {
			a.fortAbort("Spielen")
			a.startPlay(auto)
		}()
		return
	}
	a.mu.Lock()
	hosting, addr := a.hostCancel != nil, a.hostAddr
	joined := a.joinDauerPlayLocked()
	a.mu.Unlock()
	if joined {
		// Das Dauerbetrieb-Play läuft schon; es gilt jetzt als „Spielen“.
		log.Printf("Spielen: übernimmt das laufende automatische Hosten des Dauerbetriebs")
		return
	}
	if hosting {
		go func() {
			notify("Du hostest gerade – verbinde dich mit " + addr + " (Profil wanderwelt)." + openLauncher())
		}()
		return
	}
	if ok, running := a.begin("Spielen"); !ok {
		go notify("Moment – „" + running + "“ läuft noch.")
		return
	}
	a.stopJoinWatch()
	go func() {
		defer a.end()
		if err := a.play(a.ctx, auto, false); err != nil {
			a.fail("Spielen", err)
		}
	}()
}

// playInput ist alles, was „Spielen“ vor der Anfrage beim Koordinator
// einsammelt.
type playInput struct {
	c *client.Client // bezieht sich auf die Welt (client.ForWorld), sobald sie feststeht
	g *group.Group
	// st und dir: Zustand und Server-Ordner der Welt, die gespielt wird (die
	// der Gruppe und DefaultDir() für die erste Welt); world ist ihre Kennung
	// ("": der Koordinator kennt keine Welten). Gesetzt von bindWorld.
	st      *group.State
	dir     string
	world   string
	m       *api.Manifest
	paths   *deps.Paths
	profile api.Profile
	score   api.Score
	// fort: Bewerbung nach einer Wartung (fortsetzen.go): kein Launcher von
	// selbst, eigene Hinweistexte.
	fort bool
}

// play: Abhängigkeiten → Messung → Play → Host oder Mitspieler. Als
// Mitspieler beobachtet danach watchJoin den Host (bereit? noch da?).
//
// dauer: angefordert vom Dauerbetrieb (autoPlay). Hostet schon ein anderer
// PC, passiert dann nichts (kein Mitspielen, kein Hinweis); lehnt der
// Koordinator ab, ist das ein Fehlschlag (Wartezeit, siehe dauerFailed); als
// Host öffnet sich der Launcher nicht von selbst. Kam inzwischen „Spielen“
// (dauerState.manual), gilt das alles nicht: dann wie gewöhnliches Spielen.
func (a *App) play(ctx context.Context, auto int, dauer bool) error {
	return a.playWith(ctx, auto, dauer, nil)
}

// playWith ist play; fp: Bewerbung nach einer Wartung (fortsetzen.go). Sie
// geht denselben Weg, nur mit der Welt des Merkers, mit Warten auf den Beginn
// des gemeinsamen Fensters (nach Manifest, Abhängigkeiten und Messung), mit
// den Bedingungen kurz vor der Anfrage und mit PlayRequest.Resume; als Host
// und Mitspieler öffnet sich der Launcher nicht von selbst, und eine
// Ablehnung bleibt still.
func (a *App) playWith(ctx context.Context, auto int, dauer bool, fp *fortPlay) error {
	in, err := a.prepare(ctx)
	if err != nil {
		return err
	}
	// Welche Welt: die laufende, sonst die gewählte, sonst die erste (beim
	// Fortsetzen die des Merkers).
	var wc worldCtx
	if fp != nil {
		in.fort = true
		wc, err = a.fortWelt(ctx, in.c, fp.m)
	} else {
		wc, err = a.pickWorld(ctx, in.c)
	}
	if err != nil {
		return err
	}
	in.bindWorld(wc)

	a.setPhase(phStarting, "misst die Leistung …")
	in.score, err = retry401(ctx, func() (api.Score, error) { return measureScore(ctx, in.c, in.profile.XmxMB) })
	if err != nil {
		return fmt.Errorf("Messung: %w", err)
	}
	log.Printf("Messung: %+v", in.score)
	a.uebergabe.setScore(in.score, in.profile.XmxMB) // dieselbe Messung gilt für die Bereitschaft

	if fp != nil {
		if err := a.fortWarten(ctx, in.c, fp); err != nil {
			return err
		}
	}
	if err := a.acquireWorld(ctx, "host"); err != nil {
		return err
	}
	a.setPhase(phStarting, "fragt den Koordinator …")
	resp, err := retry401(ctx, func() (*api.PlayResponse, error) {
		return in.c.Play(ctx, api.PlayRequest{MachineID: in.st.MachineID, Score: in.score, Resume: fp != nil})
	})
	if err != nil {
		a.releaseWorld()
		return fmt.Errorf("Anfrage beim Koordinator: %w", err)
	}
	log.Printf("Play: Rolle %s (Zuschlag %d, Welt %q, Head %s/%d, Grund %q)", resp.Role, resp.LeaseID, resp.GroupWorld, resp.WorldID, resp.HeadGeneration, resp.Reason)
	dauer = dauer && !a.dauer.manual.Load()
	switch resp.Role {
	case api.RoleHost:
		a.startHost(in, resp, dauer, nil) // übernimmt den Welt-Ordner
		return nil
	case api.RoleJoin:
		a.releaseWorld()
		if dauer {
			// Ein anderer PC war schneller und hostet: nichts zu tun.
			name := "ein anderer PC"
			if resp.Host != nil {
				name = cmp.Or(resp.Host.MinecraftName, name)
			}
			log.Printf("Dauerbetrieb: %s hostet schon – nichts zu tun", name)
			a.setPhase(phReady, "")
			return nil
		}
		a.startJoin(in, resp, auto)
		return nil
	case api.RoleDenied:
		a.releaseWorld()
		a.setPhase(phReady, "")
		if fp != nil {
			// Still: Das Fenster war zu, oder die Welt geht gerade nicht; wer
			// spielen will, klickt „Spielen“.
			log.Printf("Fortsetzen: der Koordinator lehnt ab: %s", cmp.Or(resp.Reason, "ohne Grund"))
			if resp.Reason != api.ReasonResumeClosed {
				go notify("Das Hosten wurde nach der Wartung nicht fortgesetzt: " + cmp.Or(resp.Reason, "der Koordinator lehnt ab"))
			}
			return nil
		}
		if dauer {
			return fmt.Errorf("der Koordinator lehnt ab: %s", cmp.Or(resp.Reason, "ohne Grund"))
		}
		go notify("Spielen geht gerade nicht: " + cmp.Or(resp.Reason, "der Koordinator lehnt ab"))
		return nil
	default:
		a.releaseWorld()
		return fmt.Errorf("unbekannte Antwort des Koordinators: %q", resp.Role)
	}
}

// prepare sammelt, was „Spielen“ und die Übernahme des Hostens vor dem
// Zuschlag brauchen: Manifest, Leistungsprofil, Abhängigkeiten.
func (a *App) prepare(ctx context.Context) (playInput, error) {
	a.mu.Lock()
	in := playInput{c: a.c, g: a.g, st: a.st}
	a.mu.Unlock()

	a.setPhase(phStarting, "prüft Java, Server und Mods …")
	m, err := retry401(ctx, func() (*api.Manifest, error) { return in.c.Manifest(ctx) })
	if err != nil {
		return in, fmt.Errorf("Versions-Manifest: %w", err)
	}
	if m.MinecraftVersion == "" {
		return in, errors.New("das Versions-Manifest nennt keine Minecraft-Version")
	}
	in.m = m
	prof, err := bench.LoadProfile()
	if err != nil {
		return in, fmt.Errorf("Leistungsprofil fehlerhaft (Tray → Profil öffnen): %w", err)
	}
	in.profile = prof
	// Vor dem Zuschlag: Ein neues Manifest kann Downloads bedeuten, und
	// nach „host“ muss das erste Lebenszeichen sofort gehen.
	var (
		pmu      sync.Mutex
		lastName string
		lastPct  = -1
	)
	in.paths, err = deps.Ensure(ctx, m, prof, in.c, func(name string, done, total int64) {
		if total <= 0 {
			return
		}
		p := int(done * 100 / total)
		pmu.Lock()
		changed := name != lastName || p != lastPct
		lastName, lastPct = name, p
		pmu.Unlock()
		if changed {
			a.setDetail(fmt.Sprintf("lädt %s: %d %%", name, p))
		}
	})
	if err != nil {
		return in, fmt.Errorf("Abhängigkeiten: %w", err)
	}
	if err := putProfile(ctx, in.c, in.st.MachineID, prof); err != nil {
		log.Printf("Profil nicht gemeldet: %v", err)
	}

	return in, nil
}

// retry0 ist retry401 für Aufrufe ohne Ergebnis.
func retry0(ctx context.Context, f func() error) error {
	_, err := retry401(ctx, func() (struct{}, error) { return struct{}{}, f() })
	return err
}

// acquireWorld belegt den Welt-Ordner für eine mögliche Host-Sitzung bzw.
// für „Welt übernehmen“ (holder). Ein Vorladen wird dafür abgebrochen; ein
// Nachreichen nicht.
func (a *App) acquireWorld(ctx context.Context, holder string) error {
	if a.tryWorld(holder) {
		return nil
	}
	a.mu.Lock()
	holder, cancel := a.worldHolder, a.prefetchCancel
	a.mu.Unlock()
	switch holder {
	case "prefetch":
		if cancel != nil {
			cancel()
		}
		wctx, done := context.WithTimeout(ctx, time.Minute)
		defer done()
		if err := a.waitWorld(wctx, holder); err != nil {
			return errors.New("das Vorladen der Welt hört nicht auf – bitte gleich noch einmal")
		}
		return nil
	case "recover":
		// Spielen wartet, bis der letzte Stand nachgereicht ist (danach baut
		// die Sitzung auf ihm auf).
		a.setDetail("wartet, bis der letzte Stand dieses PCs nachgereicht ist …")
		wctx, done := context.WithTimeout(ctx, recoverWait)
		defer done()
		if err := a.waitWorld(wctx, holder); err != nil {
			return errors.New("der letzte Stand dieses PCs wird gerade nachgereicht – bitte gleich noch einmal")
		}
		return nil
	default:
		return errors.New("die Welt wird gerade benutzt – bitte gleich noch einmal")
	}
}

// startJoin: Mitspieler. Das Launcher-Profil wird gleich gesetzt; Launcher
// und „Server bereit“ kommen erst, wenn der Host bereit ist (watchJoin).
func (a *App) startJoin(in playInput, resp *api.PlayResponse, auto int) {
	if err := deps.EnsureLauncherProfile(in.m.MinecraftVersion); err != nil {
		log.Printf("Launcher-Profil: %v", err)
	}
	var host api.HostInfo
	if resp.Host != nil {
		host = *resp.Host
	}
	addr := cmp.Or(resp.MinecraftAddress, a.minecraftAddress())
	a.setPhase(phJoined, "Host startet die Welt …")
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	if a.joinCancel != nil {
		a.joinCancel()
	}
	a.joinSeq++
	seq := a.joinSeq
	a.joinCancel = cancel
	a.joinWorld = in.world
	a.mu.Unlock()
	go a.watchJoin(ctx, cancel, seq, in.c, host, addr, auto, in.fort)
}

// stopJoinWatch beendet die Beobachtung des Hosts als Mitspieler.
func (a *App) stopJoinWatch() {
	a.mu.Lock()
	cancel := a.joinCancel
	a.joinCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// watchJoin beobachtet als Mitspieler den Host (watchHost): Ist er bereit,
// Launcher öffnen und „Server bereit“ zeigen; verschwindet er, von selbst
// noch einmal „Spielen“ (höchstens maxAutoPlay-mal hintereinander).
func (a *App) watchJoin(ctx context.Context, cancel context.CancelFunc, seq int, c *client.Client, host api.HostInfo, addr string, auto int, fort bool) {
	defer func() {
		cancel()
		a.mu.Lock()
		if a.joinSeq == seq {
			a.joinCancel = nil
		}
		a.mu.Unlock()
	}()
	name := cmp.Or(host.MinecraftName, "Der Host")
	res := watchHost(ctx, func(ctx context.Context) (*api.Status, error) {
		st, err := c.Status(ctx)
		if err == nil {
			a.mu.Lock()
			if a.c == c {
				a.status, a.statusErr = st, nil
			}
			a.mu.Unlock()
			a.refresh()
			// Nennt der Status eine Wartung, merkt sich dieser Mitspieler sie
			// (Fortsetzen), noch bevor der Host stoppt und „weg“ gemeldet wird.
			a.fortNote(st)
		}
		return st, err
	}, host.Email, joinPoll, joinWatch, joinReadyMax, func(*api.Status) {
		a.mu.Lock()
		if a.phase == phJoined {
			a.detail = ""
		}
		a.mu.Unlock()
		a.refresh()
		var text string
		if fort {
			// Nach der Wartung: kein Launcher von selbst.
			text = name + " hostet nach der Wartung wieder – Server bereit, verbinde mit " + addr + " (Profil wanderwelt)."
		} else {
			text = name + " hostet – Server bereit, verbinde mit " + addr + " (Profil wanderwelt)." + openLauncher()
		}
		go notify(text)
	})
	switch res {
	case hostGone:
		a.mu.Lock()
		joined := a.phase == phJoined
		a.mu.Unlock()
		if !joined {
			return // inzwischen etwas anderes gewählt
		}
		if auto < maxAutoPlay {
			log.Printf("Mitspielen: %s ist weg – frage neu", name)
			go notify(name + " hostet nicht mehr – wanderwelt fragt neu, wer hostet …")
			a.startPlay(auto + 1)
			return
		}
		a.setPhase(phReady, "")
		go notify(name + " hostet nicht mehr. Klicke „Spielen“, um neu zu starten.")
	case watchHandedOver:
		// Der Host wechselte per Übergabe: Der Mitspieler bleibt Mitspieler
		// (Phase bleibt „spielt mit“); kein neues „Spielen“, kein Launcher.
		log.Printf("Mitspielen: %s hat das Hosten übergeben – weiter als Mitspieler", name)
	case hostNeverReady:
		a.setPhase(phReady, "")
		go notify(fmt.Sprintf("%s ist nach %v noch nicht bereit. Klicke „Spielen“, um es noch einmal zu versuchen.", name, joinReadyMax))
	}
}

type watchResult int

const (
	watchCanceled   watchResult = iota // ctx beendet
	watchDone                          // Host bereit und danach watchAfter lang da
	hostGone                           // kein Host mehr oder ein anderer
	hostNeverReady                     // nach readyMax noch nicht bereit
	watchHandedOver                    // der Host wechselte per Übergabe auf den neuen Host
)

// watchHost fragt alle interval den Status ab, bis der Host (hostEmail)
// verschwindet (hostGone), ctx endet oder er nach seiner Bereitschaft noch
// watchAfter lang da war (watchDone). Sobald der Status ihn als bereit
// meldet, läuft ready (einmal). Ist er nach readyMax nicht bereit, endet es
// mit hostNeverReady. Fehler beim Abfragen werden übergangen.
func watchHost(ctx context.Context, status func(context.Context) (*api.Status, error), hostEmail string,
	interval, watchAfter, readyMax time.Duration, ready func(*api.Status)) watchResult {
	start := time.Now()
	var readySince time.Time
	// target: Ziel einer zuletzt gesehenen laufenden Übergabe. Der
	// Koordinator setzt Status.Handover bei der Beförderung im selben
	// Moment auf nil, in dem der Host zum Ziel wird.
	var target *api.HostInfo
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		st, err := status(ctx)
		if err == nil && st.Handover != nil {
			if handoverActive(st) {
				t := st.Handover.To
				target = &t
			} else {
				target = nil // gescheitert
			}
		}
		switch {
		case ctx.Err() != nil:
			return watchCanceled
		case err != nil:
			log.Printf("Mitspielen: Status: %v", err)
		case st.Host == nil && handoverActive(st):
			// Der Host wechselt per Übergabe: weiter beobachten, nicht „weg“.
		case st.Host != nil && !strings.EqualFold(st.Host.Email, hostEmail) && target != nil && isTarget(st.Host, *target):
			return watchHandedOver
		case st.Host == nil || !strings.EqualFold(st.Host.Email, hostEmail):
			return hostGone
		case st.HostReady && readySince.IsZero():
			readySince = time.Now()
			ready(st)
		}
		switch {
		case !readySince.IsZero() && time.Since(readySince) >= watchAfter:
			return watchDone
		case readySince.IsZero() && time.Since(start) >= readyMax:
			return hostNeverReady
		}
		select {
		case <-ctx.Done():
			return watchCanceled
		case <-t.C:
		}
	}
}

func (a *App) minecraftAddress() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status != nil && a.status.MinecraftAddress != "" {
		return a.status.MinecraftAddress
	}
	if a.me != nil {
		return a.me.MinecraftAddress
	}
	return "der Minecraft-Adresse der Gruppe"
}

// ---- Host ----

// startHost startet host.Run in einer Goroutine. Der Welt-Ordner ist
// belegt und wird am Ende der Sitzung freigegeben. dauer: vom Dauerbetrieb
// angefordert (siehe play). ov: Übernahme des Hostens im laufenden Betrieb
// (Ersatz-Port, Name des alten Hosts); nil: gewöhnlicher Start.
func (a *App) startHost(in playInput, resp *api.PlayResponse, dauer bool, ov *overtake) {
	// Die Welt des Zuschlags gilt: Ordner, Zustand und Client sind die dieser
	// Welt (nie die einer anderen).
	if resp.GroupWorld != "" && resp.GroupWorld != in.world {
		if wc, err := a.worldFor(resp.GroupWorld, a.client(), true); err == nil {
			in.bindWorld(wc)
		} else {
			log.Printf("Welt %s: %v", resp.GroupWorld, err)
		}
	}
	ctx, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	addr := cmp.Or(resp.MinecraftAddress, a.minecraftAddress())
	a.mu.Lock()
	a.hostCancel, a.hostDone, a.hostAddr = cancel, done, addr
	ownName := ""
	if a.me != nil {
		ownName = a.me.MinecraftName
	}
	a.mu.Unlock()
	a.setPhase(phStarting, "lädt die Welt …")
	winsys.KeepAwake(true)

	srv := &watchedServer{
		Server:   mcserver.NewFabric(),
		started:  func() { a.setDetail("Server läuft, wartet auf tunnel …") },
		stopping: func() { a.setPhase(phUploading, "stoppt den Server und lädt den Stand hoch …") },
	}
	pr := newPercent(func(p int) { a.setDetail(fmt.Sprintf("lädt die Welt: %d %%", p)) })
	ho := a.uebergabe.newSession()
	a.tunnelHinweis.Store(false)
	d := host.Deps{
		Handover:         ho,
		OnHandover:       a.onHandover,
		Client:           in.c,
		Server:           srv,
		Paths:            in.paths,
		LeaseID:          resp.LeaseID,
		MachineID:        in.st.MachineID,
		Base:             resp.HeadGeneration,
		BaseWorldID:      resp.WorldID,
		Group:            in.g,
		State:            in.st,
		Dir:              in.dir,
		Fresh:            resp.Empty,
		Profile:          in.profile,
		MinecraftVersion: in.m.MinecraftVersion,
		HashCache:        a.hc,
		DownloadProgress: pr.report,
		OnTunnelProblem:  a.onTunnelProblem,
		OnReady: func() {
			a.dauerReady()
			go a.serverReadyMode(addr, in.m.MinecraftVersion, dauer, ov, in.fort)
		},
		Discarded: func(de *host.DiscardedError) { go notify(discardText(de)) },
		// Bei jeder Entscheidung der Sitzung neu gefragt: Haken im Profil
		// und am Netz – gleich, wie die Sitzung begonnen hat.
		Dauerbetrieb:   a.dauerbetriebActive,
		OnEmptyTimeout: func(d time.Duration) { a.dauer.frist.Store(int64(d)) },
		OwnName:        ownName,
		OnCommitted:    a.onCommitted,
		WeltLogs:       weltLogsHaken(in.st.MachineID),
	}
	if ov != nil {
		d.TunnelPort = ov.port
	}
	go func() {
		defer close(done)
		log.Printf("Host-Sitzung beginnt (Zuschlag %d)", resp.LeaseID)
		err := hostRun(ctx, d)
		cancel()
		gave := a.uebergabe.endSession()
		pending := in.st.PendingUpload
		// Sauber gestoppt und vorgemerkt: Die Sitzung hat die Markierung
		// gelöscht (eine ältere Vormerkung zählt nach einem harten Ende
		// nicht). Vor releaseWorld prüfen, solange niemand sonst an die Welt
		// darf.
		markerDir := cmp.Or(in.dir, host.DefaultDir())
		_, markerErr := os.Lstat(filepath.Join(markerDir, host.MarkerName))
		saved := pending && errors.Is(markerErr, fs.ErrNotExist)
		winsys.KeepAwake(false)
		a.mu.Lock()
		a.hostCancel, a.hostDone = nil, nil
		a.mu.Unlock()
		a.releaseWorld()
		if me, ok := errors.AsType[*host.MaintenanceError](err); ok {
			// Das Ende wegen Wartung (nie ein Ende durch den Nutzer): merken, damit
			// nach der Wartung gemeinsam fortgesetzt wird (fortsetzen.go).
			a.fortMerken(fensterapi.RolleHost, in.world, me.Maintenance.Since)
		}
		if err == nil && gave != "" && !pending {
			// Übergeben: kein Schluss-Stand, der neue Host hat die Welt.
			log.Printf("Host-Sitzung beendet: an %s übergeben", gave)
			a.setPhase(phReady, "")
			go notify("Übergabe an " + gave + " fertig – der Server läuft jetzt auf seinem PC. Du bleibst im Spiel.")
			return
		}
		a.hostEnded(err, pending, saved, dauer)
	}()
}

// serverReady: Der Server läuft; Launcher-Profil, Hinweis, Launcher (vom
// Dauerbetrieb gestartet: nur der Hinweis, der Launcher bleibt zu).
func (a *App) serverReady(addr, version string, dauer bool, ov *overtake) {
	a.serverReadyMode(addr, version, dauer, ov, false)
}

// serverReadyMode ist serverReady; fort: Hosten nach einer Wartung wieder
// aufgenommen (nur der Hinweis, der Launcher bleibt zu).
func (a *App) serverReadyMode(addr, version string, dauer bool, ov *overtake, fort bool) {
	a.mu.Lock()
	if a.hostCancel == nil {
		a.mu.Unlock()
		return
	}
	a.phase, a.detail, a.lastErr = phHosting, "", ""
	a.mu.Unlock()
	a.refresh()
	if err := deps.EnsureLauncherProfile(version); err != nil {
		log.Printf("Launcher-Profil: %v", err)
	}
	if ov != nil {
		// Die Spieler kommen vom alten Host herüber; der Launcher ist offen.
		notify("Hosten übernommen – der Server ist bereit, deine Mitspieler kommen gleich von " + ov.from + " herüber.")
		return
	}
	if fort {
		notify("Hosten nach der Wartung fortgesetzt – der Server ist bereit, erreichbar unter " + addr + " (Profil wanderwelt).")
		return
	}
	if dauer {
		notify("Dauerbetrieb: Dieser PC hostet, der Server ist bereit – erreichbar unter " + addr + " (Profil wanderwelt).")
		return
	}
	notify("Server bereit – verbinde mit " + addr + " (Profil wanderwelt)." + openLauncher())
}

// hostEnded wertet das Ende der Host-Sitzung aus. saved: Der Server wurde
// sauber gestoppt und sein Stand vorgemerkt. dauer: vom Dauerbetrieb
// gestartet – ein Fehler zählt dann als Fehlschlag des Dauerbetriebs
// (Wartezeit, ein Hinweis je Fehlerserie, siehe dauerFailed).
func (a *App) hostEnded(err error, pending, saved, dauer bool) {
	log.Printf("Host-Sitzung beendet: %v (vorgemerkter Stand: %v, sauber gestoppt: %v, Dauerbetrieb: %v)", err, pending, saved, dauer)
	fail := a.fail
	if dauer {
		fail = func(prefix string, err error) {
			if prefix != "" {
				err = fmt.Errorf("%s: %w", prefix, err)
			}
			a.dauerFailed(err)
		}
	}
	switch {
	case err == nil:
		a.setPhase(phReady, "")
		go notify("Hosten beendet – der Stand ist hochgeladen.")
	case errors.Is(err, host.ErrMaintenance):
		// Sauber beendet, Stand hochgeladen. Kein neuer Start: Der
		// Koordinator lehnt Spielen bis zum Ende der Wartung ab, auch der
		// Dauerbetrieb wartet (autoPlay). Das Ende der Wartung meldet der
		// Status (pollStatus).
		text := "Wartung am Server – der Stand ist hochgeladen. Spielen ist gleich wieder möglich."
		if me, ok := errors.AsType[*host.MaintenanceError](err); ok && me.Maintenance.Reason != "" {
			text = "Wartung am Server (" + me.Maintenance.Reason + ") – der Stand ist hochgeladen. Spielen ist gleich wieder möglich."
		}
		if a.fortPending() {
			text += " Danach setzt wanderwelt das Hosten automatisch fort (Tray: „" + fensterapi.FortsetzenAbbrechenLabel + "“)."
		}
		a.setPhase(phReady, "")
		go notify(text)
		go a.pollStatus(false)
	case errors.Is(err, host.ErrRestore):
		// Sauber beendet; der Koordinator legt den zurückgeholten Stand nach
		// der Freigabe als Head an. Gleich neu fragen (dieser PC ist weiter
		// der beste Host), die Spieler verbinden sich neu.
		text := "Ein Admin hat einen älteren Stand zurückgeholt – wanderwelt startet die Welt neu."
		if re, ok := errors.AsType[*host.RestoreError](err); ok {
			text = fmt.Sprintf("%s hat den Stand von %s zurückgeholt – wanderwelt startet die Welt neu.",
				cmp.Or(re.Notice.By, "Ein Admin"), host.RestoreTime(re.Notice))
		}
		a.setPhase(phReady, "")
		go notify(text)
		go a.replayAfterRestore(dauer)
	case errors.Is(err, host.ErrFenced) && saved:
		// Nachgereicht wird über startRecoverLoop (unten); hat inzwischen
		// jemand anderes weitergespielt, verwirft Recover mit Hinweis.
		// Fencing heißt auch „Zuschlag verloren“ (409): Dann hat vielleicht
		// schon ein anderer PC übernommen.
		fail("", errors.New("Server gestoppt: Die Verbindung zum Koordinator war zu lange weg (oder ein anderer PC hat übernommen). "+
			"Er wurde sauber gestoppt; der Stand wird nachgereicht, sobald möglich – außer jemand hat inzwischen weitergespielt."))
	case errors.Is(err, host.ErrFenced):
		fail("", errors.New("Server gestoppt: Die Verbindung zum Koordinator war zu lange weg (oder ein anderer PC hat übernommen)."))
	default:
		fail("Hosten", fmt.Errorf("%w. %s", err, pendingText(pending)))
	}
	if pending {
		a.startRecoverLoop()
	}
}

// restoreReplayDelay: So lange nach dem Ende wegen Zurückholen fragt
// wanderwelt von selbst neu. restoreReplayAfter ist die Uhr dafür (im Test
// austauschbar).
const restoreReplayDelay = 3 * time.Second

var restoreReplayAfter = time.After

// replayAfterRestore fragt nach dem Ende wegen Zurückholen von selbst neu
// (dauer: die Sitzung kam vom Dauerbetrieb). Läuft inzwischen schon etwas
// (ein „Spielen“, der Takt des Dauerbetriebs, Mitspielen) oder endet das
// Programm bzw. die Windows-Sitzung, passiert nichts – nie zwei „Spielen“
// nebeneinander. Kam die Sitzung vom Dauerbetrieb und wirkt er noch, geht
// es wie sein Takt (frischer Status, autoPlay: ohne Launcher, und hostet
// inzwischen ein anderer PC, nichts); sonst wie „Spielen“.
func (a *App) replayAfterRestore(dauer bool) {
	select {
	case <-a.ctx.Done():
		return
	case <-restoreReplayAfter(restoreReplayDelay):
	}
	a.mu.Lock()
	idle := !a.quitting && !a.ending && a.busy == "" && a.hostCancel == nil && a.joinCancel == nil
	a.mu.Unlock()
	if !idle {
		log.Printf("Zurückholen: kein neues „Spielen“ – inzwischen läuft schon etwas anderes")
		return
	}
	if dauer && a.dauerbetriebActive() {
		log.Printf("Zurückholen: Dauerbetrieb fragt neu")
		a.pollStatus(false)
		// Über eine Schnittstelle: dauerPlay führt bei der Initialisierung über
		// play und hostEnded hierher, ein direkter Aufruf von autoPlay wäre ein
		// Initialisierungszyklus.
		var tick interface{ autoPlay() bool } = a
		if !tick.autoPlay() {
			log.Printf("Zurückholen: Dauerbetrieb hostet nicht von selbst (hostet schon ein anderer PC, oder Status unbekannt) – sein Takt prüft weiter")
		}
		return
	}
	log.Printf("Zurückholen: frage neu")
	a.startPlay(0)
}

func pendingText(pending bool) string {
	if pending {
		return "Der Stand wird nachgereicht, sobald es geht."
	}
	return ""
}

// isHosting meldet, ob gerade eine Host-Sitzung läuft (auch während Start
// und Schluss-Upload).
func (a *App) isHosting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hostCancel != nil
}

// endHosting beendet die Host-Sitzung sauber (host.Run: Stop, Schluss-
// Stand, Zuschlag zurück). Kehrt sofort zurück.
func (a *App) endHosting(why string) {
	a.mu.Lock()
	cancel := a.hostCancel
	a.mu.Unlock()
	if cancel == nil {
		return
	}
	log.Printf("Hosten beenden: %s", why)
	a.setPhase(phUploading, "stoppt den Server und lädt den Stand hoch …")
	cancel()
}

// finishForShutdown: Windows fährt herunter, während dieser PC hostet oder
// das Dauerbetrieb-Play läuft (shutdownHosting). Das Play wird abgebrochen;
// hostet der PC danach, endet die Sitzung sauber. Kehrt zurück, wenn alles
// zu Ende ist (winsys wartet höchstens FinishTimeout).
func (a *App) finishForShutdown() {
	began := time.Now()
	a.fortVerwerfen("Windows fährt herunter")
	a.mu.Lock()
	// Ab jetzt startet der Dauerbetrieb nichts mehr von selbst; ein
	// abgebrochenes Herunterfahren setzt das zurück (sessionEnded).
	a.ending = true
	a.mu.Unlock()
	a.stopDauerPlay()
	a.mu.Lock()
	done := a.hostDone
	a.mu.Unlock()
	if done == nil {
		return
	}
	a.endHosting("Windows fährt herunter")
	<-done
	// Die Notfallkopie nur so lange, wie das Herunterfahren ohnehin gehalten
	// wird (danach gibt winsys es frei).
	a.waitNotfall(max(0, winsys.FinishTimeout-time.Since(began)))
}

// watchedServer meldet, wann der Server bereit ist und wann er gestoppt
// wird; sonst reicht er alles an den Server-Baustein durch (auch PID, die
// host für die Markierung braucht).
type watchedServer struct {
	mcserver.Server
	started  func()
	stopping func()
}

func (w *watchedServer) Start(ctx context.Context) error {
	err := w.Server.Start(ctx)
	if err == nil && w.started != nil {
		w.started()
	}
	return err
}

func (w *watchedServer) Stop(ctx context.Context) error {
	if w.stopping != nil {
		w.stopping()
	}
	return w.Server.Stop(ctx)
}

// PID reicht die Prozess-ID des Servers durch (0, wenn unbekannt).
func (w *watchedServer) PID() int {
	if p, ok := w.Server.(interface{ PID() int }); ok {
		return p.PID()
	}
	return 0
}

// onTunnelProblem zeigt, dass der tunnel-Client nicht startet: im Fenster als
// Detail und einmal je Host-Sitzung als Hinweis, mit der letzten Fehlerzeile
// aus tunnel.log. Solange tunnel nicht läuft, kann niemand beitreten.
func (a *App) onTunnelProblem(grund string) {
	a.setDetail("Tunnel startet nicht – niemand kann beitreten (siehe Hinweis)")
	if !a.tunnelHinweis.CompareAndSwap(false, true) {
		return
	}
	text := "Der Tunnel startet nicht, deshalb kann niemand deiner Welt beitreten."
	switch {
	case strings.Contains(grund, "missing port"):
		text += "\n\nIn deiner Gruppendatei fehlt beim Tunnel-Server der Port. Trage tunnel_server mit Port ein (zum Beispiel twebl.de:7443)."
	case grund != "":
		text += "\n\nGrund: " + grund
	}
	text += "\n\nPrüfe Windows-Sicherheit (Schutzverlauf: wurde tunnel.exe blockiert?) und die Firewall. Mehr steht im Log (Tray: Log öffnen) und in logs\tunnel.log."
	go notify(text)
}
