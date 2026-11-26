package fenster

import (
	"cmp"
	"image"
	"io"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Reiter „Start“: wer hostet, wer online ist, Adresse, Spielen/Beenden,
// letzter und nächster Stand, Fortschritt.

type startAnsicht struct {
	liste                                   widget.List
	spielen, beenden, beendenOhne, kopieren widget.Clickable
	kopiertBis                              time.Time
	codeKopieren                            widget.Clickable
	codeKopiertBis                          time.Time
	wartet                                  bool
	fehler                                  string
}

// mitspielen: Die Instanz spielt mit (ist nicht Host).
func mitspielen(z *fensterapi.Zustand) bool {
	return strings.Contains(strings.ToLower(z.Phase), "spielt mit")
}

// adresse: wohin man sich verbindet.
func adresse(z *fensterapi.Zustand) string {
	a := z.HostAddr
	if z.Status != nil {
		a = cmp.Or(a, z.Status.MinecraftAddress)
	}
	if z.Me != nil {
		a = cmp.Or(a, z.Me.MinecraftAddress)
	}
	return a
}

func (v *startAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	z := s.z
	jetzt := gtx.Now
	beendbar := z.Hosting || mitspielen(z)

	if v.spielen.Clicked(gtx) && !v.wartet {
		v.wartet, v.fehler = true, ""
		u.d.aktion("play", nil, func(err error) {
			v.wartet = false
			if err != nil {
				v.fehler = err.Error()
			}
		})
	}
	if v.beenden.Clicked(gtx) && !v.wartet {
		beenden := func() {
			v.wartet, v.fehler = true, ""
			u.d.aktion("beenden", nil, func(err error) {
				v.wartet = false
				if err != nil {
					v.fehler = err.Error()
				}
			})
		}
		if z.Hosting {
			// Wie im Tray: Beim Hosten erst nachfragen (die Instanz fragt bei
			// Aufrufen aus dem Fenster nicht mehr selbst).
			u.frage(rueckfrage{titel: "Spiel beenden?", text: beendenFrage(z, gibtWeiter(z)),
				ja: "Beenden", nein: "Weiterhosten", gefahr: true, beiJa: beenden})
		} else {
			beenden()
		}
	}
	if v.beendenOhne.Clicked(gtx) && !v.wartet && z.Hosting {
		// „Hosten ohne Weitergabe beenden“: wie früher, mit der alten Rückfrage.
		u.frage(rueckfrage{titel: "Hosten ohne Weitergabe beenden?", text: beendenFrage(z, false),
			ja: "Beenden", nein: "Weiterhosten", gefahr: true, beiJa: func() {
				v.wartet, v.fehler = true, ""
				u.d.aktion("beendenohne", nil, func(err error) {
					v.wartet = false
					if err != nil {
						v.fehler = err.Error()
					}
				})
			}})
	}
	addr := adresse(z)
	if v.kopieren.Clicked(gtx) && addr != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(addr))})
		v.kopiertBis = jetzt.Add(2 * time.Second)
		u.zeige("info", "Adresse kopiert: "+addr, jetzt)
	}

	if v.codeKopieren.Clicked(gtx) && z.Gruppencode != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(z.Gruppencode))})
		v.codeKopiertBis = jetzt.Add(2 * time.Second)
		u.zeige("info", "Gruppencode kopiert.", jetzt)
	}

	var teile []layout.Widget
	if z.Status != nil && z.Status.Maintenance != nil {
		wm := z.Status.Maintenance
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			titel, txt := fensterapi.WartungText(wm, jetzt)
			return t.meldung(gtx, meldungWarn, titel, txt)
		})
	}
	if rp := restore(z); rp != nil {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			wer := cmp.Or(rp.By, "Ein Admin")
			txt := wer + " holt den Stand von " + zeitpunkt(rp.TargetCreated, jetzt) + " zurück. "
			if z.Status.Host != nil {
				txt += "Der Server startet dafür gleich neu; alle verbinden sich danach wieder."
			} else {
				txt += "Gleich ist er wieder die aktuelle Welt."
			}
			return t.meldung(gtx, meldungWarn, "Ein älterer Stand wird zurückgeholt", txt)
		})
	}
	if h := z.Status; h != nil && h.Handover != nil {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			art, titel := meldungInfo, "Übergabe läuft"
			if h.Handover.State == api.HandoverFailed {
				art, titel = meldungWarn, "Übergabe gescheitert"
			}
			return t.meldung(gtx, art, titel, fensterapi.UebergabeText(h.Handover)+".")
		})
	}
	if z.LastErr != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungFehler, "Das hat nicht geklappt", z.LastErr)
		})
	}
	teile = append(teile, func(gtx layout.Context) layout.Dimensions {
		return v.hostKarte(gtx, u, z, beendbar)
	})
	teile = append(teile, func(gtx layout.Context) layout.Dimensions {
		return gleichHoch(gtx, 14,
			func(gtx layout.Context) layout.Dimensions { return v.onlineKarte(gtx, u, z) },
			func(gtx layout.Context) layout.Dimensions { return v.staendeKarte(gtx, u, z, jetzt) },
		)
	})
	if addr != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return v.adressKarte(gtx, u, addr, jetzt)
		})
	}
	if z.Gruppencode != "" {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return v.codeKarte(gtx, u, jetzt)
		})
	}
	return seite(gtx, &v.liste, teile)
}

