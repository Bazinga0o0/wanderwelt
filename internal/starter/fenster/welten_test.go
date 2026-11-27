package fenster

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

const sperreText = "Es hostet gerade jemand – gespielt wird seine Welt. Wechseln geht, wenn niemand hostet."

// mitWelten gibt z drei Welten der Gruppe; laufend: die Welt, die gerade
// gehostet wird ("" keine). Namen sind fremder Text (hier mit Markup und sehr
// lang) und dürfen nur als Text erscheinen.
func mitWelten(z *fensterapi.Zustand, laufend string) *fensterapi.Zustand {
	z.Status.Worlds = []api.GroupWorld{
		{ID: "w1", Name: "Survival", Gamemode: "survival", Difficulty: "hard", HeadGeneration: 412},
		{ID: "w2", Name: "<b>Creative</b> mit einem wirklich sehr, sehr langen Namen für die Probe", Gamemode: "creative", Difficulty: "peaceful", HeadGeneration: 40},
		{ID: "w3", Name: "Neu", Empty: true},
	}
	z.Status.RunningWorld = laufend
	z.Welt = fensterapi.WeltZustand{Aktiv: "w1", Laufend: laufend}
	if laufend != "" {
		z.Welt.Aktiv, z.Welt.Sperre = laufend, sperreText
		for i := range z.Status.Worlds {
			z.Status.Worlds[i].Running = z.Status.Worlds[i].ID == laufend
		}
	}
	return z
}

func weltBedienung(t *testing.T, z *fensterapi.Zustand, r reiter) *bedienTest {
	t.Helper()
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	return neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z, staende: beispielStaende(jetzt), staendeAb: jetzt}, r)
}

// Die Auswahl erscheint erst ab zwei Welten; ein Klick wählt (Aktion „welt“),
// die schon gewählte Welt und jeder Klick während gehostet wird tun nichts.
func TestWeltAuswahlKlick(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)

	t.Run("eine Welt: keine Auswahl", func(t *testing.T) {
		z := beispielZustand(jetzt, false, false)
		z.Status.Worlds = []api.GroupWorld{{ID: "w1", Name: "Welt"}}
		b := weltBedienung(t, z, reiterStart)
		b.bild()
		if len(b.u.start.welten.knoepfe) != 0 {
			t.Fatalf("Knöpfe bei einer Welt: %v", b.u.start.welten.knoepfe)
		}
	})
	t.Run("Wählen", func(t *testing.T) {
		b := weltBedienung(t, mitWelten(beispielZustand(jetzt, false, false), ""), reiterStart)
		b.bild()
		b.u.start.welten.knoepfe["w2"].Click()
		b.bild()
		got := b.warte(1)
		if len(got) != 1 || got[0] != `welt {"id":"w2"}` {
			t.Fatalf("Aktionen: %q", got)
		}
	})
	t.Run("die gewählte Welt: nichts", func(t *testing.T) {
		b := weltBedienung(t, mitWelten(beispielZustand(jetzt, false, false), ""), reiterStart)
		b.bild()
		b.u.start.welten.knoepfe["w1"].Click()
		b.bild()
		time.Sleep(50 * time.Millisecond)
		if got := b.q.liste(); len(got) != 0 {
			t.Fatalf("Aktionen: %q", got)
		}
	})
	t.Run("während gehostet wird: nichts, Hinweis", func(t *testing.T) {
		b := weltBedienung(t, mitWelten(beispielZustand(jetzt, false, true), "w2"), reiterStart)
		b.bild()
		b.u.start.welten.knoepfe["w1"].Click()
		b.bild()
		time.Sleep(50 * time.Millisecond)
		if got := b.q.liste(); len(got) != 0 {
			t.Fatalf("Aktionen trotz Sperre: %q", got)
		}
		if len(b.u.hinweise) == 0 || b.u.hinweise[0].text != sperreText || b.u.hinweise[0].art != "fehler" {
			t.Fatalf("Hinweis: %+v", b.u.hinweise)
		}
	})
}

// Tab-Reihenfolge: Die Knöpfe aller Welten (wie überall im Fenster auch
// gesperrte) kommen vor „Spielen“; ohne Auswahl bleibt die Zahl der Tabs bis
// „Spielen“ wie bisher.
func TestWeltAuswahlTabFolge(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	fokusFolge := func(z *fensterapi.Zustand) (chips, spielen int, fokussiert map[string]bool) {
		b := weltBedienung(t, z, reiterStart)
		fokussiert = map[string]bool{}
		b.bild()
		spielen = -1
		for i := 1; i <= 40; i++ {
			b.r.MoveFocus(key.FocusForward)
			b.bild()
			src := b.r.Source()
			for id, k := range b.u.start.welten.knoepfe {
				if src.Focused(k) {
					fokussiert[id] = true
					chips = i
				}
			}
			if src.Focused(&b.u.start.spielen) {
				spielen = i
				break
			}
		}
		return chips, spielen, fokussiert
	}
	chips, spielen, fok := fokusFolge(mitWelten(beispielZustand(jetzt, false, false), ""))
	if spielen < 0 || chips == 0 || chips >= spielen {
		t.Fatalf("Welten bei Tab %d, „Spielen“ bei %d", chips, spielen)
	}
	if !fok["w1"] || !fok["w2"] || !fok["w3"] {
		t.Errorf("Fokus auf Welten: %v, erwartet alle drei", fok)
	}
	_, spielenOhne, fokOhne := fokusFolge(beispielZustand(jetzt, false, false))
	if spielenOhne != spielen-3 || len(fokOhne) != 0 {
		t.Errorf("„Spielen“ ohne Auswahl bei Tab %d, mit Auswahl bei %d (erwartet drei mehr)", spielenOhne, spielen)
	}
}

