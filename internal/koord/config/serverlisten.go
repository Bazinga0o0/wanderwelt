package config

// serverlisten.json (Überlagerung in data_dir): Gäste und Banns des Reiters
// „Server“. Zusammen mit der Mitgliederliste (und deren Op-Recht in
// roles.json) ist sie die einzige Wahrheit für Whitelist, Ops und Banns der
// Gruppe; die Dateien im Server-Ordner eines Hosts sind nur daraus
// abgeleitet.

import (
	"fmt"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/serverbefehl"
)

// ServerListsFile ist der Dateiname in data_dir.
const ServerListsFile = "serverlisten.json"

// Höchstzahlen der Listen.
const (
	MaxGuests = 100
	MaxBans   = 500
)

// Guest steht auf der Whitelist, ohne Mitglied zu sein; Op: auch Op.
// By: E-Mail des Admins, der ihn eingetragen hat.
type Guest struct {
	Name  string    `json:"name"`
	UUID  string    `json:"uuid"`
	Op    bool      `json:"op,omitempty"`
	By    string    `json:"by,omitempty"`
	Added time.Time `json:"added,omitzero"`
}

// Ban ist ein gebannter Spieler. By: E-Mail des Admins.
type Ban struct {
	Name  string    `json:"name"`
	UUID  string    `json:"uuid"`
	By    string    `json:"by,omitempty"`
	Added time.Time `json:"added,omitzero"`
}

// ServerLists ist der Inhalt von serverlisten.json.
type ServerLists struct {
	Guests []Guest `json:"guests"`
	Bans   []Ban   `json:"bans"`
}

// LoadServerLists liest serverlisten.json; eine fehlende Datei ist leer.
// Fehler: unbekannte Felder, ungültige Namen oder UUIDs, eine UUID doppelt
// in einer Liste, zu viele Einträge.
func LoadServerLists(path string) (ServerLists, error) {
	var l ServerLists
	if err := loadOverlay(path, &l); err != nil {
		return ServerLists{}, err
	}
	if len(l.Guests) > MaxGuests || len(l.Bans) > MaxBans {
		return ServerLists{}, fmt.Errorf("%s: zu viele Einträge (höchstens %d Gäste, %d Banns)", path, MaxGuests, MaxBans)
	}
	seen := map[string]bool{}
	for _, g := range l.Guests {
		if err := checkListEntry(g.Name, g.UUID); err != nil {
			return ServerLists{}, fmt.Errorf("%s: Gast: %w", path, err)
		}
		if seen[strings.ToLower(g.UUID)] {
			return ServerLists{}, fmt.Errorf("%s: Gast %s steht doppelt", path, g.Name)
		}
		seen[strings.ToLower(g.UUID)] = true
	}
	seen = map[string]bool{}
	for _, b := range l.Bans {
		if err := checkListEntry(b.Name, b.UUID); err != nil {
			return ServerLists{}, fmt.Errorf("%s: Bann: %w", path, err)
		}
		if seen[strings.ToLower(b.UUID)] {
			return ServerLists{}, fmt.Errorf("%s: Bann %s steht doppelt", path, b.Name)
		}
		seen[strings.ToLower(b.UUID)] = true
	}
	return l, nil
}

// SaveServerLists schreibt l atomar nach path (0600).
func SaveServerLists(path string, l ServerLists) error {
	if l.Guests == nil {
		l.Guests = []Guest{}
	}
	if l.Bans == nil {
		l.Bans = []Ban{}
	}
	return saveOverlay(path, l)
}

func checkListEntry(name, uuid string) error {
	if !serverbefehl.GueltigerName(name) {
		return fmt.Errorf("ungültiger Minecraft-Name %q", name)
	}
	if !ValidUUID(uuid) {
		return fmt.Errorf("ungültige UUID %q für %s", uuid, name)
	}
	return nil
}

// ValidUUID: 8-4-4-4-12 Hex-Zeichen in Kleinbuchstaben.
func ValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
				return false
			}
		}
	}
	return true
}
