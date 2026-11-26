package group

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Invite sind die beim Bauen in die exe eingesetzten Gruppen-Adressen
// (Gruppen-Build): alles, was eine Gruppendatei enthält, außer dem
// persönlichen Token. Mit einer Einladung richtet sich wanderwelt ohne
// Gruppendatei ein: Das Mitglied legt auf der Webseite ein Konto an,
// erstellt dort einen Token und fügt ihn bei der Einrichtung ein.
type Invite struct {
	// Coordinator wie in der Gruppendatei.
	Coordinator string `yaml:"coordinator"`
	// TunnelServer wie in der Gruppendatei.
	TunnelServer string `yaml:"tunnel_server"`
	// WebURL ist die tunnel-Webseite für Konto und Token (https).
	WebURL string `yaml:"web_url"`
	// TokenURL ist optional die Seite zum Erstellen des Tokens; ohne sie
	// gilt WebURL.
	TokenURL string `yaml:"token_url"`
}

// ParseInvite liest die eingebettete Einladung: Base64 (Standard-Alphabet,
// mit oder ohne Auffüllung) eines YAML mit coordinator, tunnel_server und
// web_url. Leer: keine Einladung (nil, nil). Unbekannte Schlüssel (auch ein
// token) sind Fehler.
func ParseInvite(b64 string) (*Invite, error) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	}
	if err != nil {
		return nil, fmt.Errorf("eingebettete Gruppen-Adressen: kein Base64: %w", err)
	}
	var inv Invite
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&inv); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("eingebettete Gruppen-Adressen: leer")
		}
		return nil, fmt.Errorf("eingebettete Gruppen-Adressen: %w", err)
	}
	inv.Coordinator = strings.TrimRight(strings.TrimSpace(inv.Coordinator), "/")
	inv.TunnelServer = strings.TrimSpace(inv.TunnelServer)
	inv.WebURL = strings.TrimSpace(inv.WebURL)
	inv.TokenURL = strings.TrimSpace(inv.TokenURL)
	if err := checkCoordinator(inv.Coordinator); err != nil {
		return nil, fmt.Errorf("eingebettete Gruppen-Adressen: coordinator: %w", err)
	}
	if inv.TunnelServer == "" {
		return nil, errors.New("eingebettete Gruppen-Adressen: tunnel_server fehlt")
	}
	if err := checkWebURL(inv.WebURL); err != nil {
		return nil, fmt.Errorf("eingebettete Gruppen-Adressen: web_url: %w", err)
	}
	if inv.TokenURL != "" {
		if err := checkWebURL(inv.TokenURL); err != nil {
			return nil, fmt.Errorf("eingebettete Gruppen-Adressen: token_url: %w", err)
		}
	}
	return &inv, nil
}

// checkWebURL verlangt eine absolute https-Adresse (http nur für Loopback).
// Die Adresse wird im Browser geöffnet; andere Schemata (file:, javascript:)
// gehören nicht dahin.
func checkWebURL(s string) error {
	if s == "" {
		return errors.New("fehlt")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("ungültige Adresse %q", s)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("%q: Schema muss https sein", s)
}

// AccountURL ist die Seite zum Anlegen des Kontos.
func (i *Invite) AccountURL() string { return i.WebURL }

// CreateTokenURL ist die Seite zum Erstellen des Tokens.
func (i *Invite) CreateTokenURL() string {
	if i.TokenURL != "" {
		return i.TokenURL
	}
	return i.WebURL
}

// WithToken macht aus der Einladung und dem Token eine Gruppe.
func (i *Invite) WithToken(token string) *Group {
	return &Group{Coordinator: i.Coordinator, TunnelServer: i.TunnelServer, WebURL: i.WebURL, Token: strings.TrimSpace(token)}
}

// CheckToken prüft die Form eines eingefügten Tokens (nicht leer, eine
// Zeile ohne Leerzeichen, nicht übermäßig lang). Der Text des Tokens steht
// nie in der Fehlermeldung.
func CheckToken(token string) error {
	t := strings.TrimSpace(token)
	switch {
	case t == "":
		return errors.New("Der Token ist leer.")
	case len(t) > 512:
		return errors.New("Der Token ist zu lang – wurde mehr als der Token kopiert?")
	case strings.IndexFunc(t, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0:
		return errors.New("Der Token enthält Leerzeichen oder Zeilenumbrüche – wurde mehr als der Token kopiert?")
	}
	return nil
}

// SaveGroup schreibt g (samt Token) atomar in den Gruppenordner (siehe
// AddGroup) und macht sie zur aktiven Gruppe, mit Zugriff nur für das eigene
// Windows-Konto (Vererbung entfernt). Scheitert das Einschränken, wird die
// Datei nicht geschrieben: Der Token soll nie in einer für andere lesbaren
// Datei liegen.
func SaveGroup(g *Group) error { return AddGroup(g, true) }

func writeGroupFile(dir string, g *Group) error {
	data, err := yaml.Marshal(g)
	if err != nil {
		return err
	}
	header := "# wanderwelt-Gruppendatei (von der Einrichtung geschrieben). Enthält deinen\n# persönlichen Token: nicht weitergeben.\n"
	if err := ensureNotSymlink(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomicPrivate(filepath.Join(dir, groupFileName), append([]byte(header), data...))
}
