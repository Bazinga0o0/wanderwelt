package fenster

import (
	"runtime"
	"strconv"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Reiter „Hosten“: Leistungsprofil dieses PCs und Autostart.

// Grenzen wie bench.checkProfile (Xmx mindestens 1024 MB, Weiten 3…32).
const (
	minXmxMB    = 1024
	xmxSchritt  = 256
	reserveMB   = 2048 // bleiben für Windows und Minecraft selbst
	minWeite    = 3
	maxWeite    = 32
	fallbackXmx = 8192 // Obergrenze, wenn der Arbeitsspeicher unbekannt ist
)

// bereich bildet einen Schieber (0…1) auf ganze Zahlen ab.
type bereich struct{ min, max, schritt int }

func (b bereich) wert(f float32) int {
	if b.max <= b.min {
		return b.min
	}
	n := (b.max - b.min) / b.schritt
	i := int(f*float32(n) + 0.5)
	return min(b.min+i*b.schritt, b.max)
}

func (b bereich) anteil(v int) float32 {
	if b.max <= b.min {
		return 0
	}
	return float32(v-b.min) / float32(b.max-b.min)
}

// xmxBereich: 1024 … Arbeitsspeicher − 2048, in Schritten von 256; ein
// größerer gespeicherter Wert bleibt erreichbar.
func xmxBereich(ramMB, aktuell int) bereich {
	hoch := ramMB - reserveMB
	if ramMB <= 0 {
		hoch = fallbackXmx
	}
	hoch = hoch / xmxSchritt * xmxSchritt
	hoch = max(hoch, minXmxMB, aktuell)
	return bereich{minXmxMB, hoch, xmxSchritt}
}

func threadBereich(aktuell int) bereich {
	return bereich{1, max(runtime.NumCPU(), aktuell, 1), 1}
}

var weitenBereich = bereich{minWeite, maxWeite, 1}

type hostenAnsicht struct {
	liste widget.List

	xmx, sicht, sim, threads widget.Float
	c2me, dauer, auto        widget.Bool
	uebernahme               widget.Bool // „Darf das Hosten übernehmen“ (Profil: KeineUebernahme umgekehrt)
	speichern, verwerfen     widget.Clickable

	form, basis api.Profile
	// dauerGeaendert: Der Schalter Dauerbetrieb wurde in diesem Formular
	// umgelegt. Sonst gilt beim Speichern der neueste Zustand (der Tray
	// kann ihn inzwischen umgeschaltet haben).
	dauerGeaendert bool
	geladen        bool
	runde          uint64
	wartet         bool
	fehler         string
	gespeichert    merker[api.Profile] // bis der Zustand das Gespeicherte zeigt

	autoWartet  bool
	autoFehler  string
	autoGemerkt merker[bool]

	// Notfallkopie: Haken und Knopf „Ordner öffnen“.
	notfall        widget.Bool
	notfallOrdner  widget.Clickable
	notfallWartet  bool
	notfallFehler  string
	notfallGemerkt merker[bool]
}

// uebernehmen setzt Formular und Schieber auf p.
func (v *hostenAnsicht) uebernehmen(p api.Profile, ramMB int) {
	v.form, v.basis = p, p
	v.dauerGeaendert = false
	v.schieberSetzen(ramMB)
}

func (v *hostenAnsicht) schieberSetzen(ramMB int) {
	v.xmx.Value = xmxBereich(ramMB, v.form.XmxMB).anteil(v.form.XmxMB)
	v.sicht.Value = weitenBereich.anteil(v.form.ViewDistance)
	v.sim.Value = weitenBereich.anteil(v.form.SimulationDistance)
	v.threads.Value = threadBereich(v.form.C2METhreads).anteil(v.form.C2METhreads)
	v.c2me.Value = v.form.C2ME
	v.dauer.Value = v.form.Dauerbetrieb
	v.uebernahme.Value = !v.form.KeineUebernahme
}

func (v *hostenAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	z := s.z
	ram := z.RAMTotalMB

	// Neue Instanz oder unverändertes Formular: vom Zustand übernehmen –
	// außer, gerade Gespeichertes ist dort noch nicht angekommen.
	profil, _ := v.gespeichert.gilt(z.Profil, gtx.Now, s.runde)
	if !v.geladen || v.runde != s.runde || (v.form == v.basis && profil != v.basis && !v.wartet) {
		v.uebernehmen(profil, ram)
		v.geladen, v.runde = true, s.runde
	}
	if !v.dauerGeaendert && !v.wartet {
		// Der Tray (oder ein anderes Fenster) hat den Dauerbetrieb
		// umgeschaltet: Formular und Vergleichsstand folgen.
		v.form.Dauerbetrieb, v.basis.Dauerbetrieb = profil.Dauerbetrieb, profil.Dauerbetrieb
		v.dauer.Value = profil.Dauerbetrieb
	}
	if !v.autoWartet {
		v.auto.Value, _ = v.autoGemerkt.gilt(z.Autostart, gtx.Now, s.runde)
	}

	if !v.notfallWartet {
		v.notfall.Value, _ = v.notfallGemerkt.gilt(z.Notfallkopie.Behalten, gtx.Now, s.runde)
	}
	if v.notfall.Update(gtx) && !v.notfallWartet {
		an, jetzt, runde := v.notfall.Value, gtx.Now, s.runde
		v.notfallWartet, v.notfallFehler = true, ""
		u.d.aktion("notfallkopie", map[string]bool{"an": an}, func(err error) {
			v.notfallWartet = false
			if err != nil {
				v.notfallFehler = err.Error()
				return
			}
			v.notfallGemerkt.setze(an, jetzt, runde)
		})
	}
	if v.notfallOrdner.Clicked(gtx) {
		v.notfallFehler = ""
		u.d.aktion("notfallordner", nil, func(err error) {
			if err != nil {
				v.notfallFehler = err.Error()
			}
		})
	}

	xb := xmxBereich(ram, v.basis.XmxMB)
	if v.xmx.Update(gtx) {
		v.form.XmxMB = xb.wert(v.xmx.Value)
	}
	if v.sicht.Update(gtx) {
		v.form.ViewDistance = weitenBereich.wert(v.sicht.Value)
	}
	if v.sim.Update(gtx) {
		v.form.SimulationDistance = weitenBereich.wert(v.sim.Value)
	}
	tb := threadBereich(v.basis.C2METhreads)
	if v.threads.Update(gtx) {
		v.form.C2METhreads = tb.wert(v.threads.Value)
	}
	if v.c2me.Update(gtx) {
		v.form.C2ME = v.c2me.Value
		if v.form.C2ME && v.form.C2METhreads < 1 {
			v.form.C2METhreads = max(1, runtime.NumCPU()/2)
		}
	}
	if v.dauer.Update(gtx) {
		v.form.Dauerbetrieb = v.dauer.Value
		v.dauerGeaendert = v.form.Dauerbetrieb != v.basis.Dauerbetrieb
	}
	if v.uebernahme.Update(gtx) {
		v.form.KeineUebernahme = !v.uebernahme.Value
	}
	// Schieber rasten auf die Schritte ein.
	v.xmx.Value = xb.anteil(v.form.XmxMB)
	v.sicht.Value = weitenBereich.anteil(v.form.ViewDistance)
	v.sim.Value = weitenBereich.anteil(v.form.SimulationDistance)
	v.threads.Value = tb.anteil(v.form.C2METhreads)
	v.c2me.Value, v.dauer.Value = v.form.C2ME, v.form.Dauerbetrieb
	v.uebernahme.Value = !v.form.KeineUebernahme

	if v.auto.Update(gtx) && !v.autoWartet {
		an, jetzt, runde := v.auto.Value, gtx.Now, s.runde
		v.autoWartet, v.autoFehler = true, ""
		u.d.aktion("autostart", map[string]bool{"an": an}, func(err error) {
			v.autoWartet = false
			if err != nil {
				v.autoFehler = err.Error()
				return
			}
			v.autoGemerkt.setze(an, jetzt, runde)
		})
	}
	geaendert := v.form != v.basis
	if v.verwerfen.Clicked(gtx) {
		v.form, v.fehler = v.basis, ""
		v.dauerGeaendert = false
		v.schieberSetzen(ram)
	}
	if v.speichern.Clicked(gtx) && geaendert && !v.wartet {
		p, jetzt, runde := speicherProfil(v.form, z.Profil, v.dauerGeaendert), gtx.Now, s.runde
		v.wartet, v.fehler = true, ""
		u.d.aktion("profil", p, func(err error) {
			v.wartet = false
			if err != nil {
				v.fehler = err.Error()
				return
			}
			v.basis = p
			v.dauerGeaendert = false
			v.gespeichert.setze(p, jetzt, runde)
			u.zeige("info", "Leistungsprofil gespeichert.", time.Now())
		})
	}

	var teile []layout.Widget
	if z.Hosting {
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungInfo, "Du hostest gerade", "Änderungen gelten ab dem nächsten Serverstart.")
		})
	}
	teile = append(teile, func(gtx layout.Context) layout.Dimensions {
		return v.profilKarte(gtx, u, z, xb, tb, geaendert)
	})
	teile = append(teile, func(gtx layout.Context) layout.Dimensions {
		return v.windowsKarte(gtx, u)
	})
	teile = append(teile, func(gtx layout.Context) layout.Dimensions {
		return v.notfallKarte(gtx, u, z.Notfallkopie)
	})
	return seite(gtx, &v.liste, teile)
}

