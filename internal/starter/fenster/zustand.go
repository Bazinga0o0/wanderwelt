package fenster

import (
	"cmp"
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Reiter „Zustand“ (nur Admins): was ein Admin sonst per SSH im Log des
// Koordinators nachsieht – Ampel, Warnungen und die Werte als Karten. Alles
// sind Textknoten; die Daten kommen von GET /v1/health des Koordinators.

// gesundheitTakt: So oft holt das Fenster die Gesundheit bei sichtbarem
// Reiter „Zustand“ (und nur dann).
const gesundheitTakt = 30 * time.Second

// gesundheitDaten sind die Gesundheit im Schnappschuss.
type gesundheitDaten struct {
	h      *api.Health
	err    string
	ab     time.Time // wann geholt (Null: noch nie)
	laeuft bool
}

// gesundheitsQuelle ist die Fähigkeit der Quelle, die Gesundheit zu holen
// (*fensterapi.Client kann es; andere Quellen, etwa in Tests, müssen es nicht).
type gesundheitsQuelle interface {
	Gesundheit(ctx context.Context) (*api.Health, error)
}

// gesundheitRunde holt die Gesundheit, wenn der Reiter „Zustand“ sichtbar
// ist, der Nutzer Admin ist und der letzte Abruf länger als d.gesundheitTakt
// zurückliegt (oder „Aktualisieren“ ihn verworfen hat). Der Abruf läuft in
// der Runde selbst (die Instanz ist lokal; der Koordinator antwortet schnell,
// und die Frist ist anfrageFrist).
func (d *daten) gesundheitRunde(ctx context.Context, c quelle, r reiter, admin bool) {
	gq, ok := c.(gesundheitsQuelle)
	d.mu.Lock()
	takt := cmp.Or(d.gesundheitTakt, gesundheitTakt)
	hole := ok && admin && r == reiterZustand && !d.s.gesundheit.laeuft && faellig(d.s.gesundheit.ab, d.jetzt(), takt)
	if hole {
		d.s.gesundheit.laeuft = true
	}
	d.mu.Unlock()
	if !hole {
		return
	}
	actx, cancel := context.WithTimeout(ctx, anfrageFrist)
	h, err := gq.Gesundheit(actx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	g := &d.s.gesundheit
	g.laeuft, g.ab = false, d.jetzt()
	if err != nil {
		g.err = err.Error()
	} else {
		g.h, g.err = h, ""
	}
	d.mu.Unlock()
}

// gesundheitNeuLaden: „Aktualisieren“ – die nächste Runde holt sofort.
func (d *daten) gesundheitNeuLaden() {
	d.mu.Lock()
	d.s.gesundheit.ab = time.Time{}
	d.mu.Unlock()
	d.wecken()
}

// ---- Modell (rein, getestet) ----

// zustandKarte ist eine Karte der Liste: Titel und Textzeilen.
type zustandKarte struct {
	titel  string
	zeilen []string
}

// ampelText: Überschrift und Art der Ampel zu Health.Status; ein unbekannter
// Wert gilt als Warnung.
func ampelText(status string) (text string, art meldungsArt) {
	switch status {
	case api.HealthOK:
		return "OK", meldungInfo
	case api.HealthFehler:
		return "Fehler", meldungFehler
	}
	return "Warnung", meldungWarn
}

// warnungsArt: Darstellung einer Warnstufe.
func warnungsArt(stufe string) meldungsArt {
	switch stufe {
	case api.WarnFehler:
		return meldungFehler
	case api.WarnWarnung:
		return meldungWarn
	}
	return meldungInfo
}

// laufzeit: „3 Min.“, „5 Std. 12 Min.“, „2 Tage 4 Std.“.
func laufzeit(sekunden int64) string {
	d := time.Duration(max(sekunden, 0)) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d Min.", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d Std. %d Min.", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	t := int(d / (24 * time.Hour))
	return fmt.Sprintf("%d %s %d Std.", t, plural(t, "Tag", "Tage"), int(d%(24*time.Hour)/time.Hour))
}

// vorSekunden: „vor 12 s“, „vor 3 Min.“ …
func vorSekunden(t, jetzt time.Time) string {
	d := jetzt.Sub(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("vor %d s", int(d/time.Second))
	}
	return seit(t, jetzt)
}

// zustandKarten macht aus der Gesundheit die Karten (feste Reihenfolge).
func zustandKarten(h *api.Health, jetzt time.Time) []zustandKarte {
	var ks []zustandKarte
	add := func(titel string, zeilen ...string) { ks = append(ks, zustandKarte{titel, zeilen}) }

	add("Koordinator", "Version "+cmp.Or(h.Version, "unbekannt"), "läuft seit "+laufzeit(h.UptimeS))

	platz := []string{"unbekannt"}
	if h.Disk != nil {
		platz = []string{groesse(h.Disk.FreeBytes) + " frei"}
		if h.Disk.TotalBytes > 0 {
			platz = []string{fmt.Sprintf("%s frei von %s (%.0f %%)", groesse(h.Disk.FreeBytes), groesse(h.Disk.TotalBytes),
				100*float64(h.Disk.FreeBytes)/float64(h.Disk.TotalBytes))}
		}
	}
	if h.StoreKnown {
		platz = append(platz, "Speicher der Stände: "+groesse(h.StoreBytes))
	} else {
		platz = append(platz, "Speicher der Stände: wird noch ermittelt")
	}
	add("Speicherplatz", platz...)

	if h.WorldID == "" {
		add("Weltstand", "Noch keine Welt hochgeladen.")
	} else {
		z := []string{fmt.Sprintf("Stand %d der Welt %s", h.HeadGeneration, kurzID(h.WorldID))}
		if !h.HeadCreated.IsZero() {
			z = append(z, "letzter Upload "+seit(h.HeadCreated, jetzt)+" ("+zeitpunkt(h.HeadCreated, jetzt)+")")
		}
		add("Weltstand", z...)
	}

	if h.Host == nil {
		add("Host", "Niemand hostet gerade.")
	} else {
		z := []string{cmp.Or(h.Host.MinecraftName, "ein PC") + " (PC " + kurzID(h.Host.MachineID) + ")"}
		if h.Host.UploadOnly {
			z = append(z, "hält den Zuschlag nur zum Hochladen")
		}
		z = append(z, "letztes Lebenszeichen "+vorSekunden(h.Host.LastBeat, jetzt))
		add("Host", z...)
	}

	st := []string{"Im Manifest: " + cmp.Or(h.ManifestStarter, "keine Angabe")}
	for _, s := range h.Starters {
		st = append(st, fmt.Sprintf("%s: %s (gesehen %s)", cmp.Or(s.MinecraftName, "ein PC"), s.Version, seit(s.Seen, jetzt)))
	}
	if len(h.Starters) == 0 {
		st = append(st, "Seit dem Start des Koordinators hat sich noch kein PC gemeldet.")
	}
	add("Starter-Versionen", st...)

	if h.OpenJoins == 0 {
		add("Beitrittsanfragen", "keine offen")
	} else {
		add("Beitrittsanfragen", fmt.Sprintf("%d offen – im Reiter Mitglieder bearbeiten", h.OpenJoins))
	}

	if m := h.Maintenance; m == nil {
		add("Wartung", "keine")
	} else {
		z := []string{"läuft seit " + zeitpunkt(m.Since, jetzt)}
		if m.Reason != "" {
			z = append(z, "Grund: "+m.Reason)
		}
		if !m.StopAt.IsZero() && m.StopAt.After(jetzt) {
			z = append(z, "der Host stoppt "+bis(m.StopAt, jetzt))
		}
		add("Wartung", z...)
	}

	if !h.Backup.Known {
		add("Letzte Sicherung", "unbekannt")
	} else {
		z := []string{seit(h.Backup.Time, jetzt) + " (" + zeitpunkt(h.Backup.Time, jetzt) + ")"}
		if h.Backup.Target != "" {
			z = append(z, "nach "+h.Backup.Target)
		}
		add("Letzte Sicherung", z...)
	}

	switch p := h.Prune; {
	case p == nil:
		add("Aufräumen des Speichers", "Seit dem Start des Koordinators noch nicht gelaufen.")
	case p.Error != "":
		add("Aufräumen des Speichers", "fehlgeschlagen "+seit(p.Time, jetzt), p.Error)
	case p.Probe:
		add("Aufräumen des Speichers", "Probe während der Wartung "+seit(p.Time, jetzt),
			fmt.Sprintf("würde %d %s und %d Blobs (%s) löschen", p.Generations, plural(p.Generations, "Stand", "Stände"), p.Blobs, groesse(p.Bytes)))
	default:
		add("Aufräumen des Speichers", "gelaufen "+seit(p.Time, jetzt))
	}
	return ks
}

// ---- Ansicht ----

type zustandAnsicht struct {
	liste       widget.List
	aktualisier widget.Clickable
}

func (v *zustandAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	v.liste.Axis = layout.Vertical
	g := s.gesundheit
	jetzt := gtx.Now
	if v.aktualisier.Clicked(gtx) {
		u.d.gesundheitNeuLaden()
	}

	var teile []layout.Widget
	teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.kopf(gtx, u, g, jetzt) })
	switch {
	case g.h == nil && g.err != "":
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungWarn, "Zustand nicht abrufbar", g.err)
		})
	case g.h == nil:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.karte(gtx, t.leise("Der Zustand wird geladen …"))
		})
	default:
		if g.err != "" {
			teile = append(teile, func(gtx layout.Context) layout.Dimensions {
				return t.meldung(gtx, meldungWarn, "Aktualisieren nicht gelungen – die Werte sind vom letzten Abruf", g.err)
			})
		}
		teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.warnungen(gtx, t, g.h) })
		ks := zustandKarten(g.h, jetzt)
		for i := 0; i < len(ks); i += 2 {
			if i+1 == len(ks) {
				k := ks[i]
				teile = append(teile, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = (gtx.Constraints.Max.X - gtx.Dp(14)) / 2
					return v.karte(gtx, t, k)
				})
				continue
			}
			a, b := ks[i], ks[i+1]
			teile = append(teile, func(gtx layout.Context) layout.Dimensions {
				return gleichHoch(gtx, 14,
					func(gtx layout.Context) layout.Dimensions { return v.karte(gtx, t, a) },
					func(gtx layout.Context) layout.Dimensions { return v.karte(gtx, t, b) })
			})
		}
	}
	return seite(gtx, &v.liste, teile)
}