// codeKarte: „Gruppencode kopieren“ zum Weitergeben an neue Mitspieler.
func (v *startAnsicht) codeKarte(gtx layout.Context, u *oberflaeche, jetzt time.Time) layout.Dimensions {
	t := u.th
	knopfText := "Gruppencode kopieren"
	if jetzt.Before(v.codeKopiertBis) {
		knopfText = "Kopiert"
		gtx.Execute(op.InvalidateCmd{At: v.codeKopiertBis})
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("NEUE MITSPIELER")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.knopf(gtx, &v.codeKopieren, knopfText, knopfNeben, true)
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(t.leise("Gib den Code nur Leuten, die mitspielen sollen; der Admin muss sie noch annehmen. Er enthält nur öffentliche Adressen, keinen Zugang.")),
		)
	})
}

// beendenFrage: Text der Rückfrage vor „Spiel für mich beenden“ beim
// Hosten, wie im Tray (app.endPlaying, app.dauerHint). Sind andere Spieler
// online, wird die Welt übergeben (uebergabe); ohne Weitergabe gilt der
// Text von „Hosten ohne Weitergabe beenden“.
func beendenFrage(z *fensterapi.Zustand, uebergabe bool) string {
	q := "Du hostest gerade. Der Server wird gestoppt, der Stand hochgeladen und der Zuschlag freigegeben."
	if z.Status != nil && len(z.Status.Players) > 0 {
		q = "Du hostest gerade, online sind: " + strings.Join(z.Status.Players, ", ") +
			".\n\nDer Server stoppt dann für alle. Wer weiterspielen will, klickt danach „Spielen“ und wird neuer Host."
		if uebergabe {
			q = "Du hostest gerade, online sind: " + strings.Join(z.Status.Players, ", ") +
				".\n\n" + fensterapi.UebergabeFrage
		}
	}
	if z.Profil.Dauerbetrieb {
		q += "\n\nDauerbetrieb ist an: Er pausiert danach – dieser PC hostet erst wieder von selbst, " +
			"wenn du „Spielen“ klickst oder wanderwelt neu startest."
	}
	return q + "\n\nJetzt beenden?"
}

// gibtWeiter: Beim Beenden als Host wird die Welt übergeben (andere Spieler
// sind online, ohne den eigenen Namen).
func gibtWeiter(z *fensterapi.Zustand) bool {
	if z.Status == nil {
		return false
	}
	own := ""
	if z.Me != nil {
		own = z.Me.MinecraftName
	}
	return len(fensterapi.AndereSpieler(z.Status.Players, own)) > 0
}

func restore(z *fensterapi.Zustand) *api.RestoreNotice {
	if z.Status == nil {
		return nil
	}
	return z.Status.RestorePending
}

// hostKarte: wer hostet, Knopf, Fortschritt.
func (v *startAnsicht) hostKarte(gtx layout.Context, u *oberflaeche, z *fensterapi.Zustand, beendbar bool) layout.Dimensions {
	t := u.th
	st := z.Status

	name, zeile, farbe := "", "", t.f.leise
	switch {
	case st == nil && z.StatusErr != "":
		name, zeile, farbe = "Koordinator nicht erreichbar", z.StatusErr, t.f.fehler
	case st == nil:
		name, zeile = "…", "Status wird geladen"
	case st.Host == nil:
		name, zeile = "Niemand hostet gerade", "Wer als Erstes „Spielen“ klickt, hostet die Welt auf seinem PC."
	default:
		// Keine E-Mail: Die zeigt nur der Reiter Mitglieder (Admins).
		name = cmp.Or(st.Host.MinecraftName, "jemand ohne Minecraft-Namen")
		switch {
		case st.HostUploading:
			zeile, farbe = "lädt gerade einen Stand hoch", t.f.warn
		case !st.HostReady:
			zeile, farbe = "startet die Welt …", t.f.warn
		default:
			zeile, farbe = "Server läuft", t.f.akzent
		}
		if z.Hosting {
			zeile += " · auf diesem PC"
		}
	}

	knopf := func(gtx layout.Context) layout.Dimensions {
		if beendbar {
			label, _ := fensterapi.BeendenText(z.Hosting)
			return t.grosserKnopf(gtx, &v.beenden, label, knopfNeben, !v.wartet)
		}
		frei := !v.wartet && (z.Busy == "" || z.Hosting)
		return t.grosserKnopf(gtx, &v.spielen, "Spielen", knopfHaupt, frei)
	}

	fortschritt := z.Busy != "" || z.Detail != ""
	return t.karteAbstand(gtx, 20, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(t.klein("HOST")),
							layout.Rigid(layout.Spacer{Height: 4}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								groesse := unit.Sp(24)
								if st == nil || st.Host == nil {
									groesse = 20
								}
								return t.text(gtx, schrift{groesse: groesse, fett: true, farbe: t.f.text, zeilen: 2}, name)
							}),
							layout.Rigid(layout.Spacer{Height: 6}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions { return punkt(gtx, farbe, 8) }),
									layout.Rigid(layout.Spacer{Width: 8}.Layout),
									layout.Flexed(1, t.leise(zeile)),
								)
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Width: 16}.Layout),
					layout.Rigid(knopf),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if z.Hosting || !beendbar {
					return layout.Dimensions{}
				}
				_, hinweis := fensterapi.BeendenText(false)
				return layout.Inset{Top: 10}.Layout(gtx, t.leise(hinweis))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !z.Hosting {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.beendenOhne, "Hosten ohne Weitergabe beenden", knopfNeben, !v.wartet)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if v.fehler == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 12}.Layout(gtx, t.farbig(v.fehler, t.f.fehler))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !fortschritt {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return v.fortschritt(gtx, u, z)
				})
			}),
		)
	})
}

