package fenster

import (
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// ansicht ist ein benannter Zustand für Rauchtest und Bildschirmfotos.
type ansicht struct {
	name   string
	s      schnappschuss
	reiter reiter
	// vorher richtet die Oberfläche her (aufgeklappt, Rückfrage …).
	vorher func(u *oberflaeche, jetzt time.Time)
}

// beispielAnsichten: jede Ansicht in den Fällen Admin/Nicht-Admin, kein
// Host, Host, ausstehendes Zurückholen, Status nil, ohne Instanz.
func beispielAnsichten(jetzt time.Time) []ansicht {
	voll := func(z *fensterapi.Zustand) schnappschuss {
		return schnappschuss{verb: verbunden, runde: 1, z: z,
			staende: beispielStaende(jetzt), staendeAb: jetzt,
			saves: beispielSaves(jetzt), savesAb: jetzt,
			log: beispielLog(jetzt), logAb: jetzt,
			mitglieder: beispielMitglieder(), mitgliederAb: jetzt}
	}
	admin := beispielZustand(jetzt, true, true)
	gast := beispielZustand(jetzt, false, true)

	ohneHost := beispielZustand(jetzt, true, false)

	hostIch := beispielZustand(jetzt, true, true)
	hostIch.Hosting, hostIch.Phase = true, "Hostet"
	hostIch.HostAddr = "wanderwelt.example.de"
	hostIch.Status.Host = &api.HostInfo{Email: "lukas@example.de", MinecraftName: "Lukas"}
	hostIch.Profil.Dauerbetrieb = true

	laedt := beispielZustand(jetzt, false, false)
	laedt.Phase, laedt.Busy, laedt.Detail = "Startet", "Spielen", "lädt die Welt: 45 %"

	uebergabe := beispielZustand(jetzt, true, true)
	uebergabe.Hosting, uebergabe.Phase = true, "Hostet"
	uebergabe.Status.Host = &api.HostInfo{Email: "lukas@example.de", MinecraftName: "Lukas"}
	uebergabe.Status.Handover = &api.HandoverState{HandoverID: 3, State: api.HandoverStarting,
		To: api.HostInfo{MinecraftName: "Mara"}}
	uebergabeFehl := beispielZustand(jetzt, false, true)
	uebergabeFehl.Status.Handover = &api.HandoverState{HandoverID: 3, State: api.HandoverFailed,
		To: api.HostInfo{MinecraftName: "Mara"}}

	zurueck := beispielZustand(jetzt, true, true)
	zurueck.Status.RestorePending = &api.RestoreNotice{By: "Lukas", Target: api.GenRef{WorldID: "w-2026-11-03", Number: 405},
		TargetCreated: jetzt.Add(-2 * time.Hour), RequestedAt: jetzt.Add(-10 * time.Second), DelayS: 30}

	ohneStatus := beispielZustand(jetzt, false, false)
	ohneStatus.Status = nil
	ohneStatus.StatusErr = "Get \"https://wanderwelt.example.de/v1/status\": dial tcp: no such host"

	ohneMe := beispielZustand(jetzt, false, false)
	ohneMe.Me, ohneMe.Status = nil, nil

	fehler := beispielZustand(jetzt, false, false)
	fehler.Phase, fehler.LastErr = "Fehler", "Spielen: Der Koordinator ist nicht erreichbar (Zeitüberschreitung)."

	leer := voll(beispielZustand(jetzt, true, false))
	leer.staende, leer.saves, leer.log, leer.mitglieder = nil, nil, "", nil
	leer.staendeAb, leer.savesAb, leer.logAb, leer.mitgliederAb = time.Time{}, time.Time{}, time.Time{}, time.Time{}

	altKoord := voll(beispielZustand(jetzt, true, false))
	altKoord.staende, altKoord.staendeErr = nil, "Der Koordinator ist zu alt für diese Ansicht"
	altKoord.mitglieder, altKoord.mitgliederErr = nil, "Der Koordinator ist zu alt für diese Ansicht"

	ueberBudget := voll(beispielZustand(jetzt, true, true))
	ueberBudget.staende.Storage = &api.StorageInfo{UsedBytes: 23 << 30, BudgetBytes: 20 << 30, OverBudget: true}

	ohneSpeicher := voll(beispielZustand(jetzt, false, true))
	ohneSpeicher.staende.Storage = nil

	var as []ansicht
	add := func(name string, s schnappschuss, r reiter, vorher func(*oberflaeche, time.Time)) {
		as = append(as, ansicht{name: name, s: s, reiter: r, vorher: vorher})
	}
	add("start-host", voll(admin), reiterStart, nil)
	add("start-niemand", voll(ohneHost), reiterStart, nil)
	add("start-ich-hoste", voll(hostIch), reiterStart, nil)
	add("start-startet", voll(laedt), reiterStart, nil)
	add("start-zurueckholen", voll(zurueck), reiterStart, nil)
	add("start-uebergabe", voll(uebergabe), reiterStart, nil)
	add("start-uebergabe-gescheitert", voll(uebergabeFehl), reiterStart, nil)
	add("start-ohne-status", voll(ohneStatus), reiterStart, nil)
	add("start-ohne-me", voll(ohneMe), reiterStart, nil)
	add("start-fehler", voll(fehler), reiterStart, nil)
	add("start-hinweis", voll(admin), reiterStart, func(u *oberflaeche, jetzt time.Time) {
		u.zeige("info", "Mara hostet – verbinde dich mit wanderwelt.example.de (Profil wanderwelt).", jetzt)
		u.zeige("fehler", "Spielen: Der Koordinator antwortet nicht.", jetzt)
	})
	add("welt-admin", voll(admin), reiterWelt, func(u *oberflaeche, _ time.Time) {
		u.welt.offen = map[string]bool{"w-2026-10-12": true}
	})
	add("welt-admin-uebernehmen", voll(admin), reiterWelt, func(u *oberflaeche, _ time.Time) {
		u.welt.savesOffen = true
		u.welt.budgetOffen = true
		u.welt.budgetEd.SetText("20")
		u.welt.budgetFehler = "Das Budget muss zwischen 1 GB und 48 GB liegen."
	})
	add("welt-gast", voll(gast), reiterWelt, nil)
	add("welt-zurueckholen", voll(zurueck), reiterWelt, nil)
	add("welt-ueber-budget", ueberBudget, reiterWelt, nil)
	add("welt-ohne-speicher", ohneSpeicher, reiterWelt, nil)
	add("welt-laedt", leer, reiterWelt, nil)
	add("welt-alter-koordinator", altKoord, reiterWelt, nil)
	add("welt-rueckfrage", voll(admin), reiterWelt, func(u *oberflaeche, jetzt time.Time) {
		g := beispielStaende(jetzt)
		u.welt.frageZurueckholen(u, g.Worlds[0], g.Worlds[0].Generations[3], true, jetzt)
	})
	add("start-beenden-rueckfrage", voll(hostIch), reiterStart, func(u *oberflaeche, _ time.Time) {
		u.start.beenden.Click()
	})
	add("hosten", voll(admin), reiterHosten, nil)
	add("hosten-unten", voll(admin), reiterHosten, func(u *oberflaeche, _ time.Time) {
		u.hosten.liste.Position.Offset = 400 // Dauerbetrieb, Speichern, Windows
	})
	add("hosten-ich-hoste", voll(hostIch), reiterHosten, nil)
	ohneRAM := beispielZustand(jetzt, false, false)
	ohneRAM.RAMTotalMB, ohneRAM.Profil.C2ME = 0, false
	add("hosten-ohne-ram", voll(ohneRAM), reiterHosten, nil)
	add("mitglieder", voll(admin), reiterMitglieder, nil)
	add("mitglieder-fehler", voll(admin), reiterMitglieder, func(u *oberflaeche, _ time.Time) {
		u.mitgl.fehler = "Lukas ist der einzige Admin – erst jemand anderem das Admin-Recht geben."
	})
	mitAnfragen := voll(admin)
	mitAnfragen.anfragen = beispielAnfragen(jetzt)
	add("mitglieder-anfragen", mitAnfragen, reiterMitglieder, nil)
	add("mitglieder-anfragen-rueckfrage", mitAnfragen, reiterMitglieder, func(u *oberflaeche, _ time.Time) {
		u.mitgl.anfr.klick(&u.mitgl.anfr.ablehnen, "tim@example.de").Click()
	})
	add("mitglieder-laedt", leer, reiterMitglieder, nil)
	add("mitglieder-alter-koordinator", altKoord, reiterMitglieder, nil)
	add("mitglieder-als-gast", voll(gast), reiterMitglieder, nil) // fällt auf Start zurück
	add("log", voll(admin), reiterLog, nil)
	add("log-leer", leer, reiterLog, nil)
	add("laeuft-nicht", schnappschuss{verb: laeuftNicht}, reiterStart, nil)
	add("laeuft-nicht-startfehler", schnappschuss{verb: laeuftNicht, startFehl: "wanderwelt hat sich nach dem Starten nicht gemeldet."}, reiterStart, nil)
	add("startet", schnappschuss{verb: startet}, reiterStart, nil)
	add("verbindet", schnappschuss{verb: verbindet}, reiterStart, nil)
	add("antwortet-falsch", schnappschuss{verb: verbunden, zErr: "die laufende wanderwelt-Version kennt das noch nicht – bitte wanderwelt neu starten"}, reiterStart, nil)
	mehrere := beispielZustand(jetzt, false, true)
	mehrere.Gruppen = []fensterapi.GruppeEintrag{{ID: "wanderwelt.example.de", Titel: "Wandergruppe", Aktiv: true},
		{ID: "zweite.example.de", Titel: "Eine Gruppe mit einem sehr langen Titel, der gekürzt werden muss"}, {ID: "dritte.example.de"}}
	add("start-mehrere-gruppen", voll(mehrere), reiterStart, nil)
	gesperrt := beispielZustand(jetzt, false, true)
	gesperrt.Gruppen = mehrere.Gruppen
	gesperrt.WechselSperre = "Du hostest oder spielst gerade."
	add("start-gruppen-gesperrt", voll(gesperrt), reiterStart, func(u *oberflaeche, _ time.Time) {
		u.gruppen.knoepfe = map[string]*widget.Clickable{"zweite.example.de": new(widget.Clickable)}
		u.gruppen.knoepfe["zweite.example.de"].Click()
	})
	entd := beispielZustand(jetzt, false, true)
	liste := &fensterapi.Entdecken{MinecraftName: "Steve_1", Gruppen: []fensterapi.EntdeckenEintrag{
		{URL: "https://a.example.org", Titel: "Wandergruppe", Beschreibung: "Survival mit Freunden. Wir spielen abends.", Mitglieder: 7, Zustand: fensterapi.EntdeckenMitglied, Lokal: true},
		{URL: "https://b.example.org", Titel: "<b>Fremde</b> Gruppe mit einem sehr langen Titel, der umbrechen muss, weil er so lang ist", Beschreibung: strings.Repeat("Lange Beschreibung. ", 20), Mitglieder: 1, Zustand: fensterapi.EntdeckenKeiner},
		{URL: "https://c.example.org", Titel: "Angefragt", Mitglieder: 12, Zustand: fensterapi.EntdeckenAngefragt},
		{URL: "https://d.example.org", Titel: "Kaputt", Zustand: fensterapi.EntdeckenUnbekannt, Fehler: "Der Koordinator dieser Gruppe ist nicht erreichbar."},
	}, Hinweise: []string{"tunnel.example.org hat keine gelisteten Gruppen."}}
	mitListe := voll(entd)
	mitListe.entdecken, mitListe.entdeckenAb = liste, jetzt
	add("entdecken", mitListe, reiterEntdecken, nil)
	add("entdecken-formular", mitListe, reiterEntdecken, func(u *oberflaeche, _ time.Time) {
		u.entd.nameFuer, u.entd.nameTitel, u.entd.nameFehler = "https://b.example.org", "Fremde Gruppe", "Der Name besteht aus 3 bis 16 Buchstaben, Ziffern oder Unterstrichen."
		u.entd.nameEd.SetText("Steve_1")
	})
	add("entdecken-laedt", voll(entd), reiterEntdecken, nil)
	fehlerE := voll(entd)
	fehlerE.entdeckenErr = "Steuerung antwortet nicht"
	add("entdecken-fehler", fehlerE, reiterEntdecken, nil)
	leerE := voll(entd)
	leerE.entdecken = &fensterapi.Entdecken{}
	add("entdecken-leer", leerE, reiterEntdecken, nil)
	return as
}

// testOberflaeche baut eine Oberfläche ohne Fenster und ohne Abfrage.
func testOberflaeche(t testing.TB, dunkel bool, a ansicht, jetzt time.Time) *oberflaeche {
	t.Helper()
	th, err := neuesThema(dunkel)
	if err != nil {
		t.Fatal(err)
	}
	d := neueDaten(func() error { return nil }, nil)
	d.s = a.s
	u := neueOberflaeche(th, d)
	u.reiter = a.reiter
	if a.vorher != nil {
		a.vorher(u, jetzt)
	}
	return u
}

func testKontext(ops *op.Ops, groesse image.Point, skala float32, jetzt time.Time) layout.Context {
	ops.Reset()
	return layout.Context{
		Ops:         ops,
		Now:         jetzt,
		Metric:      unit.Metric{PxPerDp: skala, PxPerSp: skala},
		Constraints: layout.Exact(groesse),
	}
}

// TestLayoutRauchtest: Jede Ansicht lässt sich in beiden Designs, in
// Standard- und Mindestgröße, ohne Panik layouten.
func TestLayoutRauchtest(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	var ops op.Ops
	for _, a := range beispielAnsichten(jetzt) {
		for _, dunkel := range []bool{false, true} {
			for _, g := range []struct {
				sz    image.Point
				skala float32
			}{{image.Pt(900, 620), 1}, {image.Pt(720, 520), 1}, {image.Pt(1125, 775), 1.25}} {
				t.Run(fmt.Sprintf("%s/dunkel=%v/%dx%d", a.name, dunkel, g.sz.X, g.sz.Y), func(t *testing.T) {
					u := testOberflaeche(t, dunkel, a, jetzt)
					// Zwei Bilder: das zweite mit dem Zustand nach dem ersten.
					for range 2 {
						gtx := testKontext(&ops, g.sz, g.skala, jetzt)
						dims := u.layout(gtx)
						if dims.Size.X != g.sz.X || dims.Size.Y != g.sz.Y {
							t.Errorf("Größe %v, want %v", dims.Size, g.sz)
						}
					}
				})
			}
		}
	}
}

// TestReiterMitglieder: Den Reiter Mitglieder gibt es nur für Admins.
func TestReiterMitglieder(t *testing.T) {
	jetzt := time.Now()
	var ops op.Ops
	for _, admin := range []bool{true, false} {
		a := ansicht{s: schnappschuss{verb: verbunden, z: beispielZustand(jetzt, admin, true)}, reiter: reiterMitglieder}
		u := testOberflaeche(t, false, a, jetzt)
		u.layout(testKontext(&ops, image.Pt(900, 620), 1, jetzt))
		if got := u.reiter == reiterMitglieder; got != admin {
			t.Errorf("admin=%v: Reiter Mitglieder bleibt %v", admin, got)
		}
	}
}

// TestHinweiseNachSeq: Hinweise, die beim Öffnen schon da waren, erscheinen
// nicht; neue (nach Seq) 8 s lang; eine neue Instanz zählt neu.
func TestHinweiseNachSeq(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.UTC)
	u := &oberflaeche{}
	z := &fensterapi.Zustand{Hinweise: []fensterapi.Hinweis{{Seq: 4, Text: "alt"}}}
	u.hinweiseNachziehen(jetzt, schnappschuss{runde: 1, z: z})
	if len(u.hinweise) != 0 {
		t.Fatalf("alter Hinweis gezeigt: %+v", u.hinweise)
	}
	z = &fensterapi.Zustand{Hinweise: []fensterapi.Hinweis{{Seq: 4, Text: "alt"}, {Seq: 5, Art: "fehler", Text: "neu"}}}
	u.hinweiseNachziehen(jetzt, schnappschuss{runde: 1, z: z})
	if len(u.hinweise) != 1 || u.hinweise[0].text != "neu" || u.hinweise[0].art != "fehler" {
		t.Fatalf("neuer Hinweis: %+v", u.hinweise)
	}
	u.hinweiseNachziehen(jetzt.Add(hinweisDauer-time.Millisecond), schnappschuss{runde: 1, z: z})
	if len(u.hinweise) != 1 {
		t.Fatalf("Hinweis vor Ablauf weg")
	}
	u.hinweiseNachziehen(jetzt.Add(hinweisDauer), schnappschuss{runde: 1, z: z})
	if len(u.hinweise) != 0 {
		t.Fatalf("Hinweis nach %v noch da", hinweisDauer)
	}
	// Neue Instanz (Seq beginnt neu): Vorhandenes gilt wieder als gesehen,
	// danach Neues wird gezeigt.
	z = &fensterapi.Zustand{Hinweise: []fensterapi.Hinweis{{Seq: 1, Text: "Start"}}}
	u.hinweiseNachziehen(jetzt, schnappschuss{runde: 2, z: z})
	z = &fensterapi.Zustand{Hinweise: []fensterapi.Hinweis{{Seq: 1, Text: "Start"}, {Seq: 2, Text: "zwei"}}}
	u.hinweiseNachziehen(jetzt, schnappschuss{runde: 2, z: z})
	if len(u.hinweise) != 1 || u.hinweise[0].text != "zwei" {
		t.Fatalf("nach neuer Instanz: %+v", u.hinweise)
	}
}
