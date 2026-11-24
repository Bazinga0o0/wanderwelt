package config

// Beitritt: Mitglieder, die ein Admin auf eine Beitrittsanfrage hin
// angenommen hat, und die Anfragen selbst. Beides sind Überlagerungen in
// data_dir wie roles.go:
//
//   - members.json: E-Mail (klein) → {"email", "minecraft", "accepted_by", "at"}
//   - joins.json: E-Mail (klein) → {"email", "minecraft", "requested",
//     "state", "decided", "decided_by"}; Zustand "offen" oder "abgelehnt"
//     (eine angenommene Anfrage verschwindet und steht in members.json)
//
// Wirksame Mitglieder sind die aus der Konfiguration plus die aus
// members.json; steht eine E-Mail in beiden, gilt die Konfiguration.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Dateinamen der Beitritts-Überlagerungen in data_dir.
const (
	MembersFile = "members.json"
	JoinsFile   = "joins.json"
)

// Joined ist ein per Beitrittsanfrage angenommenes Mitglied.
type Joined struct {
	Email      string    `json:"email"`
	Minecraft  string    `json:"minecraft"`
	AcceptedBy string    `json:"accepted_by"`
	At         time.Time `json:"at"`
}

// JoinedMembers sind die angenommenen Mitglieder; Schlüssel ist die E-Mail
// in Kleinbuchstaben.
type JoinedMembers map[string]Joined

// LoadJoined liest members.json; eine fehlende Datei ist leer. Regeln wie
// LoadRoles; dazu muss jeder Eintrag eine E-Mail (gleich dem Schlüssel,
// Groß/Klein egal) und einen gültigen Minecraft-Namen haben.
func LoadJoined(path string) (JoinedMembers, error) {
	var raw map[string]Joined
	if err := loadOverlay(path, &raw); err != nil {
		return nil, err
	}
	j := JoinedMembers{}
	for k, v := range raw {
		if !strings.EqualFold(v.Email, k) {
			return nil, fmt.Errorf("%s: Eintrag %q hat die E-Mail %q", path, k, v.Email)
		}
		if !ValidMinecraftName(v.Minecraft) {
			return nil, fmt.Errorf("%s: ungültiger Minecraft-Name %q für %q", path, v.Minecraft, k)
		}
		key, err := overlayKey(path, k, func(key string) bool { _, ok := j[key]; return ok })
		if err != nil {
			return nil, err
		}
		j[key] = v
	}
	return j, nil
}

// SaveJoined schreibt j atomar nach path (0600).
func SaveJoined(path string, j JoinedMembers) error { return saveOverlay(path, j) }

// MergeJoined hängt die Mitglieder aus j hinter members (nach Zeit der
// Annahme, dann E-Mail); wer schon in members steht, zählt nicht doppelt
// (die Konfiguration gewinnt). members bleibt unverändert.
func MergeJoined(members []Member, j JoinedMembers) []Member {
	out := slices.Clone(members)
	var add []Joined
	for k, v := range j {
		if !slices.ContainsFunc(members, func(m Member) bool { return strings.EqualFold(m.Email, k) }) {
			add = append(add, v)
		}
	}
	slices.SortFunc(add, func(a, b Joined) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email))
	})
	for _, v := range add {
		out = append(out, Member{Email: v.Email, Minecraft: v.Minecraft})
	}
	return out
}

// BaseMembers liefert members mit den angenommenen aus members.json in
// dataDir, ohne Namen- und Rollen-Überlagerung. Eine kaputte Datei zählt
// nicht; der Fehler kommt mit zurück.
func BaseMembers(members []Member, dataDir string) ([]Member, error) {
	if dataDir == "" {
		return slices.Clone(members), nil
	}
	j, err := LoadJoined(filepath.Join(dataDir, MembersFile))
	if err != nil {
		return slices.Clone(members), err
	}
	return MergeJoined(members, j), nil
}

// RemoveJoined nimmt das angenommene Mitglied email aus members.json in
// dataDir. Die neue Datei behält Besitzer und Gruppe der alten (der
// Aufrufer läuft oft als root, der Koordinator als eigener Benutzer).
// Fehler, wenn email dort nicht steht.
func RemoveJoined(dataDir, email string) error {
	path := filepath.Join(dataDir, MembersFile)
	j, err := LoadJoined(path)
	if err != nil {
		return err
	}
	key := strings.ToLower(email)
	if _, ok := j[key]; !ok {
		return fmt.Errorf("%s: kein angenommenes Mitglied %q", path, email)
	}
	fi, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(j, key)
	return saveOverlayLike(path, j, fi)
}

