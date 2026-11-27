package app

// Einrichtung ohne Gruppe: neben dem Gruppencode gibt es „Entdecken
// (Server und Token)“. Das Mitglied gibt die Adresse des Tunnel-Servers
// und seinen Token ein (verdeckt), wanderwelt fragt das Verzeichnis ab und
// lässt eine Gruppe wählen; danach läuft die Einrichtung wie beim
// Gruppen-Build (Mitgliedschaft prüfen, sonst Beitrittsanfrage und Warten).
// Der Token steht nie in Dialogen oder im Log.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/verzeichnis"
)

// errEntdeckenWahl: Im Auswahlmenü der Einrichtung wurde „Entdecken“ gewählt.
var errEntdeckenWahl = errors.New("Entdecken gewählt")

const (
	menuCode      = "Gruppencode einfügen"
	menuEntdecken = "Entdecken (Server und Token)"
	menuDatei     = "Ich habe eine Gruppendatei"

	// msgServerErklaerung erscheint einmal vor dem Eingabefenster für den Tunnel-Server.
	msgServerErklaerung = "Gib die Adresse des Tunnel-Servers ein, zum Beispiel tunnel.example.org oder " +
		"tunnel.example.org:7443 (ohne https://).\n\n" +
		"Im nächsten Schritt kannst du dort ein Konto anlegen, falls du noch keins hast."
)

var serverRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)

// Dialoge; im Test austauschbar.
var (
	// setupMenu: Wahl zwischen Gruppencode, Entdecken und Gruppendatei; ok
	// false: abgebrochen.
	setupMenu = func() (choice string, ok bool) {
		items := []string{menuCode, menuEntdecken, menuDatei}
		i, err := zeigeListe(labelGruppeWahl, items, zenity.Title(titelWillkommen), windowIcon(),
			zenity.OKLabel("Weiter"), zenity.CancelLabel("Abbrechen"), zenity.DisallowEmpty())
		if err != nil {
			return "", false
		}
		return items[i], true
	}
	// askTunnelServer fragt die Adresse des Tunnel-Servers (host oder
	// host:port), vorbelegt mit def. Ein Hinweis zur letzten Eingabe kommt als
	// Meldungsfenster vor das Eingabefenster; ohne Hinweis (der erste Aufruf)
	// erklärt ein Meldungsfenster einmal, worum es geht.
	askTunnelServer = func(def, hint string) (string, bool) {
		meldung := hint
		if hint == "" {
			meldung = msgServerErklaerung
		}
		s, err := eingabeMitMeldung(meldung, labelServer, zenity.Title(titelEinrichten), windowIcon(), zenity.EntryText(def),
			zenity.OKLabel("Weiter"), zenity.CancelLabel("Abbrechen"))
		return s, err == nil
	}
	// pickGroup lässt eine der Gruppen wählen (Index in items).
	pickGroup = func(items []string) (int, bool) {
		i, err := zeigeListe(labelGruppeBeitr, items, zenity.Title(titelEinrichten), windowIcon(),
			zenity.OKLabel("Beitreten"), zenity.CancelLabel("Abbrechen"), zenity.DisallowEmpty())
		return i, err == nil
	}
	// webFuerServer: Adresse der tunnel-Webseite (dort liegt das Verzeichnis)
	// zu einem Tunnel-Server host[:port]: https://<host>.
	webFuerServer = func(server string) string {
		h := server
		if hh, _, err := net.SplitHostPort(server); err == nil {
			h = hh
		}
		return "https://" + h
	}
)