// kopf: Ampel, Zeit des letzten Abrufs, „Aktualisieren“.
func (v *zustandAnsicht) kopf(gtx layout.Context, u *oberflaeche, g gesundheitDaten, jetzt time.Time) layout.Dimensions {
	t := u.th
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if g.h == nil {
					return punkt(gtx, t.f.aus, 22)
				}
				_, art := ampelText(g.h.Status)
				_, rand, _ := t.meldungsFarben(art)
				return punkt(gtx, rand, 22)
			}),
			layout.Rigid(layout.Spacer{Width: 14}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				titel, unter := "Zustand", "Wird geladen …"
				if g.h != nil {
					titel, _ = ampelText(g.h.Status)
					unter = "Alles, wofür du sonst im Log des Servers nachsiehst."
					if !g.ab.IsZero() {
						unter = "Aktualisiert " + vorSekunden(g.ab, jetzt) + " – danach alle 30 s, solange dieser Reiter offen ist."
					}
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 20, fett: true, farbe: t.f.text, zeilen: 1}, titel)
					}),
					layout.Rigid(t.leise(unter)),
				)
			}),
			layout.Rigid(layout.Spacer{Width: 14}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.knopf(gtx, &v.aktualisier, "Aktualisieren", knopfNeben, !g.laeuft)
			}),
		)
	})
}

