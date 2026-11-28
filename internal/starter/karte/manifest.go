package karte

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxManifestBytes: größer darf eine Manifest-Datei nicht sein.
	MaxManifestBytes = 64 << 20
	// MaxKacheln: so viele Kacheln darf ein Manifest höchstens nennen.
	MaxKacheln = 500_000
	// MaxKachelBytes: größer darf eine einzelne PNG-Kachel nicht sein.
	MaxKachelBytes = 4 << 20
	// maxIndex begrenzt Kachelindizes (x, y) im Manifest.
	maxIndex = 1 << 20
)

// Fehler, die die Oberfläche unterscheidet.
var (
	// ErrNochNichtErzeugt: Der Koordinator hat (noch) keine Karte.
	ErrNochNichtErzeugt = errors.New("die Karte ist noch nicht erzeugt")
	// ErrKachelFehlt: Die Kachel gibt es nicht (ungenerierte Fläche).
	ErrKachelFehlt = errors.New("die Kachel gibt es nicht")
	// ErrManifestUngueltig: Das Manifest ist kaputt oder nicht lesbar.
	ErrManifestUngueltig = errors.New("das Manifest der Karte ist ungültig")
)

// Eintrag ist eine Kachel im Manifest: Hash der PNG-Bytes (zugleich ETag)
// und Größe.
type Eintrag struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest ist der geprüfte Inhalt von karte.json. Nur ein Manifest, das
// LeseManifest angenommen hat, ist gültig; es wird danach nicht verändert.
type Manifest struct {
	Format    int
	Source    string
	Generated time.Time
	levels    []int
	kacheln   map[Kachel]Eintrag
	grenzen   [MaxStufe + 1]*Bereich
}

// rohManifest ist die Form in der Datei (der Vertrag, nur das Nötige).
type rohManifest struct {
	Format    int                `json:"format"`
	TileSize  int                `json:"tile_size"`
	Levels    []int              `json:"levels"`
	Source    string             `json:"source"`
	Generated time.Time          `json:"generated"`
	Tiles     map[string]Eintrag `json:"tiles"`
}

func ungueltig(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrManifestUngueltig, fmt.Sprintf(format, a...))
}

// LeseManifest liest und prüft karte.json streng: Format 1, Kachelgröße 256,
// jeder Schlüssel „z/x/y“ mit Stufe 0…6 und kanonischen Zahlen, jeder Hash 64
// Hexzeichen (klein), Größen in Grenzen; unbekannte Felder werden
// ignoriert (der Vertrag darf wachsen). Die Grenzen (Bounds) rechnet es aus
// den Kacheln selbst; die der Datei werden nicht gebraucht.
func LeseManifest(data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, ungueltig("Datei zu groß")
	}
	var r rohManifest
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, ungueltig("nicht lesbar (%v)", err)
	}
	if r.Format != 1 {
		return nil, ungueltig("unbekanntes Format %d (erwartet 1)", r.Format)
	}
	if r.TileSize != KachelPx {
		return nil, ungueltig("Kachelgröße %d (erwartet %d)", r.TileSize, KachelPx)
	}
	if len(r.Tiles) > MaxKacheln {
		return nil, ungueltig("zu viele Kacheln (%d)", len(r.Tiles))
	}
	m := &Manifest{Format: r.Format, Source: r.Source, Generated: r.Generated, kacheln: make(map[Kachel]Eintrag, len(r.Tiles))}
	for _, l := range r.Levels {
		if l < 0 || l > MaxStufe {
			return nil, ungueltig("Stufe %d außerhalb von 0…%d", l, MaxStufe)
		}
		if !slices.Contains(m.levels, l) {
			m.levels = append(m.levels, l)
		}
	}
	slices.Sort(m.levels)
	for key, e := range r.Tiles {
		k, ok := ParseSchluessel(key)
		if !ok {
			return nil, ungueltig("Kachelschlüssel %q", kurz(key))
		}
		if !GueltigerHash(e.SHA256) {
			return nil, ungueltig("Hash der Kachel %s", key)
		}
		if e.Size < 0 || e.Size > MaxKachelBytes {
			return nil, ungueltig("Größe der Kachel %s", key)
		}
		if len(m.levels) > 0 && !slices.Contains(m.levels, k.Z) {
			return nil, ungueltig("Kachel %s liegt auf einer Stufe, die das Manifest nicht nennt", key)
		}
		m.kacheln[k] = e
		g := m.grenzen[k.Z]
		if g == nil {
			m.grenzen[k.Z] = &Bereich{XMin: k.X, XMax: k.X, YMin: k.Y, YMax: k.Y}
			continue
		}
		g.XMin, g.XMax = min(g.XMin, k.X), max(g.XMax, k.X)
		g.YMin, g.YMax = min(g.YMin, k.Y), max(g.YMax, k.Y)
	}
	if len(m.levels) == 0 {
		for z, g := range m.grenzen {
			if g != nil {
				m.levels = append(m.levels, z)
			}
		}
	}
	if len(m.kacheln) == 0 {
		return nil, ungueltig("keine Kacheln")
	}
	return m, nil
}

