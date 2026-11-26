package fenster

import (
	"cmp"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Auswahl der Gruppe im Kopf des Fensters: erscheint erst, wenn der PC in
// mehreren Gruppen ist. Der Titel der Gruppe kommt vom Koordinator (nur
// Text, nie als HTML) und steht nur hier und im Tooltip, nie in Discord.

// gruppeAuftrag ist der Körper der Aktion „gruppe“.
type gruppeAuftrag struct {
	ID string `json:"id"`
}

type gruppenAuswahl struct {
	knoepfe map[string]*widget.Clickable
	wartet  bool
}

// gruppenName: Titel, sonst die Kennung; gekürzt (Titel stammen von fremden
// Servern).
func gruppenName(g fensterapi.GruppeEintrag) string {
	return kuerze(cmp.Or(g.Titel, g.ID), 28)
}

func (v *gruppenAuswahl) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	z := s.z
	if s.verb != verbunden || z == nil || len(z.Gruppen) < 2 {
		return layout.Dimensions{}
	}
	t := u.th
	if v.knoepfe == nil {
		v.knoepfe = map[string]*widget.Clickable{}
	}
	for _, g := range z.Gruppen {
		k := v.knoepfe[g.ID]
		if k == nil {
			k = new(widget.Clickable)
			v.knoepfe[g.ID] = k
		}
		if !k.Clicked(gtx) || g.Aktiv || v.wartet {
			continue
		}
		if z.WechselSperre != "" {
			u.zeige("fehler", z.WechselSperre, gtx.Now)
			continue
		}
		g := g
		v.wartet = true
		u.d.aktion("gruppe", gruppeAuftrag{ID: g.ID}, func(err error) {
			v.wartet = false
			if err != nil {
				u.zeige("fehler", err.Error(), time.Now())
				return
			}
			u.zeige("info", "Gruppe gewechselt: "+gruppenName(g), time.Now())
			u.d.neuLaden(true, true, true)
		})
	}
	kinder := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: 10}.Layout(gtx, t.leise("Gruppe"))
		}),
	}
	for _, g := range z.Gruppen {
		k := v.knoepfe[g.ID]
		art := knopfNeben
		if g.Aktiv {
			art = knopfHaupt
		}
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return t.knopf(gtx, k, gruppenName(g), art, !g.Aktiv && !v.wartet)
			})
		}))
	}
	return layout.Inset{Left: 20, Right: 20, Top: 4, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kinder...)
	})
}

// kuerze kürzt s auf höchstens n Zeichen (Runen) mit „…“.
func kuerze(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