// warnungen: die Liste der Warnungen (oder „keine“).
func (v *zustandAnsicht) warnungen(gtx layout.Context, t *thema, h *api.Health) layout.Dimensions {
	if len(h.Warnings) == 0 {
		return t.meldung(gtx, meldungInfo, "Keine Warnungen", "Speicherplatz, Sicherung, Host und Starter-Versionen sind in Ordnung.")
	}
	kinder := make([]layout.FlexChild, 0, 2*len(h.Warnings))
	for i, w := range h.Warnings {
		if i > 0 {
			kinder = append(kinder, layout.Rigid(layout.Spacer{Height: 8}.Layout))
		}
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, warnungsArt(w.Level), stufenName(w.Level), w.Text)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
}

// stufenName: Überschrift einer Warnung.
func stufenName(stufe string) string {
	switch stufe {
	case api.WarnFehler:
		return "Fehler"
	case api.WarnWarnung:
		return "Warnung"
	}
	return "Hinweis"
}

// karte: eine Karte mit Titel und Textzeilen.
func (v *zustandAnsicht) karte(gtx layout.Context, t *thema, k zustandKarte) layout.Dimensions {
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		kinder := []layout.FlexChild{
			layout.Rigid(t.klein(strings.ToUpper(k.titel))),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
		}
		for i, z := range k.zeilen {
			if i > 0 {
				kinder = append(kinder, layout.Rigid(layout.Spacer{Height: 3}.Layout))
			}
			farbe := t.f.text
			if i > 0 {
				farbe = t.f.leise
			}
			kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.text(gtx, schrift{groesse: 14, farbe: farbe}, z)
			}))
		}
		kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(0, 0)}
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
	})
}

// nurAdmin: Reiter, die es nur für Admins gibt.
func nurAdmin(r reiter) bool {
	return r == reiterMitglieder || r == reiterZustand || r == reiterServer
}
