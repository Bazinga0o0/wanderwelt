package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const baseConfig = `
listen: 127.0.0.1:8470
data_dir: /var/lib/wanderwelt
minecraft_address: example.org:15565
manifest_file: /etc/wanderwelt/manifest.yaml
`

func TestLoadRejectsBadConfig(t *testing.T) {
	const fifteenGiB = 16106127360

	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		errHas  string // Teil der Fehlermeldung (leer: nicht geprüft)
		check   func(t *testing.T, c *Config)
	}{
		{
			name: "unbekannter Schlüssel",
			yaml: baseConfig + `
auth: {mode: dev}
farbe: blau
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
		},
		{
			name: "auth.mode weder tunneld noch dev",
			yaml: baseConfig + `
auth: {mode: offen}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
		},
		{
			name: "doppelte Mitglieds-E-Mail, Groß/Klein egal",
			yaml: baseConfig + `
auth: {mode: tunneld}
members:
  - {email: lukas@x.de, minecraft: Lukas}
  - {email: Lukas@X.DE, minecraft: Lukas2}
`,
			wantErr: true,
		},
		{
			name: "storage_budget 15GiB",
			yaml: baseConfig + `
auth: {mode: tunneld}
storage_budget: 15GiB
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: func(t *testing.T, c *Config) {
				if c.StorageBudget != fifteenGiB {
					t.Errorf("StorageBudget = %d, want %d", c.StorageBudget, fifteenGiB)
				}
			},
		},
		{
			name: "storage_budget fehlt → Standard 15 GiB",
			yaml: baseConfig + `
auth: {mode: dev}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: func(t *testing.T, c *Config) {
				if c.StorageBudget != fifteenGiB {
					t.Errorf("StorageBudget = %d, want %d", c.StorageBudget, fifteenGiB)
				}
			},
		},
		{
			name: "snapshot_interval fehlt → Standard 5m",
			yaml: baseConfig + `
auth: {mode: dev}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: wantInterval(5 * time.Minute),
		},
		{
			name: "snapshot_interval 10m",
			yaml: baseConfig + `
auth: {mode: dev}
snapshot_interval: 10m
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: wantInterval(10 * time.Minute),
		},
		{
			name: "snapshot_interval untere Grenze 1m",
			yaml: baseConfig + `
auth: {mode: dev}
snapshot_interval: 1m
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: wantInterval(time.Minute),
		},
		{
			name: "snapshot_interval obere Grenze 30m",
			yaml: baseConfig + `
auth: {mode: dev}
snapshot_interval: 30m
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: wantInterval(30 * time.Minute),
		},
		{
			name: "snapshot_interval zu klein (59s)",
			yaml: baseConfig + `
auth: {mode: dev}
snapshot_interval: 59s
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
		},
		{
			name: "snapshot_interval zu groß (30m1s)",
			yaml: baseConfig + `
auth: {mode: dev}
snapshot_interval: 30m1s
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
		},
		{
			name: "snapshot_interval ohne Einheit",
			yaml: baseConfig + `
auth: {mode: dev}
snapshot_interval: 300
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
		},
		{
			name: "empty_timeout fehlt → Standard 10m",
			yaml: baseConfig + `
auth: {mode: dev}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: wantEmptyTimeout(10 * time.Minute),
		},
		{
			name: "empty_timeout untere Grenze 1m",
			yaml: baseConfig + `
auth: {mode: dev}
empty_timeout: 1m
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: wantEmptyTimeout(time.Minute),
		},
		{
			name: "empty_timeout obere Grenze 60m",
			yaml: baseConfig + `
auth: {mode: dev}
empty_timeout: 60m
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: wantEmptyTimeout(60 * time.Minute),
		},
		{
			name: "empty_timeout zu klein (59s)",
			yaml: baseConfig + `
auth: {mode: dev}
empty_timeout: 59s
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
			errHas:  "empty_timeout: \"59s\" liegt außerhalb von 1m–60m",
		},
		{
			name: "empty_timeout zu groß (61m)",
			yaml: baseConfig + `
auth: {mode: dev}
empty_timeout: 61m
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
			errHas:  "empty_timeout: \"61m\" liegt außerhalb von 1m–60m",
		},
		{
			name: "empty_timeout ohne Einheit",
			yaml: baseConfig + `
auth: {mode: dev}
empty_timeout: 300
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
			errHas:  "empty_timeout: ungültige Dauer \"300\"",
		},
		{
			name: "handover_ports zwei gültige Ports",
			yaml: baseConfig + `
auth: {mode: dev}
port_holder: {port: 25565, handover_ports: [25566, 25567]}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: func(t *testing.T, c *Config) {
				if got := c.PortHolder.HandoverPorts; len(got) != 2 || got[0] != 25566 || got[1] != 25567 {
					t.Errorf("HandoverPorts = %v, want [25566 25567]", got)
				}
			},
		},
		{
			name: "handover_ports fehlt → keine Ersatz-Ports",
			yaml: baseConfig + `
auth: {mode: dev}
port_holder: {port: 25565}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			check: func(t *testing.T, c *Config) {
				if got := c.PortHolder.HandoverPorts; len(got) != 0 {
					t.Errorf("HandoverPorts = %v, want leer", got)
				}
			},
		},
		{
			name: "handover_ports nur ein Eintrag",
			yaml: baseConfig + `
auth: {mode: dev}
port_holder: {port: 25565, handover_ports: [25566]}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
			errHas:  "genau 2 Ports erwartet, nicht 1",
		},
		{
			name: "handover_ports doppelt",
			yaml: baseConfig + `
auth: {mode: dev}
port_holder: {port: 25565, handover_ports: [25566, 25566]}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
			errHas:  "Port 25566 steht doppelt",
		},
		{
			name: "handover_ports gleich Haupt-Port",
			yaml: baseConfig + `
auth: {mode: dev}
port_holder: {port: 25565, handover_ports: [25565, 25566]}
members: [{email: lukas@x.de, minecraft: Lukas}]
`,
			wantErr: true,
			errHas:  "Port 25565 ist gleich dem Haupt-Port",
		},
		{
			name: "Member findet E-Mail unabhängig von Groß/Klein",
			yaml: baseConfig + `
auth: {mode: dev}
members:
  - {email: anna@x.de, minecraft: Anna}
  - {email: lukas@x.de, minecraft: Lukas, admin: true, op: true}
`,
			check: func(t *testing.T, c *Config) {
				m, ok := c.Member("LUKAS@x.de")
				if !ok {
					t.Fatal(`Member("LUKAS@x.de") nicht gefunden`)
				}
				if m.Email != "lukas@x.de" || m.Minecraft != "Lukas" || !m.Admin || !m.Op {
					t.Errorf("Member = %+v, want lukas@x.de/Lukas/admin/op", m)
				}
				if _, ok := c.Member("fremd@x.de"); ok {
					t.Error(`Member("fremd@x.de") gefunden, obwohl kein Mitglied`)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "koordinator.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load: kein Fehler, erwartet einen (Config: %+v)", c)
				}
				if !strings.Contains(err.Error(), tt.errHas) {
					t.Fatalf("Load: %v, erwartet einen Fehler mit %q", err, tt.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

// wantInterval prüft Config.SnapshotInterval.
func wantInterval(want time.Duration) func(t *testing.T, c *Config) {
	return func(t *testing.T, c *Config) {
		if c.SnapshotInterval != want {
			t.Errorf("SnapshotInterval = %v, want %v", c.SnapshotInterval, want)
		}
	}
}

// wantEmptyTimeout prüft Config.EmptyTimeout.
func wantEmptyTimeout(want time.Duration) func(t *testing.T, c *Config) {
	return func(t *testing.T, c *Config) {
		if c.EmptyTimeout != want {
			t.Errorf("EmptyTimeout = %v, want %v", c.EmptyTimeout, want)
		}
	}
}

func TestLoadManifestConfigs(t *testing.T) {
	h := "sha256:" + strings.Repeat("0", 64)
	write := func(configs string) string {
		p := filepath.Join(t.TempDir(), "manifest.yaml")
		if err := os.WriteFile(p, []byte("minecraft_version: \"26.2\"\n"+configs), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Ohne configs wie bisher.
	m, err := LoadManifest(write(""))
	if err != nil || len(m.Configs) != 0 {
		t.Fatalf("ohne configs: %v %+v", err, m)
	}

	m, err = LoadManifest(write(`configs:
  - path: config/viper_spawn_control.json
    url: "wanderwelt:viper_spawn_control.json"
    hash: "` + h + `"
  - path: config/x.toml
    url: "https://example.org/x.toml"
    hash: "` + h + `"
    mode: default
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Configs) != 2 || m.Configs[0].EffectiveMode() != "enforce" || m.Configs[1].Mode != "default" ||
		m.Configs[0].Path != "config/viper_spawn_control.json" {
		t.Fatalf("falsch gelesen: %+v", m.Configs)
	}

	for name, y := range map[string]string{
		"Pfad nach oben":        "configs:\n  - {path: config/../eula.txt, url: \"wanderwelt:a\", hash: \"" + h + "\"}\n",
		"absolut":               "configs:\n  - {path: /etc/a.json, url: \"wanderwelt:a\", hash: \"" + h + "\"}\n",
		"Endung":                "configs:\n  - {path: config/a.jar, url: \"wanderwelt:a\", hash: \"" + h + "\"}\n",
		"Modus":                 "configs:\n  - {path: config/a.json, url: \"wanderwelt:a\", hash: \"" + h + "\", mode: x}\n",
		"unbekannter Schlüssel": "configs:\n  - {path: config/a.json, url: \"wanderwelt:a\", hash: \"" + h + "\", sha256: x}\n",
	} {
		_, err := LoadManifest(write(y))
		if err == nil {
			t.Errorf("%s: kein Fehler", name)
		} else if name != "unbekannter Schlüssel" && !strings.Contains(err.Error(), "configs[1]") {
			t.Errorf("%s: Meldung ohne Eintragsnummer: %v", name, err)
		}
	}
}
