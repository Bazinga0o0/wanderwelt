package fenster

import (
	"image/color"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
)

// Reiter „Log“: die letzten Zeilen von wanderwelt.log, folgt unten.

type logAnsicht struct {
	liste  widget.List // ScrollToEnd: bleibt unten, solange man nicht hochscrollt
	ordner widget.Clickable
	text   string
	zeilen []string
	fehler string
}

// zeilenVon teilt den Log-Text und behält höchstens die letzten n Zeilen.
func zeilenVon(text string, n int) []string {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		return nil
	}
	z := strings.Split(text, "\n")
	if len(z) > n {
		z = z[len(z)-n:]
	}
	return z
}

func (v *logAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	if s.log != v.text || v.zeilen == nil {
		v.text = s.log
		v.zeilen = zeilenVon(s.log, logZeilen)
	}
	if v.ordner.Clicked(gtx) {
		v.fehler = ""
		u.d.aktion("logordner", nil, func(err error) {
			if err != nil {
				v.fehler = err.Error()
			}
		})
	}
	return spalteVoll(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 16, Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							txt := "Die letzten Zeilen von wanderwelt.log"
							if s.z != nil && s.z.Version != "" {
								txt += " · Version " + s.z.Version
							}
							switch {
							case v.fehler != "":
								return t.farbig(v.fehler, t.f.fehler)(gtx)
							case s.logErr != "":
								return t.farbig(s.logErr, t.f.fehler)(gtx)
							}
							return t.leise(txt)(gtx)
						}),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return t.knopf(gtx, &v.ordner, "Log-Ordner öffnen", knopfNeben, true)
						}),
					)
				})
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = gtx.Constraints.Max
					return hinterlegt(gtx, t.f.flaeche2, t.f.rand, 10, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min = gtx.Constraints.Max
						return layout.UniformInset(12).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							if len(v.zeilen) == 0 {
								txt := "Noch nichts im Log."
								if s.logAb.IsZero() && s.logErr == "" {
									txt = "Lädt …"
								}
								return t.leise(txt)(gtx)
							}
							return v.liste.List.Layout(gtx, len(v.zeilen), func(gtx layout.Context, i int) layout.Dimensions {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								return layout.Inset{Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return t.text(gtx, schrift{groesse: 12, mono: true, farbe: logFarbe(t, v.zeilen[i])}, v.zeilen[i])
								})
							})
						})
					})
				})
			}),
		)
	})
}

// logFarbe hebt Fehlerzeilen hervor.
func logFarbe(t *thema, zeile string) color.NRGBA {
	l := strings.ToLower(zeile)
	if strings.Contains(l, "fehler") || strings.Contains(l, "error") || strings.Contains(l, "panic") {
		return t.f.fehlerText
	}
	return t.f.text
}

// spalteVoll: wie spalte, aber über die ganze Höhe (für den Log-Reiter).
func spalteVoll(gtx layout.Context, w layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	return spalte(gtx, w)
}
