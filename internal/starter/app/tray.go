package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// maxTooltip: Windows zeigt höchstens 127 UTF-16-Zeichen.
const maxTooltip = 120

type trayMenu struct {
	open, play, stop, stopPlain, group, importW, profile, logs, quit *systray.MenuItem

	shownIcon   phase
	iconSet     bool
	importShown bool
	plainShown  bool // „Hosten ohne Weitergabe beenden“ ist sichtbar
	stopHosting bool // der Menüpunkt „beenden“ trägt den Host-Text

	// dauer: Haken „Dauerbetrieb“; dauerShown ist der angezeigte Stand.
	dauer      *systray.MenuItem
	dauerShown bool
}

// runTray zeigt das Tray-Symbol und blockiert, bis „Beenden“ fertig ist.
// Muss auf der Haupt-Goroutine laufen (systray hält den Haupt-Thread).
func (a *App) runTray() {
	systray.Run(a.onTrayReady, a.onTrayExit)
}

func (a *App) onTrayReady() {
	a.mu.Lock()
	a.trayUp = true
	quitting := a.quitting
	a.mu.Unlock()
	if quitting { // „update“ kam, bevor der Tray lief
		systray.Quit()
		return
	}
	go a.askMinecraftName() // nach dem Tray, blockiert nichts
	m := &trayMenu{}
	systray.SetTitle("wanderwelt")
	systray.SetTooltip("wanderwelt")
	m.open = systray.AddMenuItem("wanderwelt öffnen", "Das wanderwelt-Fenster öffnen")
	systray.AddSeparator()
	// Linksklick aufs Symbol öffnet das Fenster, Rechtsklick zeigt das Menü.
	systray.SetOnTapped(func() { go openWindow() })
	m.play = systray.AddMenuItem("Spielen", "Hosten oder mitspielen")
	stopLabel, stopHint := fensterapi.BeendenText(true)
	m.stop = systray.AddMenuItem(stopLabel, stopHint)
	m.stopHosting = true
	m.stopPlain = systray.AddMenuItem("Hosten ohne Weitergabe beenden", "Server für alle stoppen, die Welt nicht an einen Mitspieler übergeben")
	m.stopPlain.Hide()
	m.dauerShown = a.dauer.on.Load()
	m.dauer = systray.AddMenuItemCheckbox("Dauerbetrieb (Server auch ohne Spieler)",
		"Dieser PC hostet von selbst, sobald niemand hostet, auch ohne Spieler – nur am Netz", m.dauerShown)
	systray.AddSeparator()
	m.group = systray.AddMenuItem("Gruppendatei laden", "Eine neue Gruppendatei übernehmen")
	m.importW = systray.AddMenuItem("Welt übernehmen …", "Eine Einzelspieler-Welt zur Gruppenwelt machen (nur Admin)")
	m.importW.Hide()
	m.profile = systray.AddMenuItem("Profil öffnen", "Leistungsprofil im Editor öffnen")
	m.logs = systray.AddMenuItem("Log öffnen", "Log des Starters im Editor öffnen")
	systray.AddSeparator()
	m.quit = systray.AddMenuItem("Beenden", "wanderwelt beenden")
	log.Printf("Tray bereit: %v %v %v %v %v %v %v", m.play, m.stop, m.group, m.importW, m.profile, m.logs, m.quit)
	a.updateTray(m)

	go func() {
		for {
			select {
			case <-m.open.ClickedCh:
				go openWindow()
			case <-m.play.ClickedCh:
				a.Play()
			case <-a.playCh:
				a.Play()
			case <-m.stopPlain.ClickedCh:
				go a.stopHostingPlain(true)
			case <-m.stop.ClickedCh:
				go a.stopPlaying()
			case <-m.dauer.ClickedCh:
				go a.toggleDauerbetrieb()
			case <-m.group.ClickedCh:
				go a.reloadGroup()
			case <-m.importW.ClickedCh:
				go a.importDialog()
			case <-m.profile.ClickedCh:
				go openProfile()
			case <-m.logs.ClickedCh:
				go func() {
					if err := winsys.OpenInEditor(LogPath()); err != nil {
						showError(err.Error())
					}
				}()
			case <-m.quit.ClickedCh:
				go a.quit()
			case <-a.refreshCh:
				a.updateTray(m)
			case <-a.quitCh:
				return
			}
		}
	}()
}

// onTrayExit ruft systray beim Beenden auf – aber auch bei jedem
// WM_ENDSESSION, selbst wenn das Herunterfahren abgebrochen wurde; dann
// entfernt systray sein Symbol. sessionEnded holt es zurück.
func (a *App) onTrayExit() {
	a.mu.Lock()
	quitting, ending := a.quitting, a.ending
	a.mu.Unlock()
	log.Printf("Tray: Ende gemeldet (Beenden: %v, Sitzung endet: %v)", quitting, ending)
}