// Welten verwalten (nur Admins): anlegen, ändern, löschen.
func TestWeltenVerwaltenBedienung(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	admin := func(laufend string) *bedienTest {
		b := weltBedienung(t, mitWelten(beispielZustand(jetzt, true, laufend != ""), laufend), reiterWelt)
		b.bild()
		b.u.welt.verwaltung.auf.Click()
		b.bild()
		if !b.u.welt.verwaltung.offen {
			t.Fatal("„Welten verwalten“ ließ sich nicht öffnen")
		}
		return b
	}

	t.Run("anlegen", func(t *testing.T) {
		b := admin("")
		v := &b.u.welt.verwaltung
		v.neu.Click()
		b.bild()
		if !v.formular || v.formularID != "" {
			t.Fatal("Formular nicht offen")
		}
		// Ohne Namen und mit vorhandenem Namen passiert nichts.
		v.speichern.Click()
		b.bild()
		if v.fehler == "" || len(b.q.liste()) != 0 {
			t.Fatalf("leerer Name: Fehler %q, Aktionen %q", v.fehler, b.q.liste())
		}
		v.nameEd.SetText("  survival ")
		v.speichern.Click()
		b.bild()
		if !strings.Contains(v.fehler, "Es gibt schon eine Welt „Survival“") || len(b.q.liste()) != 0 {
			t.Fatalf("doppelter Name: Fehler %q, Aktionen %q", v.fehler, b.q.liste())
		}
		v.nameEd.SetText("Creative")
		klick(&v.modusKn, "creative").Click()
		klick(&v.schwerKn, "hard").Click()
		b.bild()
		v.speichern.Click()
		b.bild()
		got := b.warte(1)
		if len(got) != 1 || got[0] != `weltneu {"name":"Creative","gamemode":"creative","difficulty":"hard"}` {
			t.Fatalf("Aktionen: %q", got)
		}
		b.u.d.abarbeiten()
		b.bild()
		if v.formular || v.fehler != "" {
			t.Errorf("Formular nach dem Speichern: offen %v, Fehler %q", v.formular, v.fehler)
		}
	})
	t.Run("ändern", func(t *testing.T) {
		b := admin("")
		v := &b.u.welt.verwaltung
		klick(&v.aendern, "w2").Click()
		b.bild()
		if !v.formular || v.formularID != "w2" || v.modus != "creative" || v.schwer != "peaceful" || !strings.Contains(v.nameEd.Text(), "Creative") {
			t.Fatalf("Formular: %+v %q", v.formularID, v.nameEd.Text())
		}
		v.nameEd.SetText("Bauen")
		klick(&v.modusKn, "adventure").Click()
		klick(&v.schwerKn, "").Click()
		b.bild()
		v.speichern.Click()
		b.bild()
		got := b.warte(1)
		want := `weltaendern {"id":"w2","patch":{"name":"Bauen","gamemode":"adventure","difficulty":""}}`
		if len(got) != 1 || got[0] != want {
			t.Fatalf("Aktionen: %q, erwartet %q", got, want)
		}
	})
	t.Run("abbrechen schickt nichts", func(t *testing.T) {
		b := admin("")
		v := &b.u.welt.verwaltung
		v.neu.Click()
		b.bild()
		v.nameEd.SetText("X")
		v.abbrechen.Click()
		b.bild()
		time.Sleep(50 * time.Millisecond)
		if v.formular || len(b.q.liste()) != 0 {
			t.Fatalf("offen %v, Aktionen %q", v.formular, b.q.liste())
		}
	})
	t.Run("löschen: Rückfrage, dann Aktion", func(t *testing.T) {
		b := admin("")
		v := &b.u.welt.verwaltung
		klick(&v.loeschen, "w3").Click()
		b.bild()
		if b.u.rf == nil || !b.u.rf.gefahr || !strings.Contains(b.u.rf.titel, "Neu") || b.u.rf.ja != "Löschen" {
			t.Fatalf("Rückfrage: %+v", b.u.rf)
		}
		if len(b.q.liste()) != 0 {
			t.Fatalf("Aktion vor der Bestätigung: %q", b.q.liste())
		}
		// Escape: nichts.
		b.r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
		b.bild()
		if b.u.rf != nil || len(b.q.liste()) != 0 {
			t.Fatalf("nach Escape: Rückfrage %v, Aktionen %q", b.u.rf != nil, b.q.liste())
		}
		// Nochmal und bestätigen (Knöpfe der Rückfrage: Behalten, Löschen).
		klick(&v.loeschen, "w1").Click()
		b.bild()
		b.r = input.Router{}
		b.bild()
		b.r.MoveFocus(key.FocusForward)
		b.r.MoveFocus(key.FocusForward)
		if !b.r.Source().Focused(&b.u.rfJa) {
			t.Fatal("Fokus nach zweimal Tab nicht auf „Löschen“")
		}
		b.r.ClickFocus()
		b.bild()
		got := b.warte(1)
		if len(got) != 1 || got[0] != `weltloeschen {"id":"w1","name":"Survival"}` {
			t.Fatalf("Aktionen: %q", got)
		}
	})
	t.Run("löschen: nicht, solange die Welt läuft", func(t *testing.T) {
		b := admin("w2")
		v := &b.u.welt.verwaltung
		klick(&v.loeschen, "w2").Click()
		b.bild()
		time.Sleep(50 * time.Millisecond)
		if b.u.rf != nil || len(b.q.liste()) != 0 || !strings.Contains(v.fehler, "läuft gerade") {
			t.Fatalf("Rückfrage %v, Aktionen %q, Fehler %q", b.u.rf != nil, b.q.liste(), v.fehler)
		}
		// Eine andere Welt lässt sich löschen, während w2 läuft.
		klick(&v.loeschen, "w3").Click()
		b.bild()
		if b.u.rf == nil {
			t.Fatal("andere Welt nicht löschbar, während w2 läuft")
		}
	})
	t.Run("löschen: nicht die letzte Welt", func(t *testing.T) {
		z := mitWelten(beispielZustand(jetzt, true, false), "")
		z.Status.Worlds = z.Status.Worlds[:1]
		b := weltBedienung(t, z, reiterWelt)
		b.bild()
		b.u.welt.verwaltung.auf.Click()
		b.bild()
		klick(&b.u.welt.verwaltung.loeschen, "w1").Click()
		b.bild()
		if b.u.rf != nil || !strings.Contains(b.u.welt.verwaltung.fehler, "letzte Welt") {
			t.Fatalf("Rückfrage %v, Fehler %q", b.u.rf != nil, b.u.welt.verwaltung.fehler)
		}
	})
	t.Run("kein Admin: keine Verwaltung", func(t *testing.T) {
		b := weltBedienung(t, mitWelten(beispielZustand(jetzt, false, false), ""), reiterWelt)
		b.bild()
		b.u.welt.verwaltung.auf.Click()
		b.bild()
		time.Sleep(50 * time.Millisecond)
		if b.u.welt.verwaltung.offen || len(b.q.liste()) != 0 {
			t.Fatalf("Verwaltung für Nicht-Admin: offen %v, Aktionen %q", b.u.welt.verwaltung.offen, b.q.liste())
		}
	})
	t.Run("ohne Weltliste (älterer Koordinator): keine Verwaltung", func(t *testing.T) {
		b := weltBedienung(t, beispielZustand(jetzt, true, false), reiterWelt)
		b.bild()
		b.u.welt.verwaltung.auf.Click()
		b.bild()
		if b.u.welt.verwaltung.offen {
			t.Fatal("Verwaltung ohne Weltliste")
		}
	})
}

