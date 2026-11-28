// Package karte ist die Logik hinter dem Reiter „Karte“ des Fensters, ohne
// Gio und ohne Netzwerk: Kachelraster und Sichtbereich, Manifest, die beiden
// Zwischenspeicher (Arbeitsspeicher, Platte) und der Lader, der nur sichtbare
// Kacheln holt. Der Vertrag steht in
// dem Kartenvertrag (Kachelraster 256×256, Stufen 0–6).
package karte

import (
	"math"
	"slices"
	"strconv"
)

const (
	// KachelPx: Kantenlänge einer Kachel in Pixeln.
	KachelPx = 256
	// MaxStufe: gröbste Stufe; Stufe z hat 2^z Blöcke je Pixel.
	MaxStufe = 6
	// MinZoom und MaxZoom begrenzen den Zoomfaktor (Bildschirmpixel je Block).
	MinZoom = 1.0 / 64
	MaxZoom = 8.0
	// MaxSichtbar: so viele Kacheln gleichzeitig höchstens in einem Bild.
	MaxSichtbar = 56
	// MaxKoordinate: Blockkoordinaten werden darauf begrenzt (Minecraft endet
	// bei 30 Mio.).
	MaxKoordinate = 60_000_000.0
)

// Kachel ist die Adresse einer Kachel: Stufe z, Spalte x, Zeile y.
type Kachel struct{ Z, X, Y int }

// String ist der Schlüssel im Manifest („z/x/y“).
func (k Kachel) String() string {
	return strconv.Itoa(k.Z) + "/" + strconv.Itoa(k.X) + "/" + strconv.Itoa(k.Y)
}

// Bereich ist ein Rechteck von Kachelindizes (beide Enden eingeschlossen).
type Bereich struct{ XMin, XMax, YMin, YMax int }

// Enthaelt: Liegt die Kachel (x, y) im Bereich?
func (b Bereich) Enthaelt(x, y int) bool {
	return x >= b.XMin && x <= b.XMax && y >= b.YMin && y <= b.YMax
}

// Spanne ist die Kantenlänge einer Kachel der Stufe z in Blöcken.
func Spanne(z int) int64 { return int64(KachelPx) << uint(z) }

