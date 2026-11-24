package fenster

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"sync"

	"gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/image/font/gofont/gomono"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/symbol"
)

// Schriften: Inter (SIL OFL 1.1, fonts/OFL.txt) für die Oberfläche, Go Mono
// (BSD, aus golang.org/x/image) für das Log. Keine Systemschriften: Das
// Durchsuchen von C:\Windows\Fonts kostete Zeit und Speicher.

//go:embed fonts/Inter-Regular.ttf
var interRegular []byte

//go:embed fonts/Inter-SemiBold.ttf
var interSemiBold []byte

const (
	schriftUI   font.Typeface = "Inter"
	schriftMono font.Typeface = "Go Mono"
)

var schriften struct {
	once sync.Once
	coll []font.FontFace
	err  error
}

// sammlung lädt die eingebetteten Schriften (einmal je Prozess).
func sammlung() ([]font.FontFace, error) {
	schriften.once.Do(func() {
		for _, s := range []struct {
			name string
			data []byte
			face font.Typeface
		}{
			{"Inter-Regular", interRegular, schriftUI},
			{"Inter-SemiBold", interSemiBold, schriftUI},
			{"Go Mono", gomono.TTF, schriftMono},
		} {
			f, err := opentype.Parse(s.data)
			if err != nil {
				schriften.err = fmt.Errorf("Schrift %s: %w", s.name, err)
				return
			}
			fnt := f.Font()
			fnt.Typeface = s.face // eindeutiger Name, unabhängig von den Namenstabellen
			schriften.coll = append(schriften.coll, font.FontFace{Font: fnt, Face: f})
		}
	})
	return schriften.coll, schriften.err
}