func kurz(s string) string {
	if len(s) > 24 {
		return strings.ToValidUTF8(s[:24], "?") + "…"
	}
	return strings.ToValidUTF8(s, "?")
}

// ParseSchluessel zerlegt „z/x/y“; nur kanonische ganze Zahlen (kein „+1“,
// keine führenden Nullen, kein „-0“), z in 0…6, x und y in ±2^20.
func ParseSchluessel(s string) (Kachel, bool) {
	teile := strings.Split(s, "/")
	if len(teile) != 3 {
		return Kachel{}, false
	}
	var v [3]int
	for i, t := range teile {
		n, err := strconv.Atoi(t)
		if err != nil || strconv.Itoa(n) != t {
			return Kachel{}, false
		}
		v[i] = n
	}
	k := Kachel{Z: v[0], X: v[1], Y: v[2]}
	if k.Z < 0 || k.Z > MaxStufe || k.X < -maxIndex || k.X > maxIndex || k.Y < -maxIndex || k.Y > maxIndex {
		return Kachel{}, false
	}
	return k, true
}

// GueltigerHash: genau 64 kleine Hexziffern (so heißen die Dateien im
// Zwischenspeicher; nie etwas anderes als Dateiname).
func GueltigerHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := range len(h) {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Hash liefert den Hash der Kachel; false: Kachel steht nicht im Manifest.
func (m *Manifest) Hash(k Kachel) (string, bool) {
	e, ok := m.kacheln[k]
	return e.SHA256, ok
}

// Anzahl der Kacheln.
func (m *Manifest) Anzahl() int { return len(m.kacheln) }

// Stufen sind die vorhandenen Stufen, aufsteigend.
func (m *Manifest) Stufen() []int { return slices.Clone(m.levels) }

// Grenzen: das kleinste Rechteck von Kachelindizes der Stufe z, das alle
// Kacheln enthält; nil ohne Kacheln auf der Stufe.
func (m *Manifest) Grenzen(z int) *Bereich {
	if z < 0 || z > MaxStufe || m.grenzen[z] == nil {
		return nil
	}
	b := *m.grenzen[z]
	return &b
}

// Mitte ist die Mitte der Grenzen von Stufe 0 in Blockkoordinaten (x, z);
// fehlt Stufe 0, die der feinsten vorhandenen. ok false ohne Kacheln.
func (m *Manifest) Mitte() (bx, bz float64, ok bool) {
	for z := 0; z <= MaxStufe; z++ {
		g := m.grenzen[z]
		if g == nil {
			continue
		}
		s := float64(Spanne(z))
		return float64(g.XMin+g.XMax+1) / 2 * s, float64(g.YMin+g.YMax+1) / 2 * s, true
	}
	return 0, 0, false
}

// Ausdehnung ist das Rechteck aller Kacheln der feinsten vorhandenen Stufe
// in Blockkoordinaten (x0, z0 links oben, x1, z1 rechts unten, offen);
// ok false ohne Kacheln.
func (m *Manifest) Ausdehnung() (x0, z0, x1, z1 float64, ok bool) {
	for z := 0; z <= MaxStufe; z++ {
		g := m.grenzen[z]
		if g == nil {
			continue
		}
		s := float64(Spanne(z))
		return float64(g.XMin) * s, float64(g.YMin) * s, float64(g.XMax+1) * s, float64(g.YMax+1) * s, true
	}
	return 0, 0, 0, 0, false
}

// Alter ist die Zeit seit der Erzeugung der Karte (ohne Angabe: 0, false).
func (m *Manifest) Alter(jetzt time.Time) (time.Duration, bool) {
	if m.Generated.IsZero() {
		return 0, false
	}
	return max(jetzt.Sub(m.Generated), 0), true
}

// Gleich: Beide Manifeste beschreiben dieselbe Karte (gleicher Stand, gleiche
// Kacheln mit gleichen Hashes).
func (m *Manifest) Gleich(o *Manifest) bool {
	if m == nil || o == nil {
		return m == o
	}
	if !m.Generated.Equal(o.Generated) || m.Source != o.Source || len(m.kacheln) != len(o.kacheln) {
		return false
	}
	for k, e := range m.kacheln {
		if o.kacheln[k].SHA256 != e.SHA256 {
			return false
		}
	}
	return true
}

// AlterText: „gerade eben“, „vor 5 Min.“, „vor 3 Std.“, „vor 2 Tagen“.
func AlterText(d time.Duration) string {
	m := int64(math.Floor(d.Minutes()))
	switch {
	case m < 1:
		return "gerade eben"
	case m < 60:
		return fmt.Sprintf("vor %d Min.", m)
	case m < 48*60:
		return fmt.Sprintf("vor %d Std.", m/60)
	}
	return fmt.Sprintf("vor %d Tagen", m/(24*60))
}
