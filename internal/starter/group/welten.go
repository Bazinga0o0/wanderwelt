package group

// Mehrere Welten pro Gruppe, lokales Layout:
//
//	Dir()\                          Gruppenordner (wie bisher)
//	  state.json, server\, verworfen-*\     die ERSTE Welt (w1) – unverändert
//	  welt.json                              Auswahl {"gewaehlt": "w2"}
//	  welten\<id>\                           jede weitere Welt, nie vermischt
//	    state.json, server\, verworfen-*\, notfallkopie.json
//
// Die erste Welt bleibt, wo sie schon immer lag: Es wird nichts umbenannt,
// verschoben oder migriert, an den echten Daten ändert sich nichts. Jede
// weitere Welt hat einen eigenen Ordner mit eigener Welt, eigener Server-
// Konfiguration und eigenem Zustand (Basis des lokalen Stands, Vormerkung
// zum Nachreichen). Die PC-Kennung gehört zur Gruppe und wird beim Laden
// aus deren Zustand übernommen.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// FirstWorld ist die Kennung der ersten Welt einer Gruppe (der Welt, die
	// schon vor den Welten da war). Sie liegt im Gruppenordner selbst.
	FirstWorld = "w1"

	worldsDirName    = "welten"
	selectionFile    = "welt.json"
	maxWorldIDDigits = 9
)

var worldIDRE = regexp.MustCompile(`^w[1-9][0-9]*$`)

// ValidWorldID prüft eine Welt-Kennung (w<Zahl>, wie der Koordinator sie
// vergibt): nie ein Pfad, nie "..".
func ValidWorldID(id string) bool {
	return len(id) >= 2 && len(id) <= 1+maxWorldIDDigits && worldIDRE.MatchString(id)
}

// DefaultWorld: id meint die erste Welt (oder keine Angabe, wie bei einem
// Koordinator ohne Welten).
func DefaultWorld(id string) bool { return id == "" || id == FirstWorld }

// WorldHome ist der Ordner der Welt id in der aktiven Gruppe: Dir() für die
// erste Welt, sonst Dir()\welten\<id>.
func WorldHome(id string) (string, error) {
	return worldHomeIn(Dir(), id)
}

func worldHomeIn(dir, id string) (string, error) {
	if DefaultWorld(id) {
		return dir, nil
	}
	if !ValidWorldID(id) {
		return "", fmt.Errorf("ungültige Welt-Kennung %q", id)
	}
	return filepath.Join(dir, worldsDirName, id), nil
}

// ServerDir ist der Server-Ordner der Welt id (darin liegt die Welt in
// world\).
func ServerDir(id string) (string, error) {
	h, err := WorldHome(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "server"), nil
}

// WorldHomeOfServerDir liefert zu einem Server-Ordner den Ordner, in dem die
// beiseitegelegten Stände (verworfen-*) dieser Welt liegen: der Ordner über
// server\, wenn der Server-Ordner unter Dir()\welten liegt, sonst
// Dir() (die erste Welt und alle Ordner, die nicht zu einer Welt gehören).
func WorldHomeOfServerDir(serverDir string) string {
	root := filepath.Join(Dir(), worldsDirName) + string(filepath.Separator)
	clean := filepath.Clean(serverDir)
	if strings.HasPrefix(clean, root) && len(filepath.Dir(clean)) > len(root) {
		return filepath.Dir(clean)
	}
	return Dir()
}

// LoadWorldState lädt den Zustand der Welt id der aktiven Gruppe. Für die
// erste Welt ist das base selbst (derselbe Zustand, dieselbe Datei). Für
// jede weitere Welt kommt der Stand des lokalen Weltordners aus
// welten\<id>\state.json (fehlt sie: leer) und PC-Kennung, Version und
// Freigabe aus base; Save schreibt in den Ordner der Welt.
func LoadWorldState(id string, base *State) (*State, error) {
	if DefaultWorld(id) {
		return base, nil
	}
	home, err := WorldHome(id)
	if err != nil {
		return nil, err
	}
	ws, err := loadStateIn(home)
	if err != nil {
		return nil, err
	}
	if base != nil {
		ws.MachineID, ws.InstalledVersion, ws.Optin = base.MachineID, base.InstalledVersion, base.Optin
	}
	return ws, nil
}

// LocalWorlds liefert die Welten, die diese Gruppe auf diesem PC schon hat:
// die erste (immer) und jede, deren Ordner unter welten\ liegt; erste Welt
// zuerst, dann nach Zahl.
func LocalWorlds() []string {
	out := []string{FirstWorld}
	entries, err := os.ReadDir(filepath.Join(Dir(), worldsDirName))
	if err != nil {
		return out
	}
	var more []string
	for _, e := range entries {
		if e.IsDir() && ValidWorldID(e.Name()) && e.Name() != FirstWorld {
			more = append(more, e.Name())
		}
	}
	sort.Slice(more, func(i, j int) bool { return worldNum(more[i]) < worldNum(more[j]) })
	return append(out, more...)
}

func worldNum(id string) int {
	n, _ := strconv.Atoi(id[1:])
	return n
}

type selection struct {
	Gewaehlt string `json:"gewaehlt"`
}

func selectionPath() string { return filepath.Join(Dir(), selectionFile) }

// SelectedWorld liest die gewählte Welt der aktiven Gruppe ("" ohne gültige
// Wahl).
func SelectedWorld() string {
	data, err := os.ReadFile(selectionPath())
	if err != nil {
		return ""
	}
	var s selection
	if json.Unmarshal(data, &s) != nil || !ValidWorldID(s.Gewaehlt) {
		return ""
	}
	return s.Gewaehlt
}

// SetSelectedWorld merkt sich die gewählte Welt der aktiven Gruppe. Ob sie
// es beim Koordinator gibt und ob gerade gewechselt werden darf, prüft der
// Aufrufer.
func SetSelectedWorld(id string) error {
	if !ValidWorldID(id) {
		return fmt.Errorf("ungültige Welt-Kennung %q", id)
	}
	data, err := json.Marshal(selection{Gewaehlt: id})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(selectionPath(), append(data, '\n'))
}
