// Package config lädt die Konfiguration des Koordinators (Mitglieder,
// Welt-Einstellungen, Speicher-Budget, Stand-Abstand, Leerlauf-Frist) und
// das Versions-Manifest.
package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// DefaultStorageBudget gilt, wenn storage_budget fehlt: 15 GiB.
const DefaultStorageBudget int64 = 15 << 30

// DefaultSnapshotInterval gilt, wenn snapshot_interval fehlt: 5 Minuten.
const DefaultSnapshotInterval = 5 * time.Minute

// DefaultEmptyTimeout gilt, wenn empty_timeout fehlt: 10 Minuten.
const DefaultEmptyTimeout = 10 * time.Minute

// Erlaubte Bereiche für snapshot_interval und empty_timeout (wie beim Host,
// siehe api.HostSettings).
const (
	minSnapshotInterval = api.MinSnapshotIntervalS * time.Second
	maxSnapshotInterval = api.MaxSnapshotIntervalS * time.Second
	minEmptyTimeout     = api.MinEmptyTimeoutS * time.Second
	maxEmptyTimeout     = api.MaxEmptyTimeoutS * time.Second
)

// Config ist die Konfiguration des Koordinators.
type Config struct {
	Listen           string `yaml:"listen"`
	DataDir          string `yaml:"data_dir"`
	MinecraftAddress string `yaml:"minecraft_address"`
	Auth             struct {
		Mode string `yaml:"mode"` // "tunneld" | "dev"
	} `yaml:"auth"`
	PortHolder struct {
		API        string `yaml:"api"`
		SecretFile string `yaml:"secret_file"`
		Port       int    `yaml:"port"`
		// HandoverPorts: Ersatz-Ports für die Übergabe, genau 0 oder 2
		// Einträge (siehe Load).
		HandoverPorts []int `yaml:"handover_ports"`
	} `yaml:"port_holder"`
	// SnapshotInterval: Abstand der Zwischenstände jedes Hosts, aus
	// snapshot_interval (Go-Dauer wie "5m"), 1m–30m; Standard
	// DefaultSnapshotInterval.
	SnapshotInterval time.Duration `yaml:"-"`
	// EmptyTimeout: Leerlauf-Frist jedes Hosts (so lange ohne Spieler, dann
	// endet die Sitzung; nicht im Dauerbetrieb), aus empty_timeout (Go-Dauer
	// wie "10m"), 1m–60m; Standard DefaultEmptyTimeout.
	EmptyTimeout time.Duration `yaml:"-"`

	// Provisioning: tunneld-CLI und Gruppendatei für „mitglied …“
	// (provisioning.go); für serve ohne Bedeutung.
	Provisioning Provisioning `yaml:"provisioning"`
	// StorageBudget in Bytes, aus storage_budget ("15GiB", "512MiB" oder
	// reine Bytes); Standard DefaultStorageBudget.
	StorageBudget int64             `yaml:"-"`
	Members       []Member          `yaml:"members"`
	WorldSettings map[string]string `yaml:"world_settings"`
	ManifestFile  string            `yaml:"manifest_file"`
	// Gruppe: Titel und Beschreibung für GET /v1/info (Entdecken-Reiter).
	Gruppe Gruppe `yaml:"gruppe"`
	// NoDiscordJoin: beitritt_ueber_discord ist false; Standard (Schlüssel
	// fehlt): Beitritt über Discord erlaubt.
	NoDiscordJoin bool `yaml:"-"`
}

// Grenzen für gruppe.titel und gruppe.beschreibung (Zeichen).
const (
	MaxGroupTitle       = 80
	MaxGroupDescription = 400
)

// Gruppe ist die öffentliche Beschreibung der Gruppe (nur Text, nie HTML).
type Gruppe struct {
	Titel        string `yaml:"titel"`
	Beschreibung string `yaml:"beschreibung"`
}

// checkText prüft Länge (in Zeichen) und Steuerzeichen (auch Zeilenumbruch).
func checkText(s string, max int) error {
	if n := len([]rune(s)); n > max {
		return fmt.Errorf("%d Zeichen, erlaubt sind höchstens %d", n, max)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("Steuerzeichen (auch Zeilenumbruch) sind nicht erlaubt")
		}
	}
	return nil
}

// Member ist ein Mitglied der Gruppe: tunnel-Konto (E-Mail) und
// Minecraft-Name. Der Name darf fehlen: Dann meldet ihn das Mitglied selbst
// über den Starter (Überlagerung names.json, roles.go); bis dahin fehlt es
// in der Whitelist.
type Member struct {
	Email     string `yaml:"email"`
	Minecraft string `yaml:"minecraft"`
	Admin     bool   `yaml:"admin"`
	Op        bool   `yaml:"op"`
}

// rawConfig ist die Datei-Form: storage_budget, snapshot_interval und
// empty_timeout stehen dort als Text.
type rawConfig struct {
	Config           `yaml:",inline"`
	StorageBudget    string `yaml:"storage_budget"`
	SnapshotInterval string `yaml:"snapshot_interval"`
	EmptyTimeout     string `yaml:"empty_timeout"`
	// BeitrittUeberDiscord: nil (fehlt) heißt erlaubt.
	BeitrittUeberDiscord *bool `yaml:"beitritt_ueber_discord"`
}

