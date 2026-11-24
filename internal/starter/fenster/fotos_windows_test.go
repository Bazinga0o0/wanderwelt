//go:build windows

package fenster

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/op"
)

// TestBildschirmfotos zeichnet jede Beispielansicht hell und dunkel in
// 900×620 (100 %) ohne Fenster und legt PNGs ab – zur Durchsicht, kein
// Vergleich. Nur mit WANDERWELT_FENSTER_FOTOS=<Ordner>; optional
// WANDERWELT_FENSTER_SKALA (etwa 1.25).
func TestBildschirmfotos(t *testing.T) {
	dir := os.Getenv("WANDERWELT_FENSTER_FOTOS")
	if dir == "" {
		t.Skip("WANDERWELT_FENSTER_FOTOS nicht gesetzt")
	}
	skala := float32(1)
	if s := os.Getenv("WANDERWELT_FENSTER_SKALA"); s != "" {
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			t.Fatal(err)
		}
		skala = float32(f)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sz := image.Pt(int(900*skala), int(620*skala))
	win, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer win.Release()

	jetzt := time.Now()
	var ops op.Ops
	for _, a := range beispielAnsichten(jetzt) {
		for _, dunkel := range []bool{false, true} {
			u := testOberflaeche(t, dunkel, a, jetzt)
			// Zweimal: Klicks/Zustände des ersten Bildes wirken im zweiten.
			for range 2 {
				gtx := testKontext(&ops, sz, skala, jetzt)
				u.layout(gtx)
			}
			if err := win.Frame(&ops); err != nil {
				t.Fatal(err)
			}
			img := image.NewRGBA(image.Rectangle{Max: sz})
			if err := win.Screenshot(img); err != nil {
				t.Fatal(err)
			}
			name := a.name + "-hell.png"
			if dunkel {
				name = a.name + "-dunkel.png"
			}
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}