// updateTray bringt Symbol, Tooltip und Menü auf den Stand. Nach dem
// Beenden tut es nichts mehr (sonst „systray error: unable to set icon“).
func (a *App) updateTray(m *trayMenu) {
	a.trayMu.Lock()
	defer a.trayMu.Unlock()
	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		return
	}
	ph, detail, lastErr := a.phase, a.detail, a.lastErr
	me, status, statusErr := a.me, a.status, a.statusErr
	hosting := a.hostCancel != nil
	busy := a.busy
	a.mu.Unlock()

	if !m.iconSet || m.shownIcon != ph {
		systray.SetIcon(phaseIcon(ph))
		m.iconSet, m.shownIcon = true, ph
	}

	line1 := "wanderwelt – " + ph.String()
	switch {
	case ph == phError && lastErr != "":
		line1 += ": " + lastErr
	case detail != "":
		line1 += ": " + detail
	}
	var line2 string
	switch {
	case statusErr != nil:
		line2 = "Koordinator nicht erreichbar"
	case status == nil:
		line2 = ""
	case status.Handover != nil:
		line2 = fensterapi.UebergabeText(status.Handover)
	case status.Host == nil:
		line2 = "Niemand hostet"
	case status.HostUploading:
		line2 = hostAnzeige(status.Host) + " lädt einen Stand hoch"
	case !status.HostReady:
		line2 = "Host: " + hostAnzeige(status.Host) + " · startet die Welt …"
	default:
		line2 = "Host: " + hostAnzeige(status.Host)
		if len(status.Players) > 0 {
			line2 += " · Spieler: " + strings.Join(status.Players, ", ")
		} else {
			line2 += " · Spieler: –"
		}
	}
	systray.SetTooltip(tooltip(line1, line2))

	if asHost := hosting || busy == dauerLabel; asHost != m.stopHosting {
		label, hint := fensterapi.BeendenText(asHost)
		m.stop.SetTitle(label)
		m.stop.SetTooltip(hint)
		m.stopHosting = asHost
	}
	if hosting || ph == phJoined || busy == dauerLabel {
		m.stop.Enable()
	} else {
		m.stop.Disable()
	}
	if hosting != m.plainShown {
		if hosting {
			m.stopPlain.Show()
		} else {
			m.stopPlain.Hide()
		}
		m.plainShown = hosting
	}
	if busy != "" && !hosting {
		m.play.Disable()
	} else {
		m.play.Enable()
	}
	admin := me != nil && me.Admin
	if admin != m.importShown {
		if admin {
			m.importW.Show()
		} else {
			m.importW.Hide()
		}
		m.importShown = admin
	}
	if on := a.dauer.on.Load(); on != m.dauerShown {
		if on {
			m.dauer.Check()
		} else {
			m.dauer.Uncheck()
		}
		m.dauerShown = on
	}
}