func (v *hostenAnsicht) profilKarte(gtx layout.Context, u *oberflaeche, z *fensterapi.Zustand, xb, tb bereich, geaendert bool) layout.Dimensions {
	t := u.th
	ramText := "Wie viel Arbeitsspeicher der Minecraft-Server bekommt."
	if z.RAMTotalMB > 0 {
		ramText = "Von " + gb(z.RAMTotalMB) + " in diesem PC; mindestens 2 GB bleiben für Windows und Minecraft."
	}
	return t.karteAbstand(gtx, 20, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("SERVER AUF DIESEM PC")),
			layout.Rigid(layout.Spacer{Height: 14}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return einstellung(gtx, t, "Arbeitsspeicher", gb(v.form.XmxMB), ramText, &v.xmx, !v.wartet)
			}),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return einstellung(gtx, t, "Sichtweite", chunks(v.form.ViewDistance),
					"Wie weit Spieler sehen. Mehr kostet Arbeitsspeicher und Rechenzeit.", &v.sicht, !v.wartet)
			}),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return einstellung(gtx, t, "Simulationsweite", chunks(v.form.SimulationDistance),
					"Wie weit um Spieler herum die Welt lebt: Tiere, Pflanzen, Redstone.", &v.sim, !v.wartet)
			}),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return t.trenner(gtx) }),
			layout.Rigid(layout.Spacer{Height: 16}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return schalterZeile(gtx, t, "C2ME", "Erzeugt und lädt Chunks auf mehreren Prozessorkernen.", &v.c2me, !v.wartet)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !v.form.C2ME {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return einstellung(gtx, t, "Threads für C2ME", strconv.Itoa(v.form.C2METhreads),
						"Dieser PC hat "+strconv.Itoa(runtime.NumCPU())+" logische Kerne.", &v.threads, !v.wartet)
				})
			}),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return t.trenner(gtx) }),
			layout.Rigid(layout.Spacer{Height: 16}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return schalterZeile(gtx, t, "Dauerbetrieb (Server auch ohne Spieler)",
					"Dieser PC hostet von selbst, sobald niemand hostet, und sein Server läuft auch ohne Spieler weiter. "+
						"Nur am Netzteil – auf Akku hostet ein Notebook wie sonst.", &v.dauer, !v.wartet)
			}),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return schalterZeile(gtx, t, "Darf das Hosten übernehmen",
					"Wenn der Host aufhört, darf dieser PC das Hosten von ihm übernehmen, ohne dass der Server für alle stoppt. "+
						"Gilt sofort, auch während du mitspielst.", &v.uebernahme, !v.wartet)
			}),
			layout.Rigid(layout.Spacer{Height: 22}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						txt := "Speichern"
						if v.wartet {
							txt = "Speichert …"
						}
						return t.knopf(gtx, &v.speichern, txt, knopfHaupt, geaendert && !v.wartet)
					}),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.verwerfen, "Verwerfen", knopfNeben, geaendert && !v.wartet)
					}),
					layout.Rigid(layout.Spacer{Width: 14}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						switch {
						case v.fehler != "":
							return t.farbig(v.fehler, t.f.fehler)(gtx)
						case serverGeaendert(v.form, v.basis):
							return t.leise("Gilt ab dem nächsten Serverstart.")(gtx)
						}
						return layout.Dimensions{}
					}),
				)
			}),
		)
	})
}

