// Package group verwaltet das Datenverzeichnis des Starters, die
// Gruppendatei (Koordinator, tunnel-Server, persönlicher Token) und den
// lokalen Zustand des Starters (state.json).
package group

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	groupFileName = "gruppe.yaml"
	stateFileName = "state.json"
)

// Home ist die Wurzel des Datenverzeichnisses des Starters (gemeinsame Dateien
// und gruppen\<id>, siehe gruppen.go): WANDERWELT_HOME, falls
// gesetzt, sonst %LOCALAPPDATA%\wanderwelt. Außerhalb von Windows (Tests,
// Entwicklung) $HOME/.local/share/wanderwelt.
func Home() string {
	if h := os.Getenv("WANDERWELT_HOME"); h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			return abs
		}
		return h
	}
	if runtime.GOOS == "windows" {
		// Bewusst LOCALAPPDATA und nicht os.UserCacheDir: das ist zwar auf
		// Windows derselbe Ordner, gilt aber als wegwerfbarer Cache.
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "wanderwelt")
		}
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "AppData", "Local", "wanderwelt")
		}
	} else if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "share", "wanderwelt")
	}
	return filepath.Join(os.TempDir(), "wanderwelt")
}

// HomeKey ist eine kurze Kennung für Home() (Groß-/Kleinschreibung egal)
// für Namen, die je Datenverzeichnis eindeutig sein müssen: die Sperre der
// Instanz, Sperre und Fensterklasse des Fensters.
func HomeKey() string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(Home()))))
	return hex.EncodeToString(sum[:8])
}

// GroupPath ist der Ort der Gruppendatei der aktiven Gruppe: Dir()/gruppe.yaml
// (siehe gruppen.go).
func GroupPath() string { return filepath.Join(Dir(), groupFileName) }

// Group ist der Inhalt der Gruppendatei (siehe deploy/gruppe.example.yaml).
type Group struct {
	// Coordinator ist die Basis-Adresse des Koordinators, ohne "/" am Ende.
	Coordinator string `yaml:"coordinator"`
	// TunnelServer ist der tunneld-Server für die Freigabe des Minecraft-Ports.
	TunnelServer string `yaml:"tunnel_server"`
	// Token ist der persönliche tunnel-API-Token des Mitglieds (geheim).
	Token string `yaml:"token"`
	// DevIdentityEmail wird nur gegen einen Koordinator mit auth.mode: dev
	// als Identität mitgeschickt (lokale Tests).
	DevIdentityEmail string `yaml:"dev_identity_email"`
	// TunnelInsecureSkipVerify gilt nur für den tunnel-Client (lokale Tests
	// mit selbst signiertem Zertifikat), nie für die Verbindung zum
	// Koordinator.
	TunnelInsecureSkipVerify bool `yaml:"tunnel_insecure_skip_verify"`
	// WebURL ist optional die Webseite des tunnel-Servers (Konto, Token und
	// Verzeichnis der Gruppen für den Reiter „Entdecken“, https). Fehlt sie,
	// gilt https://<Host von tunnel_server> (siehe WebBase).
	WebURL string `yaml:"web_url,omitempty"`
}

// LoadGroup liest und prüft die Gruppendatei aus path. Unbekannte Schlüssel
// sind Fehler. coordinator ist Pflicht (https; http nur für Loopback-
// Adressen in lokalen Tests); token und tunnel_server sind Pflicht, außer
// dev_identity_email ist gesetzt (Dev-Modus ohne tunneld).
func LoadGroup(path string) (*Group, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseGroup(path, data)
}

// InstallGroup prüft die Gruppendatei src und kopiert sie unverändert
// (samt Kommentaren) atomar nach gruppen/<id>/gruppe.yaml und macht sie zur
// aktiven Gruppe.
func InstallGroup(src string) (*Group, error) { return installGroup(src, true) }

// InstallGroupInactive wie InstallGroup, wählt die Gruppe aber nicht aus (eine
// weitere Gruppe hinzufügen, ohne dass sich unter einem laufenden Betrieb die
// aktive Gruppe ändert).
func InstallGroupInactive(src string) (*Group, error) { return installGroup(src, false) }

func installGroup(src string, activate bool) (*Group, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	g, err := parseGroup(src, data)
	if err != nil {
		return nil, err
	}
	dir, id, err := target(g)
	if err != nil {
		return nil, err
	}
	if err := ensureNotSymlink(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, groupFileName), data); err != nil {
		return nil, err
	}
	if activate && id != "" {
		if err := setActiveFile(id); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func parseGroup(name string, data []byte) (*Group, error) {
	var g Group
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&g); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: Gruppendatei ist leer", name)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	g.Coordinator = strings.TrimRight(strings.TrimSpace(g.Coordinator), "/")
	g.TunnelServer = strings.TrimSpace(g.TunnelServer)
	g.Token = strings.TrimSpace(g.Token)
	g.DevIdentityEmail = strings.TrimSpace(g.DevIdentityEmail)
	g.WebURL = strings.TrimRight(strings.TrimSpace(g.WebURL), "/")

	if g.WebURL != "" {
		if err := checkWebURL(g.WebURL); err != nil {
			return nil, fmt.Errorf("%s: web_url: %w", name, err)
		}
	}
	if err := checkCoordinator(g.Coordinator); err != nil {
		return nil, fmt.Errorf("%s: coordinator: %w", name, err)
	}
	if g.DevIdentityEmail == "" {
		if g.Token == "" {
			return nil, fmt.Errorf("%s: token fehlt (persönlicher tunnel-API-Token)", name)
		}
		if g.TunnelServer == "" {
			return nil, fmt.Errorf("%s: tunnel_server fehlt", name)
		}
	}
	return &g, nil
}

