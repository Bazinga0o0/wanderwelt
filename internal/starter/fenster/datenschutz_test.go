package fenster

import (
	"os"
	"strings"
	"testing"
)

// Datenschutz: Die Reiter Mitglieder und Beitrittsanfragen zeigen nie die
// E-Mail-Adresse anderer; sie zeigen Minecraft-Namen und die Kennung (für
// Rückfragen beim Serverbetreiber). Die Zeilen und Rückfragen fassen
// api.MemberInfo.Email und api.JoinRequestInfo.Email darum nicht an (die
// Aktionen nehmen Key(), die Anzeige ID).
func TestMitgliederUndAnfragenZeigenKeineEmail(t *testing.T) {
	for _, f := range []string{"mitglieder.go", "anfragen.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, z := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(z, "//")
			if strings.Contains(code, ".Email") {
				t.Errorf("%s:%d greift auf .Email zu: %s", f, i+1, strings.TrimSpace(z))
			}
		}
	}
}

func TestKennzeileNenntNieEineAdresse(t *testing.T) {
	for in, want := range map[string]string{
		"9d3b71c8a05e24f6":   "9d3b71c8a05e24f6",
		"":                   "–",
		"jemand@example.org": "–", // älterer Koordinator oder eigene Zeile: nie als Kennung anzeigen
	} {
		if got := kennzeile(in); got != want {
			t.Errorf("kennzeile(%q) = %q, erwartet %q", in, got, want)
		}
	}
}
