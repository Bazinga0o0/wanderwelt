package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadGruppe(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "k.yaml")
	y := baseConfig + "auth: {mode: dev}\nmembers: [{email: a@x.de}]\n" + extra
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestGruppeUndDiscordSchalter(t *testing.T) {
	c, err := loadGruppe(t, "")
	if err != nil || c.NoDiscordJoin || c.Gruppe.Titel != "" {
		t.Fatalf("Standard: %+v %v", c, err)
	}
	c, err = loadGruppe(t, "beitritt_ueber_discord: false\ngruppe: {titel: ' Hallo ', beschreibung: Text}\n")
	if err != nil || !c.NoDiscordJoin || c.Gruppe.Titel != "Hallo" || c.Gruppe.Beschreibung != "Text" {
		t.Fatalf("gesetzt: %+v %v", c, err)
	}
	if c, err = loadGruppe(t, "beitritt_ueber_discord: true\n"); err != nil || c.NoDiscordJoin {
		t.Fatalf("true: %+v %v", c, err)
	}
	bad := map[string]string{
		"gruppe.titel":        "gruppe: {titel: \"" + strings.Repeat("x", MaxGroupTitle+1) + "\"}\n",
		"Steuerzeichen":       "gruppe: {titel: \"a\\nb\"}\n",
		"gruppe.beschreibung": "gruppe: {beschreibung: \"" + strings.Repeat("ä", MaxGroupDescription+1) + "\"}\n",
	}
	for want, y := range bad {
		if _, err := loadGruppe(t, y); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	if _, err := loadGruppe(t, "gruppe: {titel: \""+strings.Repeat("ä", MaxGroupTitle)+"\"}\n"); err != nil {
		t.Errorf("Grenze in Zeichen: %v", err)
	}
}
