// Package symbol zeichnet das wanderwelt-Symbol (Entwurf A „Pfad“): einen
// isometrischen Gras-/Erdblock in Pixelgrafik, über dessen Oberseite ein
// heller Weg läuft – eigene Grafik, nichts aus Minecraft. Jede Größe wird
// eigens gerastert, nicht verkleinert.
//
// Übertragen aus dem Entwurf symbol-entwurf.html (render(N, "pfad",
// zustand)) und dort Pixel für Pixel gleich (siehe testdata/referenz.txt).
// Deshalb steht die Rechnung hier in derselben Reihenfolge wie im
// JavaScript, und Produkte vor einer Addition werden ausdrücklich gerundet
// (float64(…)): Ein Compiler darf a*b+c sonst zu einem FMA zusammenziehen,
// und an Rändern kippte dann einzelne Pixel.
package symbol

import (
	"image"
	"image/color"
	"math"
)

// AppSizes sind die Größen des Programmsymbols (exe, Fenster, Hinweise).
var AppSizes = []int{16, 32, 48, 64, 256}

// Farben wie PAL im Entwurf.
var (
	palTop  = rgbs("#7cc444", "#6db83b", "#8ad152", "#74bd40")
	palGL   = rgbs("#5fa534", "#579c2f") // Gras, linke Seite
	palGR   = rgbs("#4f9029", "#478526") // Gras, rechte Seite
	palDL   = rgbs("#9a6a42", "#8c5e39", "#a47448")
	palDR   = rgbs("#7b5131", "#6f482b", "#845836")
	palOut  = rgb("#24170f")
	palPath = rgbs("#dcc690", "#cdb47c")
)

var sqrt5 = math.Sqrt(5)

// Render zeichnet das Symbol mit n×n Pixeln (ohne Zustandspunkt).
func Render(n int) *image.NRGBA { return render(n, nil) }

// RenderBadge zeichnet das Symbol mit einem Zustandspunkt der Farbe c
// unten rechts (dunkler Ring, wie im Entwurf).
func RenderBadge(n int, c color.Color) *image.NRGBA {
	nc := color.NRGBAModel.Convert(c).(color.NRGBA)
	return render(n, &nc)
}

// Scaled ist Render(n) mit jedem Pixel als k×k-Block – für Bilder, die
// Windows selbst auf die Anzeigegröße bringt (Hinweise).
func Scaled(n, k int) *image.NRGBA {
	src := Render(n)
	img := image.NewNRGBA(image.Rect(0, 0, n*k, n*k))
	for y := 0; y < n*k; y++ {
		for x := 0; x < n*k; x++ {
			img.SetNRGBA(x, y, src.NRGBAAt(x/k, y/k))
		}
	}
	return img
}

func render(n int, badge *color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	N := float64(n)
	cx, q := N/2, N/4
	inTop := func(x, y float64) bool { return math.Abs(x-cx)/cx+math.Abs(y-q)/q <= 1 }
	inHex := func(x, y float64) bool {
		if y < 0 || y > N || x < 0 || x > N {
			return false
		}
		if inTop(x, y) {
			return true
		}
		if y < q {
			return false
		}
		yl := y - float64(3*q) // untere Schrägen
		if y <= float64(3*q) {
			return true
		}
		return math.Abs(x-cx)/cx+yl/q <= 1
	}

	// Fläche: Oberseite mit Weg, darunter links und rechts Gras, dann Erde.
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			x, y := float64(px)+0.5, float64(py)+0.5
			if !inHex(x, y) {
				continue
			}
			var col color.NRGBA
			if inTop(x, y) {
				col = pick(palTop, px, py, 1)
				dx, dy := x-cx, y-q
				s := (float64(2*dx) - dy) / sqrt5
				t := (dx + float64(2*dy)) / sqrt5
				if math.Abs(t-float64(math.Sin(s*6/N*1.3)*N*0.06)) < float64(N*0.05)+0.25 {
					col = pick(palPath, px, py, 2)
				}
			} else {
				left := x < cx
				var edge float64
				if left {
					edge = q + float64((x/cx)*q)
				} else {
					edge = float64(2*q) - float64(((x-cx)/cx)*q)
				}
				depth := y - edge
				g := float64(N * 0.11)
				if !(rnd(px, 0, 7) < 0.5) {
					g += float64(N * 0.05)
				}
				switch {
				case depth < g && left:
					col = pick(palGL, px, py, 3)
				case depth < g:
					col = pick(palGR, px, py, 4)
				case left:
					col = pick(palDL, px, py, 5)
				default:
					col = pick(palDR, px, py, 6)
				}
			}
			img.SetNRGBA(px, py, col)
		}
	}

	// Umriss für die Lesbarkeit auf hellen und dunklen Leisten: jeder
	// gefüllte Pixel mit einem leeren Nachbarn (oder am Rand).
	solid := make([]bool, n*n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			solid[y*n+x] = img.NRGBAAt(x, y).A != 0
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !solid[y*n+x] {
				continue
			}
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				X, Y := x+d[0], y+d[1]
				if X < 0 || Y < 0 || X >= n || Y >= n || !solid[Y*n+X] {
					img.SetNRGBA(x, y, palOut)
					break
				}
			}
		}
	}

	// Zustandspunkt unten rechts mit dunklem Ring.
	if badge != nil {
		bx, by, br := float64(N*0.8), float64(N*0.8), float64(N*0.19)
		ring := 0.9
		if n >= 32 {
			ring = 1.4
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				d := hypot(float64(x)+0.5-bx, float64(y)+0.5-by)
				if d <= br+ring {
					img.SetNRGBA(x, y, palOut)
				}
				if d <= br {
					img.SetNRGBA(x, y, *badge)
				}
			}
		}
	}
	return img
}

// rnd ist der Pixel-Zufall des Entwurfs (ganzzahliger Hash, Ergebnis in
// [0, 1]). Math.imul und >>> 0 entsprechen uint32-Rechnung mit Überlauf.
func rnd(x, y, s int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263 + uint32(s)*1442695041
	h = (h ^ h>>13) * 1274126177
	return float64((h^h>>16)&0xffff) / 0xffff
}

// pick wählt eine Farbe aus a: a[floor(rnd·len) % len].
func pick(a []color.NRGBA, x, y, s int) color.NRGBA {
	return a[int(math.Floor(rnd(x, y, s)*float64(len(a))))%len(a)]
}

// hypot rechnet wie Math.hypot in V8 (größter Betrag mal Wurzel der Summe
// der Quadrate der Verhältnisse); math.Hypot ist nicht auf jeder Plattform
// gleich gerundet.
func hypot(a, b float64) float64 {
	a, b = math.Abs(a), math.Abs(b)
	m := math.Max(a, b)
	if m == 0 {
		return 0
	}
	p, r := a/m, b/m
	return math.Sqrt(float64(p*p)+float64(r*r)) * m
}

func rgb(s string) color.NRGBA {
	var v [3]uint8
	for i := range v {
		v[i] = unhex(s[1+2*i])<<4 | unhex(s[2+2*i])
	}
	return color.NRGBA{v[0], v[1], v[2], 0xff}
}

func unhex(c byte) uint8 {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	panic("symbol: keine Hex-Ziffer: " + string(c))
}

func rgbs(s ...string) []color.NRGBA {
	out := make([]color.NRGBA, len(s))
	for i, c := range s {
		out[i] = rgb(c)
	}
	return out
}
