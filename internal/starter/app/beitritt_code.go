package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// Beitritt mit Gruppencode. Der Code (`w1.…`, group.EncodeJoin) enthält nur
// die öffentlichen Gruppen-Adressen, nie einen Token. Er steht nicht im
// Discord-Link und nicht auf der Beitreten-Seite: Ein Mitglied gibt ihn im
// Fenster („Gruppencode kopieren“) weiter, der Neue fügt ihn bei der
// ersten Einrichtung ein.
//   - Die Einrichtung ohne Gruppendatei und ohne eingebettete Adressen fragt
//     nach dem Gruppencode (oder einer Gruppendatei). Der Code wird streng
//     geprüft, seine Adressen werden zur Bestätigung gezeigt; erst danach
//     läuft die Einrichtung wie beim Gruppen-Build (Konto, Token,
//     Beitrittsanfrage), und gruppe.yaml entsteht ohne Token.
//   - Der Link wanderwelt://beitreten (von der Beitreten-Seite) trägt keine
//     Daten und wird nicht ausgewertet: Er öffnet nur das Fenster. Eine
//     vorhandene Gruppe wird nie ersetzt.

const (
	// JoinScheme ist das URL-Protokoll des Starters (wanderwelt://).
	JoinScheme = "wanderwelt"
	// JoinLinkArg ist das einzige Argument, das der Starter für den Link
	// weiterreicht; ein Argument hinter beitreten gibt es nicht.
	JoinLinkArg = JoinScheme + "://beitreten"

	// maxCodeInput: Eingaben darüber sind kein Gruppencode (Einfügen von Müll).
	maxCodeInput = 1000
	// joinLinkGap: Mindestabstand, in dem ein Link das Fenster öffnet (jede
	// Webseite kann wanderwelt:// auslösen).
	joinLinkGap = 5 * time.Second
	// zeigeEntdeckenDauer: So lange meldet der Zustand „Reiter Entdecken zeigen“.
	zeigeEntdeckenDauer = 15 * time.Second
)

// IsJoinLinkArg: Ist das Kommandozeilenargument ein wanderwelt://-Link? Was
// dahinter steht, wird nie ausgewertet.
func IsJoinLinkArg(arg string) bool {
	return len(arg) >= len(JoinScheme)+1 && strings.EqualFold(arg[:len(JoinScheme)+1], JoinScheme+":")
}

// msgGruppencodeErklaerung erscheint vor dem Eingabefenster für den Gruppencode.
const msgGruppencodeErklaerung = "Füge den Gruppencode ein, den du von einem Mitspieler oder dem Admin bekommen hast.\n\n" +
	"Hast du stattdessen eine Gruppendatei vom Admin, wähle im Eingabefenster „Gruppendatei wählen“."

// Im Test austauschbar.
var (
	openWindowOnJoin = openWindow

	// askGroupCode fragt den Gruppencode. Fehler: zenity.ErrExtraButton
	// („Gruppendatei wählen“), zenity.ErrCanceled (Abbruch).
	askGroupCode = func(hint string) (string, error) {
		if hint == "" {
			// Erst die Wahl: Gruppencode, Entdecken oder Gruppendatei.
			switch c, ok := setupMenu(); {
			case !ok:
				return "", zenity.ErrCanceled
			case c == menuEntdecken:
				return "", errEntdeckenWahl
			case c == menuDatei:
				return "", zenity.ErrExtraButton
			}
		}
		// Die Erklärung kommt einmal als Meldungsfenster (nach der Wahl im
		// Menü), ein Hinweis zur letzten Eingabe an ihrer Stelle; das
		// Eingabefenster hat nur eine kurze Zeile.
		meldung := hint
		if hint == "" {
			meldung = msgGruppencodeErklaerung
		}
		return eingabeMitMeldung(meldung, labelGruppencode, zenity.Title(titelWillkommen), windowIcon(),
			zenity.OKLabel("Weiter"), zenity.CancelLabel("Abbrechen"), zenity.ExtraButton("Gruppendatei wählen"))
	}
	// confirmJoin zeigt die Adressen und fragt ausdrücklich.
	confirmJoin = func(inv *group.Invite) bool {
		return confirm("Gruppe beitreten?\n\nKoordinator: "+inv.Coordinator+"\nTunnel-Server: "+inv.TunnelServer+
			"\nWebseite: "+inv.WebURL+"\n\nBestätige nur, wenn du diese Adressen kennst und der Gruppe vertraust: "+
			"Du legst danach ein Konto an und fügst einen Token ein – bei falschen Adressen könnte jemand diesen Token abgreifen.",
			"Beitreten", "Abbrechen")
	}
)

// ParseGroupCode prüft einen eingefügten Gruppencode streng (Leerraum
// außen wird abgeschnitten) und liefert die Adressen ohne Token.
func ParseGroupCode(s string) (*group.Invite, error) {
	if len(s) > maxCodeInput {
		return nil, errors.New("zu lang für einen Gruppencode")
	}
	return group.DecodeJoin(strings.TrimSpace(s))
}