// farben eines Designs.
type farben struct {
	hinter     color.NRGBA // Fensterhintergrund
	flaeche    color.NRGBA // Karten
	flaeche2   color.NRGBA // abgesetzte Flächen in Karten (Log, Code)
	rand       color.NRGBA
	text       color.NRGBA
	leise      color.NRGBA // Nebentext
	akzent     color.NRGBA // Knöpfe, aktiver Reiter
	akzentHov  color.NRGBA
	aufAkzent  color.NRGBA
	akzentHell color.NRGBA // Chips, Info-Hinweise
	akzentText color.NRGBA // Text auf akzentHell
	warn       color.NRGBA
	warnHell   color.NRGBA
	warnText   color.NRGBA
	fehler     color.NRGBA
	fehlerHov  color.NRGBA
	fehlerHell color.NRGBA
	fehlerText color.NRGBA
	aus        color.NRGBA // Schalter aus, Balken-Hintergrund
	knauf      color.NRGBA
	schleier   color.NRGBA // hinter Rückfragen
	hover      color.NRGBA // Überlagerung beim Zeigen
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func argb(a uint8, c uint32) color.NRGBA {
	n := rgb(c)
	n.A = a
	return n
}

var hell = farben{
	hinter:     rgb(0xf3f4f1),
	flaeche:    rgb(0xffffff),
	flaeche2:   rgb(0xf6f7f4),
	rand:       rgb(0xe0e4dc),
	text:       rgb(0x1b2119),
	leise:      rgb(0x5f675c),
	akzent:     rgb(0x367a24),
	akzentHov:  rgb(0x2d6a1e),
	aufAkzent:  rgb(0xffffff),
	akzentHell: rgb(0xe5f1dd),
	akzentText: rgb(0x285d1a),
	warn:       rgb(0xb36b00),
	warnHell:   rgb(0xfdf1dc),
	warnText:   rgb(0x7a4a00),
	fehler:     rgb(0xb3261e),
	fehlerHov:  rgb(0x9a1f18),
	fehlerHell: rgb(0xfbe9e7),
	fehlerText: rgb(0x8c1d16),
	aus:        rgb(0xd5dad1),
	knauf:      rgb(0xffffff),
	schleier:   argb(0x66, 0x0d110c),
	hover:      argb(0x0d, 0x000000),
}

var dunkel = farben{
	hinter:     rgb(0x151915),
	flaeche:    rgb(0x1e231d),
	flaeche2:   rgb(0x191d18),
	rand:       rgb(0x2f362d),
	text:       rgb(0xe5e9e2),
	leise:      rgb(0x9aa396),
	akzent:     rgb(0x6db83b),
	akzentHov:  rgb(0x7cc64a),
	aufAkzent:  rgb(0x0e1a09),
	akzentHell: rgb(0x23341b),
	akzentText: rgb(0xa9d98b),
	warn:       rgb(0xe3a33a),
	warnHell:   rgb(0x352a14),
	warnText:   rgb(0xf1c77e),
	fehler:     rgb(0xef7d72),
	fehlerHov:  rgb(0xf4968c),
	fehlerHell: rgb(0x3a1d1a),
	fehlerText: rgb(0xf7b3ab),
	aus:        rgb(0x3a4238),
	knauf:      rgb(0xf2f4f0),
	schleier:   argb(0x99, 0x000000),
	hover:      argb(0x14, 0xffffff),
}

// thema: Farben, Schriften, Bausteine.
type thema struct {
	dunkel bool
	f      farben
	mat    *material.Theme // Shaper und Palette für Eingabefelder

	symbole map[int]paint.ImageOp // App-Symbol je Pixelgröße
}

func neuesThema(istDunkel bool) (*thema, error) {
	coll, err := sammlung()
	if err != nil {
		return nil, err
	}
	f := hell
	if istDunkel {
		f = dunkel
	}
	m := material.NewTheme()
	m.Shaper = text.NewShaper(text.NoSystemFonts(), text.WithCollection(coll))
	m.Face = schriftUI
	m.TextSize = 14
	m.Palette = material.Palette{Bg: f.flaeche, Fg: f.text, ContrastBg: f.akzent, ContrastFg: f.aufAkzent}
	return &thema{dunkel: istDunkel, f: f, mat: m, symbole: map[int]paint.ImageOp{}}, nil
}

// ---- Text ----

// schrift: Größe, Gewicht, Farbe.
type schrift struct {
	groesse unit.Sp
	fett    bool
	farbe   color.NRGBA
	mono    bool
	zeilen  int // höchstens so viele Zeilen (0: beliebig)
	mitte   bool
	rechts  bool
}

func (t *thema) text(gtx layout.Context, s schrift, txt string) layout.Dimensions {
	l := material.Label(t.mat, s.groesse, txt)
	l.Color = s.farbe
	if s.fett {
		l.Font.Weight = font.SemiBold
	}
	if s.mono {
		l.Font.Typeface = schriftMono
	}
	l.MaxLines = s.zeilen
	switch {
	case s.mitte:
		l.Alignment = text.Middle
	case s.rechts:
		l.Alignment = text.End
	}
	return l.Layout(gtx)
}

// Häufige Textarten.
func (t *thema) titel(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return t.text(gtx, schrift{groesse: 15, fett: true, farbe: t.f.text}, txt)
	}
}

func (t *thema) normal(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return t.text(gtx, schrift{groesse: 14, farbe: t.f.text}, txt)
	}
}

func (t *thema) leise(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise}, txt)
	}
}

func (t *thema) klein(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return t.text(gtx, schrift{groesse: 12, fett: true, farbe: t.f.leise}, txt)
	}
}

func (t *thema) farbig(txt string, c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return t.text(gtx, schrift{groesse: 13, farbe: c}, txt)
	}
}

// ---- Flächen ----

