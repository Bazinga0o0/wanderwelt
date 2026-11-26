package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/discord"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// „Beitreten“ in Discord. Der Absender legt in die Aktivität ein Geheimnis
// mit den öffentlichen Gruppen-Adressen (nie ein Token). Beim Empfänger:
//   - mit Gruppe: Fenster öffnen (nicht sofort spielen); die vorhandene Gruppe
//     wird nie ersetzt;
//   - ohne Gruppe (erste Einrichtung): Adressen prüfen, mit sichtbaren Adressen
//     bestätigen lassen, dann läuft die Einrichtung wie beim Gruppen-Build
//     (Konto, Token, Beitrittsanfrage). Erst danach entsteht gruppe.yaml.

// Im Test austauschbar.
var (
	openWindowOnJoin = openWindow

	// askJoinChoice: Über Discord beitreten (true) oder Gruppendatei (false).
	askJoinChoice = func() bool {
		return confirm("Willkommen bei wanderwelt!\n\nWie willst du der Gruppe beitreten?\n\n"+
			"• Über Discord: Ein Freund spielt gerade wanderwelt – klicke in Discord bei ihm auf „Beitreten“.\n"+
			"• Mit einer Gruppendatei vom Admin.", "Über Discord beitreten", "Gruppendatei wählen")
	}
	// showJoinWait zeigt das Warten auf den Klick, bis ctx endet oder das
	// Mitglied doch die Gruppendatei will.
	showJoinWait = func(ctx context.Context) {
		zenity.Info("Warte auf den Beitritt über Discord …\n\nKlicke in Discord beim Freund auf „Beitreten“. "+
			"Dieses Fenster schließt sich dann von selbst.",
			zenity.Title(notifyTitle), windowIcon(), zenity.Context(ctx), zenity.OKLabel("Doch mit Gruppendatei"))
	}
	// confirmJoin zeigt die Adressen und fragt ausdrücklich.
	confirmJoin = func(inv *group.Invite) bool {
		return confirm("Gruppe beitreten?\n\nKoordinator: "+inv.Coordinator+"\nTunnel-Server: "+inv.TunnelServer+
			"\nWebseite: "+inv.WebURL+"\n\nBestätige nur, wenn du diese Adressen kennst und der Gruppe vertraust: "+
			"Du legst danach ein Konto an und fügst einen Token ein – bei falschen Adressen könnte jemand diesen Token abgreifen.",
			"Beitreten", "Abbrechen")
	}
)

// onDiscordJoin: Jemand hat bei einem Freund auf „Beitreten“ geklickt. Das
// Geheimnis ist unbekannte Eingabe und steht nie im Log.
func (a *App) onDiscordJoin(secret string) {
	a.mu.Lock()
	haveGroup := a.g != nil
	a.mu.Unlock()
	if haveGroup {
		log.Printf("Discord: Beitreten geklickt – öffne das Fenster")
		go openWindowOnJoin()
		return
	}
	log.Printf("Discord: Beitritts-Geheimnis empfangen (noch keine Gruppe)")
	select {
	case a.joinCh <- secret:
	default: // schon eins da
	}
}

// needsJoinOffer: Nur die erste Einrichtung ohne Gruppendatei und ohne
// eingebettete Adressen bietet den Beitritt über Discord an.
func (a *App) needsJoinOffer() bool {
	if a.invite != nil || a.presence == nil || !discord.Enabled(discord.ClientID) {
		return false
	}
	_, err := os.Stat(group.GroupPath())
	return errors.Is(err, os.ErrNotExist)
}

// discordJoinOffer holt die Adressen für die Einrichtung ohne Gruppe aus
// einem Discord-Beitritt; nil: Gruppendatei verwenden (abgelehnt, ungültig,
// nicht gewollt).
func (a *App) discordJoinOffer(ctx context.Context) *group.Invite {
	var secret string
	select {
	case secret = <-a.joinCh: // Discord hat uns für den Klick gestartet
	default:
		if !askJoinChoice() {
			return nil
		}
		if secret = a.waitForJoin(ctx); secret == "" {
			return nil
		}
	}
	inv, err := group.DecodeJoin(secret)
	if err != nil {
		log.Printf("Discord: Beitritts-Geheimnis abgelehnt: %v", err)
		showError("Der Beitritts-Link aus Discord ist ungültig. Bitte wähle die Gruppendatei vom Admin.")
		return nil
	}
	if !confirmJoin(inv) {
		log.Printf("Discord: Beitritt abgelehnt")
		return nil
	}
	log.Printf("Discord: Beitritt bestätigt (Koordinator %s)", inv.Coordinator)
	return inv
}

// waitForJoin wartet auf das Geheimnis; "" bei Abbruch oder wenn das
// Mitglied doch die Gruppendatei wählt.
func (a *App) waitForJoin(ctx context.Context) string {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closed := make(chan struct{})
	go func() { showJoinWait(wctx); close(closed) }()
	select {
	case s := <-a.joinCh:
		cancel()
		<-closed
		return s
	case <-closed:
		return ""
	case <-ctx.Done():
		return ""
	}
}

// joinSecret: das Beitritts-Geheimnis dieser Gruppe samt Party-Kennung; "":
// nicht möglich (kein Tunnel-Server, Adressen nicht streng genug).
func (a *App) joinSecret() (secret, partyID string) {
	a.mu.Lock()
	g, inv := a.g, a.invite
	a.mu.Unlock()
	if g == nil {
		return "", ""
	}
	i := group.Invite{Coordinator: g.Coordinator, TunnelServer: g.TunnelServer}
	if inv != nil {
		i.WebURL, i.TokenURL = inv.WebURL, inv.TokenURL
	}
	s, err := group.EncodeJoin(&i)
	if err != nil {
		if !a.joinLogged {
			a.joinLogged = true
			log.Printf("Discord: kein Beitritts-Geheimnis: %v", err)
		}
		return "", ""
	}
	sum := sha256.Sum256([]byte(g.Coordinator))
	return s, "wanderwelt-" + hex.EncodeToString(sum[:4])
}

// registerDiscordProtocol trägt discord-<ClientID>:// einmalig in HKCU ein
// (ohne Adminrechte), damit Discord den Starter für „Beitreten“ starten
// kann. Nur die installierte Kopie trägt sich ein.
func (a *App) registerDiscordProtocol() {
	if !discord.Enabled(discord.ClientID) {
		return
	}
	self, err := os.Executable()
	if err != nil || !samePath(self, installedExe()) {
		return
	}
	changed, err := winsys.RegisterURLProtocol("discord-"+discord.ClientID, self, "wanderwelt")
	switch {
	case errors.Is(err, winsys.ErrUnsupported):
	case err != nil:
		log.Printf("Discord: %v", err)
	case changed:
		log.Printf("Discord: URL-Protokoll eingetragen")
	}
}
