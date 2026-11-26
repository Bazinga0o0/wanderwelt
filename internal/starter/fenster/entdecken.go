package fenster

import (
	"cmp"
	"fmt"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Reiter „Entdecken“: gelistete Gruppen der tunnel-Server dieses PCs mit
// Titel, Beschreibung, Mitgliederzahl und dem eigenen Zustand. „Beitritt
// anfragen“ fragt den Minecraft-Namen (wie bei der Einrichtung) und schickt
// die Anfrage über die Instanz. Titel und Beschreibung kommen von fremden
// Servern: Sie werden nur als Text gezeichnet (nie als HTML oder Markup
// ausgewertet).

type entdeckenAnsicht struct {
	liste         widget.List
	aktualisieren widget.Clickable
	anfragen      map[string]*widget.Clickable
	// gemerkt: Adressen, für die gerade eine Anfrage unterwegs ist oder die
	// gerade gestellt wurden (bis die Liste „angefragt“ zeigt).
	gestellt map[string]bool
	fehler   string

	// Namensformular: nameFuer ist die Adresse der Gruppe in Bearbeitung.
	nameFuer, nameTitel string
	nameWartet          bool
	nameFehler          string
	nameEd              widget.Editor
	nameOK, nameAbbr    widget.Clickable
}

// beitrittAuftrag ist der Körper der Aktion „entdeckenbeitritt“.
type beitrittAuftrag struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

func (v *entdeckenAnsicht) klick(url string) *widget.Clickable {
	if v.anfragen == nil {
		v.anfragen = map[string]*widget.Clickable{}
	}
	k := v.anfragen[url]
	if k == nil {
		k = new(widget.Clickable)
		v.anfragen[url] = k
	}
	return k
}

// bedienen wertet Klicks aus (Aktualisieren, Beitritt anfragen, Formular).
func (v *entdeckenAnsicht) bedienen(gtx layout.Context, u *oberflaeche, e *fensterapi.Entdecken) {
	if v.aktualisieren.Clicked(gtx) {
		u.d.entdeckenNeuLaden()
	}
	if e != nil {
		for _, g := range e.Gruppen {
			if !v.klick(g.URL).Clicked(gtx) || v.nameWartet || g.Zustand != fensterapi.EntdeckenKeiner || g.Lokal || v.gestellt[g.URL] {
				continue
			}
			v.nameFuer, v.nameTitel, v.nameFehler = g.URL, cmp.Or(g.Titel, g.URL), ""
			v.nameEd.SingleLine, v.nameEd.Submit = true, true
			v.nameEd.Filter = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
			v.nameEd.SetText(e.MinecraftName)
			gtx.Execute(key.FocusCmd{Tag: &v.nameEd})
		}
	}
	if v.nameAbbr.Clicked(gtx) && !v.nameWartet {
		v.nameFuer, v.nameFehler = "", ""
	}
	senden := v.nameOK.Clicked(gtx)
	for {
		ev, ok := v.nameEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			senden = true
		}
	}
	if !senden || v.nameFuer == "" || v.nameWartet {
		return
	}
	name := v.nameEd.Text()
	if err := pruefeMcName(name); err != nil {
		v.nameFehler = err.Error()
		return
	}
	url, titel := v.nameFuer, v.nameTitel
	v.nameWartet, v.nameFehler = true, ""
	u.d.aktion("entdeckenbeitritt", beitrittAuftrag{URL: url, Name: name}, func(err error) {
		v.nameWartet = false
		if err != nil {
			v.nameFehler = err.Error()
			return
		}
		if v.gestellt == nil {
			v.gestellt = map[string]bool{}
		}
		v.gestellt[url] = true
		if v.nameFuer == url {
			v.nameFuer = ""
		}
		u.zeige("info", "Beitritt zu „"+titel+"“ angefragt. Ein Admin der Gruppe muss dich noch annehmen.", time.Now())
		u.d.entdeckenNeuLaden()
	})
}

