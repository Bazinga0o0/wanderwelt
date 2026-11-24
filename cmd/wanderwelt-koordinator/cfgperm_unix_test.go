//go:build unix

package main

import (
	"os"
	"strings"
	"testing"
)

// Die Tests laufen meist nicht als root: Ihre Konfigurationen gehören dem
// eigenen Benutzer.
func init() { configOwnerUID = os.Getuid() }

// „mitglied neu|entfernen“ führt reload_command (als root) und tunneld_bin
// aus der Konfiguration aus: Gehört sie nicht root oder dürfen Gruppe oder
// andere sie schreiben, bricht es vor jedem Aufruf ab. Lesen darf die Gruppe
// (0640, so legen die übrigen Tests sie an).
func TestMitgliedPrueftKonfigurationsrechte(t *testing.T) {
	tests := []struct {
		name  string
		mode  os.FileMode
		owner int
		want  string
	}{
		{name: "Gruppe darf schreiben", mode: 0o660, owner: os.Getuid(), want: "für Gruppe oder andere beschreibbar (0660)"},
		{name: "andere dürfen schreiben", mode: 0o642, owner: os.Getuid(), want: "für Gruppe oder andere beschreibbar (0642)"},
		{name: "gehört nicht root", mode: 0o640, owner: os.Getuid() + 1, want: "gehört nicht root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := configOwnerUID
			configOwnerUID = tt.owner
			t.Cleanup(func() { configOwnerUID = old })
			for _, sub := range []string{"neu", "entfernen"} {
				cfgPath, aus := setup(t, testConfig)
				if err := os.Chmod(cfgPath, tt.mode); err != nil {
					t.Fatal(err)
				}
				args := []string{"entfernen", "--config", cfgPath, "Bob_42"}
				if sub == "neu" {
					args = []string{"neu", "--name", "carla", "--minecraft", "Carla", "--aus", aus, "--config", cfgPath}
				}
				f := &fakeRunner{}
				code, _, stderr := runMitglied(t, f, args...)
				if code != 1 || !strings.Contains(stderr, tt.want) || !strings.Contains(stderr, "nichts geändert") {
					t.Errorf("%s: Exit %d, stderr: %s\nwant Exit 1 mit %q", sub, code, stderr, tt.want)
				}
				checkCalls(t, f.calls, nil)
				if data, _ := os.ReadFile(cfgPath); string(data) != testConfig {
					t.Errorf("%s: Konfiguration verändert", sub)
				}
			}
		})
	}
}