// BlockZuKachel: Die Blockkoordinaten (bx, bz) gehören zur Kachel
// x = floor(bx / (256·2^z)), y = floor(bz / (256·2^z)); negative Werte
// runden nach unten.
func BlockZuKachel(z int, bx, bz int64) Kachel {
	s := Spanne(z)
	return Kachel{Z: z, X: int(floorDiv(bx, s)), Y: int(floorDiv(bz, s))}
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// KlemmeZoom begrenzt den Zoomfaktor auf MinZoom…MaxZoom (ungültig: 1).
func KlemmeZoom(zoom float64) float64 {
	if math.IsNaN(zoom) || zoom <= 0 {
		return 1
	}
	return min(max(zoom, MinZoom), MaxZoom)
}

// StufeFuerZoom wählt die Stufe zum Zoomfaktor (Bildschirmpixel je Block):
// die gröbste Stufe, deren Pixel noch mindestens so groß wie ein
// Bildschirmpixel sind (zoom·2^z ≥ 1). So wird nie unscharf verkleinert,
// sondern höchstens (Nearest-Neighbor) bis knapp unter Faktor 2 vergrößert.
func StufeFuerZoom(zoom float64) int {
	zoom = KlemmeZoom(zoom)
	z := int(math.Ceil(-math.Log2(zoom) - 1e-9))
	return min(max(z, 0), MaxStufe)
}

// StufeWaehlen wie StufeFuerZoom, aber nur aus den vorhandenen Stufen: fehlt
// die gewünschte, gilt die nächstfeinere, sonst die nächstgröbere. Ohne
// Angabe (levels leer) sind alle Stufen vorhanden.
func StufeWaehlen(zoom float64, levels []int) int {
	z := StufeFuerZoom(zoom)
	if len(levels) == 0 || slices.Contains(levels, z) {
		return z
	}
	best, found := -1, false
	for _, l := range levels {
		if l >= 0 && l <= z && l > best {
			best, found = l, true
		}
	}
	if found {
		return best
	}
	best = MaxStufe + 1
	for _, l := range levels {
		if l >= 0 && l <= MaxStufe && l < best {
			best = l
		}
	}
	if best > MaxStufe {
		return z
	}
	return best
}

// KachelIndex ist der Index der Kachel der Stufe z, in der die Blockkoordinate
// b liegt (nach unten gerundet, auch negativ).
func KachelIndex(z int, b float64) int {
	return int(math.Floor(b / float64(Spanne(z))))
}

// KachelEnde ist der Index der letzten Kachel der Stufe z, die ein Bereich
// berührt, der bei der Blockkoordinate b endet (b selbst gehört nicht mehr
// dazu).
func KachelEnde(z int, b float64) int {
	return int(math.Ceil(b/float64(Spanne(z)))) - 1
}

// Sichtbar liefert die Kacheln der Stufe z, die das Rechteck
// [x0, x1) × [z0, z1) (Blockkoordinaten) berühren und, falls grenzen nicht
// nil ist, darin liegen – die der Mitte zuerst, höchstens hoechstens Stück (≤ 0:
// MaxSichtbar). Ungültige Koordinaten ergeben nichts.
func Sichtbar(z int, x0, z0, x1, z1 float64, grenzen *Bereich, hoechstens int) []Kachel {
	if hoechstens <= 0 {
		hoechstens = MaxSichtbar
	}
	for _, v := range []float64{x0, z0, x1, z1} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
	}
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if z1 < z0 {
		z0, z1 = z1, z0
	}
	k := func(v float64) float64 { return min(max(v, -MaxKoordinate), MaxKoordinate) }
	x0, x1, z0, z1 = k(x0), k(x1), k(z0), k(z1)
	// Rechts und unten ist das Rechteck offen: Eine Kachel, die nur an der
	// Kante liegt, gehört nicht dazu.
	ix0, ix1 := KachelIndex(z, x0), max(KachelEnde(z, x1), KachelIndex(z, x0))
	iy0, iy1 := KachelIndex(z, z0), max(KachelEnde(z, z1), KachelIndex(z, z0))
	if grenzen != nil {
		ix0, ix1 = max(ix0, grenzen.XMin), min(ix1, grenzen.XMax)
		iy0, iy1 = max(iy0, grenzen.YMin), min(iy1, grenzen.YMax)
	}
	if ix1 < ix0 || iy1 < iy0 {
		return nil
	}
	// Zu große Bereiche (riesiges Fenster) nicht ausrechnen: um die Mitte kürzen.
	cx, cy := KachelIndex(z, (x0+x1)/2), KachelIndex(z, (z0+z1)/2)
	const halb = 32
	ix0, ix1 = max(ix0, cx-halb), min(ix1, cx+halb)
	iy0, iy1 = max(iy0, cy-halb), min(iy1, cy+halb)
	var out []Kachel
	for y := iy0; y <= iy1; y++ {
		for x := ix0; x <= ix1; x++ {
			out = append(out, Kachel{Z: z, X: x, Y: y})
		}
	}
	mx, my := (x0+x1)/2, (z0+z1)/2
	sp := float64(Spanne(z))
	abstand := func(t Kachel) float64 {
		dx := (float64(t.X)+0.5)*sp - mx
		dy := (float64(t.Y)+0.5)*sp - my
		return dx*dx + dy*dy
	}
	slices.SortStableFunc(out, func(a, b Kachel) int {
		da, db := abstand(a), abstand(b)
		switch {
		case da < db:
			return -1
		case da > db:
			return 1
		}
		return 0
	})
	if len(out) > hoechstens {
		out = out[:hoechstens]
	}
	return out
}
