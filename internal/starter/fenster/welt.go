package fenster

import (
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Reiter „Welt“: Speicher, Welt übernehmen (Admins), Welten mit Ständen,
// Zurückholen (Admins).

type weltAnsicht struct {
	liste widget.List

	offen   map[string]bool // Welt aufgeklappt (Archive anfangs zu)
	kopf    map[string]*widget.Clickable
	zurueck map[api.GenRef]*widget.Clickable
	messen  widget.Clickable // nur zum Messen der Knopfbreite
	fehler  string
	wartet  bool

	budgetAendern, budgetOK, budgetAbbr widget.Clickable
	budgetEd                            widget.Editor
	budgetOffen, budgetWartet           bool
	budgetFehler                        string

	savesKnopf, savesNeu widget.Clickable
	savesOffen           bool
	uebernehmen          map[string]*widget.Clickable
	importFehler         string
	importWartet         bool

	verwaltung weltenVerwaltung // Welten der Gruppe verwalten (Admins)
}

// importAuftrag ist der Körper der Aktion „import“.
type importAuftrag struct {
	Dir              string `json:"dir"`
	OhneSpielerdatei bool   `json:"ohne_spielerdatei"`
}

func klick[K comparable](m *map[K]*widget.Clickable, k K) *widget.Clickable {
	if *m == nil {
		*m = map[K]*widget.Clickable{}
	}
	c, ok := (*m)[k]
	if !ok {
		c = new(widget.Clickable)
		(*m)[k] = c
	}
	return c
}

func (v *weltAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss, admin bool) layout.Dimensions {
	t := u.th
	jetzt := gtx.Now
	g := s.staende

	var teile []layout.Widget
	if v.fehler != "" {
		teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungFehler, "Zurückholen geht gerade nicht", v.fehler)
		}))
	}
	if g != nil && g.Storage != nil {
		teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
			return v.speicherKarte(gtx, u, g.Storage, admin)
		}))
	}
	if admin {
		// Nur mit Weltliste: ein Koordinator ohne mehrere Welten hat nichts zu verwalten.
		if len(weltenListe(s)) > 0 {
			teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
				return v.verwaltung.layout(gtx, u, s)
			}))
		}
		teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
			return v.importKarte(gtx, u, s, jetzt)
		}))
	}
	switch {
	case s.staendeErr != "" && g == nil:
		teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungWarn, "Stände nicht abrufbar", s.staendeErr)
		}))
	case g == nil:
		teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
			return t.karte(gtx, t.leise("Die Stände werden geladen …"))
		}))
	case len(g.Worlds) == 0:
		teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
			return t.karte(gtx, t.leise("Noch keine Stände – die Welt entsteht beim ersten Hosten."))
		}))
	default:
		if s.staendeErr != "" {
			teile = append(teile, mitAbstand(func(gtx layout.Context) layout.Dimensions {
				return t.meldung(gtx, meldungWarn, "Liste nicht aktuell", s.staendeErr)
			}))
		}
		teile = append(teile, v.weltTeile(gtx, u, s, g, admin, jetzt)...)
	}
	return seiteDicht(gtx, &v.liste, teile)
}

// mitAbstand: w mit Abstand darunter (Karten in seiteDicht).
func mitAbstand(w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 14}.Layout(gtx, w)
	}
}

