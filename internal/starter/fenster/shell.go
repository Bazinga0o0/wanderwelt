// Package fenster ist das wanderwelt-Fenster („wanderwelt.exe fenster“):
// ein eigener Prozess mit Gio, der nur über die lokale Steuerung
// (fensterapi) mit der laufenden Starter-Instanz spricht. Schließen beendet
// den Prozess; ein Absturz oder Hängen des Fensters berührt Hosten, Stände
// und Zuschlag nie.
//
// Nur unter Windows; anderswo liefert Run winsys.ErrUnsupported (Gio
// bräuchte dort cgo).
package fenster

import (
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Rahmen des Fensters: Kopf mit Symbol und Zustand, Reiter, Inhalt,
// Hinweis-Leiste unten, Rückfragen darüber.

const (
	// hinweisDauer: So lange steht ein Hinweis der Instanz in der Leiste.
	hinweisDauer = 8 * time.Second
	// eigenerHinweis: So lange stehen Bestätigungen des Fensters selbst.
	eigenerHinweis = 4 * time.Second
	maxHinweise    = 3
	// inhaltBreite: breiter wird der Inhalt nicht (große Fenster).
	inhaltBreite = 860
)

var reiterNamen = [anzahlReiter]string{"Start", "Welt", "Hosten", "Mitglieder", "Log"}

type sichtbarerHinweis struct {
	art  string // "info" | "fehler"
	text string
	bis  time.Time
}

// rueckfrage ist ein Dialog über dem Fenster.
type rueckfrage struct {
	titel, text, ja string
	nein            string // Text des Abbruch-Knopfs ("" = „Abbrechen“)
	gefahr          bool
	beiJa           func()
}

// oberflaeche ist der Zustand der Oberfläche (nur aus der Fenster-Goroutine).
type oberflaeche struct {
	th *thema
	d  *daten

	reiter reiter
	tabs   [anzahlReiter]widget.Clickable

	hinweisRunde   uint64
	hinweisSeq     int64
	hinweisBekannt bool
	hinweise       []sichtbarerHinweis

	rf         *rueckfrage
	rfJa, rfNo widget.Clickable
	rfFlaeche  int // Kennung für Klicks auf den Schleier

	starten widget.Clickable

	start  startAnsicht
	welt   weltAnsicht
	hosten hostenAnsicht
	mitgl  mitgliederAnsicht
	log    logAnsicht
}

func neueOberflaeche(th *thema, d *daten) *oberflaeche {
	u := &oberflaeche{th: th, d: d}
	u.start.liste.Axis = layout.Vertical
	u.welt.liste.Axis = layout.Vertical
	u.hosten.liste.Axis = layout.Vertical
	u.mitgl.liste.Axis = layout.Vertical
	u.log.liste.Axis = layout.Vertical
	u.log.liste.ScrollToEnd = true
	return u
}

// zeige blendet einen eigenen Hinweis ein (Bestätigung einer Aktion).
func (u *oberflaeche) zeige(art, text string, jetzt time.Time) {
	u.hinweise = append(u.hinweise, sichtbarerHinweis{art: art, text: text, bis: jetzt.Add(eigenerHinweis)})
	if n := len(u.hinweise); n > maxHinweise {
		u.hinweise = u.hinweise[n-maxHinweise:]
	}
}

// frage öffnet eine Rückfrage.
func (u *oberflaeche) frage(r rueckfrage) { u.rf = &r }

// layout zeichnet ein Bild.
func (u *oberflaeche) layout(gtx layout.Context) layout.Dimensions {
	u.d.abarbeiten()
	s := u.d.lesen()
	u.hinweiseNachziehen(gtx.Now, s)

	z := s.z
	admin := z != nil && z.Me != nil && z.Me.Admin
	if u.reiter == reiterMitglieder && !admin {
		u.reiter = reiterStart
	}
	// Bei offener Rückfrage ist alles darunter gesperrt, auch für die
	// Tastatur (Tab, Enter).
	haupt := gtx
	if u.rf != nil {
		haupt = gtx.Disabled()
	}
	for i := range u.tabs {
		if u.tabs[i].Clicked(haupt) && (reiter(i) != reiterMitglieder || admin) {
			u.reiter = reiter(i)
		}
	}
	u.d.setzeReiter(u.reiter)

	paint.FillShape(gtx.Ops, u.th.f.hinter, clip.Rect(image.Rectangle{Max: gtx.Constraints.Max}).Op())

	dims := layout.Flex{Axis: layout.Vertical}.Layout(haupt,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.kopf(gtx, s, admin) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.inhalt(gtx, s, admin) }),
	)
	u.hinweisLeiste(gtx)
	if u.rf != nil {
		u.rueckfrageZeichnen(gtx)
	}
	return dims
}