// tooltip setzt zwei Zeilen zusammen und kürzt sie auf die Länge, die
// Windows anzeigt (ein zu langer Text käme ohne Ende-Zeichen an).
func tooltip(line1, line2 string) string {
	s := line1
	if line2 != "" {
		s += "\n" + line2
	}
	if len(utf16.Encode([]rune(s))) <= maxTooltip {
		return s
	}
	r := []rune(s)
	for len(utf16.Encode(r)) > maxTooltip-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// statusLoop holt alle 30 s Host und Spieler (und, solange unbekannt, wer
// man selbst ist).
func (a *App) statusLoop() {
	t := time.NewTicker(statusInterval)
	defer t.Stop()
	for n := 0; ; n++ {
		// Alle 5 Minuten auch Me: Admin-Merker und Name können sich beim
		// Koordinator ändern.
		a.pollStatus(n%10 == 9)
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) pollStatus(refreshMe bool) {
	c := a.client()
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	st, err := retry401(ctx, func() (*api.Status, error) { return c.Status(ctx) })
	if err != nil && a.ctx.Err() == nil {
		log.Printf("Status: %v", err)
	}
	a.mu.Lock()
	// Admins fragen Me jedes Mal: Es trägt die Zahl der Beitrittsanfragen.
	needMe := a.me == nil || refreshMe || a.me.Admin
	if err == nil && a.statusErr != nil {
		a.kickRecoverLocked() // wieder erreichbar: Vorgemerktes gleich nachreichen
	}
	a.status, a.statusErr = st, err
	// Mitspieler, aber niemand hostet mehr: wieder bereit (solange watchJoin
	// läuft, entscheidet das dort, ob neu gefragt wird).
	if err == nil && st.Host == nil && !handoverActive(st) && a.phase == phJoined && a.busy == "" && a.joinCancel == nil {
		a.phase, a.detail = phReady, ""
	}
	a.mu.Unlock()
	if needMe && err == nil {
		if me, err := c.Me(ctx); err == nil {
			a.mu.Lock()
			cur := a.c == c
			if cur {
				a.me = me
			}
			a.mu.Unlock()
			if cur {
				a.beitrittsHinweis(ctx, c, me)
			}
		} else {
			log.Printf("Me: %v", err)
		}
	}
	a.refresh()
}

// ---- Menü ----

// ownPlayers liefert die Spieler aus status, wenn me selbst der Host ist
// (der Status nennt keine MachineID, nur das Konto).
func ownPlayers(status *api.Status, me *api.Me) []string {
	if status == nil || status.Host == nil || me == nil || me.Email == "" ||
		!strings.EqualFold(status.Host.Email, me.Email) {
		return nil
	}
	return status.Players
}

// stopPlaying: „Spiel für mich beenden“ im Tray; als Host erst nach einer
// Rückfrage.
func (a *App) stopPlaying() { a.endPlaying(true) }

// endPlaying beendet das eigene Spiel (Tray und Fenster): als Host mit
// Mitspielern die Welt weitergeben (klappt das nicht, stoppt der Server wie
// gewohnt), sonst Server stoppen, Stand hochladen, Zuschlag zurück
// (Dauerbetrieb pausiert); als Mitspieler nur die Beobachtung des Hosts.
// ask: als Host vorher fragen (das Fenster hat schon selbst gefragt).
func (a *App) endPlaying(ask bool) { a.endPlayingMode(ask, true) }

// endPlayingMode ist endPlaying; handover false: als Host nie weitergeben
// („Hosten ohne Weitergabe beenden“).
func (a *App) endPlayingMode(ask, handover bool) {
	a.mu.Lock()
	hosting := a.hostCancel != nil
	ph := a.phase
	players := ownPlayers(a.status, a.me)
	dauerStarting := a.busy == dauerLabel
	a.mu.Unlock()
	others := a.andereSpieler()
	switch {
	case dauerStarting && !hosting:
		a.cancelDauerPlay()
	case hosting:
		q := "Du hostest gerade. Der Server wird gestoppt, der Stand hochgeladen und der Zuschlag freigegeben."
		if len(players) > 0 {
			q = fmt.Sprintf("Du hostest gerade, online sind: %s.\n\nDer Server stoppt dann für alle. Wer weiterspielen will, klickt danach „Spielen“ und wird neuer Host.", strings.Join(players, ", "))
		}
		give := handover && len(others) > 0
		if give {
			q = fmt.Sprintf("Du hostest gerade, online sind: %s.\n\n%s", strings.Join(players, ", "), fensterapi.UebergabeFrage)
		}
		if ask && !confirm(q+a.dauerHint()+"\n\nJetzt beenden?", "Beenden", "Weiterhosten") {
			return
		}
		if give {
			a.handoverHosting()
			return
		}
		a.stopHosting()
	case ph == phJoined:
		a.stopJoinWatch()
		a.setPhase(phReady, "")
		go notify("Okay. Minecraft selbst schließt du wie gewohnt.")
	}
}

// reloadGroup: „Gruppendatei laden“. Ein Fehlerfenster erscheint erst nach
// dem Ende der Aktion (endWith).
func (a *App) reloadGroup() {
	if ok, running := a.begin("Gruppendatei laden"); !ok {
		go notify("Erst wenn „" + running + "“ fertig ist.")
		return
	}
	a.endWith(a.chooseGroup())
}

// chooseGroup lässt die neue Gruppendatei wählen, prüft und übernimmt sie;
// das Ergebnisfenster (nil: keins) zeigt reloadGroup.
func (a *App) chooseGroup() *resultDialog {
	if a.isHosting() {
		return infoDialog("Die Gruppendatei lässt sich nicht tauschen, während du hostest.")
	}
	path, err := zenity.SelectFile(
		zenity.Title("Neue Gruppendatei wählen"), windowIcon(),
		zenity.FileFilters{{Name: "Gruppendatei (*.yaml)", Patterns: []string{"*.yaml", "*.yml"}, CaseFold: true}},
	)
	if err != nil {
		return nil
	}
	g, err := group.LoadGroup(path)
	if err != nil {
		return errorDialog("Diese Gruppendatei ist nicht lesbar:\n\n" + err.Error())
	}
	me, err := memberCheck(a.ctx, g)
	if err != nil {
		return errorDialog("Mit dieser Gruppendatei geht es nicht:\n\n" + explain(err))
	}
	if _, err := group.InstallGroup(path); err != nil {
		return errorDialog("Gruppendatei übernehmen: " + err.Error())
	}
	a.mu.Lock()
	a.g, a.c, a.me = g, client.New(g), me
	a.mu.Unlock()
	log.Printf("Gruppendatei %s übernommen (Mitglied %s)", path, me.Email)
	go a.pollStatus(false)
	go notify("Gruppendatei übernommen" + mitPunkt(me.MinecraftName))
	return nil
}

// mitPunkt: „ – du bist Mara.“ bzw. „.“ ohne Namen.
func mitPunkt(name string) string {
	if name == "" {
		return "."
	}
	return " – du bist " + name + "."
}

// openProfile: „Profil öffnen“ (Stufe 1: die Datei im Editor).
func openProfile() {
	path := bench.ProfilePath()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := bench.SaveProfile(bench.DefaultProfile()); err != nil {
			showError("Profil anlegen: " + err.Error())
			return
		}
	}
	if err := winsys.OpenInEditor(path); err != nil {
		showError(err.Error())
	}
}

// openWindow: „wanderwelt öffnen“ (und Linksklick aufs Symbol) startet das
// Fenster als eigenen Prozess („wanderwelt.exe fenster“); ein schon offenes
// holt es nach vorn.
func openWindow() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("Fenster: %v", err)
		return
	}
	// Nach dem Klick darf dieser Prozess ein Fenster nach vorn holen; das
	// Recht an den neuen Prozess weitergeben.
	winsys.AllowForeground()
	cmd := exec.Command(exe, "fenster")
	cmd.Dir = group.Home()
	if err := cmd.Start(); err != nil {
		log.Printf("Fenster: %v", err)
		showError("Das wanderwelt-Fenster lässt sich nicht öffnen: " + err.Error())
		return
	}
	cmd.Process.Release()
}