// weltTeile: je Welt ein Kopf und (aufgeklappt) ihre Stände als Zeilen
// einer Karte.
func (v *weltAnsicht) weltTeile(gtx layout.Context, u *oberflaeche, s schnappschuss, g *api.GenerationsResponse, admin bool, jetzt time.Time) []layout.Widget {
	t := u.th
	if v.offen == nil {
		v.offen = map[string]bool{}
	}
	// Erstellzeiten aller Stände, für „zurückgeholt von …“.
	zeiten := map[api.GenRef]time.Time{}
	for _, w := range g.Worlds {
		for _, gen := range w.Generations {
			zeiten[api.GenRef{WorldID: w.WorldID, Number: gen.Number}] = gen.Created
		}
	}
	hostet := s.z != nil && s.z.Status != nil && s.z.Status.Host != nil
	ausstehend := s.z != nil && s.z.Status != nil && s.z.Status.RestorePending != nil
	// Gibt es mehrere Welten der Gruppe, tragen die Köpfe deren Namen.
	gruppenwelten := map[string]bool{}
	for _, w := range g.Worlds {
		if w.GroupWorld != "" {
			gruppenwelten[w.GroupWorld] = true
		}
	}
	mehrere := len(gruppenwelten) > 1

	var teile []layout.Widget
	for wi, w := range g.Worlds {
		k := klick(&v.kopf, w.WorldID)
		offen, gesetzt := v.offen[w.WorldID]
		if !gesetzt {
			offen = w.Active
		}
		if k.Clicked(gtx) {
			offen = !offen
			v.offen[w.WorldID] = offen
		}
		abstand := 6
		if wi > 0 {
			abstand = 16
		}
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(abstand), Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return v.weltKopf(gtx, u, w, k, offen, mehrere, jetzt)
			})
		})
		if !offen {
			continue
		}
		if len(w.Generations) == 0 {
			teile = append(teile, func(gtx layout.Context) layout.Dimensions {
				return t.karte(gtx, t.leise("Keine Stände."))
			})
			continue
		}
		for i, gen := range w.Generations {
			erste, letzte := i == 0, i == len(w.Generations)-1
			aktuell := w.Active && i == 0
			ref := api.GenRef{WorldID: w.WorldID, Number: gen.Number}
			kn := klick(&v.zurueck, ref)
			if kn.Clicked(gtx) && admin && !aktuell && !v.wartet && !ausstehend {
				v.frageZurueckholen(u, w, gen, hostet, jetzt)
			}
			teile = append(teile, func(gtx layout.Context) layout.Dimensions {
				return kartenZeile(gtx, t, erste, letzte, func(gtx layout.Context) layout.Dimensions {
					return v.standZeile(gtx, u, w, gen, aktuell, admin && !aktuell, admin, !v.wartet && !ausstehend, kn, zeiten, jetzt)
				})
			})
		}
	}
	return teile
}

func (v *weltAnsicht) weltKopf(gtx layout.Context, u *oberflaeche, w api.WorldInfo, k *widget.Clickable, offen, mehrere bool, jetzt time.Time) layout.Dimensions {
	t := u.th
	titel := "Aktive Welt"
	if !w.Active {
		titel = "Archiv"
	}
	// Mit mehreren Welten steht der Name der Welt der Gruppe im Titel.
	if mehrere && w.GroupName != "" {
		titel = kuerze(w.GroupName, 30)
		if !w.Active {
			titel = "Archiv von " + titel
		}
	}
	info := fmt.Sprintf("%s · %d %s", kurzID(w.WorldID), len(w.Generations), plural(len(w.Generations), "Stand", "Stände"))
	if !w.Active && len(w.Generations) > 0 {
		info += " · zuletzt " + zeitpunkt(w.Generations[0].Created, jetzt)
	}
	return k.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return pfeil(gtx, t, offen) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(t.titel(titel)),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise, zeilen: 1}, info)
			}),
		)
	})
}

// pfeil: ▸ zu, ▾ offen (als Dreieck gezeichnet).
func pfeil(gtx layout.Context, t *thema, offen bool) layout.Dimensions {
	n := gtx.Dp(12)
	var p clip.Path
	p.Begin(gtx.Ops)
	f := float32(n)
	if offen {
		p.MoveTo(f32.Pt(f*0.15, f*0.3))
		p.LineTo(f32.Pt(f*0.85, f*0.3))
		p.LineTo(f32.Pt(f*0.5, f*0.75))
	} else {
		p.MoveTo(f32.Pt(f*0.3, f*0.15))
		p.LineTo(f32.Pt(f*0.75, f*0.5))
		p.LineTo(f32.Pt(f*0.3, f*0.85))
	}
	p.Close()
	paint.FillShape(gtx.Ops, t.f.leise, clip.Outline{Path: p.End()}.Op())
	return layout.Dimensions{Size: image.Pt(n, n)}
}