// hinweiseNachziehen übernimmt neue Hinweise der Instanz (nach Seq) und
// räumt abgelaufene weg. Was beim Öffnen (bzw. bei einer neuen Instanz)
// schon da war, gilt als gesehen.
func (u *oberflaeche) hinweiseNachziehen(jetzt time.Time, s schnappschuss) {
	if s.runde != u.hinweisRunde {
		u.hinweisRunde, u.hinweisBekannt, u.hinweisSeq = s.runde, false, 0
	}
	if s.z != nil {
		for _, h := range s.z.Hinweise {
			if h.Seq <= u.hinweisSeq {
				continue
			}
			u.hinweisSeq = h.Seq
			if u.hinweisBekannt {
				u.hinweise = append(u.hinweise, sichtbarerHinweis{art: h.Art, text: h.Text, bis: jetzt.Add(hinweisDauer)})
			}
		}
		u.hinweisBekannt = true
	}
	n := 0
	for _, h := range u.hinweise {
		if jetzt.Before(h.bis) {
			u.hinweise[n] = h
			n++
		}
	}
	u.hinweise = u.hinweise[:n]
	if n > maxHinweise {
		u.hinweise = u.hinweise[n-maxHinweise:]
	}
}

// ---- Kopf ----

func (u *oberflaeche) kopf(gtx layout.Context, s schnappschuss, admin bool) layout.Dimensions {
	t := u.th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	m := op.Record(gtx.Ops)
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 14, Left: 20, Right: 20, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return t.symbol(gtx, 26) }),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 18, fett: true, farbe: t.f.text}, "wanderwelt")
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.zustandsMarke(gtx, s) }),
				)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if s.verb != verbunden || s.z == nil {
				return layout.Dimensions{Size: image.Pt(0, gtx.Dp(10))}
			}
			return layout.Inset{Left: 8, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.reiterLeiste(gtx, admin)
			})
		}),
	)
	call := m.Stop()
	rechteck(gtx, dims.Size, t.f.flaeche, 0)
	call.Add(gtx.Ops)
	// Linie unter dem Kopf
	st := op.Offset(image.Pt(0, dims.Size.Y-gtx.Dp(1))).Push(gtx.Ops)
	rechteck(gtx, image.Pt(dims.Size.X, gtx.Dp(1)), t.f.rand, 0)
	st.Pop()
	return dims
}

// zustandsMarke: Punkt und Text rechts im Kopf („Hostet“, „Bereit“ …).
func (u *oberflaeche) zustandsMarke(gtx layout.Context, s schnappschuss) layout.Dimensions {
	t := u.th
	text, farbe := "", t.f.leise
	switch s.verb {
	case verbindet:
		text = "verbindet …"
	case startet:
		text, farbe = "startet …", t.f.warn
	case laeuftNicht:
		text = "läuft nicht"
	case verbunden:
		if s.z == nil {
			text, farbe = "keine Antwort", t.f.fehler
			break
		}
		text, farbe = gross(s.z.Phase), phasenFarbe(t, s.z)
		if text == "" {
			text = "Bereit"
		}
	}
	return hinterlegt(gtx, t.f.flaeche2, t.f.rand, 13, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return punkt(gtx, farbe, 8) }),
				layout.Rigid(layout.Spacer{Width: 7}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 13, fett: true, farbe: t.f.text, zeilen: 1}, text)
				}),
			)
		})
	})
}

