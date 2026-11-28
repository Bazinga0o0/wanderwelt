package fenster

import (
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
)

// Karten-Viewer: Draufsicht aus 256×256-Kacheln. Ziehen verschiebt, das
// Mausrad zoomt um die Mausposition (stufenlos 1/64 … 8, Nearest-Neighbor),
// zu den Zoomfaktoren wählt karte.StufeWaehlen die passende Kachelstufe. Die
// Kacheln holt der karte.Lader; hier wird nur gezeichnet.

const (
	// radSchritt: Zoomfaktor je Raststufe des Mausrads (Windows liefert ±120).
	radSchritt = 1.25
	// radRast: so viel „Scroll“ ist eine Raststufe.
	radRast = 120.0
	// knopfSchritt: Zoomfaktor der Knöpfe + und −.
	knopfSchritt = 1.5
	// vorladen: So viele Pixel über den Rand hinaus werden Kacheln geholt.
	vorladen = 96
)

// kartenTextur ist das Bild einer Kachel für Gio (einmal angelegt, damit die
// Textur zwischen den Bildern wiederverwendet wird).
type kartenTextur struct{ op paint.ImageOp }

func neueTextur(img *image.RGBA) kartenTextur {
	o := paint.NewImageOp(img)
	o.Filter = paint.FilterNearest
	return kartenTextur{op: o}
}

// kartenBild ist das Widget; der Zustand liegt in Ansicht (Mitte, Zoom).
type kartenBild struct {
	sicht    karte.Ansicht
	hatSicht bool // die Ansicht ist schon eingepasst oder wiederhergestellt

	groesse karte.Groesse // Größe des Zeichenbereichs im letzten Bild

	ziehen  bool
	zeiger  pointer.ID
	letzter f32.Point

	maus   f32.Point
	mausDa bool

	// Ergebnis des letzten Bildes (für Statusleiste und Tests).
	stufe    int
	sichtbar []karte.Kachel
}

// mausBlock: Block unter der Maus (nach unten gerundet); false, wenn die Maus
// nicht über der Karte ist.
func (v *kartenBild) mausBlock() (bx, bz int, ok bool) {
	if !v.mausDa || v.groesse.B <= 0 {
		return 0, 0, false
	}
	x, z := v.sicht.BlockBei(float64(v.maus.X), float64(v.maus.Y), v.groesse)
	return int(math.Floor(x)), int(math.Floor(z)), true
}

// zoomMitte: Zoom um die Mitte des Zeichenbereichs (Knöpfe + und −).
func (v *kartenBild) zoomMitte(faktor float64, m *karte.Manifest) {
	v.sicht = v.sicht.ZoomUm(faktor, v.groesse.B/2, v.groesse.H/2, v.groesse).KlemmeAuf(m)
}

// zurMitte: Mitte der Karte (Stufe-0-Grenzen), Zoom bleibt.
func (v *kartenBild) zurMitte(m *karte.Manifest) {
	if bx, bz, ok := m.Mitte(); ok {
		v.sicht.X, v.sicht.Z = bx, bz
	}
}

// ereignisse wertet Zeiger-Ereignisse aus: Ziehen, Mausrad, Mausposition.
func (v *kartenBild) ereignisse(gtx layout.Context, m *karte.Manifest) {
	for {
		e, ok := gtx.Event(pointer.Filter{
			Target: v,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Scroll | pointer.Move | pointer.Enter | pointer.Leave,
			// Senkrecht zoomen; waagrechtes Rad (Umschalt) wird nicht verbraucht.
			ScrollY: pointer.ScrollRange{Min: -4000, Max: 4000},
		})
		if !ok {
			return
		}
		pe, ok := e.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			v.maus, v.mausDa = pe.Position, true
			if pe.Buttons.Contain(pointer.ButtonPrimary) && !v.ziehen {
				v.ziehen, v.zeiger, v.letzter = true, pe.PointerID, pe.Position
				gtx.Execute(pointer.GrabCmd{Tag: v, ID: pe.PointerID})
			}
		case pointer.Drag:
			v.maus, v.mausDa = pe.Position, true
			if v.ziehen && pe.PointerID == v.zeiger {
				d := pe.Position.Sub(v.letzter)
				v.letzter = pe.Position
				v.sicht = v.sicht.Verschieben(float64(d.X), float64(d.Y)).KlemmeAuf(m)
			}
		case pointer.Release, pointer.Cancel:
			if pe.PointerID == v.zeiger {
				v.ziehen = false
			}
		case pointer.Move, pointer.Enter:
			v.maus, v.mausDa = pe.Position, true
		case pointer.Leave:
			v.mausDa = false
		case pointer.Scroll:
			v.maus, v.mausDa = pe.Position, true
			// Rad nach oben (negativ) vergrößert.
			faktor := math.Pow(radSchritt, -float64(pe.Scroll.Y)/radRast)
			v.sicht = v.sicht.ZoomUm(faktor, float64(pe.Position.X), float64(pe.Position.Y), v.groesse).KlemmeAuf(m)
		}
	}
}

