package config

// Überlagerungen in data_dir: Was Admins im Fenster ändern (Rollen,
// Minecraft-Namen, Speicherbudget), schreibt der Koordinator nicht in seine
// Konfiguration – die ist für den Dienst nur lesbar (systemd
// ProtectSystem=strict) –, sondern in kleine JSON-Dateien in data_dir. Sie
// gelten vor den Werten aus der Konfiguration:
//
//   - roles.json: E-Mail (klein) → {"admin": …, "op": …}
//   - names.json: E-Mail (klein) → {"minecraft": …, "by_admin": …}
//   - settings.json: {"storage_budget_bytes": n}
//   - members.json und joins.json: angenommene Mitglieder und
//     Beitrittsanfragen (joins.go)
//
// Alle drei schreibt der Koordinator atomar mit Rechten 0600 (Besitzer ist
// der Dienst-Benutzer). Fehlt eine Datei, ist sie leer; ist sie kaputt,
// meldet Load* einen Fehler, und der Aufrufer entscheidet (der Koordinator
// lässt sie dann nicht gelten).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Dateinamen der Überlagerungen in data_dir.
const (
	RolesFile    = "roles.json"
	NamesFile    = "names.json"
	SettingsFile = "settings.json"
)

// Roles überlagert Admin- und Op-Recht der Mitglieder; Schlüssel ist die
// E-Mail in Kleinbuchstaben.
type Roles map[string]api.RoleRequest

// NameEntry ist ein Minecraft-Name aus der Überlagerung. Ein Name, den das
// Mitglied selbst gemeldet hat, gilt nur, solange in der Konfiguration
// keiner steht; einen, den ein Admin gesetzt hat (ByAdmin), gilt immer.
type NameEntry struct {
	Minecraft string `json:"minecraft"`
	ByAdmin   bool   `json:"by_admin,omitempty"`
}

// Names überlagert die Minecraft-Namen; Schlüssel ist die E-Mail in
// Kleinbuchstaben.
type Names map[string]NameEntry

// Settings sind die Einstellungen aus settings.json.
type Settings struct {
	// StorageBudgetBytes überlagert storage_budget; 0 heißt: nicht gesetzt.
	StorageBudgetBytes int64 `json:"storage_budget_bytes,omitempty"`
}

// LoadRoles liest roles.json. Eine fehlende Datei ist leer. Unbekannte
// Felder, eine leere E-Mail oder zwei Schlüssel, die sich nur in Groß/Klein
// unterscheiden, sind Fehler.
func LoadRoles(path string) (Roles, error) {
	var raw map[string]api.RoleRequest
	if err := loadOverlay(path, &raw); err != nil {
		return nil, err
	}
	r := Roles{}
	for k, v := range raw {
		key, err := overlayKey(path, k, func(key string) bool { _, ok := r[key]; return ok })
		if err != nil {
			return nil, err
		}
		r[key] = v
	}
	return r, nil
}

// SaveRoles schreibt r atomar nach path (0600).
func SaveRoles(path string, r Roles) error { return saveOverlay(path, r) }

// ApplyRoles liefert members mit Admin und Op aus r, wo r einen Eintrag hat;
// Einträge für E-Mails, die nicht in members stehen, zählen nicht. members
// bleibt unverändert.
func ApplyRoles(members []Member, r Roles) []Member {
	out := slices.Clone(members)
	for i, m := range out {
		if rr, ok := r[strings.ToLower(m.Email)]; ok {
			out[i].Admin, out[i].Op = rr.Admin, rr.Op
		}
	}
	return out
}

// LoadNames liest names.json; Regeln wie LoadRoles, dazu muss jeder Name
// ein gültiger Minecraft-Name sein (ValidMinecraftName).
func LoadNames(path string) (Names, error) {
	var raw map[string]NameEntry
	if err := loadOverlay(path, &raw); err != nil {
		return nil, err
	}
	n := Names{}
	for k, v := range raw {
		if !ValidMinecraftName(v.Minecraft) {
			return nil, fmt.Errorf("%s: ungültiger Minecraft-Name %q für %q", path, v.Minecraft, k)
		}
		key, err := overlayKey(path, k, func(key string) bool { _, ok := n[key]; return ok })
		if err != nil {
			return nil, err
		}
		n[key] = v
	}
	return n, nil
}