// phasenFarbe: grün beim Hosten/Mitspielen, gelb bei laufenden Vorgängen,
// rot bei Fehlern, sonst grau.
func phasenFarbe(t *thema, z *fensterapi.Zustand) color.NRGBA {
	p := strings.ToLower(z.Phase)
	switch {
	case z.LastErr != "" || strings.Contains(p, "fehler"):
		return t.f.fehler
	case z.Hosting && z.Busy == "" && !strings.Contains(p, "start") && !strings.Contains(p, "lädt"),
		strings.Contains(p, "hostet"), strings.Contains(p, "spielt"):
		return t.f.akzent
	case z.Busy != "" || strings.Contains(p, "start") || strings.Contains(p, "lädt"):
		return t.f.warn
	}
	return t.f.leise
}

func (u *oberflaeche) reiterLeiste(gtx layout.Context, admin bool) layout.Dimensions {
	t := u.th
	var kinder []layout.FlexChild
	for i := range u.tabs {
		r := reiter(i)
		if r == reiterMitglieder && !admin {
			continue
		}
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			k := &u.tabs[i]
			aktiv := u.reiter == r
			return k.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				pointer.CursorPointer.Add(gtx.Ops)
				farbe := t.f.leise
				switch {
				case aktiv:
					farbe = t.f.text
				case k.Hovered():
					farbe = t.f.text
				}
				dims := layout.Inset{Top: 8, Bottom: 11, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 14, fett: aktiv, farbe: farbe, zeilen: 1}, reiterNamen[r])
				})
				if aktiv {
					h := gtx.Dp(3)
					st := op.Offset(image.Pt(gtx.Dp(8), dims.Size.Y-h)).Push(gtx.Ops)
					rechteck(gtx, image.Pt(dims.Size.X-gtx.Dp(16), h), t.f.akzent, h/2)
					st.Pop()
				}
				if gtx.Focused(k) {
					st := op.Offset(image.Pt(gtx.Dp(2), gtx.Dp(3))).Push(gtx.Ops)
					t.fokusRing(gtx, dims.Size.Sub(image.Pt(gtx.Dp(4), gtx.Dp(6))), 6)
					st.Pop()
				}
				return dims
			})
		}))
	}
	return layout.Flex{}.Layout(gtx, kinder...)
}

// ---- Inhalt ----

func (u *oberflaeche) inhalt(gtx layout.Context, s schnappschuss, admin bool) layout.Dimensions {
	switch {
	case s.verb == verbindet:
		return u.mittig(gtx, func(gtx layout.Context) layout.Dimensions {
			return u.th.leise("Verbinde mit wanderwelt …")(gtx)
		})
	case s.verb == startet || s.verb == laeuftNicht:
		return u.laeuftNichtAnsicht(gtx, s)
	case s.z == nil:
		return u.mittig(gtx, func(gtx layout.Context) layout.Dimensions {
			return u.th.karte(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.th.meldung(gtx, meldungFehler, "wanderwelt antwortet nicht wie erwartet", s.zErr)
			})
		})
	}
	switch u.reiter {
	case reiterWelt:
		return u.welt.layout(gtx, u, s, admin)
	case reiterHosten:
		return u.hosten.layout(gtx, u, s)
	case reiterMitglieder:
		return u.mitgl.layout(gtx, u, s)
	case reiterLog:
		return u.log.layout(gtx, u, s)
	}
	return u.start.layout(gtx, u, s)
}

// mittig zeichnet w in der Mitte des Inhalts (höchstens 520 dp breit).
func (u *oberflaeche) mittig(gtx layout.Context, w layout.Widget) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(40), gtx.Dp(520))
		gtx.Constraints.Min.X = 0
		return w(gtx)
	})
}