// quit: „Beenden“. Hostet der PC, wird erst sauber beendet.
func (a *App) quit() {
	a.mu.Lock()
	hosting := a.hostCancel != nil
	done := a.hostDone
	busy := a.busy
	a.mu.Unlock()
	if hosting {
		if !confirm("Du hostest gerade. Beim Beenden stoppt der Server für alle, der Stand wird hochgeladen und der Zuschlag freigegeben.\n\nJetzt beenden?", "Beenden", "Weiterlaufen lassen") {
			return
		}
		a.endHosting("Beenden")
		<-done
	} else if busy != "" {
		if !confirm("Gerade läuft „"+busy+"“. Trotzdem beenden?", "Beenden", "Weiterlaufen lassen") {
			return
		}
	}
	a.quitNow()
}

// quitForUpdate: Eine neue Version wartet auf die Sperre (Aktion update).
// Ohne Rückfrage: Hosten sauber beenden (Stop, Schluss-Stand, Zuschlag
// zurück), dann enden.
func (a *App) quitForUpdate() {
	log.Printf("Update: eine neue Version von wanderwelt wartet")
	a.mu.Lock()
	done := a.hostDone
	a.mu.Unlock()
	if done != nil {
		go notify("Eine neue Version von wanderwelt wird installiert. Das Hosten wird dafür sauber beendet (der Stand wird hochgeladen) …")
		a.endHosting("Update")
		<-done
	}
	a.quitNow()
}

// quitNow beendet das Programm: Hintergrundarbeit abbrechen, Tray zu.
func (a *App) quitNow() {
	a.trayMu.Lock() // kein updateTray mehr neben oder nach systray.Quit
	defer a.trayMu.Unlock()
	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		return
	}
	a.quitting = true
	trayUp := a.trayUp
	a.mu.Unlock()
	log.Printf("Beenden")
	close(a.quitCh)
	a.cancel()
	if trayUp {
		systray.Quit()
	}
}

func (a *App) isQuitting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.quitting
}

// ---- Ende der Windows-Sitzung ----

var restoreOnce sync.Mutex

// sessionEnded bekommt WM_ENDSESSION. Wurde das Herunterfahren
// abgebrochen, hat systray sein Symbol trotzdem entfernt: zurückholen.
func (a *App) sessionEnded(ending bool) {
	a.mu.Lock()
	a.ending = ending
	a.mu.Unlock()
	if ending {
		log.Printf("Windows-Sitzung endet")
		return
	}
	go func() {
		restoreOnce.Lock()
		defer restoreOnce.Unlock()
		// systray bekommt WM_ENDSESSION auf seinem eigenen Thread, vielleicht
		// erst nach uns.
		for _, d := range []time.Duration{time.Second, 3 * time.Second} {
			time.Sleep(d)
			if err := winsys.RestoreTrayIcon(); err != nil {
				log.Printf("Tray-Symbol wiederherstellen: %v", err)
			}
		}
		log.Printf("Herunterfahren abgebrochen – Tray-Symbol wiederhergestellt")
		a.mu.Lock()
		a.ending = false
		a.mu.Unlock()
	}()
}