// checkCoordinator verlangt eine absolute https-Adresse ohne Query und
// Fragment. http ist nur für Loopback erlaubt, weil sonst der Token im
// Klartext über das Netz ginge.
func checkCoordinator(s string) error {
	if s == "" {
		return errors.New("fehlt")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("ungültige Adresse %q: %w", s, err)
	}
	if u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("ungültige Adresse %q (erwartet z. B. https://wanderwelt.example.org)", s)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%q: http nur für lokale Tests (localhost), sonst https", s)
	default:
		return fmt.Errorf("%q: Schema muss https sein", s)
	}
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// State ist der lokale Zustand des Starters (Home()/state.json).
type State struct {
	// MachineID ist die zufällige Kennung dieses PCs (bei der Installation
	// erzeugt).
	MachineID string `json:"machine_id"`
	// InstalledVersion ist die installierte Version des Starters.
	InstalledVersion string `json:"installed_version"`
	// LastUploadedGeneration ist die Basis des lokalen Stands: die letzte
	// Generation, die dieser PC erfolgreich eingereicht hat, oder – solange
	// er in einer Sitzung noch nichts eingereicht hat – die Generation, die
	// er als Host übernommen hat.
	LastUploadedGeneration int64 `json:"last_uploaded_generation"`
	// LastUploadedWorldID ist die Welt zu LastUploadedGeneration
	// (Generationsnummern beginnen in jeder Welt bei 1).
	LastUploadedWorldID string `json:"last_uploaded_world_id"`
	// PendingUpload: ein sauber gestoppter Stand wartet noch auf den Upload
	// (nach Fencing oder gescheitertem Schluss-Upload). Er baut auf
	// LastUploadedWorldID/LastUploadedGeneration auf.
	PendingUpload bool `json:"pending_upload"`
	// Optin: der PC ist als freigegebener PC angemeldet (Stufe 2).
	Optin bool `json:"optin"`
}

// LoadState liest Dir()/state.json der aktiven Gruppe. Fehlt die Datei, ist
// das ein leerer Zustand (frische Installation), kein Fehler.
func LoadState() (*State, error) {
	return loadStateIn(Dir())
}

func loadStateIn(dir string) (*State, error) {
	path := filepath.Join(dir, stateFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		s := &State{}
		stateDirs.Store(s, dir)
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	stateDirs.Store(&s, dir)
	return &s, nil
}

// Save schreibt den Zustand atomar (Temp-Datei, dann Umbenennen) nach
// state.json im Ordner, aus dem er geladen wurde (neuer Zustand: Dir()).
func (s *State) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := ""
	if v, ok := stateDirs.Load(s); ok {
		dir = v.(string)
	}
	if dir == "" {
		dir = Dir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, stateFileName), append(data, '\n'))
}

// writeFileAtomic schreibt data in eine Temp-Datei neben path und benennt sie
// dann um; Leser sehen also entweder den alten oder den neuen Inhalt.
func writeFileAtomic(path string, data []byte) error {
	return writeAtomic(path, data, false)
}

// writeFileAtomicPrivate wie writeFileAtomic; die Temp-Datei bekommt die
// Rechte (nur das eigene Konto), bevor der Inhalt hineinkommt.
func writeFileAtomicPrivate(path string, data []byte) error {
	return writeAtomic(path, data, true)
}

func writeAtomic(path string, data []byte, private bool) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if private {
		if err = restrictToOwner(tmp); err != nil {
			f.Close()
			return fmt.Errorf("Zugriff auf %s einschränken: %w", filepath.Base(path), err)
		}
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
	return renameRetry(tmp, path)
}

// renameRetry wiederholt das Umbenennen kurz: Unter Windows halten
// Virenscanner oder Indexdienst die Zieldatei manchmal einen Moment offen.
func renameRetry(from, to string) error {
	var err error
	for i := range 10 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}

// stateDirs merkt sich zu jedem geladenen Zustand den Gruppenordner, aus dem
// er kam: Save schreibt immer dorthin, auch wenn die aktive Gruppe
// inzwischen gewechselt hat (nie in eine andere Gruppe schreiben). Eine
// Map statt eines Feldes, damit Zustände weiter mit == vergleichbar bleiben.
var stateDirs sync.Map // *State -> string
