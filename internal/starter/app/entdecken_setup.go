package app

// Einrichtung ohne Gruppe: neben dem Gruppencode gibt es „Entdecken
// (Tunnel-Server und Token)“. Das Mitglied gibt die Adresse des Tunnel-Servers
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
	menuEntdecken = "Entdecken (Tunnel-Server und Token)"
	menuDatei     = "Ich habe eine Gruppendatei vom Admin"
)

var serverRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)

// Dialoge; im Test austauschbar.
var (
	// setupMenu: Wahl zwischen Gruppencode, Entdecken und Gruppendatei; ok
	// false: abgebrochen.
	setupMenu = func() (choice string, ok bool) {
		s, err := zenity.List("Willkommen bei wanderwelt!\n\nWie kommst du in eine Gruppe?",
			[]string{menuCode, menuEntdecken, menuDatei}, zenity.Title("wanderwelt einrichten"), windowIcon(),
			zenity.OKLabel("Weiter"), zenity.CancelLabel("Abbrechen"), zenity.DisallowEmpty())
		return s, err == nil
	}
	// askTunnelServer fragt die Adresse des Tunnel-Servers (host oder
	// host:port), vorbelegt mit def.
	askTunnelServer = func(def, hint string) (string, bool) {
		text := "Adresse des Tunnel-Servers (zum Beispiel tunnel.example.org:7443):"
		if hint != "" {
			text = hint + "\n\n" + text
		}
		s, err := zenity.Entry(text, zenity.Title("wanderwelt einrichten"), windowIcon(), zenity.EntryText(def),
			zenity.OKLabel("Weiter"), zenity.CancelLabel("Abbrechen"))
		return s, err == nil
	}
	// pickGroup lässt eine der Gruppen wählen (Index in items).
	pickGroup = func(items []string) (int, bool) {
		s, err := zenity.List("Welcher Gruppe möchtest du beitreten?", items, zenity.Title("wanderwelt einrichten"), windowIcon(),
			zenity.OKLabel("Beitreten"), zenity.CancelLabel("Abbrechen"), zenity.DisallowEmpty())
		if err != nil {
			return 0, false
		}
		for i, it := range items {
			if it == s {
				return i, true
			}
		}
		return 0, false
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
		prompt := "Füge deinen Token ein (Strg+V). Er wird nicht angezeigt."
		var token string
		for {
			s, ok := tokenEntry(prompt)
			if !ok {
				return nil, "", errCanceled
			}
			if err := group.CheckToken(s); err != nil {
				prompt = err.Error() + "\n\nFüge deinen Token ein (Strg+V). Er wird nicht angezeigt."
				continue
			}
			token = strings.TrimSpace(s)
			break
		}
		web := webFuerServer(server)
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
		inv := &group.Invite{Coordinator: ds[i].URL, TunnelServer: server, WebURL: web}
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
			line := ds[i].Title
			c := client.New(&group.Group{Coordinator: ds[i].URL, Token: token})
			if gi, err := c.Info(ctx); err == nil {
				if gi.Title != "" {
					line = gi.Title
				}
				line += fmt.Sprintf(" – %d Mitglieder", gi.Members)
				switch gi.You {
				case api.YouMember:
					line += " (du bist schon Mitglied)"
				case api.YouRequested:
					line += " (angefragt)"
				}
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
