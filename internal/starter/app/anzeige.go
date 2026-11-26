package app

import (
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/discord"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// discordTakt: So oft wird der Zustand für die Discord-Anzeige geprüft
// (lokal und billig; gesendet wird nur bei Änderung). Die Spielerzahl selbst
// kommt vom Status-Abruf alle statusInterval.
const discordTakt = 5 * time.Second

// discordActivity: Soll Discord etwas anzeigen? Nur beim Hosten und
// Mitspielen: „mit N Freunden“ (andere Spieler, ohne den eigenen Namen, keine
// Namen selbst). ok false: Aktivität löschen.
func discordActivity(ph phase, status *api.Status, me *api.Me, since time.Time, secret, partyID string) (discord.Activity, bool) {
	if ph != phHosting && ph != phJoined {
		return discord.Activity{}, false
	}
	others := 0
	if status != nil {
		own := ""
		if me != nil {
			own = strings.TrimSpace(me.MinecraftName)
		}
		others = len(fensterapi.AndereSpieler(status.Players, own))
	}
	act := discord.Activity{State: discord.FriendsText(others), Start: since}
	if secret != "" {
		// „N von M“: N = Spieler online (mit mir), M = Mitglieder laut Status
		// (mindestens N+1, damit Neue über Discord auch dann beitreten können, wenn alle online sind). Fehlt die Zahl (älterer Koordinator, 0): N+1, damit
		// „Beitreten“ nicht als voll gesperrt wird.
		n := others + 1
		m := n + 1
		if status != nil && status.Members > 0 {
			m = max(status.Members, n+1)
		}
		act.PartyID, act.PartySize, act.PartyMax = partyID, n, m
		act.JoinSecret = secret
	}
	return act, true
}

// discordLoop hält die Discord-Anzeige auf dem Stand, bis das Programm endet.
// Stört nie den Starter: Läuft Discord nicht oder streikt es, passiert
// nichts (Fehler nur im Log).
func (a *App) discordLoop() {
	t := time.NewTicker(discordTakt)
	defer t.Stop()
	var since time.Time
	for {
		since = a.discordTick(since)
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// discordTick setzt oder löscht die Anzeige; since ist der Beginn der
// laufenden Spielzeit (Null: gerade nicht im Spiel), der neue Wert kommt
// zurück.
func (a *App) discordTick(since time.Time) time.Time {
	if a.presence == nil {
		return since
	}
	a.mu.Lock()
	ph, status, me, quitting := a.phase, a.status, a.me, a.quitting
	a.mu.Unlock()
	if quitting {
		return since
	}
	if ph == phHosting || ph == phJoined {
		if since.IsZero() {
			since = time.Now()
		}
	} else {
		since = time.Time{}
	}
	var secret, partyID string
	if ph == phHosting || ph == phJoined {
		secret, partyID = a.joinSecret()
	}
	if act, ok := discordActivity(ph, status, me, since, secret, partyID); ok {
		a.presence.Set(act)
	} else {
		a.presence.Clear()
	}
	return since
}

// discordClose löscht die Anzeige beim Beenden.
func (a *App) discordClose() {
	if a.presence != nil {
		a.presence.Close()
	}
}
