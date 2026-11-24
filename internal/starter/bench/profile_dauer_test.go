package bench

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Dauerbetrieb aus steht weder in der Profilmeldung an den Koordinator
// (ein älterer liest JSON streng und lehnte sie sonst ab) noch in
// profil.yaml (ein älterer Starter liest sie streng); an steht er in beiden
// und kommt beim Lesen zurück.
func TestProfileDauerbetriebOnlyWhenOn(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	p := defaultProfile(16<<10, 8)

	for _, on := range []bool{false, true} {
		p.Dauerbetrieb = on
		js, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(js), `"dauerbetrieb"`); got != on {
			t.Errorf("Dauerbetrieb %v: JSON %s enthält dauerbetrieb: %v", on, js, got)
		}
		if err := SaveProfile(p); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(ProfilePath())
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(data), "\ndauerbetrieb:"); got != on {
			t.Errorf("Dauerbetrieb %v: profil.yaml enthält den Schlüssel: %v\n%s", on, got, data)
		}
		back, err := LoadProfile()
		if err != nil || back != p {
			t.Errorf("Dauerbetrieb %v: gelesen %+v, %v; erwartet %+v", on, back, err, p)
		}
	}
}
