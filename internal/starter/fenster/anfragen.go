package fenster

import (
	"cmp"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Abschnitt „Beitrittsanfragen“ im Reiter Mitglieder (nur Admins): wer sich
// selbst angemeldet hat, mit Annehmen und Ablehnen (je mit Rückfrage).

// anfrageAuftrag ist der Körper der Aktion „beitritt“.
type anfrageAuftrag struct {
	Email    string `json:"email"`
	Annehmen bool   `json:"annehmen"`
}

// anfragenZustand gehört zur mitgliederAnsicht.
type anfragenZustand struct {
	annehmen, ablehnen map[string]*widget.Clickable
	wartet             map[string]bool
	// erledigt: bearbeitet, die Liste zeigt die Anfrage aber noch; sie bleibt
	// verborgen, bis sie dort verschwindet.
	erledigt map[string]bool
	fehler   string
}

func (z *anfragenZustand) klick(m *map[string]*widget.Clickable, k string) *widget.Clickable {
	if *m == nil {
		*m = map[string]*widget.Clickable{}
	}
	c := (*m)[k]
	if c == nil {
		c = new(widget.Clickable)
		(*m)[k] = c
	}
	return c
}

// sichtbar sind die Anfragen, die noch nicht bearbeitet wurden, älteste
// zuerst (wie der Koordinator sie liefert).
func (z *anfragenZustand) sichtbar(alle []api.JoinRequestInfo) []api.JoinRequestInfo {
	if z.erledigt == nil {
		z.erledigt = map[string]bool{}
	}
	da := map[string]bool{}
	var out []api.JoinRequestInfo
	for _, r := range alle {
		k := strings.ToLower(r.Email)
		da[k] = true
		if !z.erledigt[k] {
			out = append(out, r)
		}
	}
	for k := range z.erledigt {
		if !da[k] {
			delete(z.erledigt, k)
		}
	}
	return out
}

// klicks wertet Annehmen/Ablehnen aus: erst die Rückfrage, dann die Aktion.
func (z *anfragenZustand) klicks(gtx layout.Context, u *oberflaeche, rs []api.JoinRequestInfo) {
	if z.wartet == nil {
		z.wartet = map[string]bool{}
	}
	for _, r := range rs {
		key, r := strings.ToLower(r.Email), r
		name := cmp.Or(r.MinecraftName, r.Email)
		an, ab := z.klick(&z.annehmen, key).Clicked(gtx), z.klick(&z.ablehnen, key).Clicked(gtx)
		if z.wartet[key] || (!an && !ab) {
			continue
		}
		annehmen := an
		senden := func() {
			z.wartet[key], z.fehler = true, ""
			u.d.aktion("beitritt", anfrageAuftrag{Email: r.Email, Annehmen: annehmen}, func(err error) {
				delete(z.wartet, key)
				if err != nil {
					z.fehler = err.Error()
					u.d.neuLaden(false, false, true) // vielleicht schon von jemand anderem bearbeitet
					return
				}
				z.erledigt[key] = true
				if annehmen {
					u.zeige("info", name+" ist jetzt Mitglied.", time.Now())
				} else {
					u.zeige("info", "Anfrage von "+name+" abgelehnt.", time.Now())
				}
				u.d.neuLaden(false, false, true)
			})
		}
		if annehmen {
			u.frage(rueckfrage{titel: "Anfrage annehmen?",
				text: name + " (" + r.Email + ") wird Mitglied und kann mit der Welt spielen.",
				ja:   "Annehmen", nein: "Zurück", beiJa: senden})
		} else {
			u.frage(rueckfrage{titel: "Anfrage ablehnen?",
				text: name + " (" + r.Email + ") wird nicht aufgenommen. Wer abgelehnt wurde, kann sich später neu anmelden.",
				ja:   "Ablehnen", nein: "Zurück", gefahr: true, beiJa: senden})
		}
	}
}

func (z *anfragenZustand) karte(gtx layout.Context, u *oberflaeche, rs []api.JoinRequestInfo) layout.Dimensions {
	t := u.th
	kopf := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, t.klein("BEITRITTSANFRAGEN")),
				layout.Flexed(1.4, t.klein("E-MAIL")),
				feste(spalteAnfrage, t.klein("ANGEMELDET")),
			)
		})
	}
	kinder := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return kartenZeileFarbe(gtx, t, t.f.flaeche2, true, false, kopf)
	})}
	for i, r := range rs {
		key := strings.ToLower(r.Email)
		frei := !z.wartet[key]
		letzte := i == len(rs)-1
		an, ab := z.klick(&z.annehmen, key), z.klick(&z.ablehnen, key)
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kartenZeile(gtx, t, false, letzte, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 9, Bottom: 9, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text, zeilen: 1}, cmp.Or(r.MinecraftName, "–"))
							})
						}),
						layout.Flexed(1.4, func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return t.text(gtx, schrift{groesse: 13.5, farbe: t.f.leise, zeilen: 1}, r.Email)
							})
						}),
						feste(spalteAnfrage, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise}, seit(r.Requested, gtx.Now))
								}),
								layout.Rigid(layout.Spacer{Width: 12}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return t.knopf(gtx, an, "Annehmen", knopfHaupt, frei)
								}),
								layout.Rigid(layout.Spacer{Width: 8}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return t.knopf(gtx, ab, "Ablehnen", knopfNeben, frei)
								}),
							)
						}),
					)
				})
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
}

// spalteAnfrage: Breite (dp) der rechten Spalte mit Zeit und Knöpfen.
const spalteAnfrage = 330

// feste Spalte fester Breite, damit Kopf und Zeilen übereinander stehen.
func feste(breite int, w layout.Widget) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		b := gtx.Dp(unit.Dp(breite))
		gtx.Constraints.Min.X, gtx.Constraints.Max.X = b, b
		d := w(gtx)
		d.Size.X = b
		return d
	})
}