// Load liest die Konfiguration aus path. Unbekannte Schlüssel, ein
// unbekannter auth.mode, doppelte Mitglieder (E-Mail ohne Rücksicht auf
// Groß/Klein) und eine ungültige Größe sind Fehler.
func Load(path string) (*Config, error) {
	var raw rawConfig
	if err := decodeStrict(path, &raw); err != nil {
		return nil, err
	}
	c := raw.Config

	switch c.Auth.Mode {
	case "tunneld", "dev":
	default:
		return nil, fmt.Errorf("%s: auth.mode muss \"tunneld\" oder \"dev\" sein, nicht %q", path, c.Auth.Mode)
	}

	c.Gruppe.Titel = strings.TrimSpace(c.Gruppe.Titel)
	c.Gruppe.Beschreibung = strings.TrimSpace(c.Gruppe.Beschreibung)
	if err := checkText(c.Gruppe.Titel, MaxGroupTitle); err != nil {
		return nil, fmt.Errorf("%s: gruppe.titel: %w", path, err)
	}
	if err := checkText(c.Gruppe.Beschreibung, MaxGroupDescription); err != nil {
		return nil, fmt.Errorf("%s: gruppe.beschreibung: %w", path, err)
	}
	c.NoDiscordJoin = raw.BeitrittUeberDiscord != nil && !*raw.BeitrittUeberDiscord

	seen := make(map[string]bool, len(c.Members))
	for i, m := range c.Members {
		if m.Email == "" {
			return nil, fmt.Errorf("%s: members[%d]: email ist Pflicht", path, i)
		}
		key := strings.ToLower(m.Email)
		if seen[key] {
			return nil, fmt.Errorf("%s: Mitglied %q steht doppelt in members", path, m.Email)
		}
		seen[key] = true
	}

	budget, err := parseSize(raw.StorageBudget)
	if err != nil {
		return nil, fmt.Errorf("%s: storage_budget: %w", path, err)
	}
	c.StorageBudget = budget

	interval, err := parseDuration(raw.SnapshotInterval, DefaultSnapshotInterval, minSnapshotInterval, maxSnapshotInterval)
	if err != nil {
		return nil, fmt.Errorf("%s: snapshot_interval: %w", path, err)
	}
	c.SnapshotInterval = interval

	timeout, err := parseDuration(raw.EmptyTimeout, DefaultEmptyTimeout, minEmptyTimeout, maxEmptyTimeout)
	if err != nil {
		return nil, fmt.Errorf("%s: empty_timeout: %w", path, err)
	}
	c.EmptyTimeout = timeout

	if err := checkHandoverPorts(c.PortHolder.HandoverPorts, c.PortHolder.Port); err != nil {
		return nil, fmt.Errorf("%s: port_holder.handover_ports: %w", path, err)
	}

	return &c, nil
}

// checkHandoverPorts prüft die Ersatz-Ports der Übergabe: genau 0 oder 2
// Einträge, jeder 1–65535, die beiden verschieden und beide ungleich dem
// Haupt-Port.
func checkHandoverPorts(ports []int, main int) error {
	switch len(ports) {
	case 0:
		return nil
	case 2:
	default:
		return fmt.Errorf("genau 2 Ports erwartet, nicht %d", len(ports))
	}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("Port %d ist kein gültiger Port", p)
		}
		if p == main {
			return fmt.Errorf("Port %d ist gleich dem Haupt-Port", p)
		}
	}
	if ports[0] == ports[1] {
		return fmt.Errorf("Port %d steht doppelt", ports[0])
	}
	return nil
}

// parseDuration liest eine Dauer wie snapshot_interval oder empty_timeout
// ("5m", "90s", "1m30s"). Leer heißt def; erlaubt ist lo–hi (ganze
// Minuten).
func parseDuration(s string, def, lo, hi time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("ungültige Dauer %q (erlaubt: Go-Dauer wie 5m oder 90s)", s)
	}
	if d < lo || d > hi {
		return 0, fmt.Errorf("%q liegt außerhalb von %dm–%dm", s, lo/time.Minute, hi/time.Minute)
	}
	return d, nil
}

// Member sucht ein Mitglied über die E-Mail; Groß/Klein spielt keine Rolle.
func (c *Config) Member(email string) (Member, bool) {
	want := strings.ToLower(email)
	for _, m := range c.Members {
		if strings.ToLower(m.Email) == want {
			return m, true
		}
	}
	return Member{}, false
}

// LoadManifest liest das Versions-Manifest (YAML) aus path.
func LoadManifest(path string) (*api.Manifest, error) {
	var m api.Manifest
	if err := decodeStrict(path, &m); err != nil {
		return nil, err
	}
	if m.MinecraftVersion == "" {
		return nil, fmt.Errorf("%s: minecraft_version fehlt", path)
	}
	if err := api.ValidateConfigs(m.Configs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// decodeStrict liest ein YAML-Dokument aus path in v; unbekannte Schlüssel
// sind Fehler.
func decodeStrict(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: Datei ist leer", path)
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

var sizeUnits = []struct {
	suffix string
	factor int64
}{
	// Längere Endungen zuerst, sonst passt "B" auf "15GiB".
	{"TiB", 1 << 40},
	{"GiB", 1 << 30},
	{"MiB", 1 << 20},
	{"KiB", 1 << 10},
	{"B", 1},
}

// parseSize liest Größen wie "15GiB", "512 MiB" oder "16106127360" (Bytes).
// Leer heißt Standard (DefaultStorageBudget).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultStorageBudget, nil
	}
	num, factor := s, int64(1)
	for _, u := range sizeUnits {
		if strings.HasSuffix(s, u.suffix) {
			num, factor = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.factor
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("ungültige Größe %q (erlaubt: ganze Zahl mit KiB, MiB, GiB, TiB oder in Bytes)", s)
	}
	if n > math.MaxInt64/factor {
		return 0, fmt.Errorf("Größe %q ist zu groß", s)
	}
	return n * factor, nil
}
