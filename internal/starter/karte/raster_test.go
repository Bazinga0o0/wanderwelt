package karte

import (
	"math"
	"testing"
	"time"
)

func TestBlockZuKachelNegativ(t *testing.T) {
	for _, c := range []struct {
		z      int
		bx, bz int64
		want   Kachel
	}{
		{0, 0, 0, Kachel{0, 0, 0}},
		{0, 255, 255, Kachel{0, 0, 0}},
		{0, 256, 256, Kachel{0, 1, 1}},
		{0, -1, -1, Kachel{0, -1, -1}},
		{0, -256, -256, Kachel{0, -1, -1}},
		{0, -257, 300, Kachel{0, -2, 1}},
		{2, 1023, -1, Kachel{2, 0, -1}},
		{2, 1024, -1024, Kachel{2, 1, -1}},
		{2, -1025, -1025, Kachel{2, -2, -2}},
		{6, -1, 16383, Kachel{6, -1, 0}},
		{6, 16384, -16385, Kachel{6, 1, -2}},
	} {
		if got := BlockZuKachel(c.z, c.bx, c.bz); got != c.want {
			t.Errorf("BlockZuKachel(%d, %d, %d) = %v, want %v", c.z, c.bx, c.bz, got, c.want)
		}
	}
}

func TestSpanne(t *testing.T) {
	for z, want := range []int64{256, 512, 1024, 2048, 4096, 8192, 16384} {
		if got := Spanne(z); got != want {
			t.Errorf("Spanne(%d) = %d, want %d", z, got, want)
		}
	}
}

func TestStufeFuerZoom(t *testing.T) {
	for _, c := range []struct {
		zoom float64
		want int
	}{
		{8, 0}, {2, 0}, {1, 0}, {0.9, 1}, {0.51, 1}, {0.5, 1}, {0.4999, 2}, {0.25, 2}, {0.2, 3},
		{1.0 / 16, 4}, {1.0 / 32, 5}, {1.0 / 64, 6}, {1.0 / 1000, 6}, {100, 0}, {math.NaN(), 0}, {-3, 0}, {0, 0},
	} {
		if got := StufeFuerZoom(c.zoom); got != c.want {
			t.Errorf("StufeFuerZoom(%v) = %d, want %d", c.zoom, got, c.want)
		}
	}
	// Eigenschaft: Der Maßstab der gewählten Stufe ist ≥ 1, und die nächst
	// gröbere Stufe wäre schon verkleinert.
	for z := MinZoom; z <= MaxZoom; z *= 1.013 {
		s := StufeFuerZoom(z)
		ma := z * math.Pow(2, float64(s))
		if ma < 1-1e-6 {
			t.Fatalf("Zoom %v: Stufe %d macht Maßstab %v < 1", z, s, ma)
		}
		if s > 0 && z*math.Pow(2, float64(s-1)) >= 1 {
			t.Fatalf("Zoom %v: Stufe %d ist nicht die gröbste passende", z, s)
		}
	}
}

func TestStufeWaehlen(t *testing.T) {
	for _, c := range []struct {
		zoom   float64
		levels []int
		want   int
	}{
		{1.0 / 16, nil, 4},
		{1.0 / 16, []int{0, 1, 2, 3, 4, 5, 6}, 4},
		{1.0 / 16, []int{0, 1, 2}, 2}, // gewünscht 4: die nächstfeinere vorhandene
		{1.0 / 16, []int{5, 6}, 5},    // nichts feineres: die feinste vorhandene
		{1, []int{3}, 3},              // nur Stufe 3 da
		{1.0 / 64, []int{0, 6}, 6},
	} {
		if got := StufeWaehlen(c.zoom, c.levels); got != c.want {
			t.Errorf("StufeWaehlen(%v, %v) = %d, want %d", c.zoom, c.levels, got, c.want)
		}
	}
}

