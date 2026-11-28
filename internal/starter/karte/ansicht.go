package karte

import "math"

// Ansicht ist der Ausschnitt der Karte, den man sieht: die Mitte in
// Blockkoordinaten (X nach Osten, Z nach Süden, im Bild also nach unten) und
// der Zoomfaktor in Bildschirmpixeln je Block. Alle Rechnungen hier sind
// reine Mathematik, ohne Oberfläche.
type Ansicht struct {
	X, Z float64
	Zoom float64
}

// Groesse ist die Größe des Zeichenbereichs in Bildschirmpixeln.
type Groesse struct{ B, H float64 }

// BlockBei liefert die Blockkoordinate (nicht gerundet) unter dem
// Bildschirmpunkt (px, py), gemessen von der linken oberen Ecke des
// Zeichenbereichs.
func (a Ansicht) BlockBei(px, py float64, g Groesse) (bx, bz float64) {
	z := KlemmeZoom(a.Zoom)
	return a.X + (px-g.B/2)/z, a.Z + (py-g.H/2)/z
}

// BildschirmBei ist die Umkehrung von BlockBei.
func (a Ansicht) BildschirmBei(bx, bz float64, g Groesse) (px, py float64) {
	z := KlemmeZoom(a.Zoom)
	return (bx-a.X)*z + g.B/2, (bz-a.Z)*z + g.H/2
}

// Verschieben schiebt den Inhalt um (dx, dy) Bildschirmpixel (Ziehen mit der
// Maus: Der Inhalt folgt der Hand).
func (a Ansicht) Verschieben(dx, dy float64) Ansicht {
	z := KlemmeZoom(a.Zoom)
	a.X -= dx / z
	a.Z -= dy / z
	return a
}

// ZoomUm vergrößert (faktor > 1) oder verkleinert um den Bildschirmpunkt
// (px, py): Der Block darunter bleibt darunter. Das Ergebnis bleibt in
// MinZoom…MaxZoom.
func (a Ansicht) ZoomUm(faktor, px, py float64, g Groesse) Ansicht {
	if math.IsNaN(faktor) || math.IsInf(faktor, 0) || faktor <= 0 {
		return a
	}
	bx, bz := a.BlockBei(px, py, g)
	a.Zoom = KlemmeZoom(KlemmeZoom(a.Zoom) * faktor)
	a.X = bx - (px-g.B/2)/a.Zoom
	a.Z = bz - (py-g.H/2)/a.Zoom
	return a
}

// Sichtbereich ist das Rechteck in Blockkoordinaten, das der Zeichenbereich
// zeigt, rundum um rand Bildschirmpixel erweitert (Vorladen).
func (a Ansicht) Sichtbereich(g Groesse, rand float64) (x0, z0, x1, z1 float64) {
	x0, z0 = a.BlockBei(-rand, -rand, g)
	x1, z1 = a.BlockBei(g.B+rand, g.H+rand, g)
	return
}

// Stufe ist die Kachelstufe, die zum Zoom passt (aus den vorhandenen Stufen).
func (a Ansicht) Stufe(levels []int) int { return StufeWaehlen(a.Zoom, levels) }

// Massstab: Bildschirmpixel je Kachelpixel der Stufe z.
func (a Ansicht) Massstab(z int) float64 { return KlemmeZoom(a.Zoom) * float64(int64(1)<<uint(z)) }

// Passend liefert die Ansicht, die die ganze Karte (Stufe-0-Grenzen des
// Manifests, sonst der feinsten Stufe) in g einpasst, nicht über Faktor 1
// hinaus vergrößert; ohne Kacheln die Mitte (0, 0).
func Passend(m *Manifest, g Groesse) Ansicht {
	a := Ansicht{Zoom: 1}
	bx, bz, ok := m.Mitte()
	if !ok {
		return a
	}
	a.X, a.Z = bx, bz
	x0, z0, x1, z1, _ := m.Ausdehnung()
	if w, h := x1-x0, z1-z0; w > 0 && h > 0 && g.B > 0 && g.H > 0 {
		a.Zoom = KlemmeZoom(min(g.B/w, g.H/h, 1) * 0.92)
	}
	return a
}

// KlemmeAuf hält die Mitte innerhalb der Karte (Ausdehnung der Kacheln), damit
// man sich nicht in der Leere verliert; ohne Kacheln nur innerhalb von
// ±MaxKoordinate.
func (a Ansicht) KlemmeAuf(m *Manifest) Ansicht {
	a.Zoom = KlemmeZoom(a.Zoom)
	if math.IsNaN(a.X) || math.IsNaN(a.Z) {
		a.X, a.Z = 0, 0
	}
	x0, z0, x1, z1 := -MaxKoordinate, -MaxKoordinate, MaxKoordinate, MaxKoordinate
	if m != nil {
		if mx0, mz0, mx1, mz1, ok := m.Ausdehnung(); ok {
			x0, z0, x1, z1 = mx0, mz0, mx1, mz1
		}
	}
	a.X = min(max(a.X, x0), x1)
	a.Z = min(max(a.Z, z0), z1)
	return a
}