// speicherProfil ist, was „Speichern“ sendet: das Formular, mit dem
// Dauerbetrieb aus dem neuesten Zustand, solange der Schalter im Formular
// nicht umgelegt wurde (ein Umschalten im Tray bleibt so erhalten).
func speicherProfil(form, neuest api.Profile, dauerGeaendert bool) api.Profile {
	if !dauerGeaendert {
		form.Dauerbetrieb = neuest.Dauerbetrieb
	}
	return form
}

// serverGeaendert: Werte geändert, die erst beim nächsten Serverstart
// gelten (alles außer Dauerbetrieb und Übernahme).
func serverGeaendert(a, b api.Profile) bool {
	a.Dauerbetrieb, b.Dauerbetrieb = false, false
	a.KeineUebernahme, b.KeineUebernahme = false, false
	return a != b
}

func (v *hostenAnsicht) windowsKarte(gtx layout.Context, u *oberflaeche) layout.Dimensions {
	t := u.th
	return t.karteAbstand(gtx, 20, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("WINDOWS")),
			layout.Rigid(layout.Spacer{Height: 14}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return schalterZeile(gtx, t, "Mit Windows starten", "wanderwelt startet nach der Anmeldung im Infobereich der Taskleiste.", &v.auto, !v.autoWartet)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if v.autoFehler == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 8}.Layout(gtx, t.farbig(v.autoFehler, t.f.fehler))
			}),
		)
	})
}

