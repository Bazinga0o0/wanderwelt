package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
)

const testWebhook = "https://discord.com/api/webhooks/123456789012345678/GEHEIMES-TOKEN-abcdefghijklmnopqrstuvwxyz0123456789"

func TestDiscordWebhookStandardAus(t *testing.T) {
	c, err := loadGruppe(t, "")
	if err != nil || c.DiscordWebhook.Gesetzt() || c.DiscordArten != nil {
		t.Fatalf("Standard: %+v %v", c, err)
	}
}

func TestDiscordWebhookUndEreignisse(t *testing.T) {
	c, err := loadGruppe(t, "discord_webhook: "+testWebhook+"\ndiscord_ereignisse: {spieler: false, niemand_hostet: true}\n")
	if err != nil {
		t.Fatal(err)
	}
	if !c.DiscordWebhook.Gesetzt() || c.DiscordWebhook.Adresse() != testWebhook {
		t.Errorf("Webhook nicht übernommen")
	}
	if len(c.DiscordArten) != 2 || c.DiscordArten[ereignis.Spieler] || !c.DiscordArten[ereignis.NiemandHostet] {
		t.Errorf("Arten: %v", c.DiscordArten)
	}
	// Die Konfiguration zeigt die Adresse in keiner Ausgabe.
	for _, s := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", *c), fmt.Sprintf("%#v", c.DiscordWebhook)} {
		if strings.Contains(s, "GEHEIMES-TOKEN") || strings.Contains(s, "123456789012345678") {
			t.Errorf("Adresse in der Ausgabe: %s", s)
		}
	}
}

func TestDiscordWebhookFehlerNennenDieAdresseNie(t *testing.T) {
	bad := map[string]string{
		"discord_webhook":         "discord_webhook: https://boese.example/api/webhooks/123456789012345678/GEHEIMES-TOKEN-abcdefghijklmnopqrstuvwxyz0123456789\n",
		"unbekanntes Ereignis":    "discord_ereignisse: {gibtsnicht: true}\n",
		"gibt es auf Discord nie": "discord_ereignisse: {beitrittsanfrage: true}\n",
	}
	for want, y := range bad {
		_, err := loadGruppe(t, y)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
			continue
		}
		if strings.Contains(err.Error(), "GEHEIMES-TOKEN") || strings.Contains(err.Error(), "boese") {
			t.Errorf("Fehler nennt die Adresse: %v", err)
		}
	}
}
