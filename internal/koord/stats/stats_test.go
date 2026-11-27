package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

const (
	alice = "alice@example.org"
	bob   = "bob@example.org"
)

func neu(t *testing.T) (*Tracker, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stats.json")
	tr, err := Open(p, t0)
	if err != nil {
		t.Fatal(err)
	}
	return tr, p
}

func total(tr *Tracker, now time.Time, m string) Totals {
	return tr.Report(now).Total[m]
}

// Lebenszeichen alle 10 s: Hostzeit für den Host, Spielzeit für alle, die
// dabei waren; die erste Sitzung zählt einmal.
func TestZaehlenImTakt(t *testing.T) {
	tr, _ := neu(t)
	for i := range 7 { // 0 bis 60 s
		tr.Sample(t0.Add(time.Duration(i)*10*time.Second), 1, alice, []string{alice, bob})
	}
	a, b := total(tr, t0, alice), total(tr, t0, bob)
	if a.HostMS != 60_000 || a.PlayMS != 60_000 || a.Sessions != 1 {
		t.Errorf("alice: %+v", a)
	}
	if b.HostMS != 0 || b.PlayMS != 60_000 || b.Sessions != 0 {
		t.Errorf("bob: %+v", b)
	}
}

// Beitritt und Austritt: Wer nur in einem von zwei Lebenszeichen steht,
// bekommt die halbe Lücke (Trapezregel); Doppelte und Schreibweisen zählen
// einmal, leere Namen nie.
func TestBeitrittAustritt(t *testing.T) {
	tr, _ := neu(t)
	tr.Sample(t0, 1, alice, []string{alice})
	tr.Sample(t0.Add(10*time.Second), 1, alice, []string{alice, "BOB@example.org", bob}) // Bob kommt
	tr.Sample(t0.Add(20*time.Second), 1, alice, []string{alice, bob})
	tr.Sample(t0.Add(30*time.Second), 1, alice, []string{alice}) // Bob geht
	tr.Sample(t0.Add(40*time.Second), 1, alice, []string{alice})
	if got := total(tr, t0, bob).PlayMS; got != 5_000+10_000+5_000 {
		t.Errorf("Bob: %d ms, erwartet 20000", got)
	}
	if got := total(tr, t0, alice).PlayMS; got != 40_000 {
		t.Errorf("Alice: %d ms, erwartet 40000", got)
	}
	tr.Sample(t0.Add(50*time.Second), 1, alice, []string{"", alice})
	if _, ok := tr.Report(t0).Total[""]; ok {
		t.Error("leerer Schlüssel gezählt")
	}
}

// Stirbt der Host, zählt nichts nach seinem letzten Lebenszeichen; ein
// großer Abstand zählt gar nicht, danach läuft die Zählung weiter.
func TestHostAusfallKeinePhantomzeit(t *testing.T) {
	tr, _ := neu(t)
	tr.Sample(t0, 1, alice, []string{bob})
	tr.Sample(t0.Add(10*time.Second), 1, alice, []string{bob})
	// Danach kommt nichts mehr: Auswertung Stunden später ändert nichts.
	later := t0.Add(5 * time.Hour)
	if a := total(tr, later, alice); a.HostMS != 10_000 {
		t.Errorf("Hostzeit nach Ausfall: %d ms, erwartet 10000", a.HostMS)
	}
	// Derselbe Zuschlag meldet sich nach 80 s wieder (Netz weg): Lücke zählt nicht.
	tr.Sample(t0.Add(90*time.Second), 1, alice, []string{bob})
	tr.Sample(t0.Add(100*time.Second), 1, alice, []string{bob})
	if a, b := total(tr, later, alice), total(tr, later, bob); a.HostMS != 20_000 || b.PlayMS != 20_000 {
		t.Errorf("nach Lücke: host %d ms play %d ms, erwartet je 20000", a.HostMS, b.PlayMS)
	}
	// Genau MaxGap zählt noch, eine Sekunde mehr nicht.
	tr.Sample(t0.Add(100*time.Second+MaxGap), 1, alice, nil)
	tr.Sample(t0.Add(101*time.Second+2*MaxGap), 1, alice, nil)
	if a := total(tr, later, alice); a.HostMS != 20_000+MaxGap.Milliseconds() {
		t.Errorf("Grenze: %d ms", a.HostMS)
	}
}