// notfallStand: die Zeile unter dem Haken – Datum und Größe der Kopie, dazu
// der Ort.
func notfallStand(n fensterapi.Notfallkopie, jetzt time.Time) string {
	if !n.Vorhanden {
		txt := "Noch keine Kopie auf diesem PC."
		if n.Ordner != "" {
			txt += " Sie kommt nach " + n.Ordner + "."
		}
		return txt
	}
	txt := "Kopie liegt in " + n.Ordner
	if !n.Zeit.IsZero() {
		txt = "Letzte Kopie: " + zeitpunkt(n.Zeit, jetzt) + " · " + groesse(n.Groesse) + " · Stand " + strconv.FormatInt(n.Generation, 10) + " · " + n.Ordner
	}
	if n.NichtHochgeladen {
		txt += " · enthält einen noch NICHT hochgeladenen Stand"
	}
	return txt
}

func (v *hostenAnsicht) notfallKarte(gtx layout.Context, u *oberflaeche, n fensterapi.Notfallkopie) layout.Dimensions {
	t := u.th
	return t.karteAbstand(gtx, 20, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("NOTFALLKOPIE")),
			layout.Rigid(layout.Spacer{Height: 14}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return schalterZeile(gtx, t, "Notfallkopie behalten",
					"Nach jeder Sitzung als Host bleibt der zuletzt hochgeladene Stand in deinen Dokumenten (Ordner „wanderwelt-Notfallkopie“), "+
						"falls der Koordinator ausfällt. Es gibt nur eine Kopie; die nächste ersetzt sie. "+
						"Ausschalten löscht die vorhandene Kopie nicht.", &v.notfall, !v.notfallWartet)
			}),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, t.leise(notfallStand(n, gtx.Now))),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.notfallOrdner, "Ordner öffnen", knopfNeben, n.Vorhanden)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if v.notfallFehler == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 8}.Layout(gtx, t.farbig(v.notfallFehler, t.f.fehler))
			}),
		)
	})
}

func chunks(n int) string {
	return strconv.Itoa(n) + " Chunks"
}

// einstellung: Name und Wert über einem Schieber, Erklärung darunter.
func einstellung(gtx layout.Context, t *thema, name, wert, hilfe string, f *widget.Float, aktiv bool) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text}, name)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.akzentText}, wert)
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: 4}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return t.schieber(gtx, f, aktiv) }),
		layout.Rigid(layout.Spacer{Height: 2}.Layout),
		layout.Rigid(t.leise(hilfe)),
	)
}

// schalterZeile: Name und Erklärung links, Schalter rechts.
func schalterZeile(gtx layout.Context, t *thema, name, hilfe string, b *widget.Bool, aktiv bool) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text}, name)
				}),
				layout.Rigid(layout.Spacer{Height: 2}.Layout),
				layout.Rigid(t.leise(hilfe)),
			)
		}),
		layout.Rigid(layout.Spacer{Width: 16}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return t.schalter(gtx, b, aktiv) }),
	)
}