func TestSichtbar(t *testing.T) {
	menge := func(ks []Kachel) map[Kachel]bool {
		m := map[Kachel]bool{}
		for _, k := range ks {
			m[k] = true
		}
		return m
	}
	// Über den Nullpunkt: vier Kacheln.
	got := Sichtbar(0, -10, -10, 10, 10, nil, 0)
	want := []Kachel{{0, -1, -1}, {0, 0, -1}, {0, -1, 0}, {0, 0, 0}}
	if len(got) != 4 {
		t.Fatalf("Sichtbar = %v", got)
	}
	for _, k := range want {
		if !menge(got)[k] {
			t.Errorf("Kachel %v fehlt in %v", k, got)
		}
	}
	// Das Rechteck ist rechts/unten offen: genau eine Kachel.
	if got := Sichtbar(0, 0, 0, 256, 256, nil, 0); len(got) != 1 || got[0] != (Kachel{0, 0, 0}) {
		t.Errorf("genau eine Kachel erwartet, got %v", got)
	}
	if got := Sichtbar(0, -256, -256, 0, 0, nil, 0); len(got) != 1 || got[0] != (Kachel{0, -1, -1}) {
		t.Errorf("negativ: eine Kachel erwartet, got %v", got)
	}
	// Gröbere Stufe: 1 Kachel = 1024 Blöcke.
	if got := Sichtbar(2, -500, -500, 500, 500, nil, 0); len(got) != 4 {
		t.Errorf("Stufe 2: %v", got)
	}
	// Grenzen schneiden zu.
	g := &Bereich{XMin: 0, XMax: 0, YMin: -1, YMax: 5}
	got = Sichtbar(0, -10, -10, 10, 10, g, 0)
	if len(got) != 2 || !menge(got)[Kachel{0, 0, -1}] || !menge(got)[Kachel{0, 0, 0}] {
		t.Errorf("mit Grenzen: %v", got)
	}
	if got := Sichtbar(0, 5000, 5000, 6000, 6000, g, 0); len(got) != 0 {
		t.Errorf("außerhalb der Grenzen: %v", got)
	}
	// Die Mitte zuerst, Obergrenze.
	got = Sichtbar(0, -1000, -1000, 1000, 1000, nil, 5)
	if len(got) != 5 {
		t.Fatalf("Obergrenze: %v", got)
	}
	if got[0].X < -1 || got[0].X > 0 || got[0].Y < -1 || got[0].Y > 0 {
		t.Errorf("erste Kachel nicht in der Mitte: %v", got[0])
	}
	// Ungültiges und Riesiges.
	if got := Sichtbar(0, math.NaN(), 0, 1, 1, nil, 0); got != nil {
		t.Errorf("NaN: %v", got)
	}
	if got := Sichtbar(0, 0, 0, math.Inf(1), 1, nil, 0); got != nil {
		t.Errorf("Inf: %v", got)
	}
	if got := Sichtbar(0, -1e15, -1e15, 1e15, 1e15, nil, 0); len(got) > MaxSichtbar {
		t.Errorf("Riesenbereich lieferte %d Kacheln", len(got))
	}
	// Umgekehrte Ecken werden getauscht.
	if got := Sichtbar(0, 10, 10, -10, -10, nil, 0); len(got) != 4 {
		t.Errorf("umgekehrt: %v", got)
	}
}

func TestAnsichtZoomUmHaeltBlock(t *testing.T) {
	g := Groesse{B: 800, H: 600}
	a := Ansicht{X: -1234.5, Z: 987.25, Zoom: 0.37}
	for _, px := range [][2]float64{{0, 0}, {400, 300}, {799, 599}, {123, 456}} {
		bx, bz := a.BlockBei(px[0], px[1], g)
		for _, f := range []float64{1.25, 0.8, 3, 0.1} {
			b := a.ZoomUm(f, px[0], px[1], g)
			cx, cz := b.BlockBei(px[0], px[1], g)
			if math.Abs(cx-bx) > 1e-6 || math.Abs(cz-bz) > 1e-6 {
				// Nur wenn nicht geklemmt wurde, muss der Block gleich bleiben.
				if b.Zoom > MinZoom && b.Zoom < MaxZoom {
					t.Errorf("Zoom um %v mit Faktor %v: Block (%v, %v) → (%v, %v)", px, f, bx, bz, cx, cz)
				}
			}
		}
	}
}