// standZeile: Zeit, Host, wer dabei war, Größe, Herkunft; rechts der Knopf.
func (v *weltAnsicht) standZeile(gtx layout.Context, u *oberflaeche, w api.WorldInfo, gen api.GenerationInfo, aktuell, knopf, platzFuerKnopf, frei bool, kn *widget.Clickable, zeiten map[api.GenRef]time.Time, jetzt time.Time) layout.Dimensions {
	t := u.th
	var info []string
	if gen.HostName != "" {
		info = append(info, "Host "+gen.HostName)
	}
	if len(gen.Players) > 0 {
		info = append(info, "dabei: "+strings.Join(gen.Players, ", "))
	}
	if gen.RestoredFrom != nil {
		info = append(info, "zurückgeholt von "+herkunft(*gen.RestoredFrom, w.WorldID, zeiten, jetzt))
	}
	return layout.Inset{Top: 11, Bottom: 11, Left: 16, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text, zeilen: 1}, gross(zeitpunkt(gen.Created, jetzt)))
							}),
							layout.Rigid(layout.Spacer{Width: 8}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								rel := seit(gen.Created, jetzt)
								if rel == "gestern" { // steht schon in „gestern 21:30“
									return layout.Dimensions{}
								}
								return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise, zeilen: 1}, rel)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if !aktuell {
									return layout.Dimensions{}
								}
								return layout.Inset{Left: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return t.chip(gtx, "aktuell", t.f.akzentHell, t.f.akzentText)
								})
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if len(info) == 0 {
							return layout.Dimensions{}
						}
						return layout.Inset{Top: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise, zeilen: 2}, strings.Join(info, " · "))
						})
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise, zeilen: 1, rechts: true}, groesse(gen.Size))
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if knopf {
					return t.knopf(gtx, kn, "Zurückholen", knopfNeben, frei)
				}
				if platzFuerKnopf {
					// Gleiche Breite wie der Knopf, damit die Größen untereinander stehen.
					m := op.Record(gtx.Ops)
					d := t.knopf(gtx.Disabled(), &v.messen, "Zurückholen", knopfNeben, false)
					m.Stop()
					return layout.Dimensions{Size: image.Pt(d.Size.X, 0)}
				}
				return layout.Dimensions{}
			}),
		)
	})
}

// herkunft: „Stand 12 (heute 13:00)“, aus einer anderen Welt mit deren
// Kennung.
func herkunft(r api.GenRef, welt string, zeiten map[api.GenRef]time.Time, jetzt time.Time) string {
	s := "Stand " + strconv.FormatInt(r.Number, 10)
	if r.WorldID != welt {
		s += " aus " + kurzID(r.WorldID)
	}
	if t, ok := zeiten[r]; ok {
		s += " (" + zeitpunkt(t, jetzt) + ")"
	}
	return s
}

func (v *weltAnsicht) frageZurueckholen(u *oberflaeche, w api.WorldInfo, gen api.GenerationInfo, hostet bool, jetzt time.Time) {
	wann := zeitpunkt(gen.Created, jetzt)
	text := "Alles seit " + wann + " wird für alle zurückgesetzt, auch Inventare und Positionen. Fortfahren?"
	if !w.Active {
		text = "Die archivierte Welt " + kurzID(w.WorldID) + " wird wieder die aktive, mit dem Stand von " + wann +
			". Die bisher aktive Welt kommt ins Archiv (auch Inventare und Positionen). Fortfahren?"
	}
	if hostet {
		text += "\n\nGerade hostet jemand: Im Spiel kommt eine Ansage, nach 30 Sekunden startet der Server neu."
	}
	req := api.RestoreRequest{WorldID: w.WorldID, Number: gen.Number}
	u.frage(rueckfrage{
		titel: "Stand von " + wann + " zurückholen?", text: text, ja: "Zurückholen", gefahr: true,
		beiJa: func() {
			v.wartet, v.fehler = true, ""
			u.d.aktion("zurueckholen", req, func(err error) {
				v.wartet = false
				if err != nil {
					v.fehler = err.Error()
					return
				}
				msg := "Der Stand von " + wann + " ist zurückgeholt."
				if hostet {
					msg = "Zurückholen angefordert – der Server startet in 30 Sekunden neu."
				}
				u.zeige("info", msg, time.Now())
				u.d.neuLaden(true, false, false)
			})
		},
	})
}