// Übergabe: Zwei Zuschläge nacheinander (der wartende wird nicht gemeldet):
// Jede Sitzung zählt einmal, die Hostzeit geht an den jeweiligen Host, nie
// doppelt; zwischen den Sitzungen wird nichts hinübergerechnet.
func TestUebergabeNichtDoppelt(t *testing.T) {
	tr, _ := neu(t)
	for i := range 4 { // 0 bis 30 s Alice
		tr.Sample(t0.Add(time.Duration(i)*10*time.Second), 1, alice, []string{alice, bob})
	}
	// Bobs Zuschlag wird befördert; sein erstes Lebenszeichen als Haupt-Zuschlag.
	for i := range 4 { // 35 bis 65 s Bob
		tr.Sample(t0.Add(35*time.Second+time.Duration(i)*10*time.Second), 2, bob, []string{alice, bob})
	}
	a, b := total(tr, t0, alice), total(tr, t0, bob)
	if a.HostMS != 30_000 || a.Sessions != 1 || b.HostMS != 30_000 || b.Sessions != 1 {
		t.Errorf("alice %+v bob %+v", a, b)
	}
	// Spielzeit: nur innerhalb der Sitzungen (30 s + 30 s), nicht die Übergabelücke.
	if a.PlayMS != 60_000 || b.PlayMS != 60_000 {
		t.Errorf("Spielzeit alice %d bob %d, erwartet je 60000", a.PlayMS, b.PlayMS)
	}
}

// Neustart des Koordinators: Zahlen bleiben, dieselbe Sitzung zählt nicht
// noch einmal, die Zeit des Neustarts zählt nicht, danach läuft es weiter.
func TestNeustart(t *testing.T) {
	tr, p := neu(t)
	tr.Sample(t0, 1, alice, []string{bob})
	tr.Sample(t0.Add(10*time.Second), 1, alice, []string{bob})
	if err := tr.Flush(t0.Add(11 * time.Second)); err != nil {
		t.Fatal(err)
	}

	tr2, err := Open(p, t0.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if a := total(tr2, t0, alice); a.HostMS != 10_000 || a.Sessions != 1 {
		t.Fatalf("nach Neustart: %+v", a)
	}
	tr2.Sample(t0.Add(40*time.Second), 1, alice, []string{bob}) // gleicher Zuschlag
	tr2.Sample(t0.Add(50*time.Second), 1, alice, []string{bob})
	a, b := total(tr2, t0, alice), total(tr2, t0, bob)
	if a.Sessions != 1 || a.HostMS != 20_000 || b.PlayMS != 20_000 {
		t.Errorf("alice %+v bob %+v", a, b)
	}
	// Neuer Zuschlag nach dem Neustart ist eine neue Sitzung.
	tr2.Sample(t0.Add(60*time.Second), 2, bob, nil)
	if got := total(tr2, t0, bob).Sessions; got != 1 {
		t.Errorf("Sitzungen Bob: %d", got)
	}
	if !tr2.Report(t0).Since.Equal(t0) {
		t.Errorf("Since: %v", tr2.Report(t0).Since)
	}
}

// Geschrieben wird gedrosselt, Tick und Flush schreiben auf Wunsch; keine
// Temp-Reste.
func TestSpeichern(t *testing.T) {
	tr, p := neu(t)
	tr.Sample(t0.Add(time.Second), 1, alice, nil)
	if _, err := os.Stat(p); err == nil {
		t.Fatal("sofort geschrieben, erwartet gedrosselt")
	}
	tr.Sample(t0.Add(SaveEvery+time.Second), 1, alice, nil)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("nach SaveEvery nicht geschrieben: %v", err)
	}
	tr.Sample(t0.Add(SaveEvery+3*time.Second), 1, alice, nil) // geändert, aber gedrosselt
	tr.Sample(t0.Add(SaveEvery+13*time.Second), 1, alice, nil)
	read := func() file {
		data, _ := os.ReadFile(p)
		var f file
		if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
			t.Fatalf("Datei: %v %s", err, data)
		}
		return f
	}
	before := read().Days["2026-11-26"][alice].HostMS
	tr.Tick(t0.Add(SaveEvery + 20*time.Second)) // zu früh
	if got := read().Days["2026-11-26"][alice].HostMS; got != before {
		t.Errorf("Tick zu früh geschrieben")
	}
	tr.Tick(t0.Add(3 * SaveEvery))
	if got := read().Days["2026-11-26"][alice].HostMS; got <= before {
		t.Errorf("Tick schreibt nicht: %d nach %d", got, before)
	}
	es, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("Temp-Datei übrig: %s", e.Name())
		}
	}
}

