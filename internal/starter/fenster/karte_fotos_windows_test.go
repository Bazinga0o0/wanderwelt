//go:build windows

package fenster

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/pointer"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte/kartetest"
)

// TestKarteFotos zeichnet den Reiter Karte mit einer erfundenen Landschaft in
// mehreren Zuständen, hell und dunkel, in 900×620 ohne Fenster und legt PNGs
// ab – zur Durchsicht, kein Vergleich. Nur mit WANDERWELT_KARTE_FOTOS=<Ordner>.
func TestKarteFotos(t *testing.T) {
	dir := os.Getenv("WANDERWELT_KARTE_FOTOS")
	if dir == "" {
		t.Skip("WANDERWELT_KARTE_FOTOS nicht gesetzt")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sz := image.Pt(900, 620)
	win, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer win.Release()

	foto := func(kt *karteTest, name string) {
		t.Helper()
		kt.bild()
		if err := win.Frame(&kt.ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: sz})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}
	alleSichtbarGeladen := func(kt *karteTest) bool {
		if kt.stand().manifest == nil || len(kt.u.karte.bild.sichtbar) == 0 {
			return false
		}
		for _, k := range kt.u.karte.bild.sichtbar {
			if kt.zustand(k) != karte.Fertig && kt.zustand(k) != karte.Fehlt {
				return false
			}
		}
		return true
	}

	k := kartetest.Neu(t, karteTestToken, "w1")
	k.Landschaft(t, -5, 4, -3, 3) // 10×7 Kacheln auf Stufe 0 (2560×1792 Blöcke)
	k.Erzeugt(time.Now().Add(-14*time.Minute), "foto")
	q := &karteTestQuelle{aktionsQuelle: &aktionsQuelle{}, c: client.New(&group.Group{Coordinator: k.URL(), Token: karteTestToken})}

	for _, dunkel := range []bool{false, true} {
		sfx := "-hell"
		if dunkel {
			sfx = "-dunkel"
		}
		kt := neuerKarteTest(t, karteZustand(time.Now(), true), q, dunkel)
		kt.bisBild("Karte geladen", func() bool { return alleSichtbarGeladen(kt) })
		kt.r.Queue(zeiger(pointer.Move, f32.Pt(520, 240)))
		kt.bild()
		foto(kt, "karte-uebersicht"+sfx)

		// Hineingezoomt (Nearest-Neighbor, Stufe 0 auf 230 %).
		b := &kt.u.karte.bild
		b.sicht = karte.Ansicht{X: -627, Z: -537, Zoom: 2.3}
		kt.bisBild("Zoom geladen", func() bool { return alleSichtbarGeladen(kt) && b.stufe == 0 })
		foto(kt, "karte-hineingezoomt"+sfx)

		// Weit heraus: grobe Stufe.
		b.sicht = karte.Ansicht{X: 0, Z: 0, Zoom: 1.0 / 40}
		kt.bisBild("grobe Stufe geladen", func() bool { return alleSichtbarGeladen(kt) && b.stufe >= 5 })
		foto(kt, "karte-weit-heraus"+sfx)
	}

	// Kacheln laden noch: Platzhalter.
	frei := k.Anhalten()
	kt := neuerKarteTest(t, karteZustand(time.Now(), false), q, false)
	kt.bisBild("Manifest da", func() bool { return kt.stand().manifest != nil && kt.u.karte.sitz.lader.Ausstehend() > 0 })
	foto(kt, "karte-laedt-hell")
	frei()
	kt.u.beenden()

	// Karte noch nicht erzeugt / Koordinator weg / Warnung über der Karte.
	k.OhneKarte(true)
	kt = neuerKarteTest(t, karteZustand(time.Now(), false), q, false)
	kt.bisBild("Fehler", func() bool { return kt.stand().fehler != nil })
	foto(kt, "karte-noch-nicht-erzeugt-hell")
	k.OhneKarte(false)
	k.Ausfall(true)
	kt = neuerKarteTest(t, karteZustand(time.Now(), false), q, true)
	kt.bisBild("Fehler", func() bool { return kt.stand().fehler != nil })
	foto(kt, "karte-koordinator-weg-dunkel")
	k.Ausfall(false)
	kt = neuerKarteTest(t, karteZustand(time.Now(), false), q, false)
	kt.bisBild("Karte geladen", func() bool { return alleSichtbarGeladen(kt) })
	k.Ausfall(true)
	kt.u.karte.neu.Click()
	kt.bisBild("Warnung", func() bool { return kt.stand().fehler != nil && kt.stand().manifest != nil })
	foto(kt, "karte-warnung-hell")
}