// ---- Speicher ----

func (v *weltAnsicht) speicherKarte(gtx layout.Context, u *oberflaeche, si *api.StorageInfo, admin bool) layout.Dimensions {
	t := u.th
	if v.budgetAendern.Clicked(gtx) && admin {
		v.budgetOffen, v.budgetFehler = true, ""
		v.budgetEd.SingleLine, v.budgetEd.Submit = true, true
		v.budgetEd.Filter = "0123456789,."
		v.budgetEd.SetText(strings.Replace(strconv.FormatFloat(float64(si.BudgetBytes)/(1<<30), 'f', -1, 64), ".", ",", 1))
		gtx.Execute(key.FocusCmd{Tag: &v.budgetEd})
	}
	if v.budgetAbbr.Clicked(gtx) {
		v.budgetOffen, v.budgetFehler = false, ""
	}
	uebernehmen := v.budgetOK.Clicked(gtx)
	for {
		e, ok := v.budgetEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := e.(widget.SubmitEvent); ok {
			uebernehmen = true
		}
	}
	if uebernehmen && v.budgetOffen && !v.budgetWartet {
		if n, err := gbEingabe(v.budgetEd.Text()); err != nil {
			v.budgetFehler = err.Error()
		} else {
			v.budgetWartet, v.budgetFehler = true, ""
			u.d.aktion("speicher", api.BudgetRequest{BudgetBytes: n}, func(err error) {
				v.budgetWartet = false
				if err != nil {
					v.budgetFehler = err.Error()
					return
				}
				v.budgetOffen = false
				u.zeige("info", "Speicherbudget: "+groesse(n), time.Now())
				u.d.neuLaden(true, false, false)
			})
		}
	}

	anteil := float32(0)
	if si.BudgetBytes > 0 {
		anteil = float32(float64(si.UsedBytes) / float64(si.BudgetBytes))
	}
	farbe := t.f.akzent
	if si.OverBudget {
		farbe = t.f.warn
	}
	txt := "Speicher: " + groesse(si.UsedBytes) + " von " + groesse(si.BudgetBytes) + " belegt"
	if si.DiskFreeBytes > 0 {
		txt += " · " + groesse(si.DiskFreeBytes) + " frei auf dem Server"
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, t.klein("SPEICHER AUF DEM SERVER")),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if !admin || v.budgetOffen {
							return layout.Dimensions{}
						}
						return t.knopf(gtx, &v.budgetAendern, "Budget ändern", knopfLeise, true)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return t.balken(gtx, anteil, farbe, 8) }),
			layout.Rigid(layout.Spacer{Height: 9}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.text(gtx, schrift{groesse: 13.5, farbe: t.f.text}, txt)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !si.OverBudget {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 4}.Layout(gtx, t.farbig("Über dem Budget, obwohl ältere Stände schon aufgeräumt sind. Budget erhöhen oder Platz schaffen.", t.f.warn))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !v.budgetOffen {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return v.budgetFormular(gtx, u)
				})
			}),
		)
	})
}

func (v *weltAnsicht) budgetFormular(gtx layout.Context, u *oberflaeche) layout.Dimensions {
	t := u.th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(t.leise("Neues Budget")),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return eingabe(gtx, t, &v.budgetEd, "20", 90)
				}),
				layout.Rigid(layout.Spacer{Width: 6}.Layout),
				layout.Rigid(t.normal("GB")),
				layout.Rigid(layout.Spacer{Width: 14}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.budgetOK, "Übernehmen", knopfHaupt, !v.budgetWartet)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.budgetAbbr, "Abbrechen", knopfNeben, !v.budgetWartet)
				}),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if v.budgetFehler == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: 8}.Layout(gtx, t.farbig(v.budgetFehler, t.f.fehler))
		}),
	)
}