// RemoveJoinedIfPresent wie RemoveJoined, aber ohne Fehler, wenn email nicht
// (oder members.json gar nicht) vorhanden ist; ok meldet, ob etwas entfernt
// wurde.
func RemoveJoinedIfPresent(dataDir, email string) (ok bool, err error) {
	j, err := LoadJoined(filepath.Join(dataDir, MembersFile))
	if err != nil {
		return false, err
	}
	if _, in := j[strings.ToLower(email)]; !in {
		return false, nil
	}
	return true, RemoveJoined(dataDir, email)
}

// RemoveJoinRequest nimmt die Anfrage von email aus joins.json in dataDir
// (Besitzer bleibt); ok meldet, ob es eine gab.
func RemoveJoinRequest(dataDir, email string) (ok bool, err error) {
	path := filepath.Join(dataDir, JoinsFile)
	j, err := LoadJoins(path)
	if err != nil {
		return false, err
	}
	key := strings.ToLower(email)
	if _, in := j[key]; !in {
		return false, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	delete(j, key)
	return true, saveOverlayLike(path, j, fi)
}

// PruneDeclined nimmt abgelehnte Anfragen heraus, deren Wartezeit
// (api.JoinRetryAfter) bei now vorbei ist; das Ergebnis ist eine Kopie.
func PruneDeclined(j JoinEntries, now time.Time) JoinEntries {
	out := JoinEntries{}
	for k, e := range j {
		if e.State == JoinStateDeclined && !e.Decided.Add(api.JoinRetryAfter).After(now) {
			continue
		}
		out[k] = e
	}
	return out
}

// Zustände in joins.json (gleich den Werten von api.JoinStatus.State).
const (
	JoinStateOpen     = api.JoinOpen
	JoinStateDeclined = api.JoinDeclined
)

// JoinEntry ist eine Beitrittsanfrage in joins.json.
type JoinEntry struct {
	Email     string    `json:"email"`
	Minecraft string    `json:"minecraft"`
	Requested time.Time `json:"requested"`
	State     string    `json:"state"`
	Decided   time.Time `json:"decided,omitzero"`
	DecidedBy string    `json:"decided_by,omitempty"`
}

// JoinEntries sind die Anfragen; Schlüssel ist die E-Mail in Kleinbuchstaben.
type JoinEntries map[string]JoinEntry

// LoadJoins liest joins.json; eine fehlende Datei ist leer. Regeln wie
// LoadJoined; der Zustand muss "offen" oder "abgelehnt" sein.
func LoadJoins(path string) (JoinEntries, error) {
	var raw map[string]JoinEntry
	if err := loadOverlay(path, &raw); err != nil {
		return nil, err
	}
	j := JoinEntries{}
	for k, v := range raw {
		if !strings.EqualFold(v.Email, k) {
			return nil, fmt.Errorf("%s: Eintrag %q hat die E-Mail %q", path, k, v.Email)
		}
		if !ValidMinecraftName(v.Minecraft) {
			return nil, fmt.Errorf("%s: ungültiger Minecraft-Name %q für %q", path, v.Minecraft, k)
		}
		if v.State != JoinStateOpen && v.State != JoinStateDeclined {
			return nil, fmt.Errorf("%s: ungültiger Zustand %q für %q", path, v.State, k)
		}
		key, err := overlayKey(path, k, func(key string) bool { _, ok := j[key]; return ok })
		if err != nil {
			return nil, err
		}
		j[key] = v
	}
	return j, nil
}

// SaveJoins schreibt j atomar nach path (0600).
func SaveJoins(path string, j JoinEntries) error { return saveOverlay(path, j) }

// BlockJoin sperrt das Konto email für neue Beitrittsanfragen, als wäre es
// jetzt abgelehnt worden (api.JoinRetryAfter ab now): Es schreibt in
// joins.json dataDir einen Eintrag „abgelehnt“ (ersetzt eine vorhandene
// Anfrage). Besitzer der Datei bleibt erhalten.
func BlockJoin(dataDir, email, minecraft string, now time.Time, by string) error {
	path := filepath.Join(dataDir, JoinsFile)
	j, err := LoadJoins(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err != nil {
		fi = nil
	}
	key := strings.ToLower(email)
	j[key] = JoinEntry{Email: email, Minecraft: minecraft, Requested: now, State: JoinStateDeclined, Decided: now, DecidedBy: by}
	return saveOverlayLike(path, j, fi)
}
