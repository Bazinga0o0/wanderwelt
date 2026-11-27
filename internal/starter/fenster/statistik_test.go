package fenster

import (
	"context"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Sortierung: Hostzeit absteigend, dann Spielzeit, dann Name; Anteile werden
// aus der Hostzeit gerechnet.
func TestStatistikZeilen(t *testing.T) {
	jetzt := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	st := beispielStatistik(jetzt)

	zs := statistikZeilen(st.Total)
	var namen []string
	for _, z := range zs {
		namen = append(namen, z.name)
	}
	if want := []string{"Lukas", "Mara", "Jonas", "KimTheBuilder", "Neuling"}; !gleichListe(namen, want) {
		t.Fatalf("Gesamt: %v, erwartet %v", namen, want)
	}
	var summe float64
	for _, z := range zs {
		summe += z.anteil
	}
	if summe < 0.999 || summe > 1.001 {
		t.Errorf("Anteile ergeben %.4f", summe)
	}
	if zs[0].anteil < 0.59 || zs[0].anteil > 0.60 { // 140 von etwa 235 Stunden
		t.Errorf("Anteil Lukas: %.3f", zs[0].anteil)
	}

	// 30 Tage: Mara trägt am meisten; ohne Hostzeit nach Spielzeit, dann Name.
	zs = statistikZeilen(st.Last30)
	namen = nil
	for _, z := range zs {
		namen = append(namen, z.name)
	}
	if want := []string{"Mara", "Lukas", "Jonas", "KimTheBuilder", "Neuling"}; !gleichListe(namen, want) {
		t.Fatalf("30 Tage: %v, erwartet %v", namen, want)
	}

	// Gleichstand: Name ohne Rücksicht auf Groß/Klein.
	zs = statistikZeilen(api.StatsPeriod{Members: []api.MemberStats{{MinecraftName: "bert"}, {MinecraftName: "Anna"}}})
	if zs[0].name != "Anna" || zs[0].hatHost {
		t.Errorf("Gleichstand: %+v", zs)
	}
}

func gleichListe(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStatistikFormate(t *testing.T) {
	for s, want := range map[int64]string{
		0: "–", -5: "–", 1: "unter 1 Min.", 59: "unter 1 Min.", 60: "1 Min.", 3599: "59 Min.",
		3600: "1 Std. 00 Min.", 3600*5 + 7*60 + 59: "5 Std. 07 Min.", 3600 * 212: "212 Std. 00 Min.",
	} {
		if got := statistikDauer(s); got != want {
			t.Errorf("statistikDauer(%d) = %q, erwartet %q", s, got, want)
		}
	}
	for _, tc := range []struct {
		z    statistikZeile
		want string
	}{
		{statistikZeile{}, "–"},
		{statistikZeile{hatHost: true}, "0 %"},
		{statistikZeile{hatHost: true, hostS: 40, anteil: 0.0017}, "unter 1 %"},
		{statistikZeile{hatHost: true, hostS: 1, anteil: 0.4}, "40 %"},
		{statistikZeile{hatHost: true, hostS: 1, anteil: 1}, "100 %"},
		{statistikZeile{hatHost: true, hostS: 1, anteil: 0.696}, "70 %"},
	} {
		if got := statistikProzent(tc.z); got != tc.want {
			t.Errorf("statistikProzent(%+v) = %q, erwartet %q", tc.z, got, tc.want)
		}
	}
}

// Der Umschalter wechselt zwischen Gesamt und 30 Tagen, auch für Mitglieder
// ohne Admin-Recht; der Reiter bleibt für sie sichtbar (kein Rückfall auf Start).
func TestStatistikUmschalter(t *testing.T) {
	jetzt := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	z := beispielZustand(jetzt, false, true) // kein Admin
	b := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z, statistik: beispielStatistik(jetzt), statistikAb: jetzt}, reiterStatistik)
	b.bild()
	if b.u.reiter != reiterStatistik {
		t.Fatalf("Reiter: %v, erwartet Statistik (auch ohne Admin)", b.u.reiter)
	}
	if b.u.stat.zeitraum != zeitraumGesamt {
		t.Fatalf("Zeitraum zu Beginn: %d", b.u.stat.zeitraum)
	}
	b.u.stat.tage30.Click()
	b.bild()
	if b.u.stat.zeitraum != zeitraum30Tage {
		t.Fatalf("nach Klick auf „Letzte 30 Tage“: %d", b.u.stat.zeitraum)
	}
	b.u.stat.gesamt.Click()
	b.bild()
	if b.u.stat.zeitraum != zeitraumGesamt {
		t.Fatalf("nach Klick auf „Gesamt“: %d", b.u.stat.zeitraum)
	}
	if got := len(b.q.liste()); got != 0 {
		t.Errorf("Umschalten schickt Aktionen: %v", b.q.liste())
	}
	// Das Fenster bleibt auch dann heil, wenn ein Mitglied ohne Namen auftaucht.
	s := beispielStatistik(jetzt)
	s.Total.Members = append(s.Total.Members, api.MemberStats{})
	b2 := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z, statistik: s, statistikAb: jetzt}, reiterStatistik)
	b2.bild()
}

