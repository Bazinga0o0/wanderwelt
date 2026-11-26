package group

// Mehrere Gruppen pro PC. Layout unter Home():
//
//	Home()\                         gemeinsam (gruppenunabhängig)
//	  wanderwelt.exe, control.json, logs\, update\, deps\, recipes\,
//	  hashcache.json, aktiv.json, Symbole
//	Home()\gruppen\<id>\            je Gruppe (nie vermischt)
//	  gruppe.yaml (mit Token), state.json, profil.yaml, tunnel.yaml (Token),
//	  notfallkopie.json, server\ (Welt), verworfen-*\
//
// <id> stammt aus dem Koordinator-Host (siehe IDFromCoordinator). Die aktive
// Gruppe steht in Home()\aktiv.json; ohne gültige Wahl gilt die erste Gruppe
// (alphabetisch). Solange noch das alte Einzel-Layout vorliegt (gruppe.yaml
// direkt in Home(); die Migration läuft beim Start oder ist gescheitert),
// ist Dir() == Home() und es gibt genau eine Gruppe.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	groupsDirName  = "gruppen"
	activeFileName = "aktiv.json"
	maxIDLen       = 100
)

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// ValidID prüft eine Gruppen-Kennung: nur [a-z0-9.-], beginnt mit Buchstabe
// oder Ziffer (also nie "." oder ".."), höchstens 100 Zeichen, endet nicht
// mit Punkt (Windows schneidet den ab).
func ValidID(id string) bool {
	return len(id) <= maxIDLen && idRE.MatchString(id) && !strings.HasSuffix(id, ".") &&
		!strings.Contains(id, "..")
}

// IDFromCoordinator macht aus der Koordinator-Adresse die Kennung: der Host
// in Kleinbuchstaben, ein Port als "-<port>" angehängt (localhost:8470 ->
// localhost-8470). Hosts mit anderen Zeichen (IPv6, Umlaute, Pfadtricks)
// sind Fehler.
func IDFromCoordinator(coordinator string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(coordinator))
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("Koordinator-Adresse %q nicht lesbar", coordinator)
	}
	id := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" {
		id += "-" + p
	}
	if !ValidID(id) {
		return "", fmt.Errorf("aus dem Koordinator-Host %q lässt sich keine Gruppen-Kennung bilden", u.Hostname())
	}
	return id, nil
}

// GroupsRoot ist Home()\gruppen.
func GroupsRoot() string { return filepath.Join(Home(), groupsDirName) }

// IDDir ist der Ordner der Gruppe id (id muss gültig sein, sonst "").
func IDDir(id string) string {
	if !ValidID(id) {
		return ""
	}
	return filepath.Join(GroupsRoot(), id)
}

// Legacy: Das alte Einzel-Layout ist da (gruppe.yaml direkt in Home()).
func Legacy() bool {
	_, err := os.Stat(filepath.Join(Home(), groupFileName))
	return err == nil
}

// Entry ist eine eingerichtete Gruppe.
type Entry struct {
	ID    string
	Group *Group
}