// rechteck füllt sz mit c und Eckenradius r.
func rechteck(gtx layout.Context, sz image.Point, c color.NRGBA, r int) {
	defer clip.UniformRRect(image.Rectangle{Max: sz}, r).Push(gtx.Ops).Pop()
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

// hinterlegt zeichnet w auf einer Fläche mit Farbe c, Radius r und
// (randFarbe.A > 0) einem Rand von 1 dp.
func hinterlegt(gtx layout.Context, c, randFarbe color.NRGBA, r unit.Dp, w layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	call := m.Stop()
	rr := gtx.Dp(r)
	if randFarbe.A > 0 {
		rechteck(gtx, dims.Size, randFarbe, rr)
		b := gtx.Dp(1)
		inner := image.Rectangle{Min: image.Pt(b, b), Max: dims.Size.Sub(image.Pt(b, b))}
		st := clip.UniformRRect(inner, max(rr-b, 0)).Push(gtx.Ops)
		paint.ColorOp{Color: c}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		st.Pop()
	} else {
		rechteck(gtx, dims.Size, c, rr)
	}
	call.Add(gtx.Ops)
	return dims
}

// karte: weiße (dunkle) Fläche mit Rand und Innenabstand.
func (t *thema) karte(gtx layout.Context, w layout.Widget) layout.Dimensions {
	return t.karteAbstand(gtx, 18, w)
}

func (t *thema) karteAbstand(gtx layout.Context, abstand unit.Dp, w layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	// layout.Inset reicht Min.Y unverändert durch; eine verlangte
	// Mindesthöhe (gleichHoch) gilt aber für die ganze Karte.
	gtx.Constraints.Min.Y = max(0, gtx.Constraints.Min.Y-2*gtx.Dp(abstand))
	return hinterlegt(gtx, t.f.flaeche, t.f.rand, 10, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(abstand).Layout(gtx, w)
	})
}

// trenner: waagrechte Linie über die ganze Breite.
func (t *thema) trenner(gtx layout.Context) layout.Dimensions {
	sz := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
	rechteck(gtx, sz, t.f.rand, 0)
	return layout.Dimensions{Size: sz}
}

// punkt: Kreis mit Durchmesser d.
func punkt(gtx layout.Context, c color.NRGBA, d unit.Dp) layout.Dimensions {
	n := gtx.Dp(d)
	sz := image.Pt(n, n)
	rechteck(gtx, sz, c, n/2)
	return layout.Dimensions{Size: sz, Baseline: 0}
}

// balken: Fortschritt anteil (0…1) in Farbe c.
func (t *thema) balken(gtx layout.Context, anteil float32, c color.NRGBA, hoehe unit.Dp) layout.Dimensions {
	h := gtx.Dp(hoehe)
	sz := image.Pt(gtx.Constraints.Max.X, h)
	rechteck(gtx, sz, t.f.aus, h/2)
	anteil = min(max(anteil, 0), 1)
	if w := int(float32(sz.X) * anteil); w > 0 {
		rechteck(gtx, image.Pt(max(w, h), h), c, h/2)
	}
	return layout.Dimensions{Size: sz}
}

// chip: kleine Marke mit Text.
func (t *thema) chip(gtx layout.Context, txt string, bg, fg color.NRGBA) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return hinterlegt(gtx, bg, color.NRGBA{}, 11, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 3, Bottom: 3, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return t.text(gtx, schrift{groesse: 13, fett: true, farbe: fg}, txt)
		})
	})
}

// meldung: Hinweisfläche (info, warn, fehler) mit fettem Anfang.
type meldungsArt int

const (
	meldungInfo meldungsArt = iota
	meldungWarn
	meldungFehler
)

func (t *thema) meldungsFarben(a meldungsArt) (bg, rand, fg color.NRGBA) {
	switch a {
	case meldungWarn:
		return t.f.warnHell, t.f.warn, t.f.warnText
	case meldungFehler:
		return t.f.fehlerHell, t.f.fehler, t.f.fehlerText
	}
	return t.f.akzentHell, t.f.akzent, t.f.akzentText
}

func (t *thema) meldung(gtx layout.Context, a meldungsArt, fett, txt string) layout.Dimensions {
	bg, rand, fg := t.meldungsFarben(a)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	m := op.Record(gtx.Ops)
	dims := layout.Inset{Top: 10, Bottom: 10, Left: 16, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if fett == "" {
					return layout.Dimensions{}
				}
				return t.text(gtx, schrift{groesse: 14, fett: true, farbe: fg}, fett)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if txt == "" {
					return layout.Dimensions{}
				}
				return t.text(gtx, schrift{groesse: 13.5, farbe: fg}, txt)
			}),
		)
	})
	call := m.Stop()
	r := gtx.Dp(8)
	st := clip.UniformRRect(image.Rectangle{Max: dims.Size}, r).Push(gtx.Ops)
	paint.ColorOp{Color: bg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	// Farbstreifen links
	streifen := clip.Rect(image.Rectangle{Max: image.Pt(gtx.Dp(4), dims.Size.Y)}).Push(gtx.Ops)
	paint.ColorOp{Color: rand}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	streifen.Pop()
	st.Pop()
	call.Add(gtx.Ops)
	return dims
}