// entdeckenSetup führt durch Server, Token und Gruppenwahl. Ergebnis: die
// Adressen der gewählten Gruppe und der Token; errCanceled bei Abbruch.
func (a *App) entdeckenSetup(ctx context.Context) (*group.Invite, string, error) {
	def, hint := "", ""
	tokenGezeigt := false // die Anleitung der Token-Schritte kommt nur einmal
	if a.invite != nil {
		def = a.invite.TunnelServer // der Standard aus der eingebetteten Gruppe
	}
	for {
		server, ok := askTunnelServer(def, hint)
		if !ok {
			return nil, "", errCanceled
		}
		server = strings.TrimSpace(server)
		def = server
		if !serverRE.MatchString(server) || len(server) > 253 || !portOK(server) {
			hint = "Das ist keine Server-Adresse. Erwartet: Name oder Name:Port, ohne https:// und ohne Pfad."
			continue
		}
		hint = ""
		web := webFuerServer(server)
		// Wer noch kein Konto hat (zum Beispiel ein Freund mit dem öffentlichen
		// Download), kommt hier mit einem Klick auf die Webseite des
		// Tunnel-Servers: Konto anlegen, Token erstellen, Token einfügen.
		token, useFile, err := askToken(&group.Invite{TunnelServer: server, WebURL: web}, !tokenGezeigt)
		tokenGezeigt = true
		if err != nil {
			return nil, "", errCanceled
		}
		if useFile {
			hint = "Für eine Gruppendatei wähle im Startmenü „" + menuDatei + "“."
			continue
		}
		ds, err := verzeichnis.Abfragen(ctx, entdeckenHTTP, web, token)
		var busy *verzeichnis.BusyError
		switch {
		case err == nil && len(ds) == 0:
			hint = "Dieser Tunnel-Server listet keine Gruppen auf. Frag nach einem Gruppencode oder einer Gruppendatei."
			continue
		case err == nil:
		case errors.Is(err, verzeichnis.ErrToken):
			log.Printf("Einrichtung: Entdecken: Token wird abgelehnt")
			hint = msgTokenInvalid
			continue
		case errors.Is(err, verzeichnis.ErrKeine):
			hint = "Dieser Tunnel-Server listet keine Gruppen auf. Frag nach einem Gruppencode oder einer Gruppendatei."
			continue
		case errors.As(err, &busy):
			hint = "Das Verzeichnis ist gerade überlastet. Versuche es in einer Minute noch einmal."
			continue
		case errors.Is(err, context.Canceled):
			return nil, "", errCanceled
		default:
			log.Printf("Einrichtung: Entdecken: %v", err)
			hint = "Der Tunnel-Server ist nicht erreichbar oder antwortet nicht wie erwartet."
			continue
		}
		items := a.setupItems(ctx, ds, token)
		i, ok := pickGroup(items)
		if !ok {
			return nil, "", errCanceled
		}
		inv := &group.Invite{Coordinator: ds[i].URL, TunnelServer: group.TunnelAddr(server), WebURL: web}
		log.Printf("Einrichtung: Entdecken: Gruppe %s gewählt", hostVon(ds[i].URL))
		return inv, token, nil
	}
}

func portOK(server string) bool {
	if _, p, err := net.SplitHostPort(server); err == nil {
		n, err := strconv.Atoi(p)
		return err == nil && n > 0 && n < 65536
	}
	return true
}

// setupItems: eine Zeile je Gruppe („Titel – 7 Mitglieder“, mit „(angefragt)“
// oder „(Mitglied)“); Titel und Zahl kommen aus /v1/info, sonst aus dem
// Verzeichnis. Die Zeilen sind eindeutig (die Auswahl geht über den Text).
func (a *App) setupItems(ctx context.Context, ds []verzeichnis.Dienst, token string) []string {
	items := make([]string, len(ds))
	var wg sync.WaitGroup
	sem := make(chan struct{}, entdeckenParallel)
	for i := range ds {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			line := kuerze(ds[i].Title, maxEintrag)
			c := client.New(&group.Group{Coordinator: ds[i].URL, Token: token})
			if gi, err := c.Info(ctx); err == nil {
				title := ds[i].Title
				if gi.Title != "" {
					title = gi.Title
				}
				// Der Titel wird gekürzt, die Angaben dahinter bleiben (ein
				// Listeneintrag hat nur maxEintrag Zeichen Platz).
				status := ""
				switch gi.You {
				case api.YouMember:
					status = " (Mitglied)"
				case api.YouRequested:
					status = " (angefragt)"
				}
				suffix := fmt.Sprintf(" – %d Mitglieder%s", gi.Members, status)
				if utf8.RuneCountInString(title+suffix) > maxEintrag {
					suffix = fmt.Sprintf(" – %d Mitgl.%s", gi.Members, status)
				}
				line = zeileMitName("", title, suffix, maxEintrag)
			}
			items[i] = line
		}()
	}
	wg.Wait()
	seen := map[string]int{}
	for i, it := range items {
		seen[it]++
		if seen[it] > 1 {
			items[i] = fmt.Sprintf("%s [%d]", it, seen[it])
		}
	}
	return items
}
