package kartetest

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
)

// Landschaft füllt den Fake mit einer erfundenen Landschaft (Wasser, Strand,
// Wiese, Wald, Fels, Schnee mit Schattierung): die Kacheln x0…x1, y0…y1 der
// Stufe 0 und die dazugehörigen der gröberen Stufen bis 6, jede Stufe aus
// derselben Höhenfunktion gerechnet. Für Bildschirmfotos und Abläufe, die
// eine echte Karte brauchen; die Bilder sind Paletten-PNGs wie die des
// Tools.
func (k *Koordinator) Landschaft(t testing.TB, x0, x1, y0, y1 int) {
	t.Helper()
	pal := landschaftsPalette()
	for z := 0; z <= karte.MaxStufe; z++ {
		f := 1 << uint(z)
		for ty := floorDiv(y0, f); ty <= floorDiv(y1, f); ty++ {
			for tx := floorDiv(x0, f); tx <= floorDiv(x1, f); tx++ {
				img := image.NewPaletted(image.Rect(0, 0, karte.KachelPx, karte.KachelPx), pal)
				for py := range karte.KachelPx {
					for px := range karte.KachelPx {
						bx := float64((tx*karte.KachelPx+px)*f) + float64(f)/2
						bz := float64((ty*karte.KachelPx+py)*f) + float64(f)/2
						img.SetColorIndex(px, py, landschaftsFarbe(bx, bz, float64(f)))
					}
				}
				var b bytes.Buffer
				if err := png.Encode(&b, img); err != nil {
					t.Fatal(err)
				}
				k.Setze(karte.Kachel{Z: z, X: tx, Y: ty}, b.Bytes())
			}
		}
	}
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// Klassen und Schattierungsstufen der Palette.
const (
	klTief = iota
	klWasser
	klStrand
	klWiese
	klWald
	klFels
	klSchnee
	anzKlassen
	anzSchatten = 9
)

func landschaftsPalette() color.Palette {
	basis := [anzKlassen]color.RGBA{
		klTief:   {24, 58, 112, 255},
		klWasser: {40, 98, 168, 255},
		klStrand: {218, 204, 148, 255},
		klWiese:  {110, 168, 72, 255},
		klWald:   {52, 112, 52, 255},
		klFels:   {128, 124, 120, 255},
		klSchnee: {244, 246, 250, 255},
	}
	pal := make(color.Palette, 0, anzKlassen*anzSchatten)
	for c := range anzKlassen {
		for s := range anzSchatten {
			f := 0.72 + 0.07*float64(s) // 0,72 … 1,28
			pal = append(pal, color.RGBA{clamp8(float64(basis[c].R) * f), clamp8(float64(basis[c].G) * f), clamp8(float64(basis[c].B) * f), 255})
		}
	}
	return pal
}

func clamp8(v float64) uint8 { return uint8(min(max(v, 0), 255)) }

// landschaftsFarbe: Palettenindex für den Block (bx, bz); schritt ist die
// Blockbreite eines Pixels (für die Schattierung).
func landschaftsFarbe(bx, bz, schritt float64) uint8 {
	h := hoehe(bx, bz)
	var kl int
	switch {
	case h < 0.30:
		kl = klTief
	case h < 0.42:
		kl = klWasser
	case h < 0.46:
		kl = klStrand
	case h < 0.62:
		kl = klWiese
	case h < 0.76:
		kl = klWald
	case h < 0.88:
		kl = klFels
	default:
		kl = klSchnee
	}
	s := anzSchatten / 2
	if kl >= klStrand {
		// Licht von links oben: Hang nach rechts unten ist dunkler.
		d := hoehe(bx-schritt*2, bz-schritt*2) - h
		s = min(max(int(math.Round(float64(anzSchatten/2)+d/schritt*220)), 0), anzSchatten-1)
	}
	return uint8(kl*anzSchatten + s)
}

// hoehe: Höhenfunktion 0…1 aus mehreren Oktaven Value-Noise.
func hoehe(x, z float64) float64 {
	v, amp, sum := 0.0, 1.0, 0.0
	for _, skala := range []float64{900, 330, 120, 45, 17} {
		v += amp * rauschen(x/skala, z/skala)
		sum += amp
		amp *= 0.55
	}
	v /= sum
	// Etwas spreizen, damit alle Klassen vorkommen.
	return min(max((v-0.5)*1.9+0.5, 0), 1)
}

func rauschen(x, z float64) float64 {
	ix, iz := math.Floor(x), math.Floor(z)
	fx, fz := x-ix, z-iz
	fx, fz = fx*fx*(3-2*fx), fz*fz*(3-2*fz)
	a, b := wert(int(ix), int(iz)), wert(int(ix)+1, int(iz))
	c, d := wert(int(ix), int(iz)+1), wert(int(ix)+1, int(iz)+1)
	return a + (b-a)*fx + (c-a)*fz + (a-b-c+d)*fx*fz
}

func wert(x, z int) float64 {
	h := uint32(x)*374761393 + uint32(z)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return float64(h&0xffff) / 65535
}