// SaveNames schreibt n atomar nach path (0600).
func SaveNames(path string, n Names) error { return saveOverlay(path, n) }

// ApplyNames liefert members mit den Minecraft-Namen aus n: ein vom Admin
// gesetzter Name gilt immer, ein selbst gemeldeter nur, wo die
// Konfiguration keinen nennt. members bleibt unverändert.
func ApplyNames(members []Member, n Names) []Member {
	out := slices.Clone(members)
	for i, m := range out {
		if e, ok := n[strings.ToLower(m.Email)]; ok && (m.Minecraft == "" || e.ByAdmin) {
			out[i].Minecraft = e.Minecraft
		}
	}
	return out
}

// LoadSettings liest settings.json; eine fehlende Datei ist leer, ein
// negatives Budget ein Fehler.
func LoadSettings(path string) (Settings, error) {
	var s Settings
	if err := loadOverlay(path, &s); err != nil {
		return Settings{}, err
	}
	if s.StorageBudgetBytes < 0 {
		return Settings{}, fmt.Errorf("%s: storage_budget_bytes %d ist negativ", path, s.StorageBudgetBytes)
	}
	return s, nil
}

// SaveSettings schreibt s atomar nach path (0600).
func SaveSettings(path string, s Settings) error { return saveOverlay(path, s) }

// EffectiveMembers liefert members mit den Überlagerungen aus dataDir
// (Namen, dann Rollen) – so, wie der Koordinator sie sieht. Eine kaputte
// oder unlesbare Überlagerung zählt nicht; der Fehler kommt mit zurück, die
// übrigen gelten trotzdem. Ohne dataDir gibt es keine Überlagerungen.
func EffectiveMembers(members []Member, dataDir string) ([]Member, error) {
	var errs []error
	out, err := BaseMembers(members, dataDir)
	if err != nil {
		errs = append(errs, err)
	}
	if dataDir == "" {
		return out, nil
	}
	if n, err := LoadNames(filepath.Join(dataDir, NamesFile)); err != nil {
		errs = append(errs, err)
	} else {
		out = ApplyNames(out, n)
	}
	if r, err := LoadRoles(filepath.Join(dataDir, RolesFile)); err != nil {
		errs = append(errs, err)
	} else {
		out = ApplyRoles(out, r)
	}
	return out, errors.Join(errs...)
}

// ValidMinecraftName: 1–25 Zeichen aus Buchstaben, Ziffern und "_" (heute
// 3–16; ältere Konten haben auch kürzere oder längere Namen).
func ValidMinecraftName(s string) bool {
	if s == "" || len(s) > 25 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_') {
			return false
		}
	}
	return true
}

// overlayKey prüft den Schlüssel k einer Überlagerung (eine E-Mail ohne
// Leerraum) und liefert ihn klein; seen meldet, ob es ihn schon gibt (dann
// stand er in anderer Schreibweise doppelt da).
func overlayKey(path, k string, seen func(key string) bool) (string, error) {
	if k == "" || strings.IndexFunc(k, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%s: ungültige E-Mail %q", path, k)
	}
	key := strings.ToLower(k)
	if seen(key) {
		return "", fmt.Errorf("%s: E-Mail %q steht doppelt (Groß/Klein egal)", path, k)
	}
	return key, nil
}

// loadOverlay liest genau ein JSON-Objekt aus path nach v; unbekannte Felder
// und Daten danach sind Fehler. Fehlt die Datei, bleibt v leer.
func loadOverlay(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: Datei ist leer", path)
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%s: Daten nach dem JSON-Objekt", path)
	}
	return nil
}

// saveOverlay schreibt v als JSON über eine Temp-Datei im selben Ordner
// (0600) und ersetzt path dann atomar.
func saveOverlay(path string, v any) error { return saveOverlayLike(path, v, nil) }

// saveOverlayLike wie saveOverlay; mit ref übernimmt die neue Datei dessen
// Besitzer und Gruppe (für Aufrufe als root, damit der Dienst sie lesen
// kann).
func saveOverlayLike(path string, v any, ref os.FileInfo) (err error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if ref != nil {
		if err = copyOwner(tmp, ref); err != nil {
			return err
		}
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir schreibt den Ordnereintrag nach einem Umbenennen fest (wo das
// geht; Fehler zählen nicht, die Datei selbst ist schon geschrieben).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