// fortschritt: Phase und Detail, mit Balken bei einer Prozentangabe.
func (v *startAnsicht) fortschritt(gtx layout.Context, u *oberflaeche, z *fensterapi.Zustand) layout.Dimensions {
	t := u.th
	txt := gross(cmp.Or(z.Busy, z.Phase))
	if z.Detail != "" {
		txt += ": " + z.Detail
	}
	anteil, mitBalken := prozent(z.Detail)
	return hinterlegt(gtx, t.f.flaeche2, t.f.rand, 8, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: 12, Bottom: 12, Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 13.5, farbe: t.f.text}, txt)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !mitBalken {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return t.balken(gtx, anteil, t.f.akzent, 6)
					})
				}),
			)
		})
	})
}

// onlineKarte: Spieler als Marken.
func (v *startAnsicht) onlineKarte(gtx layout.Context, u *oberflaeche, z *fensterapi.Zustand) layout.Dimensions {
	t := u.th
	var spieler []string
	if z.Status != nil {
		spieler = z.Status.Players
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("ONLINE")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if len(spieler) == 0 {
					txt := "Niemand ist im Spiel."
					if z.Status == nil {
						txt = "Unbekannt"
					}
					return t.normal(txt)(gtx)
				}
				ws := make([]layout.Widget, len(spieler))
				for i, n := range spieler {
					ws[i] = func(gtx layout.Context) layout.Dimensions {
						return t.chip(gtx, n, t.f.akzentHell, t.f.akzentText)
					}
				}
				return fluss(gtx, 6, ws)
			}),
		)
	})
}

// staendeKarte: letzter und nächster Stand.
func (v *startAnsicht) staendeKarte(gtx layout.Context, u *oberflaeche, z *fensterapi.Zustand, jetzt time.Time) layout.Dimensions {
	t := u.th
	letzter, wann, naechster := "Noch keiner", "", "–"
	if st := z.Status; st != nil {
		if !st.HeadCreated.IsZero() {
			letzter, wann = seit(st.HeadCreated, jetzt), zeitpunkt(st.HeadCreated, jetzt)
		}
		switch {
		case st.Host == nil:
			naechster = "wenn jemand hostet"
		case st.SnapshotIntervalS > 0 && !st.HeadCreated.IsZero():
			naechster = bis(st.HeadCreated.Add(time.Duration(st.SnapshotIntervalS)*time.Second), jetzt)
		case st.SnapshotIntervalS > 0:
			naechster = "in " + dauer(time.Duration(st.SnapshotIntervalS)*time.Second)
		}
	} else {
		letzter = "Unbekannt"
	}
	zeile := func(label, wert, zusatz string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(76)
					return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise}, label)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text}, wert)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					if zusatz == "" {
						return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
					}
					return layout.Inset{Left: 8}.Layout(gtx, t.leise(zusatz))
				}),
			)
		}
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("STÄNDE")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(zeile("Letzter", letzter, wann)),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(zeile("Nächster", naechster, "")),
		)
	})
}

// adressKarte: Adresse mit Kopierknopf.
func (v *startAnsicht) adressKarte(gtx layout.Context, u *oberflaeche, addr string, jetzt time.Time) layout.Dimensions {
	t := u.th
	knopfText := "Kopieren"
	if jetzt.Before(v.kopiertBis) {
		knopfText = "Kopiert"
		gtx.Execute(op.InvalidateCmd{At: v.kopiertBis})
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("ADRESSE ZUM VERBINDEN")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return hinterlegt(gtx, t.f.flaeche2, t.f.rand, 7, func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return t.text(gtx, schrift{groesse: 15, fett: true, farbe: t.f.text, zeilen: 1}, addr)
							})
						})
					}),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.kopieren, knopfText, knopfNeben, true)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(t.leise("In Minecraft unter Multiplayer → Server hinzufügen eintragen (Profil „wanderwelt“).")),
		)
	})
}