// eingabe: einzeiliges Feld mit Rand, breite dp breit.
func eingabe(gtx layout.Context, t *thema, ed *widget.Editor, hint string, breite int) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Dp(unit.Dp(breite))
	gtx.Constraints.Max.X = gtx.Constraints.Min.X
	return hinterlegt(gtx, t.f.flaeche, t.f.akzent, 7, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 7, Bottom: 7, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			e := material.Editor(t.mat, ed, hint)
			e.TextSize = 14
			e.Color = t.f.text
			e.HintColor = t.f.leise
			return e.Layout(gtx)
		})
	})
}

// ---- Welt übernehmen ----

func (v *weltAnsicht) importKarte(gtx layout.Context, u *oberflaeche, s schnappschuss, jetzt time.Time) layout.Dimensions {
	t := u.th
	if v.savesKnopf.Clicked(gtx) {
		v.savesOffen = !v.savesOffen
		u.d.setzeSavesWunsch(v.savesOffen)
	}
	if v.savesNeu.Clicked(gtx) {
		u.d.neuLaden(false, true, false)
	}
	knopfText := "Speicherstände anzeigen"
	if v.savesOffen {
		knopfText = "Ausblenden"
	}
	busy := s.z != nil && s.z.Busy != ""
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(t.titel("Welt übernehmen")),
							layout.Rigid(layout.Spacer{Height: 3}.Layout),
							layout.Rigid(t.leise("Macht eine Einzelspieler-Welt von diesem PC zur Gruppenwelt. Die bisherige Welt kommt ins Archiv und lässt sich zurückholen.")),
						)
					}),
					layout.Rigid(layout.Spacer{Width: 14}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.savesKnopf, knopfText, knopfNeben, true)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if v.importFehler == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 10}.Layout(gtx, t.farbig(v.importFehler, t.f.fehler))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !v.savesOffen {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return v.speicherstaende(gtx, u, s, busy, jetzt)
				})
			}),
		)
	})
}

func (v *weltAnsicht) speicherstaende(gtx layout.Context, u *oberflaeche, s schnappschuss, busy bool, jetzt time.Time) layout.Dimensions {
	t := u.th
	switch {
	case s.savesErr != "" && s.saves == nil:
		return t.farbig(s.savesErr, t.f.fehler)(gtx)
	case s.saves == nil:
		return t.leise("Sucht Einzelspieler-Welten …")(gtx)
	case len(s.saves) == 0:
		return t.leise("Keine Einzelspieler-Welten auf diesem PC gefunden.")(gtx)
	}
	var kinder []layout.FlexChild
	for i, sv := range s.saves {
		k := klick(&v.uebernehmen, sv.Folder)
		if k.Clicked(gtx) && sv.Problem == "" && !v.importWartet && !busy {
			v.frageImport(u, sv)
		}
		erste, letzte := i == 0, i == len(s.saves)-1
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kartenZeileFarbe(gtx, t, t.f.flaeche2, erste, letzte, func(gtx layout.Context) layout.Dimensions {
				return v.speicherstandZeile(gtx, u, sv, k, !v.importWartet && !busy, jetzt)
			})
		}))
	}
	kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return t.knopf(gtx, &v.savesNeu, "Neu einlesen", knopfLeise, true)
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
}

func (v *weltAnsicht) speicherstandZeile(gtx layout.Context, u *oberflaeche, sv fensterapi.Speicherstand, k *widget.Clickable, frei bool, jetzt time.Time) layout.Dimensions {
	t := u.th
	name := sv.LevelName
	if name == "" {
		name = sv.Folder
	}
	var info []string
	if name != sv.Folder {
		info = append(info, "Ordner „"+sv.Folder+"“")
	}
	if sv.Version != "" {
		info = append(info, "Minecraft "+sv.Version)
	}
	info = append(info, groesse(sv.Size))
	if !sv.LastPlayed.IsZero() {
		info = append(info, "zuletzt gespielt "+seit(sv.LastPlayed, jetzt))
	}
	return layout.Inset{Top: 10, Bottom: 10, Left: 14, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text, zeilen: 1}, name)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise, zeilen: 2}, strings.Join(info, " · "))
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						switch {
						case sv.Problem != "":
							return layout.Inset{Top: 4}.Layout(gtx, t.farbig(sv.Problem, t.f.fehler))
						case !sv.PlayerFile:
							return layout.Inset{Top: 4}.Layout(gtx, t.farbig("Keine Spielerdatei: Inventar und Position aus dieser Welt gehen verloren.", t.f.warn))
						}
						return layout.Dimensions{}
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.knopf(gtx, k, "Übernehmen", knopfNeben, frei && sv.Problem == "")
			}),
		)
	})
}