// farben der Karte: Leere (nicht erzeugte Fläche), Platzhalter, Fehler.
type kartenFarben struct{ leere, platzhalter, platzhalterRand, fehler color.NRGBA }

func (t *thema) kartenFarben() kartenFarben {
	if t.dunkel {
		return kartenFarben{leere: rgb(0x0e1013), platzhalter: rgb(0x23282f), platzhalterRand: rgb(0x2e353e), fehler: rgb(0x4a2a2a)}
	}
	return kartenFarben{leere: rgb(0xd5d9de), platzhalter: rgb(0xe9ecef), platzhalterRand: rgb(0xdadee3), fehler: rgb(0xf3d3d3)}
}

// layout zeichnet die Karte in den ganzen verfügbaren Platz.
func (v *kartenBild) layout(gtx layout.Context, t *thema, m *karte.Manifest, l *karte.Lader[kartenTextur]) layout.Dimensions {
	size := gtx.Constraints.Max
	v.groesse = karte.Groesse{B: float64(size.X), H: float64(size.Y)}
	if size.X <= 0 || size.Y <= 0 || m == nil {
		return layout.Dimensions{Size: size}
	}
	if !v.hatSicht {
		v.sicht, v.hatSicht = karte.Passend(m, v.groesse), true
	}
	v.ereignisse(gtx, m)
	v.sicht = v.sicht.KlemmeAuf(m)

	z := v.sicht.Stufe(m.Stufen())
	v.stufe = z
	x0, z0, x1, z1 := v.sicht.Sichtbereich(v.groesse, vorladen)
	v.sichtbar = karte.Sichtbar(z, x0, z0, x1, z1, m.Grenzen(z), karte.MaxSichtbar)
	l.Anfordern(v.sichtbar)

	farben := t.kartenFarben()
	defer clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops).Pop()
	paint.ColorOp{Color: farben.leere}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	span := float64(karte.Spanne(z))
	fehler := false
	for _, k := range v.sichtbar {
		// Ränder auf ganze Pixel runden: Nachbarkacheln teilen sich genau eine
		// Kante (keine Nähte, auch bei gebrochenem Maßstab).
		ax, ay := v.sicht.BildschirmBei(float64(k.X)*span, float64(k.Y)*span, v.groesse)
		bx, by := v.sicht.BildschirmBei(float64(k.X+1)*span, float64(k.Y+1)*span, v.groesse)
		r := image.Rect(int(math.Round(ax)), int(math.Round(ay)), int(math.Round(bx)), int(math.Round(by)))
		if r.Dx() <= 0 || r.Dy() <= 0 || !r.Overlaps(image.Rectangle{Max: size}) {
			continue
		}
		tex, zu := l.Bild(k)
		switch zu {
		case karte.Fertig, karte.Veraltet:
			v.zeichneTextur(gtx, tex.op, r)
		case karte.Laedt:
			v.zeichnePlatzhalter(gtx, r, farben.platzhalter, farben.platzhalterRand)
		case karte.Fehler:
			fehler = true
			v.zeichnePlatzhalter(gtx, r, farben.fehler, farben.platzhalterRand)
		}
	}
	if fehler {
		// Nach der Pause neu versuchen (der Lader versucht erst dann wieder).
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(11 * time.Second)})
	}

	// Eingaben: ganzer Bereich; Hand beim Ziehen.
	area := clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops)
	event.Op(gtx.Ops, v)
	if v.ziehen {
		pointer.CursorGrabbing.Add(gtx.Ops)
	} else {
		pointer.CursorGrab.Add(gtx.Ops)
	}
	area.Pop()
	return layout.Dimensions{Size: size}
}

func (v *kartenBild) zeichneTextur(gtx layout.Context, img paint.ImageOp, r image.Rectangle) {
	st := clip.Rect(r).Push(gtx.Ops)
	sx, sy := float32(r.Dx())/karte.KachelPx, float32(r.Dy())/karte.KachelPx
	aff := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(sx, sy)).Offset(f32.Pt(float32(r.Min.X), float32(r.Min.Y)))).Push(gtx.Ops)
	img.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	aff.Pop()
	st.Pop()
}

// zeichnePlatzhalter: eine ruhige Fläche mit feinem Rand, bis die Kachel da ist.
func (v *kartenBild) zeichnePlatzhalter(gtx layout.Context, r image.Rectangle, fuell, rand color.NRGBA) {
	st := clip.Rect(r).Push(gtx.Ops)
	paint.ColorOp{Color: rand}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	st.Pop()
	innen := r.Inset(1)
	if innen.Empty() {
		return
	}
	st = clip.Rect(innen).Push(gtx.Ops)
	paint.ColorOp{Color: fuell}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	st.Pop()
}
