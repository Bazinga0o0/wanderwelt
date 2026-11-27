package app

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
)

// maxEinzelHinweise: So viele neue Anfragen auf einmal bekommen je einen
// Hinweis; bei mehr gibt es einen gemeinsamen.
const maxEinzelHinweise = 3

// beitrittsHinweis meldet einem Admin neue Beitrittsanfragen, höchstens
// einmal je Anfrage (gemerkt nach E-Mail, solange sie offen ist). Der Hinweis
// nennt nur den Minecraft-Namen, nie die E-Mail-Adresse (Tray und Log).
func (a *App) beitrittsHinweis(ctx context.Context, c *client.Client, me *api.Me) {
	a.joinMu.Lock() // Abruf und Merken in einem Schritt (überlappende pollStatus)
	defer a.joinMu.Unlock()
	if me == nil || !me.Admin || me.PendingJoins <= 0 {
		a.mu.Lock()
		a.joinGemeldet = nil
		a.mu.Unlock()
		return
	}
	liste, err := c.ListJoinRequests(ctx)
	if err != nil {
		log.Printf("Beitrittsanfragen: %s", logFehler(err))
		return
	}
	var neu []string
	a.mu.Lock()
	jetzt := make(map[string]bool, len(liste))
	for _, r := range liste {
		k := strings.ToLower(r.Email)
		jetzt[k] = true
		if !a.joinGemeldet[k] {
			neu = append(neu, cmp.Or(r.MinecraftName, "einem neuen Spieler"))
		}
	}
	a.joinGemeldet = jetzt
	a.mu.Unlock()
	if !a.benachAn(ereignis.Beitrittsanfrage) {
		return // vom Mitglied abgeschaltet; die Anfragen gelten als gemeldet
	}
	switch {
	case len(neu) == 0:
	case len(neu) > maxEinzelHinweise:
		go notify(fmt.Sprintf("%d neue Beitrittsanfragen", len(neu)))
	default:
		for _, n := range neu {
			go notify("Neue Beitrittsanfrage von " + n)
		}
	}
}