func (v *weltAnsicht) frageImport(u *oberflaeche, sv fensterapi.Speicherstand) {
	name := sv.LevelName
	if name == "" {
		name = sv.Folder
	}
	text := "„" + name + "“ wird zur Welt der ganzen Gruppe. Die bisherige Welt kommt ins Archiv und lässt sich unter „Welt“ zurückholen."
	if !sv.PlayerFile {
		text += "\n\nDiese Welt hat keine Spielerdatei: Inventar und Position aus der Einzelspieler-Welt gehen verloren."
	}
	// Nur Ordnernamen: Die Liste kommt von der Instanz auf diesem PC.
	ordner := filepath.Base(sv.Folder)
	if ordner != sv.Folder || ordner == "." || ordner == ".." {
		v.importFehler = "Ungültiger Ordnername: " + sv.Folder
		return
	}
	req := importAuftrag{
		Dir:              filepath.Join(deps.MinecraftDir(), "saves", ordner),
		OhneSpielerdatei: !sv.PlayerFile,
	}
	u.frage(rueckfrage{
		titel: "Welt übernehmen?", text: text, ja: "Übernehmen",
		beiJa: func() {
			v.importWartet, v.importFehler = true, ""
			u.d.aktion("import", req, func(err error) {
				v.importWartet = false
				if err != nil {
					v.importFehler = err.Error()
					return
				}
				u.zeige("info", "„"+name+"“ wird übernommen – den Fortschritt zeigt der Reiter Start.", time.Now())
				u.d.neuLaden(true, true, false)
			})
		},
	})
}

// ---- Karten aus Zeilen ----

// kartenZeile zeichnet eine Zeile einer Karte: Rand außen, Linie zwischen
// den Zeilen, runde Ecken nur oben an der ersten und unten an der letzten.
func kartenZeile(gtx layout.Context, t *thema, erste, letzte bool, w layout.Widget) layout.Dimensions {
	return kartenZeileFarbe(gtx, t, t.f.flaeche, erste, letzte, w)
}

func kartenZeileFarbe(gtx layout.Context, t *thema, bg color.NRGBA, erste, letzte bool, w layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	call := m.Stop()
	r := gtx.Dp(10)
	b := gtx.Dp(1)
	ecken := func(rect image.Rectangle, rr int) clip.RRect {
		c := clip.RRect{Rect: rect}
		if erste {
			c.NW, c.NE = rr, rr
		}
		if letzte {
			c.SW, c.SE = rr, rr
		}
		return c
	}
	aussen := image.Rectangle{Max: dims.Size}
	paint.FillShape(gtx.Ops, t.f.rand, ecken(aussen, r).Op(gtx.Ops))
	innen := aussen
	innen.Min.X += b
	innen.Max.X -= b
	innen.Max.Y -= b
	if erste {
		innen.Min.Y += b
	}
	paint.FillShape(gtx.Ops, bg, ecken(innen, r-b).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

// seiteDicht: wie seite, aber ohne Abstand zwischen den Teilen (Zeilen
// einer Karte schließen aneinander an; Abstände setzen die Teile selbst).
func seiteDicht(gtx layout.Context, l *widget.List, teile []layout.Widget) layout.Dimensions {
	return l.List.Layout(gtx, len(teile), func(gtx layout.Context, i int) layout.Dimensions {
		oben, unten := unit.Dp(0), unit.Dp(0)
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

func plural(n int, eins, mehr string) string {
	if n == 1 {
		return eins
	}
	return mehr
}
