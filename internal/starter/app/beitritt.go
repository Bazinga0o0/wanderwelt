package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// Einrichtung ohne Gruppendatei (Gruppen-Build): Die Adressen stecken in der
// exe (group.Invite). Das Mitglied legt auf der Webseite ein Konto an,
// erstellt dort einen Token und fügt ihn ein; ist es noch kein Mitglied der
// Gruppe, stellt die Einrichtung eine Beitrittsanfrage und wartet auf die
// Freigabe durch einen Admin. Der Token steht nie in Dialogen oder im Log.

// Auswahl im Einrichtungsmenü.
type tokenChoice int

const (
	choiceAccount tokenChoice = iota // Konto anlegen (Webseite)
	choiceToken                      // Token erstellen (Webseite)
	choicePaste                      // Token einfügen
	choiceFile                       // „Ich habe eine Gruppendatei“
)

var tokenMenuItems = map[tokenChoice]string{
	choiceAccount: "1. Konto anlegen (öffnet die Webseite)",
	choiceToken:   "2. Token erstellen (öffnet die Webseite)",
	choicePaste:   "3. Token einfügen",
	choiceFile:    "Ich habe eine Gruppendatei vom Admin",
}

// Dialoge der Einrichtung; im Test austauschbar.
var (
	// tokenMenu zeigt das Menü; ok false: abgebrochen.
	tokenMenu = func(text string) (tokenChoice, bool) {
		order := []tokenChoice{choiceAccount, choiceToken, choicePaste, choiceFile}
		items := make([]string, len(order))
		for i, c := range order {
			items[i] = tokenMenuItems[c]
		}
		s, err := zenity.List(text, items, zenity.Title("wanderwelt einrichten"), windowIcon(),
			zenity.OKLabel("Weiter"), zenity.CancelLabel("Abbrechen"), zenity.DisallowEmpty())
		if err != nil {
			return 0, false
		}
		for _, c := range order {
			if tokenMenuItems[c] == s {
				return c, true
			}
		}
		return 0, false
	}
	// tokenEntry fragt den Token mit verdeckter Eingabe.
	tokenEntry = func(text string) (string, bool) {
		s, err := zenity.Entry(text, zenity.Title("wanderwelt einrichten"), windowIcon(), zenity.HideText())
		return s, err == nil
	}
	openURL = winsys.OpenURL
	// openJoinWait öffnet das Wartefenster („wartet auf Freigabe“).
	openJoinWait = func() (zenity.ProgressDialog, error) {
		return zenity.Progress(zenity.Title("wanderwelt – wartet auf Freigabe"), windowIcon(),
			zenity.Pulsate(), zenity.CancelLabel("Später fortsetzen"))
	}
	// joinPollInterval: So oft fragt das Wartefenster den Stand ab.
	joinPollInterval = 60 * time.Second
)

const (
	msgTokenInvalid = "Token ungültig oder Konto noch nicht freigeschaltet.\n\n" +
		"Prüfe, ob du den ganzen Token kopiert hast, und ob dein tunnel-Konto schon freigeschaltet ist " +
		"(das macht der Admin). Dann kannst du es noch einmal versuchen."
	nameEntryTries = 5
	// waitFailLimit: So viele Fehlschläge in Folge, bis das Wartefenster es sagt.
	waitFailLimit = 3
	// memberTries: Versuche, die Mitgliedschaft nach der Annahme zu bestätigen.
	memberTries = 5
)

// errNoJoin: Der Koordinator kennt keine Beitrittsanfragen (403/404 auf
// /v1/join: alte Fassung).
var errNoJoin = errors.New("der Koordinator kennt keine Beitrittsanfragen")

const msgNoJoin = "Der Koordinator deiner Gruppe kann noch keine Beitrittsanfragen – bitte den Admin um eine Gruppendatei."

func noJoinEndpoint(err error) bool { return isStatus(err, 403) || isStatus(err, 404) }