// laeuftNichtAnsicht: keine Instanz (oder sie startet gerade).
func (u *oberflaeche) laeuftNichtAnsicht(gtx layout.Context, s schnappschuss) layout.Dimensions {
	t := u.th
	startend := s.verb == startet
	if u.starten.Clicked(gtx) && !startend {
		u.d.starten() // setzt „startet“ sofort; kein Doppelstart
	}
	return u.mittig(gtx, func(gtx layout.Context) layout.Dimensions {
		return t.karteAbstand(gtx, 28, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return t.symbol(gtx, 56) }),
				layout.Rigid(layout.Spacer{Height: 16}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					txt := "wanderwelt läuft nicht"
					if startend {
						txt = "wanderwelt startet …"
					}
					return t.text(gtx, schrift{groesse: 19, fett: true, farbe: t.f.text, mitte: true}, txt)
				}),
				layout.Rigid(layout.Spacer{Height: 6}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					txt := "Das Fenster zeigt, was die wanderwelt im Infobereich der Taskleiste tut. Ohne sie lässt sich nicht spielen oder hosten."
					if startend {
						txt = "Einen Moment – das Fenster verbindet sich, sobald sie bereit ist."
					}
					return t.text(gtx, schrift{groesse: 14, farbe: t.f.leise, mitte: true}, txt)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if s.startFehl == "" || startend {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 13.5, farbe: t.f.fehler, mitte: true}, s.startFehl)
					})
				}),
				layout.Rigid(layout.Spacer{Height: 20}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					txt := "Starten"
					if startend {
						txt = "Startet …"
					}
					return t.grosserKnopf(gtx, &u.starten, txt, knopfHaupt, !startend)
				}),
			)
		})
	})
}

// spalte: Inhalt in einer mittigen Spalte mit Rand (für Listen-Elemente).
func spalte(gtx layout.Context, w layout.Widget) layout.Dimensions {
	rand := gtx.Dp(20)
	breite := min(gtx.Constraints.Max.X-2*rand, gtx.Dp(inhaltBreite))
	links := (gtx.Constraints.Max.X - breite) / 2
	g := gtx
	g.Constraints.Min.X, g.Constraints.Max.X = breite, breite
	st := op.Offset(image.Pt(links, 0)).Push(gtx.Ops)
	dims := w(g)
	st.Pop()
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, dims.Size.Y)}
}

// seite legt Elemente als scrollbare Spalte mit Abständen an.
func seite(gtx layout.Context, l *widget.List, teile []layout.Widget) layout.Dimensions {
	return l.List.Layout(gtx, len(teile), func(gtx layout.Context, i int) layout.Dimensions {
		oben, unten := unit.Dp(0), unit.Dp(14)
		if i == 0 {
			oben = 18
		}
		if i == len(teile)-1 {
			unten = 24
		}
		return layout.Inset{Top: oben, Bottom: unten}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return spalte(gtx, teile[i])
		})
	})
}

// gleichHoch legt zwei Widgets gleich breit nebeneinander und gibt beiden
// die Höhe des höheren (Karten in einer Reihe). Jedes wird zweimal
// gelayoutet – nur für Widgets ohne Eingaben.
func gleichHoch(gtx layout.Context, abstand unit.Dp, links, rechts layout.Widget) layout.Dimensions {
	a := gtx.Dp(abstand)
	breite := (gtx.Constraints.Max.X - a) / 2
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(breite, 0), Max: image.Pt(breite, gtx.Constraints.Max.Y)}
	// Erst messen (verworfen), dann mit gemeinsamer Höhe zeichnen.
	m := op.Record(gtx.Ops)
	h := max(links(g).Size.Y, rechts(g).Size.Y)
	m.Stop()
	g.Constraints.Min.Y = h
	links(g)
	st := op.Offset(image.Pt(breite+a, 0)).Push(gtx.Ops)
	rechts(g)
	st.Pop()
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