// List liefert die eingerichteten Gruppen (gruppen\<id>\gruppe.yaml gültig),
// nach Kennung sortiert. Im alten Einzel-Layout: die eine Gruppe, ID aus dem
// Koordinator.
func List() []Entry {
	if Legacy() {
		g, err := LoadGroup(filepath.Join(Home(), groupFileName))
		if err != nil {
			return nil
		}
		id, err := IDFromCoordinator(g.Coordinator)
		if err != nil {
			id = "gruppe"
		}
		return []Entry{{ID: id, Group: g}}
	}
	ents, err := os.ReadDir(GroupsRoot())
	if err != nil {
		return nil
	}
	var out []Entry
	for _, e := range ents {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		g, err := LoadGroup(filepath.Join(GroupsRoot(), e.Name(), groupFileName))
		if err != nil {
			continue
		}
		out = append(out, Entry{ID: e.Name(), Group: g})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type activeFile struct {
	Aktiv string `json:"aktiv"`
}

func activePath() string { return filepath.Join(Home(), activeFileName) }

// ActiveID ist die aktive Gruppe: aktiv.json, wenn die Gruppe noch da ist,
// sonst die erste; "" ohne Gruppe.
func ActiveID() string {
	list := List()
	if len(list) == 0 {
		return ""
	}
	if Legacy() {
		return list[0].ID
	}
	if data, err := os.ReadFile(activePath()); err == nil {
		var a activeFile
		if json.Unmarshal(data, &a) == nil {
			for _, e := range list {
				if e.ID == a.Aktiv {
					return a.Aktiv
				}
			}
		}
	}
	return list[0].ID
}

// Dir ist der Ordner der aktiven Gruppe (gruppe.yaml, state.json, server\ …).
// Ohne Gruppe und im alten Einzel-Layout: Home().
func Dir() string {
	if Legacy() {
		return Home()
	}
	id := ActiveID()
	if id == "" {
		return Home()
	}
	return IDDir(id)
}

// SetActive wählt die aktive Gruppe (muss eingerichtet sein) und schreibt
// aktiv.json atomar. Ob der Wechsel gerade erlaubt ist (nicht beim Hosten
// oder Spielen), prüft der Aufrufer.
func SetActive(id string) error {
	if Legacy() {
		return errors.New("das alte Einzel-Layout kennt nur eine Gruppe")
	}
	found := false
	for _, e := range List() {
		if e.ID == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("Gruppe %q ist nicht eingerichtet", id)
	}
	return setActiveFile(id)
}

func setActiveFile(id string) error {
	data, err := json.Marshal(activeFile{Aktiv: id})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(activePath(), append(data, '\n'))
}

// target ist der Ordner, in den die Gruppendatei g gehört: im alten Layout
// Home(), sonst gruppen\<id>.
func target(g *Group) (dir, id string, err error) {
	if Legacy() {
		return Home(), "", nil
	}
	id, err = IDFromCoordinator(g.Coordinator)
	if err != nil {
		return "", "", err
	}
	return IDDir(id), id, nil
}

// AddGroup schreibt g (samt Token, nur für das eigene Konto lesbar) nach
// gruppen\<id>\gruppe.yaml; activate macht sie zur aktiven Gruppe. Eine
// vorhandene Gruppe mit derselben Kennung wird ersetzt (neuer Token), nie
// eine andere Gruppe berührt.
func AddGroup(g *Group, activate bool) error {
	dir, id, err := target(g)
	if err != nil {
		return err
	}
	if err := writeGroupFile(dir, g); err != nil {
		return err
	}
	if activate && id != "" {
		return setActiveFile(id)
	}
	return nil
}

// ensureNotSymlink verweigert einen Gruppenordner, der ein Link ist (nichts
// außerhalb von Home() beschreiben).
func ensureNotSymlink(dir string) error {
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s ist ein Link", dir)
	}
	return nil
}

// WebBase ist die Basis-Adresse der tunnel-Webseite dieser Gruppe (dort liegt
// /api/v1/directory): web_url der Gruppendatei; sonst fallback (etwa die
// web_url aus einer eingebetteten Gruppen-Adresse mit demselben
// Tunnel-Server), sonst https://<Host von tunnel_server>. "": nicht
// bestimmbar (Dev-Modus ohne tunnel_server).
func (g *Group) WebBase(fallback string) string {
	if g.WebURL != "" {
		return g.WebURL
	}
	if fallback != "" && checkWebURL(fallback) == nil {
		return strings.TrimRight(fallback, "/")
	}
	host := g.TunnelServer
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "/@?# ") {
		return ""
	}
	return "https://" + host
}

// LoadStateOf liest state.json der Gruppe id (nicht nur der aktiven); der
// Zustand schreibt später wieder in genau diesen Ordner.
func LoadStateOf(id string) (*State, error) {
	dir := IDDir(id)
	if dir == "" {
		return nil, fmt.Errorf("ungültige Gruppen-Kennung %q", id)
	}
	return loadStateIn(dir)
}

// CheckEndpoint prüft eine Koordinator-Adresse aus einer fremden Quelle
// (https; http nur für Loopback).
func CheckEndpoint(s string) error { return checkCoordinator(s) }