// needsGroupCodeOffer: Nur die erste Einrichtung ohne Gruppendatei und ohne
// eingebettete Adressen fragt nach dem Gruppencode.
func (a *App) needsGroupCodeOffer() bool {
	if a.invite != nil {
		return false
	}
	_, err := os.Stat(group.GroupPath())
	return errors.Is(err, os.ErrNotExist)
}

// groupCodeOffer fragt den Gruppencode und lässt die Adressen bestätigen.
// (nil, nil): Gruppendatei wählen; errCanceled: Abbruch. Der Code steht nie
// im Log.
func (a *App) groupCodeOffer() (*group.Invite, error) {
	hint := ""
	for {
		code, err := askGroupCode(hint)
		switch {
		case errors.Is(err, errEntdeckenWahl):
			inv, token, err := a.entdeckenSetup(a.ctx)
			if err != nil {
				return nil, err
			}
			a.presetToken = token
			return inv, nil
		case errors.Is(err, zenity.ErrExtraButton):
			return nil, nil
		case errors.Is(err, zenity.ErrCanceled):
			return nil, errCanceled
		case err != nil:
			log.Printf("Gruppencode: Eingabe nicht möglich: %v", err)
			return nil, nil
		}
		inv, err := ParseGroupCode(code)
		if err != nil {
			log.Printf("Gruppencode abgelehnt: %v", err)
			hint = "Der Gruppencode ist ungültig. Kopiere ihn noch einmal ganz (er beginnt mit w1.)."
			continue
		}
		if !confirmJoin(inv) {
			log.Printf("Gruppencode: Beitritt abgelehnt")
			hint = ""
			continue
		}
		log.Printf("Gruppencode: Beitritt bestätigt (Koordinator %s)", inv.Coordinator)
		return inv, nil
	}
}

// onJoinLink: wanderwelt://beitreten wurde geöffnet (Beitreten-Seite). Das
// Argument trägt nichts und wird nie ausgewertet. Mit Gruppe: nur das
// Fenster öffnen (höchstens alle joinLinkGap). Ohne Gruppe läuft die
// Einrichtung und fragt ohnehin nach dem Gruppencode.
func (a *App) onJoinLink() {
	a.mu.Lock()
	have := a.g != nil
	now := time.Now()
	tooSoon := !a.lastJoinLink.IsZero() && now.Sub(a.lastJoinLink) < joinLinkGap
	if have && !tooSoon {
		a.lastJoinLink = now
	}
	a.mu.Unlock()
	switch {
	case !have:
		log.Printf("Beitritts-Link: die Einrichtung läuft noch")
	case tooSoon:
		log.Printf("Beitritts-Link: zu schnell wiederholt, ignoriert")
	default:
		if len(group.List()) > 1 {
			// Mehrere Gruppen: Das Fenster springt zum Reiter „Entdecken“.
			log.Printf("Beitritts-Link: öffne das Fenster (Reiter Entdecken)")
			a.mu.Lock()
			a.zeigeEntdeckenBis = time.Now().Add(zeigeEntdeckenDauer)
			a.mu.Unlock()
		} else {
			log.Printf("Beitritts-Link: öffne das Fenster")
		}
		go openWindowOnJoin()
	}
}

// groupCode: der Gruppencode dieser Gruppe (nur öffentliche Adressen, nie ein
// Token); "": nicht möglich (keine Gruppe, kein Tunnel-Server, Adressen nicht
// streng genug).
func (a *App) groupCode() string {
	a.mu.Lock()
	g, inv := a.g, a.invite
	a.mu.Unlock()
	if g == nil {
		return ""
	}
	i := group.Invite{Coordinator: g.Coordinator, TunnelServer: g.TunnelServer}
	if inv != nil {
		i.WebURL, i.TokenURL = inv.WebURL, inv.TokenURL
	}
	s, err := group.EncodeJoin(&i)
	if err != nil {
		return ""
	}
	return s
}

// partyID: Party-Kennung der Discord-Anzeige (nur Anzeige „N von M“); "":
// keine Gruppe.
func (a *App) partyID() string {
	a.mu.Lock()
	g := a.g
	a.mu.Unlock()
	if g == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(g.Coordinator))
	return "wanderwelt-" + hex.EncodeToString(sum[:4])
}

// registerJoinProtocol trägt wanderwelt:// einmalig in HKCU ein (ohne
// Adminrechte), damit die Beitreten-Seite den Starter öffnen kann. Nur die
// installierte Kopie trägt sich ein. (Das frühere discord-<ClientID>://
// wird nicht mehr gebraucht; ein alter Eintrag bleibt wirkungslos liegen.)
func (a *App) registerJoinProtocol() {
	self, err := os.Executable()
	if err != nil || !samePath(self, installedExe()) {
		return
	}
	changed, err := winsys.RegisterURLProtocol(JoinScheme, self, "wanderwelt")
	switch {
	case errors.Is(err, winsys.ErrUnsupported):
	case err != nil:
		log.Printf("Beitritts-Link: %v", err)
	case changed:
		log.Printf("Beitritts-Link: URL-Protokoll eingetragen")
	}
}