// Texte: Eigenschaften auf Deutsch, Standard als „Gruppenstandard“.
func TestWeltEigenschaftenText(t *testing.T) {
	if got := weltEigenschaften(api.GroupWorld{Gamemode: "creative", Difficulty: "hard"}); got != "Spielmodus Kreativ · Schwierigkeit Schwer" {
		t.Errorf("%q", got)
	}
	if got := weltEigenschaften(api.GroupWorld{}); got != "" {
		t.Errorf("%q", got)
	}
	if got := weltEigenschaften(api.GroupWorld{Gamemode: "adventure"}); got != "Spielmodus Abenteuer" {
		t.Errorf("%q", got)
	}
	for _, m := range spielmodiReihenfolge {
		if m != "" && !slices.Contains(api.Gamemodes, m) {
			t.Errorf("Spielmodus %q kennt der Koordinator nicht", m)
		}
	}
	for _, d := range schwierigkeitenFolge {
		if d != "" && !slices.Contains(api.Difficulties, d) {
			t.Errorf("Schwierigkeit %q kennt der Koordinator nicht", d)
		}
	}
	if len(spielmodiNamen) != len(api.Gamemodes) || len(schwierigkeitenNamen) != len(api.Difficulties) {
		t.Error("deutsche Namen und Werte des Koordinators passen nicht zusammen")
	}
}
