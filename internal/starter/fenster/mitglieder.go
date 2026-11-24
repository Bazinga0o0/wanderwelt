package fenster

import (
	"cmp"
	"errors"
	"image"
	"regexp"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Reiter „Mitglieder“ (nur Admins): Liste mit Admin/Op-Schaltern.

type mitgliederAnsicht struct {
	liste  widget.List
	admin  map[string]*widget.Bool
	op     map[string]*widget.Bool
	wartet map[string]bool
	// gemerkt: zuletzt gespeicherte Rollen (Admin, Op), bis die Liste sie zeigt.
	gemerkt map[string]*merker[[2]bool]
	fehler  string

	// Beitrittsanfragen (anfragen.go).
	anfr anfragenZustand

	// Minecraft-Namen ändern (Admin): nameFuer ist die E-Mail des Mitglieds in
	// Bearbeitung ("" = geschlossen), nameAnzeige sein bisheriger Name.
	aendern               map[string]*widget.Clickable
	nameFuer, nameAnzeige string
	nameWartet            bool
	nameFehler            string
	nameEd                widget.Editor
	nameOK, nameAbbr      widget.Clickable
}

// nameAuftrag ist der Körper der Aktion „mcname“.
type nameAuftrag struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

var mcNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

// pruefeMcName prüft die Eingabe vor dem Abschicken (der Koordinator prüft
// bei Mojang nach).
func pruefeMcName(s string) error {
	if !mcNameRE.MatchString(s) {
		return errors.New("Der Name besteht aus 3 bis 16 Buchstaben, Ziffern oder Unterstrichen.")
	}
	return nil
}

// nameDialog verwaltet Öffnen, Eingabe und Abschicken der Namensänderung.
func (v *mitgliederAnsicht) nameDialog(gtx layout.Context, u *oberflaeche, ms []api.MemberInfo) {
	if v.aendern == nil {
		v.aendern = map[string]*widget.Clickable{}
	}
	for _, m := range ms {
		k := v.aendern[strings.ToLower(m.Email)]
		if k != nil && k.Clicked(gtx) && !v.nameWartet {
			v.nameFuer, v.nameAnzeige, v.nameFehler = m.Email, cmp.Or(m.MinecraftName, "–"), ""
			v.nameEd.SingleLine, v.nameEd.Submit = true, true
			v.nameEd.Filter = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
			v.nameEd.SetText(m.MinecraftName)
			gtx.Execute(key.FocusCmd{Tag: &v.nameEd})
		}
	}
	if v.nameAbbr.Clicked(gtx) && !v.nameWartet {
		v.nameFuer, v.nameFehler = "", ""
	}
	senden := v.nameOK.Clicked(gtx)
	for {
		e, ok := v.nameEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := e.(widget.SubmitEvent); ok {
			senden = true
		}
	}
	if !senden || v.nameFuer == "" || v.nameWartet {
		return
	}
	name := strings.TrimSpace(v.nameEd.Text())
	if err := pruefeMcName(name); err != nil {
		v.nameFehler = err.Error()
		return
	}
	v.nameWartet, v.nameFehler = true, ""
	email := v.nameFuer
	u.d.aktion("mcname", nameAuftrag{Email: email, Name: name}, func(err error) {
		v.nameWartet = false
		if err != nil {
			v.nameFehler = err.Error()
			return
		}
		if v.nameFuer == email {
			v.nameFuer = ""
		}
		u.zeige("info", "Minecraft-Name geändert: "+name, time.Now())
		u.d.neuLaden(false, false, true)
	})
}

func (v *mitgliederAnsicht) nameFormular(gtx layout.Context, u *oberflaeche) layout.Dimensions {
	t := u.th
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.leise("Minecraft-Name ändern (bisher: "+v.nameAnzeige+")")),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return eingabe(gtx, t, &v.nameEd, "Name", 200)
					}),
					layout.Rigid(layout.Spacer{Width: 14}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.nameOK, "Übernehmen", knopfHaupt, !v.nameWartet)
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

// rolleAuftrag ist der Körper der Aktion „rolle“.
type rolleAuftrag struct {
	Email string `json:"email"`
	Admin bool   `json:"admin"`
	Op    bool   `json:"op"`
}

func schalterFuer(m *map[string]*widget.Bool, k string) *widget.Bool {
	if *m == nil {
		*m = map[string]*widget.Bool{}
	}
	b, ok := (*m)[k]
	if !ok {
		b = new(widget.Bool)
		(*m)[k] = b
	}
	return b
}

// Spaltenbreiten der Tabelle (dp); Name und E-Mail teilen sich den Rest.
const (
	spalteOnline = 92
	spalteAdmin  = 64
	spalteOp     = 56
)

