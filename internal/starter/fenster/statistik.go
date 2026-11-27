package fenster

import (
	"cmp"
	"fmt"
	"image"
	"slices"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Reiter „Statistik“ (jedes Mitglied): Spielzeit, Hostzeit, Host-Sitzungen
// und der Anteil an der gesamten Hostzeit je Mitglied, gesamt oder in den
// letzten 30 Tagen. Nur Text und Balken; die Zahlen kommen vom Koordinator.

// Zeiträume des Umschalters.
const (
	zeitraumGesamt = iota
	zeitraum30Tage
)

type statistikAnsicht struct {
	liste    widget.List
	zeitraum int
	gesamt   widget.Clickable
	tage30   widget.Clickable
}

// statistikZeile ist eine Zeile der Tabelle.
type statistikZeile struct {
	name    string
	spielS  int64
	hostS   int64
	sitzung int
	anteil  float64 // 0…1
	hatHost bool    // Anteil bekannt (es gab Hostzeit)
}

// statistikZeilen sortiert die Mitglieder nach Hostzeit (absteigend), dann
// Spielzeit, dann Name; Anteile rechnet es aus HostS neu, damit sie auch
// bei einem Koordinator ohne HostShare stimmen.
func statistikZeilen(p api.StatsPeriod) []statistikZeile {
	var gesamt int64
	for _, m := range p.Members {
		gesamt += m.HostS
	}
	zs := make([]statistikZeile, 0, len(p.Members))
	for _, m := range p.Members {
		z := statistikZeile{name: m.MinecraftName, spielS: m.PlayS, hostS: m.HostS, sitzung: m.HostSessions, hatHost: gesamt > 0}
		if gesamt > 0 {
			z.anteil = float64(m.HostS) / float64(gesamt)
		}
		zs = append(zs, z)
	}
	slices.SortFunc(zs, func(a, b statistikZeile) int {
		return cmp.Or(
			cmp.Compare(b.hostS, a.hostS),
			cmp.Compare(b.spielS, a.spielS),
			strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)),
		)
	})
	return zs
}

// statistikDauer: „–“, „unter 1 Min.“, „42 Min.“, „5 Std. 07 Min.“.
func statistikDauer(s int64) string {
	switch {
	case s <= 0:
		return "–"
	case s < 60:
		return "unter 1 Min."
	case s < 3600:
		return fmt.Sprintf("%d Min.", s/60)
	}
	return fmt.Sprintf("%d Std. %02d Min.", s/3600, s%3600/60)
}

// statistikProzent: „42 %“, „unter 1 %“, „–“ ohne Hostzeit.
func statistikProzent(z statistikZeile) string {
	switch {
	case !z.hatHost:
		return "–"
	case z.hostS == 0:
		return "0 %"
	case z.anteil < 0.01:
		return "unter 1 %"
	}
	return fmt.Sprintf("%d %%", int(z.anteil*100+0.5))
}

// Spaltenbreiten der Tabelle (dp); der Name bekommt den Rest.
const (
	spalteSpielzeit = 138
	spalteHostzeit  = 138
	spalteSitzungen = 96
	spalteAnteil    = 150
)

func (v *statistikAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	if v.gesamt.Clicked(gtx) {
		v.zeitraum = zeitraumGesamt
	}
	if v.tage30.Clicked(gtx) {
		v.zeitraum = zeitraum30Tage
	}
	art := func(z int) knopfArt {
		if v.zeitraum == z {
			return knopfHaupt
		}
		return knopfNeben
	}
	teile := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.gesamt, "Gesamt", art(zeitraumGesamt), true)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.tage30, "Letzte 30 Tage", art(zeitraum30Tage), true)
				}),
			)
		},
	}
	switch {
	case s.statistikErr != "" && s.statistik == nil:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungWarn, "Statistik nicht abrufbar", s.statistikErr)
		})
	case s.statistik == nil:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.karte(gtx, t.leise("Die Statistik wird geladen …"))
		})
	default:
		p := s.statistik.Total
		if v.zeitraum == zeitraum30Tage {
			p = s.statistik.Last30
		}
		zs := statistikZeilen(p)
		teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.tabelle(gtx, u, zs) })
		teile = append(teile, t.leise(v.fussnote(s.statistik)))
	}
	return seite(gtx, &v.liste, teile)
}

// fussnote erklärt die Zahlen: seit wann gezählt wird, wie genau.
func (v *statistikAnsicht) fussnote(st *api.Stats) string {
	txt := "Hostzeit: Zeit, in der jemand den Server für alle bereitgestellt hat. Spielzeit: Zeit auf dem Server. " +
		"Gezählt wird im Takt der Lebenszeichen des Hosts (etwa 10 Sekunden genau); fällt ein Host aus, zählt nur die Zeit bis zu seinem letzten Lebenszeichen."
	if st.Since.IsZero() {
		return txt + " Es wurde noch nichts gezählt."
	}
	return txt + " Gezählt wird seit " + st.Since.Local().Format("02.01.2006") + "."
}

func (v *statistikAnsicht) tabelle(gtx layout.Context, u *oberflaeche, zs []statistikZeile) layout.Dimensions {
	t := u.th
	zelle := func(breite unit.Dp, w layout.Widget) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := gtx.Dp(breite)
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = b, b
			d := w(gtx)
			d.Size.X = b
			return d
		})
	}
	kopf := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, t.klein("MINECRAFT-NAME")),
				zelle(spalteSpielzeit, t.klein("SPIELZEIT")),
				zelle(spalteHostzeit, t.klein("HOSTZEIT")),
				zelle(spalteSitzungen, t.klein("SITZUNGEN")),
				zelle(spalteAnteil, t.klein("ANTEIL AN HOSTZEIT")),
			)
		})
	}
	kinder := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return kartenZeileFarbe(gtx, t, t.f.flaeche2, true, len(zs) == 0, kopf)
	})}
	text := func(txt string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return t.text(gtx, schrift{groesse: 14, farbe: t.f.text, zeilen: 1}, txt)
		}
	}
	for i, z := range zs {
		letzte := i == len(zs)-1
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kartenZeile(gtx, t, false, letzte, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 11, Bottom: 11, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text, zeilen: 1}, cmp.Or(z.name, "–"))
							})
						}),
						zelle(spalteSpielzeit, text(statistikDauer(z.spielS))),
						zelle(spalteHostzeit, text(statistikDauer(z.hostS))),
						zelle(spalteSitzungen, text(fmt.Sprint(z.sitzung))),
						zelle(spalteAnteil, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									return t.balken(gtx, float32(z.anteil), t.f.akzent, 8)
								}),
								layout.Rigid(layout.Spacer{Width: 10}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									gtx.Constraints.Min.X = gtx.Dp(58)
									return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise, zeilen: 1, rechts: true}, statistikProzent(z))
								}),
							)
						}),
					)
				})
			})
		}))
	}
	if len(zs) == 0 {
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(0, 0)}
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
}