// statistikQuelle liefert eine Statistik oder einen Fehler und zählt Aufrufe.
type statistikQuelle struct {
	aktionsQuelle
	n   int
	st  *api.Stats
	err error
}

func (q *statistikQuelle) Statistik(context.Context) (*api.Stats, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.n++
	return q.st, q.err
}

func (q *statistikQuelle) Zustand(context.Context) (*fensterapi.Zustand, error) {
	return beispielZustand(time.Now(), false, true), nil
}

func (q *statistikQuelle) anzahl() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n
}

// Die Datenschicht holt die Statistik nur bei sichtbarem Reiter und nicht
// öfter als statistikTakt; ein Fehler (älterer Koordinator) bleibt als Text
// stehen und verschwindet mit dem nächsten Erfolg.
func TestDatenStatistik(t *testing.T) {
	if statistikTakt != 30*time.Second {
		t.Fatalf("statistikTakt = %v", statistikTakt)
	}
	jetzt := time.Now()
	q := &statistikQuelle{err: context.DeadlineExceeded}
	d := neueDaten(nil, nil)
	d.statistikTakt = time.Hour
	d.c = q
	d.s = schnappschuss{verb: verbunden, runde: 1, z: beispielZustand(jetzt, false, true)}
	ctx := t.Context()

	d.setzeReiter(reiterStart)
	d.runde(ctx)
	if q.anzahl() != 0 {
		t.Fatalf("Statistik auf Reiter Start geholt")
	}
	d.setzeReiter(reiterStatistik)
	d.runde(ctx)
	d.runde(ctx) // zu früh für die zweite Abfrage
	if q.anzahl() != 1 {
		t.Fatalf("Abfragen: %d, erwartet 1 (Takt)", q.anzahl())
	}
	if s := d.lesen(); s.statistik != nil || s.statistikErr == "" {
		t.Fatalf("Fehler nicht im Schnappschuss: %+v", s)
	}
	// Fällig: nächster Versuch gelingt, der Fehlertext verschwindet.
	q.mu.Lock()
	q.err, q.st = nil, beispielStatistik(jetzt)
	q.mu.Unlock()
	d.mu.Lock()
	d.s.statistikAb = jetzt.Add(-2 * time.Hour)
	d.mu.Unlock()
	d.runde(ctx)
	if s := d.lesen(); s.statistik == nil || s.statistikErr != "" || len(s.statistik.Total.Members) != 5 {
		t.Fatalf("Statistik nicht im Schnappschuss: %+v", s)
	}
	// Auch Admins und Mitglieder auf anderen Reitern holen nichts.
	d.setzeReiter(reiterMitglieder)
	d.runde(ctx)
	if q.anzahl() != 2 {
		t.Errorf("Abfragen: %d, erwartet 2", q.anzahl())
	}
}