// fluss reiht Widgets nebeneinander und bricht um, wenn die Zeile voll ist.
func fluss(gtx layout.Context, abstand unit.Dp, ws []layout.Widget) layout.Dimensions {
	a := gtx.Dp(abstand)
	maxX := gtx.Constraints.Max.X
	x, y, zeile, breit := 0, 0, 0, 0
	g := gtx
	g.Constraints.Min = image.Point{}
	for _, w := range ws {
		m := op.Record(gtx.Ops)
		d := w(g)
		call := m.Stop()
		if x > 0 && x+d.Size.X > maxX {
			x, y, zeile = 0, y+zeile+a, 0
		}
		st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		x += d.Size.X + a
		zeile = max(zeile, d.Size.Y)
		breit = max(breit, x-a)
	}
	return layout.Dimensions{Size: image.Pt(breit, y+zeile)}
}

// ---- Hinweis-Leiste ----

func (u *oberflaeche) hinweisLeiste(gtx layout.Context) {
	if len(u.hinweise) == 0 {
		return
	}
	t := u.th
	frueh := u.hinweise[0].bis
	for _, h := range u.hinweise {
		if h.bis.Before(frueh) {
			frueh = h.bis
		}
	}
	gtx.Execute(op.InvalidateCmd{At: frueh})

	breite := min(gtx.Constraints.Max.X-gtx.Dp(40), gtx.Dp(560))
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(breite, 0), Max: image.Pt(breite, gtx.Constraints.Max.Y)}
	m := op.Record(gtx.Ops)
	var kinder []layout.FlexChild
	for i, h := range u.hinweise {
		if i > 0 {
			kinder = append(kinder, layout.Rigid(layout.Spacer{Height: 8}.Layout))
		}
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			art := meldungInfo
			if h.art == "fehler" {
				art = meldungFehler
			}
			// Auf eigener Fläche mit Rand, damit sie sich vom Inhalt abhebt.
			return hinterlegt(gtx, t.f.flaeche, t.f.rand, 9, func(gtx layout.Context) layout.Dimensions {
				return layout.UniformInset(1).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.meldung(gtx, art, "", h.text)
				})
			})
		}))
	}
	dims := layout.Flex{Axis: layout.Vertical}.Layout(g, kinder...)
	call := m.Stop()
	x := (gtx.Constraints.Max.X - dims.Size.X) / 2
	y := gtx.Constraints.Max.Y - dims.Size.Y - gtx.Dp(18)
	st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
}

// ---- Rückfrage ----

func (u *oberflaeche) rueckfrageZeichnen(gtx layout.Context) {
	t := u.th
	r := u.rf
	schliessen := false
	if u.rfNo.Clicked(gtx) {
		schliessen = true
	}
	if u.rfJa.Clicked(gtx) {
		u.rf = nil
		if r.beiJa != nil {
			r.beiJa()
		}
		return
	}
	// Escape schließt.
	for {
		e, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if k, ok := e.(key.Event); ok && k.State == key.Press {
			schliessen = true
		}
	}
	if schliessen {
		u.rf = nil
		return
	}

	// Schleier fängt alle Zeiger-Ereignisse ab.
	voll := clip.Rect(image.Rectangle{Max: gtx.Constraints.Max}).Push(gtx.Ops)
	paint.ColorOp{Color: t.f.schleier}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	event.Op(gtx.Ops, &u.rfFlaeche)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &u.rfFlaeche, Kinds: pointer.Press | pointer.Release | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}}); !ok {
			break
		}
	}
	voll.Pop()

	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(460))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return hinterlegt(gtx, t.f.flaeche, t.f.rand, 12, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(22).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 17, fett: true, farbe: t.f.text}, r.titel)
					}),
					layout.Rigid(layout.Spacer{Height: 10}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 14, farbe: t.f.text}, r.text)
					}),
					layout.Rigid(layout.Spacer{Height: 22}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								nein := r.nein
								if nein == "" {
									nein = "Abbrechen"
								}
								return t.knopf(gtx, &u.rfNo, nein, knopfNeben, true)
							}),
							layout.Rigid(layout.Spacer{Width: 10}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								art := knopfHaupt
								if r.gefahr {
									art = knopfGefahr
								}
								return t.knopf(gtx, &u.rfJa, r.ja, art, true)
							}),
						)
					}),
				)
			})
		})
	})
}