// ---- Knöpfe, Schalter, Schieber ----

type knopfArt int

const (
	knopfHaupt  knopfArt = iota // gefüllt, Akzent
	knopfNeben                  // Fläche mit Rand
	knopfGefahr                 // gefüllt, rot
	knopfLeise                  // nur Text
)

// knopf zeichnet einen Knopf; aktiv false: ausgegraut, ohne Klick.
func (t *thema) knopf(gtx layout.Context, k *widget.Clickable, txt string, art knopfArt, aktiv bool) layout.Dimensions {
	return t.knopfGroesse(gtx, k, txt, art, aktiv, false)
}

func (t *thema) grosserKnopf(gtx layout.Context, k *widget.Clickable, txt string, art knopfArt, aktiv bool) layout.Dimensions {
	return t.knopfGroesse(gtx, k, txt, art, aktiv, true)
}

func (t *thema) knopfGroesse(gtx layout.Context, k *widget.Clickable, txt string, art knopfArt, aktiv, gross bool) layout.Dimensions {
	gtx.Constraints.Min = image.Point{} // so breit wie der Text, nicht wie die Spalte
	if !aktiv {
		gtx = gtx.Disabled()
	}
	var bg, fg, rand color.NRGBA
	hov := k.Hovered() && aktiv
	switch art {
	case knopfHaupt:
		bg, fg = t.f.akzent, t.f.aufAkzent
		if hov {
			bg = t.f.akzentHov
		}
	case knopfGefahr:
		bg, fg = t.f.fehler, t.f.aufAkzent
		if !t.dunkel {
			fg = rgb(0xffffff)
		}
		if hov {
			bg = t.f.fehlerHov
		}
	case knopfNeben:
		bg, fg, rand = t.f.flaeche, t.f.text, t.f.rand
		if hov {
			bg = mische(t.f.flaeche, t.f.hover)
		}
	case knopfLeise:
		fg = t.f.akzent
		if hov {
			bg = t.f.hover
		}
	}
	if !aktiv {
		bg, fg, rand = blass(bg), blass(fg), blass(rand)
	}
	innen := layout.Inset{Top: 7, Bottom: 7, Left: 14, Right: 14}
	groesse := unit.Sp(13.5)
	if gross {
		innen = layout.Inset{Top: 11, Bottom: 11, Left: 22, Right: 22}
		groesse = 15
	}
	return k.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if aktiv {
			pointer.CursorPointer.Add(gtx.Ops)
		}
		dims := hinterlegt(gtx, bg, rand, 8, func(gtx layout.Context) layout.Dimensions {
			return innen.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return t.text(gtx, schrift{groesse: groesse, fett: true, farbe: fg, zeilen: 1, mitte: true}, txt)
			})
		})
		if gtx.Focused(k) {
			t.fokusRing(gtx, dims.Size, 8)
		}
		return dims
	})
}

// fokusRing zeigt den Tastaturfokus (Tab) als Ring um sz.
func (t *thema) fokusRing(gtx layout.Context, sz image.Point, r unit.Dp) {
	w := float32(gtx.Dp(2))
	rect := image.Rectangle{Max: sz}
	paint.FillShape(gtx.Ops, t.f.akzent, clip.Stroke{
		Path:  clip.UniformRRect(rect, gtx.Dp(r)).Path(gtx.Ops),
		Width: w,
	}.Op())
}