func (v *mitgliederAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	if v.wartet == nil {
		v.wartet = map[string]bool{}
		v.gemerkt = map[string]*merker[[2]bool]{}
	}
	ms := slices.Clone(s.mitglieder)
	slices.SortFunc(ms, func(a, b api.MemberInfo) int {
		return cmp.Compare(strings.ToLower(cmp.Or(a.MinecraftName, a.Email)), strings.ToLower(cmp.Or(b.MinecraftName, b.Email)))
	})
	for _, m := range ms {
		key := strings.ToLower(m.Email)
		a, o := schalterFuer(&v.admin, key), schalterFuer(&v.op, key)
		if v.wartet[key] {
			continue
		}
		mk := v.gemerkt[key]
		if mk == nil {
			mk = new(merker[[2]bool])
			v.gemerkt[key] = mk
		}
		rollen, _ := mk.gilt([2]bool{m.Admin, m.Op}, gtx.Now, s.runde)
		a.Value, o.Value = rollen[0], rollen[1]
		ac, oc := a.Update(gtx), o.Update(gtx)
		if !ac && !oc {
			continue
		}
		req := rolleAuftrag{Email: m.Email, Admin: a.Value, Op: o.Value}
		name := cmp.Or(m.MinecraftName, m.Email)
		jetzt, runde := gtx.Now, s.runde
		senden := func() {
			v.wartet[key], v.fehler = true, ""
			a.Value, o.Value = req.Admin, req.Op
			u.d.aktion("rolle", req, func(err error) {
				delete(v.wartet, key)
				if err != nil {
					v.fehler = err.Error()
					return
				}
				mk.setze([2]bool{req.Admin, req.Op}, jetzt, runde)
				u.zeige("info", "Rollen von "+name+" geändert.", time.Now())
				u.d.neuLaden(false, false, true)
			})
		}
		// Sich selbst das Admin-Recht zu nehmen sperrt einen aus (der Reiter
		// verschwindet): erst nachfragen.
		if s.z != nil && s.z.Me != nil && strings.EqualFold(s.z.Me.Email, m.Email) && rollen[0] && !req.Admin {
			a.Value = true
			u.frage(rueckfrage{titel: "Eigenes Admin-Recht abgeben?",
				text: "Du nimmst dir selbst das Admin-Recht. Danach verschwindet dieser Reiter, und nur ein anderer Admin kann es dir zurückgeben.",
				ja:   "Abgeben", nein: "Behalten", gefahr: true, beiJa: senden})
			continue
		}
		senden()
	}

	v.nameDialog(gtx, u, ms)
	anfragen := v.anfr.sichtbar(s.anfragen)
	v.anfr.klicks(gtx, u, anfragen)
	var teile []layout.Widget
	if v.anfr.fehler != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungFehler, "Anfrage nicht bearbeitet", v.anfr.fehler)
		})
	}
	if s.anfragenErr != "" && !strings.Contains(s.anfragenErr, "zu alt") {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungWarn, "Beitrittsanfragen nicht abrufbar", s.anfragenErr)
		})
	}
	if len(anfragen) > 0 {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.anfr.karte(gtx, u, anfragen) })
	}
	if v.nameFuer != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.nameFormular(gtx, u) })
	}
	if v.fehler != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungFehler, "Rolle nicht geändert", v.fehler)
		})
	}
	switch {
	case s.mitgliederErr != "" && s.mitglieder == nil:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungWarn, "Mitglieder nicht abrufbar", s.mitgliederErr)
		})
	case s.mitglieder == nil:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.karte(gtx, t.leise("Die Mitglieder werden geladen …"))
		})
	default:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return v.tabelle(gtx, u, ms)
		})
	}
	teile = append(teile, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.leise("Neue Mitglieder anlegen oder entfernen: auf dem Server mit")),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = 0
				return hinterlegt(gtx, t.f.flaeche2, t.f.rand, 6, func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 13, mono: true, farbe: t.f.text}, "wanderwelt-koordinator mitglied neu …")
					})
				})
			}),
		)
	})
	return seite(gtx, &v.liste, teile)
}

func (v *mitgliederAnsicht) tabelle(gtx layout.Context, u *oberflaeche, ms []api.MemberInfo) layout.Dimensions {
	t := u.th
	zelle := func(breite unit.Dp, w layout.Widget) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := gtx.Dp(breite)
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = b, b
			d := w(gtx)
			d.Size.X = b // Spalte immer gleich breit, auch wenn der Inhalt schmaler ist
			return d
		})
	}
	kopf := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, t.klein("MINECRAFT-NAME")),
				layout.Flexed(1.4, t.klein("E-MAIL")),
				zelle(spalteOnline, t.klein("ONLINE")),
				zelle(spalteAdmin, t.klein("ADMIN")),
				zelle(spalteOp, t.klein("OP")),
			)
		})
	}
	kinder := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return kartenZeileFarbe(gtx, t, t.f.flaeche2, true, len(ms) == 0, kopf)
	})}
	for i, m := range ms {
		key := strings.ToLower(m.Email)
		a, o := schalterFuer(&v.admin, key), schalterFuer(&v.op, key)
		frei := !v.wartet[key]
		letzte := i == len(ms)-1
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kartenZeile(gtx, t, false, letzte, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 11, Bottom: 11, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
										return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text, zeilen: 1}, cmp.Or(m.MinecraftName, "–"))
									}),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										if v.aendern == nil {
											v.aendern = map[string]*widget.Clickable{}
										}
										k := v.aendern[key]
										if k == nil {
											k = new(widget.Clickable)
											v.aendern[key] = k
										}
										return t.knopf(gtx, k, "ändern", knopfLeise, !v.nameWartet)
									}),
								)
							})
						}),
						layout.Flexed(1.4, func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return t.text(gtx, schrift{groesse: 13.5, farbe: t.f.leise, zeilen: 1}, m.Email)
							})
						}),
						zelle(spalteOnline, func(gtx layout.Context) layout.Dimensions {
							farbe, txt := t.f.aus, "offline"
							if m.Online {
								farbe, txt = t.f.akzent, "online"
							}
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions { return punkt(gtx, farbe, 8) }),
								layout.Rigid(layout.Spacer{Width: 7}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise}, txt)
								}),
							)
						}),
						zelle(spalteAdmin, func(gtx layout.Context) layout.Dimensions { return t.schalter(gtx, a, frei) }),
						zelle(spalteOp, func(gtx layout.Context) layout.Dimensions { return t.schalter(gtx, o, frei) }),
					)
				})
			})
		}))
	}
	if len(ms) == 0 {
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(0, 0)}
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
}