var (
	memberRetryDelay = 2 * time.Second
	// errLater: „Später fortsetzen“ – kein Fehler, die Einrichtung geht beim
	// nächsten Start weiter (Run endet mit 0).
	errLater = errors.New("Einrichtung später fortsetzen")
	// prepareWait trägt vor dem Warten Autostart und Verknüpfung ein; im Test
	// austauschbar.
	// undoPrepareWait nimmt Autostart und Verknüpfung von prepareWait zurück.
	undoPrepareWait = func() {
		if err := winsys.RemoveAutostart(); err != nil {
			log.Printf("Einrichtung: Autostart entfernen: %v", err)
		}
		if desk, err := winsys.DesktopDir(); err == nil {
			if err := os.Remove(filepath.Join(desk, ShortcutName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				log.Printf("Einrichtung: Verknüpfung entfernen: %v", err)
			}
		}
	}
	prepareWait = func() {
		exe := installedExe()
		if err := winsys.SetAutostart(exe); err != nil {
			log.Printf("Einrichtung: Autostart: %v", err)
		}
		if err := createShortcut(exe); err != nil {
			log.Printf("Einrichtung: Verknüpfung: %v", err)
		}
	}
)

// chooseGroupInvite ist chooseGroup für den Gruppen-Build: Token einfügen
// statt Gruppendatei wählen. Eine schon eingerichtete Gruppendatei mit
// Token (abgebrochene Einrichtung, etwa beim Warten) gilt weiter.
func (a *App) chooseGroupInvite(ctx context.Context) (*group.Group, *api.Me, error) {
	g, me, err := a.chooseGroupInviteRun(ctx)
	if err != nil && !errors.Is(err, errLater) {
		// Endet die Einrichtung endgültig ohne Mitgliedschaft (abgelehnt,
		// Abbruch, Token ungültig), soll nichts mehr von selbst starten.
		undoPrepareWait()
	}
	return g, me, err
}

func (a *App) chooseGroupInviteRun(ctx context.Context) (*group.Group, *api.Me, error) {
	inv := a.invite
	var g *group.Group
	onDisk := false
	if installed, err := group.LoadGroup(group.GroupPath()); err == nil {
		g, onDisk = installed, true
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("installierte Gruppendatei: %v", err)
	}
	first := true
	for {
		if g == nil && a.presetToken != "" {
			// Token aus „Entdecken“ der Einrichtung: nicht noch einmal fragen.
			g, onDisk, a.presetToken = inv.WithToken(a.presetToken), false, ""
		}
		if g == nil {
			tok, useFile, err := askToken(inv, first)
			if err != nil {
				return nil, nil, err
			}
			first = false
			if useFile {
				return chooseGroupFile(ctx, "")
			}
			g, onDisk = inv.WithToken(tok), false
		}
		me, err := memberCheck(ctx, g)
		switch {
		case err == nil:
			if !onDisk {
				if err := group.SaveGroup(g); err != nil {
					return nil, nil, fmt.Errorf("Gruppendatei speichern: %w", err)
				}
			}
			return g, me, nil
		case errors.Is(err, errTokenInvalid):
			log.Printf("Einrichtung: Token wird abgelehnt")
			showError(msgTokenInvalid)
			g = nil
		case isStatus(err, 403):
			// Gültiger Token, aber (noch) kein Mitglied: speichern, damit ein
			// Neustart beim Warten weitermacht, und Beitritt anfragen.
			if !onDisk {
				if err := group.SaveGroup(g); err != nil {
					return nil, nil, fmt.Errorf("Gruppendatei speichern: %w", err)
				}
				onDisk = true
			}
			me, err := awaitJoin(ctx, g)
			if errors.Is(err, errTokenInvalid) {
				// Token während des Wartens entzogen: neu eingeben lassen.
				log.Printf("Einrichtung: Token wird abgelehnt")
				showError(msgTokenInvalid)
				g = nil
				continue
			}
			if errors.Is(err, errNoJoin) {
				// Alter Koordinator ohne Beitrittsanfragen: nur mit Gruppendatei.
				if !confirm(msgNoJoin, "Gruppendatei wählen", "Abbrechen") {
					return nil, nil, errCanceled
				}
				return chooseGroupFile(ctx, "")
			}
			if err != nil {
				return nil, nil, err
			}
			return g, me, nil
		case errors.Is(err, context.Canceled):
			return nil, nil, errCanceled
		default:
			log.Printf("Einrichtung: %v", err)
			if !confirm(explain(err)+"\n\nNoch einmal versuchen?", "Erneut versuchen", "Abbrechen") {
				return nil, nil, errCanceled
			}
		}
	}
}

// askToken führt durch Webseite und Token-Eingabe. useFile: Das Mitglied hat
// doch eine Gruppendatei.
func askToken(inv *group.Invite, first bool) (token string, useFile bool, err error) {
	text := "Willkommen bei wanderwelt!\n\nSo kommst du in die Gruppe:\n" +
		"1. Lege auf der Webseite ein Konto an (wenn du noch keins hast).\n" +
		"2. Erstelle dort einen Token.\n" +
		"3. Füge den Token hier ein."
	if !first {
		text = "Wie geht es weiter?"
	}
	for {
		c, ok := tokenMenu(text)
		if !ok {
			return "", false, errCanceled
		}
		switch c {
		case choiceAccount:
			openPage(inv.AccountURL())
		case choiceToken:
			openPage(inv.CreateTokenURL())
		case choiceFile:
			return "", true, nil
		case choicePaste:
			prompt := "Füge deinen Token ein (Strg+V). Er wird nicht angezeigt."
			for {
				s, ok := tokenEntry(prompt)
				if !ok {
					break // zurück ins Menü
				}
				if err := group.CheckToken(s); err != nil {
					prompt = err.Error() + "\n\nFüge deinen Token ein (Strg+V). Er wird nicht angezeigt."
					continue
				}
				return strings.TrimSpace(s), false, nil
			}
		}
		text = "Wie geht es weiter?"
	}
}

func openPage(url string) {
	if err := openURL(url); err != nil {
		log.Printf("Webseite öffnen: %v", err)
		showInfo("Die Webseite ließ sich nicht öffnen. Öffne diese Adresse selbst im Browser:\n\n" + url)
	}
}

// awaitJoin stellt (falls noch nötig) die Beitrittsanfrage und wartet, bis
// ein Admin sie annimmt; danach liefert es das Mitglied. Eine schon
// gestellte Anfrage (Neustart beim Warten) wird nicht doppelt gestellt.
func awaitJoin(ctx context.Context, g *group.Group) (*api.Me, error) {
	c := client.New(g)
	var state string
	waitPrepared := false
	for {
		st, err := retry401(ctx, func() (*api.JoinStatus, error) { return c.JoinStatus(ctx) })
		if err == nil {
			state = st.State
			break
		}
		if errors.Is(err, errTokenInvalid) {
			return nil, err
		}
		if noJoinEndpoint(err) {
			return nil, errNoJoin
		}
		if errors.Is(err, context.Canceled) {
			return nil, errCanceled
		}
		log.Printf("Beitritt: Stand: %v", err)
		if !confirm(explain(err)+"\n\nNoch einmal versuchen?", "Erneut versuchen", "Abbrechen") {
			return nil, errCanceled
		}
	}
	for {
		switch state {
		case api.JoinAccepted:
			// Der Koordinator kennt die Überlagerung unter Umständen einen
			// Moment später: ein paar Wiederholversuche.
			var me *api.Me
			var err error
			for i := 0; i < memberTries; i++ {
				if me, err = memberCheck(ctx, g); err == nil || errors.Is(err, errTokenInvalid) || ctx.Err() != nil {
					break
				}
				select {
				case <-time.After(memberRetryDelay):
				case <-ctx.Done():
				}
			}
			if err != nil {
				if errors.Is(err, errTokenInvalid) {
					return nil, err
				}
				return nil, fmt.Errorf("Mitgliedschaft prüfen: %w", err)
			}
			log.Printf("Beitritt angenommen")
			showInfo("Du bist dabei! Die Einrichtung geht jetzt weiter.")
			return me, nil
		case api.JoinOpen:
			if !waitPrepared {
				// Vor dem Warten: Autostart und Verknüpfung, damit der nächste
				// Windows-Start die Einrichtung fortsetzt.
				waitPrepared = true
				prepareWait()
			}
			var err error
			if state, err = waitForJoin(ctx, c); err != nil {
				return nil, err
			}
		case api.JoinDeclined:
			if !confirm("Ein Admin hat deine Beitrittsanfrage abgelehnt. Frag ihn nach dem Grund.\n\n"+
				"Du kannst frühestens 24 Stunden nach der Ablehnung erneut anfragen. Jetzt erneut anfragen?",
				"Erneut anfragen", "Beenden") {
				return nil, errCanceled
			}
			state = api.JoinNone
		default: // keine
			st, err := requestJoin(ctx, c)
			if err != nil {
				return nil, err
			}
			state = st.State
		}
	}
}

// requestJoin fragt den Minecraft-Namen und stellt die Beitrittsanfrage.
// Der erkannte Name aus dem Minecraft-Launcher wird bestätigt (wie bei der
// Namensfrage der Mitglieder); sonst wird er eingegeben.
func requestJoin(ctx context.Context, c *client.Client) (*api.JoinStatus, error) {
	prompt := "Dein Minecraft-Java-Name (damit der Admin weiß, wer du bist, und du auf die Whitelist kommst):"
	name := ""
	if n, _, ok := detectMinecraftName(deps.MinecraftDir()); ok && mcNameRE.MatchString(n) &&
		nameConfirm(fmt.Sprintf("Du spielst Minecraft als „%s“ – stimmt das?", n)) {
		name = n
	}
	for i := 0; i < nameEntryTries; i++ {
		if name == "" {
			s, ok := nameEntry(prompt)
			if !ok {
				return nil, errCanceled
			}
			if !mcNameRE.MatchString(s) {
				prompt = "Der Name besteht aus 3 bis 16 Buchstaben, Ziffern oder Unterstrichen.\n\nDein Minecraft-Java-Name:"
				continue
			}
			name = s
		}
		st, err := c.Join(ctx, name)
		if err == nil {
			log.Printf("Beitritt angefragt (Minecraft-Name %s)", name)
			return st, nil
		}
		taken := name
		name = ""
		se, ok := errors.AsType[*client.StatusError](err)
		if !ok {
			if errors.Is(err, context.Canceled) {
				return nil, errCanceled
			}
			log.Printf("Beitritt: %v", err)
			showError("Die Beitrittsanfrage ging nicht durch:\n\n" + explain(err) + "\n\nStarte wanderwelt später noch einmal.")
			return nil, errCanceled
		}
		switch se.StatusCode {
		case 400:
			prompt = se.Message + "\n\nDein Minecraft-Java-Name:"
		case 409:
			// Name vergeben oder schon angefragt – oder die eigene Anfrage
			// läuft/ist angenommen (anderes Gerät, Neustart).
			if cur, err := c.JoinStatus(ctx); err == nil && (cur.State == api.JoinOpen || cur.State == api.JoinAccepted) {
				return cur, nil
			}
			prompt = fmt.Sprintf("Der Name „%s“ gehört schon einem Mitglied oder ist schon angefragt.\n\nBitte gib einen anderen Namen ein, oder frag den Admin.\n\nDein Minecraft-Java-Name:", taken)
		case 429:
			if cur, err := c.JoinStatus(ctx); err == nil && cur.State == api.JoinOpen {
				return cur, nil
			}
			showError("Die Beitrittsanfrage ist gerade nicht möglich:\n\n" + se.Message)
			return nil, errCanceled
		case 403, 404:
			return nil, errNoJoin
		default:
			log.Printf("Beitritt: %v", err)
			showError("Die Beitrittsanfrage ging nicht durch:\n\n" + explain(err) + "\n\nStarte wanderwelt später noch einmal.")
			return nil, errCanceled
		}
	}
	showError("Der Minecraft-Name ließ sich nicht eintragen. Frag den Admin und starte wanderwelt danach noch einmal.")
	return nil, errCanceled
}

// waitForJoin wartet im Wartefenster, bis die Anfrage nicht mehr offen ist,
// und fragt dazu alle joinPollInterval den Stand. „Später fortsetzen“ beendet
// die Einrichtung (errCanceled); der nächste Start macht beim Warten weiter.
func waitForJoin(ctx context.Context, c *client.Client) (string, error) {
	dlg, err := openJoinWait()
	var gone <-chan struct{}
	if err != nil {
		log.Printf("Wartefenster: %v", err)
		showError("Das Wartefenster ließ sich nicht öffnen. Starte wanderwelt später noch einmal.")
		return "", errCanceled
	}
	defer dlg.Close()
	waitText := "Deine Beitrittsanfrage ist raus. Ein Admin muss sie noch annehmen – das kann dauern.\n\n" +
		"Du musst nichts tun: wanderwelt fragt regelmäßig nach. Du kannst das Fenster auch mit „Später fortsetzen“ " +
		"schließen: wanderwelt startet mit Windows wieder und meldet sich, sobald ein Admin dich angenommen hat."
	dlg.Text(waitText)
	gone = dlg.Done()
	t := time.NewTicker(joinPollInterval)
	defer t.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return "", errCanceled
		case <-gone:
			log.Printf("Beitritt: Warten vertagt")
			showInfo("wanderwelt startet mit Windows wieder und meldet sich, sobald ein Admin dich angenommen hat.")
			return "", errLater
		case <-t.C:
			st, err := retry401(ctx, func() (*api.JoinStatus, error) { return c.JoinStatus(ctx) })
			if errors.Is(err, errTokenInvalid) {
				return "", err
			}
			if err != nil {
				log.Printf("Beitritt: Stand: %v", err)
				if fails++; fails == waitFailLimit {
					dlg.Text("Die Abfrage beim Koordinator klappt gerade nicht (" + explain(err) + ").\n\n" +
						"wanderwelt versucht es weiter. Bleibt es so, frag den Admin.\n\n" + waitText)
				}
				continue
			}
			if fails >= waitFailLimit {
				dlg.Text(waitText)
			}
			fails = 0
			if st.State != api.JoinOpen {
				return st.State, nil
			}
		}
	}
}
