// Package benachrichtigung schickt Ereignisse der Gruppe an einen Discord-
// Kanal: Der Koordinator vergleicht im Takt den Status (Dienst), fasst
// Beitritte und Abgänge zusammen und gibt Nachrichten an den Sender, der sie
// ratenbegrenzt an den Webhook schickt. Alles ist nicht blockierend: Eine
// Störung bei Discord darf den Koordinator nie aufhalten und steht nur im
// Debug-Log.
//
// Die Webhook-Adresse ist ein Geheimnis (wer sie kennt, kann im Kanal
// schreiben): Sie steht nie im Log, in keiner Fehlermeldung, nie im Status
// und nie in einer Antwort an Starter oder Fenster.
package benachrichtigung

import (
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

// Webhook ist die Adresse eines Discord-Webhooks. Der Typ zeigt sich in
// fmt, slog und JSON nur als „[geheim]“; die Adresse selbst gibt nur
// Adresse() heraus.
type Webhook string

const geheim = "[geheim]"

// String, GoString, LogValue und die Marshaler halten die Adresse aus
// Logs, Fehlertexten und Ausgaben (etwa beim Ausgeben der Konfiguration).
func (Webhook) String() string   { return geheim }
func (Webhook) GoString() string { return geheim }

// LogValue verbirgt die Adresse in slog.
func (Webhook) LogValue() slog.Value { return slog.StringValue(geheim) }

// MarshalText und MarshalJSON verbergen die Adresse.
func (Webhook) MarshalText() ([]byte, error) { return []byte(geheim), nil }

// MarshalJSON verbirgt die Adresse.
func (Webhook) MarshalJSON() ([]byte, error) { return []byte(`"` + geheim + `"`), nil }

// Adresse ist die echte Adresse; nur zum Senden benutzen.
func (w Webhook) Adresse() string { return string(w) }

// Gesetzt meldet, ob ein Webhook eingetragen ist.
func (w Webhook) Gesetzt() bool { return w != "" }

var (
	webhookHosts = map[string]bool{
		"discord.com":        true,
		"discordapp.com":     true,
		"ptb.discord.com":    true,
		"canary.discord.com": true,
	}
	webhookPfad = regexp.MustCompile(`^/api(/v[0-9]{1,2})?/webhooks/[0-9]{5,25}/[A-Za-z0-9_-]{20,200}$`)
)

// ErrWebhook: die Adresse ist kein Discord-Webhook. Der Text nennt die
// Adresse nie.
var ErrWebhook = errors.New("keine gültige Discord-Webhook-Adresse (erwartet: https://discord.com/api/webhooks/<Nummer>/<Schlüssel>, " +
	"zu finden in Discord unter Kanal → Integrationen → Webhooks → „Webhook-URL kopieren“)")

// Pruefe prüft die Adresse eines Webhooks und liefert sie bereinigt (ohne
// Leerraum an den Enden). Erlaubt ist nur https auf discord.com und
// discordapp.com (auch ptb./canary.) mit dem Pfad /api/webhooks/<Nummer>/<Schlüssel>,
// ohne Port, Anmeldedaten, Anfrage und Fragment: So kann ein Tippfehler oder
// eine untergeschobene Konfiguration den Koordinator nicht dazu bringen, an
// einen fremden Ort zu senden. Jeder Fehler ist ErrWebhook.
func Pruefe(roh string) (Webhook, error) {
	roh = strings.TrimSpace(roh)
	u, err := url.Parse(roh)
	switch {
	case err != nil,
		u.Scheme != "https",
		!webhookHosts[strings.ToLower(u.Hostname())],
		u.Port() != "",
		u.User != nil,
		u.RawQuery != "" || u.ForceQuery,
		u.Fragment != "",
		!webhookPfad.MatchString(u.EscapedPath()):
		return "", ErrWebhook
	}
	return Webhook(roh), nil
}