// schalter: Ein/Aus im Stil von Windows 11.
func (t *thema) schalter(gtx layout.Context, b *widget.Bool, aktiv bool) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	if !aktiv {
		gtx = gtx.Disabled()
	}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if aktiv {
			pointer.CursorPointer.Add(gtx.Ops)
		}
		w, h := gtx.Dp(40), gtx.Dp(22)
		sz := image.Pt(w, h)
		spur, knauf := t.f.aus, t.f.knauf
		if b.Value {
			spur = t.f.akzent
			if t.dunkel {
				knauf = t.f.aufAkzent
			}
		}
		if b.Hovered() && aktiv {
			spur = mische(spur, t.f.hover)
		}
		if !aktiv {
			spur, knauf = blass(spur), blass(knauf)
		}
		rechteck(gtx, sz, spur, h/2)
		d := h - gtx.Dp(6)
		x := gtx.Dp(3)
		if b.Value {
			x = w - d - gtx.Dp(3)
		}
		st := op.Offset(image.Pt(x, gtx.Dp(3))).Push(gtx.Ops)
		rechteck(gtx, image.Pt(d, d), knauf, d/2)
		st.Pop()
		if gtx.Focused(b) {
			t.fokusRing(gtx, sz, 11)
		}
		return layout.Dimensions{Size: sz}
	})
}

// schieber: Spur, gefüllter Teil, Knauf; f.Value in 0…1.
func (t *thema) schieber(gtx layout.Context, f *widget.Float, aktiv bool) layout.Dimensions {
	if !aktiv {
		gtx = gtx.Disabled()
	}
	knaufD := gtx.Dp(18)
	spurH := gtx.Dp(4)
	breite := gtx.Constraints.Max.X
	hoehe := gtx.Dp(28)
	// Eingabefläche: die Spur ohne die halben Knäufe an den Enden.
	innen := breite - knaufD
	st := op.Offset(image.Pt(knaufD/2, 0)).Push(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(innen, hoehe))
	f.Layout(g, layout.Horizontal, 9)
	st.Pop()
	if aktiv {
		area := clip.Rect(image.Rectangle{Max: image.Pt(breite, hoehe)}).Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		area.Pop()
	}

	v := min(max(f.Value, 0), 1)
	ak, sp, kn := t.f.akzent, t.f.aus, t.f.knauf
	if !aktiv {
		ak, sp = blass(ak), blass(sp)
	}
	y := (hoehe - spurH) / 2
	st = op.Offset(image.Pt(knaufD/2, y)).Push(gtx.Ops)
	rechteck(gtx, image.Pt(innen, spurH), sp, spurH/2)
	rechteck(gtx, image.Pt(int(float32(innen)*v), spurH), ak, spurH/2)
	st.Pop()
	kx := int(float32(innen) * v)
	st = op.Offset(image.Pt(kx, (hoehe-knaufD)/2)).Push(gtx.Ops)
	rechteck(gtx, image.Pt(knaufD, knaufD), ak, knaufD/2)
	r := gtx.Dp(4)
	st2 := op.Offset(image.Pt(r, r)).Push(gtx.Ops)
	rechteck(gtx, image.Pt(knaufD-2*r, knaufD-2*r), kn, (knaufD-2*r)/2)
	st2.Pop()
	st.Pop()
	return layout.Dimensions{Size: image.Pt(breite, hoehe)}
}

// symbol: das App-Symbol in d dp, pixelgenau gerastert.
func (t *thema) symbol(gtx layout.Context, d unit.Dp) layout.Dimensions {
	n := gtx.Dp(d)
	img, ok := t.symbole[n]
	if !ok {
		img = paint.NewImageOp(symbol.Render(n))
		img.Filter = paint.FilterNearest
		t.symbole[n] = img
	}
	img.Add(gtx.Ops)
	sz := image.Pt(n, n)
	defer clip.Rect(image.Rectangle{Max: sz}).Push(gtx.Ops).Pop()
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: sz}
}

// ---- Farben mischen ----

// mische legt o (mit Deckkraft) über c.
func mische(c, o color.NRGBA) color.NRGBA {
	a := uint32(o.A)
	f := func(x, y uint8) uint8 { return uint8((uint32(x)*(255-a) + uint32(y)*a) / 255) }
	return color.NRGBA{R: f(c.R, o.R), G: f(c.G, o.G), B: f(c.B, o.B), A: c.A}
}

// blass: halb durchsichtig (ausgegraut).
func blass(c color.NRGBA) color.NRGBA {
	c.A = uint8(uint32(c.A) * 45 / 100)
	return c
}