func TestAnsichtZoomGrenzen(t *testing.T) {
	g := Groesse{B: 800, H: 600}
	a := Ansicht{Zoom: 1}
	for range 100 {
		a = a.ZoomUm(1.5, 10, 10, g)
	}
	if a.Zoom != MaxZoom {
		t.Errorf("Zoom nach oben = %v, want %v", a.Zoom, MaxZoom)
	}
	for range 200 {
		a = a.ZoomUm(0.5, 10, 10, g)
	}
	if a.Zoom != MinZoom {
		t.Errorf("Zoom nach unten = %v, want %v", a.Zoom, MinZoom)
	}
	if b := a.ZoomUm(math.NaN(), 0, 0, g); b != a {
		t.Errorf("NaN-Faktor ändert die Ansicht: %+v", b)
	}
}

func TestAnsichtVerschieben(t *testing.T) {
	a := Ansicht{X: 100, Z: 100, Zoom: 2}
	b := a.Verschieben(20, -10) // der Inhalt folgt der Hand: nach rechts, nach oben
	if b.X != 90 || b.Z != 105 {
		t.Errorf("Verschieben = %+v", b)
	}
}

func TestSichtbereichUndBildschirm(t *testing.T) {
	g := Groesse{B: 1000, H: 500}
	a := Ansicht{X: 0, Z: 0, Zoom: 2}
	x0, z0, x1, z1 := a.Sichtbereich(g, 0)
	if x0 != -250 || x1 != 250 || z0 != -125 || z1 != 125 {
		t.Errorf("Sichtbereich = %v %v %v %v", x0, z0, x1, z1)
	}
	px, py := a.BildschirmBei(0, 0, g)
	if px != 500 || py != 250 {
		t.Errorf("Mitte auf dem Bildschirm: %v, %v", px, py)
	}
}

func TestKlemmeAufUndPassend(t *testing.T) {
	// Kacheln x −1…1, y −1…0 auf Stufe 0: Blöcke x −256…512, z −256…256.
	m := testManifest(t, time.Unix(1, 0), map[string][]byte{"0/-1/-1": {1}, "0/1/0": {2}})
	for _, c := range []struct{ in, want Ansicht }{
		{Ansicht{X: 5000, Z: 5000, Zoom: 1}, Ansicht{X: 512, Z: 256, Zoom: 1}},
		{Ansicht{X: -5000, Z: -5000, Zoom: 1}, Ansicht{X: -256, Z: -256, Zoom: 1}},
		{Ansicht{X: 100, Z: 100, Zoom: 1}, Ansicht{X: 100, Z: 100, Zoom: 1}},
		{Ansicht{X: 100, Z: 100, Zoom: 99}, Ansicht{X: 100, Z: 100, Zoom: MaxZoom}},
		{Ansicht{X: math.NaN(), Z: 0, Zoom: math.NaN()}, Ansicht{X: 0, Z: 0, Zoom: 1}},
	} {
		if got := c.in.KlemmeAuf(m); got != c.want {
			t.Errorf("KlemmeAuf(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
	// Passend: Mitte der Karte, so weit herausgezoomt, dass alles ins Bild passt (höchstens 1).
	a := Passend(m, Groesse{B: 400, H: 400})
	if a.X != 128 || a.Z != 0 || math.Abs(a.Zoom-400.0/768*0.92) > 1e-9 {
		t.Errorf("Passend = %+v", a)
	}
	if b := Passend(m, Groesse{B: 100000, H: 100000}); b.Zoom != 0.92 {
		t.Errorf("Passend für riesiges Fenster: Zoom %v, want 0.92 (nicht über 1 hinaus vergrößert)", b.Zoom)
	}
}