// Kompaktierung: Tage älter als KeepDays gehen in die Summe; 30-Tage-Fenster
// und Gesamt stimmen, die Datei hält nur noch KeepDays Tage.
func TestKompaktierung(t *testing.T) {
	tr, p := neu(t)
	day := func(back int, secs int) {
		at := t0.AddDate(0, 0, -back)
		tr.Sample(at, int64(1000+back), alice, []string{bob})
		tr.Sample(at.Add(time.Duration(secs)*time.Second), int64(1000+back), alice, []string{bob})
	}
	day(100, 10) // alt: nur Summe
	day(60, 10)  // alt: nur Summe
	day(40, 20)  // einzeln, aber außerhalb 30 Tage
	day(30, 5)   // gerade raus
	day(29, 30)  // gerade noch im Fenster
	day(0, 10)   // heute
	if err := tr.Flush(t0); err != nil {
		t.Fatal(err)
	}
	tot, l30 := tr.Report(t0).Total[alice], tr.Report(t0).Last30[alice]
	if tot.HostMS != 85_000 || tot.Sessions != 6 {
		t.Errorf("gesamt: %+v", tot)
	}
	if l30.HostMS != 40_000 || l30.Sessions != 2 {
		t.Errorf("30 Tage: %+v, erwartet 40000 ms und 2 Sitzungen (Tag -29 und heute)", l30)
	}
	data, _ := os.ReadFile(p)
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Days) != 4 { // -40, -30, -29, 0
		t.Errorf("Tage in der Datei: %d", len(f.Days))
	}
	if f.Older[alice].HostMS != 20_000 || f.Older[alice].Sessions != 2 {
		t.Errorf("Older: %+v", f.Older[alice])
	}
	// Auch beim Laden wird kompaktiert: In zehn Tagen fällt Tag -40 raus.
	spaeter := t0.AddDate(0, 0, 10)
	tr2, err := Open(p, spaeter)
	if err != nil {
		t.Fatal(err)
	}
	if o := tr2.f.Older[alice]; o.HostMS != 40_000 {
		t.Errorf("Older nach Laden: %+v", o)
	}
	if got := tr2.Report(spaeter).Total[alice].HostMS; got != 85_000 {
		t.Errorf("gesamt nach Laden: %d", got)
	}
}

// Wachstum: ein Jahr mit zehn Mitgliedern bleibt klein.
func TestDateiBleibtKlein(t *testing.T) {
	tr, p := neu(t)
	var ps []string
	for i := range 10 {
		ps = append(ps, "mitglied"+string(rune('a'+i))+"@example.org")
	}
	for d := 365; d >= 0; d-- {
		at := t0.AddDate(0, 0, -d)
		tr.Sample(at, int64(1000-d), ps[d%10], ps)
		tr.Sample(at.Add(10*time.Second), int64(1000-d), ps[d%10], ps)
	}
	if err := tr.Flush(t0); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Size() > 100_000 {
		t.Errorf("stats.json: %d Bytes", st.Size())
	}
	if len(tr.f.Days) > KeepDays {
		t.Errorf("Tage: %d", len(tr.f.Days))
	}
}

// Kaputte Datei: beiseitegelegt, Zählung beginnt leer.
func TestKaputteDatei(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stats.json")
	os.WriteFile(p, []byte("{kaputt"), 0o600)
	tr, err := Open(p, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Report(t0).Total) != 0 {
		t.Error("nicht leer")
	}
	es, _ := os.ReadDir(dir)
	if len(es) != 1 || !strings.HasPrefix(es[0].Name(), "stats.json.kaputt-") {
		t.Errorf("Dateien: %v", es)
	}
}

// Ohne Pfad zählt der Tracker nur im Speicher.
func TestOhnePfad(t *testing.T) {
	tr, err := Open("", t0)
	if err != nil {
		t.Fatal(err)
	}
	tr.Sample(t0, 1, alice, nil)
	tr.Sample(t0.Add(10*time.Second), 1, alice, nil)
	if err := tr.Flush(t0); err != nil {
		t.Fatal(err)
	}
	if total(tr, t0, alice).HostMS != 10_000 {
		t.Error("nicht gezählt")
	}
}

// Uhr springt zurück oder zwei Lebenszeichen zur selben Zeit: nichts Negatives.
func TestUhrSprung(t *testing.T) {
	tr, _ := neu(t)
	tr.Sample(t0.Add(10*time.Second), 1, alice, []string{bob})
	tr.Sample(t0, 1, alice, []string{bob})
	tr.Sample(t0, 1, alice, []string{bob})
	if a := total(tr, t0, alice); a.HostMS != 0 || a.PlayMS != 0 {
		t.Errorf("%+v", a)
	}
}