func (v *entdeckenAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	e := s.entdecken
	v.bedienen(gtx, u, e)
	// Was die Liste schon als „angefragt“ zeigt, braucht die Merkung nicht mehr.
	if e != nil {
		for _, g := range e.Gruppen {
			if g.Zustand != fensterapi.EntdeckenKeiner {
				delete(v.gestellt, g.URL)
			}
		}
	}

	var teile []layout.Widget
	teile = append(teile, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(t.titel("Gruppen entdecken")),
					layout.Rigid(t.leise("Gruppen, die ihr Tunnel-Server auflistet. Mit „Beitritt anfragen“ meldest du dich dort an; ein Admin nimmt dich an.")),
				)
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.knopf(gtx, &v.aktualisieren, "Aktualisieren", knopfNeben, !s.entdeckenLaeuft)
			}),
		)
	})
	if v.nameFuer != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.nameFormular(gtx, u) })
	}
	if v.fehler != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungFehler, "Das ging nicht", v.fehler)
		})
	}
	switch {
	case s.entdeckenErr != "":
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungWarn, "Entdecken ist gerade nicht möglich", s.entdeckenErr)
		})
	case e == nil:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.karte(gtx, t.leise("Frage die Tunnel-Server nach Gruppen …"))
		})
	}
	if e != nil {
		for _, h := range e.Hinweise {
			teile = append(teile, func(gtx layout.Context) layout.Dimensions {
				return t.meldung(gtx, meldungInfo, "Hinweis", h)
			})
		}
		if len(e.Gruppen) == 0 && len(e.Hinweise) == 0 {
			teile = append(teile, func(gtx layout.Context) layout.Dimensions {
				return t.karte(gtx, t.leise("Zurzeit listet kein Tunnel-Server Gruppen auf. Private Gruppen erreichst du mit dem Gruppencode."))
			})
		}
		for _, g := range e.Gruppen {
			teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.gruppenKarte(gtx, u, g) })
		}
	}
	return seite(gtx, &v.liste, teile)
}

func (v *entdeckenAnsicht) nameFormular(gtx layout.Context, u *oberflaeche) layout.Dimensions {
	t := u.th
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.normal("Beitritt zu „"+v.nameTitel+"“ anfragen")),
			layout.Rigid(layout.Spacer{Height: 4}.Layout),
			layout.Rigid(t.leise("Dein Minecraft-Java-Name (damit der Admin weiß, wer du bist, und du auf die Whitelist kommst):")),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return eingabe(gtx, t, &v.nameEd, "Minecraft-Name", 220)
					}),
					layout.Rigid(layout.Spacer{Width: 14}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.nameOK, "Anfragen", knopfHaupt, !v.nameWartet)
					}),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.nameAbbr, "Abbrechen", knopfNeben, !v.nameWartet)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if v.nameFehler == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 8}.Layout(gtx, t.farbig(v.nameFehler, t.f.fehler))
			}),
		)
	})
}

func (v *entdeckenAnsicht) gruppenKarte(gtx layout.Context, u *oberflaeche, g fensterapi.EntdeckenEintrag) layout.Dimensions {
	t := u.th
	zustand := g.Zustand
	if v.gestellt[g.URL] && zustand == fensterapi.EntdeckenKeiner {
		zustand = fensterapi.EntdeckenAngefragt
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 16, fett: true, farbe: t.f.text}, g.Titel)
					}),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return v.zustandsMarke(gtx, u, g, zustand) }),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if g.Beschreibung == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 14, farbe: t.f.text}, g.Beschreibung)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				txt := mitgliederText(g)
				return layout.Inset{Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise}, txt)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if zustand != fensterapi.EntdeckenKeiner || g.Lokal {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, v.klick(g.URL), "Beitritt anfragen", knopfHaupt, !v.nameWartet)
				})
			}),
		)
	})
}

func mitgliederText(g fensterapi.EntdeckenEintrag) string {
	switch {
	case g.Fehler != "":
		return g.Fehler
	case g.Mitglieder == 1:
		return "1 Mitglied"
	}
	return fmt.Sprintf("%d Mitglieder", g.Mitglieder)
}

func (v *entdeckenAnsicht) zustandsMarke(gtx layout.Context, u *oberflaeche, g fensterapi.EntdeckenEintrag, zustand string) layout.Dimensions {
	t := u.th
	switch zustand {
	case fensterapi.EntdeckenMitglied:
		txt := "Mitglied"
		if g.Lokal {
			txt = "Mitglied · in deiner Auswahl"
		}
		return t.chip(gtx, txt, blass(t.f.akzent), t.f.akzent)
	case fensterapi.EntdeckenAngefragt:
		return t.chip(gtx, "angefragt", blass(t.f.warn), t.f.warn)
	}
	return layout.Dimensions{}
}
