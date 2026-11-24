package host

import (
	"strings"
	"testing"
)

// checkWorldVersion folgt api.CheckWorldVersion: gleiche, ältere oder
// unbekannte Version wird gehostet, eine neuere oder nicht vergleichbare
// nicht – mit dem passenden Grund.
func TestCheckWorldVersionMessages(t *testing.T) {
	for _, tc := range []struct {
		world, hosted, want string // want: Teil der Meldung, "" = kein Fehler
	}{
		{"26.1", "26.2", ""},
		{"", "26.2", ""},
		{"26.2", "26.2", ""},
		{"26.3", "26.2", "ein älterer Server würde sie beschädigen"},
		{"26w14a", "26.2", "lassen sich nicht vergleichen"},
	} {
		err := checkWorldVersion(tc.world, tc.hosted)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s auf %s: %v, erwartet kein Fehler", tc.world, tc.hosted, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.world)):
			t.Errorf("%s auf %s: %v, erwartet %q", tc.world, tc.hosted, err, tc.want)
		}
	}
}
