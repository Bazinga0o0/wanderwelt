package gesundheit

import (
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

const gib = int64(1) << 30

var jetzt = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// gesund ist eine Eingabe ohne jede Warnung.
func gesund() Eingabe {
	return Eingabe{
		Now:         jetzt,
		FreiBekannt: true, Frei: 60 * gib, Gesamt: 100 * gib,
		SicherungBekannt: true, Sicherung: jetzt.Add(-time.Hour),
		ManifestStarter: "0.2.0",
		Starters:        []Starter{{Name: "Alice", Version: "0.2.0"}},
		Host:            &Host{Name: "Alice", LastBeat: jetzt.Add(-5 * time.Second)},
	}
}

func hat(w []api.HealthWarning, level, teil string) bool {
	for _, x := range w {
		if x.Level == level && strings.Contains(x.Text, teil) {
			return true
		}
	}
	return false
}

func TestKeineWarnungen(t *testing.T) {
	w := Warnungen(gesund())
	if len(w) != 0 || w == nil {
		t.Fatalf("Warnungen %#v (erwartet leere, nicht-nil Liste für JSON [])", w)
	}
	if Ampel(w) != api.HealthOK {
		t.Errorf("Ampel %q", Ampel(w))
	}
}

// Grenzfälle des Speicherplatzes: genau 15 % und genau 5 GB sind noch in
// Ordnung, knapp darunter nicht; unter 5 GB ist es ein Fehler, nur unter
// 15 % eine Warnung; ohne Gesamtgröße gilt nur die Byte-Schwelle.
func TestSpeicherplatzGrenzen(t *testing.T) {
	for _, tc := range []struct {
		name         string
		frei, gesamt int64
		bekannt      bool
		level        string // "" keine Warnung
	}{
		{"genau 15 %", 15 * gib, 100 * gib, true, ""},
		{"knapp unter 15 %", 15*gib - 1, 100 * gib, true, api.WarnWarnung},
		{"14 %", 14 * gib, 100 * gib, true, api.WarnWarnung},
		{"genau 5 GB, aber unter 15 %", 5 * gib, 100 * gib, true, api.WarnWarnung},
		{"knapp unter 5 GB", 5*gib - 1, 100 * gib, true, api.WarnFehler},
		{"0 frei", 0, 100 * gib, true, api.WarnFehler},
		{"kleine Platte: 4 GB von 20", 4 * gib, 20 * gib, true, api.WarnFehler},
		{"ohne Gesamtgröße, 6 GB", 6 * gib, 0, true, ""},
		{"ohne Gesamtgröße, 1 GB", 1 * gib, 0, true, api.WarnFehler},
		{"unbekannt", 0, 0, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := gesund()
			in.FreiBekannt, in.Frei, in.Gesamt = tc.bekannt, tc.frei, tc.gesamt
			w := Warnungen(in)
			if tc.level == "" {
				if len(w) != 0 {
					t.Fatalf("Warnungen %+v", w)
				}
				return
			}
			if len(w) != 1 || w[0].Level != tc.level {
				t.Fatalf("Warnungen %+v, want eine der Stufe %s", w, tc.level)
			}
		})
	}
}

func TestSpeicherplatzText(t *testing.T) {
	in := gesund()
	in.Frei, in.Gesamt = 12*gib, 100*gib
	w := Warnungen(in)
	if len(w) != 1 || !strings.Contains(w[0].Text, "12 %") || !strings.Contains(w[0].Text, "12,0 GB von 100,0 GB") {
		t.Fatalf("%+v", w)
	}
}

// Sicherung: genau 7 Tage sind in Ordnung, eine Sekunde mehr warnt;
// unbekannt warnt auch.
func TestSicherungGrenzen(t *testing.T) {
	in := gesund()
	in.Sicherung = jetzt.Add(-7 * 24 * time.Hour)
	if w := Warnungen(in); len(w) != 0 {
		t.Errorf("genau 7 Tage: %+v", w)
	}
	in.Sicherung = jetzt.Add(-7*24*time.Hour - time.Second)
	if w := Warnungen(in); len(w) != 1 || w[0].Level != api.WarnWarnung || !strings.Contains(w[0].Text, "7 Tage alt") {
		t.Errorf("7 Tage + 1 s: %+v", w)
	}
	in.Sicherung = jetzt.Add(-30 * 24 * time.Hour)
	if w := Warnungen(in); len(w) != 1 || !strings.Contains(w[0].Text, "30 Tage alt") {
		t.Errorf("30 Tage: %+v", w)
	}
	in.SicherungBekannt = false
	w := Warnungen(in)
	if len(w) != 1 || w[0].Level != api.WarnWarnung || !strings.Contains(w[0].Text, "unbekannt") {
		t.Errorf("unbekannt: %+v", w)
	}
	// Eine Sicherung „aus der Zukunft“ (Uhr) warnt nicht.
	in.SicherungBekannt, in.Sicherung = true, jetzt.Add(time.Hour)
	if w := Warnungen(in); len(w) != 0 {
		t.Errorf("Zukunft: %+v", w)
	}
}

// Host: bis 25 s nichts, darüber Warnung, über 2 Minuten Fehler; kein
// Zuschlag: nichts.
func TestHostLebenszeichen(t *testing.T) {
	for _, tc := range []struct {
		ohne  time.Duration
		level string
	}{
		{0, ""},
		{25 * time.Second, ""},
		{25*time.Second + time.Millisecond, api.WarnWarnung},
		{2 * time.Minute, api.WarnWarnung},
		{2*time.Minute + time.Millisecond, api.WarnFehler},
		{10 * time.Minute, api.WarnFehler},
	} {
		in := gesund()
		in.Host = &Host{Name: "Alice", LastBeat: jetzt.Add(-tc.ohne)}
		w := Warnungen(in)
		if tc.level == "" {
			if len(w) != 0 {
				t.Errorf("%v: %+v", tc.ohne, w)
			}
			continue
		}
		if len(w) != 1 || w[0].Level != tc.level || !strings.Contains(w[0].Text, "Alice") || !strings.Contains(w[0].Text, "Lebenszeichen") {
			t.Errorf("%v: %+v, want %s", tc.ohne, w, tc.level)
		}
	}
	in := gesund()
	in.Host = nil
	if w := Warnungen(in); len(w) != 0 {
		t.Errorf("ohne Host: %+v", w)
	}
	in.Host = &Host{LastBeat: jetzt.Add(-5 * time.Minute)}
	if w := Warnungen(in); len(w) != 1 || !strings.Contains(w[0].Text, "ein PC") {
		t.Errorf("Host ohne Namen: %+v", w)
	}
}

// Starter-Versionen: nur Release-Versionen unter der im Manifest zählen;
// „dev“, Commit-Kurzformen, gleiche und neuere nicht; ohne Manifest-Version
// nie.
func TestStarterVersionen(t *testing.T) {
	in := gesund()
	in.ManifestStarter = "v0.2.0"
	in.Starters = []Starter{
		{"Alice", "0.2.0"},
		{"Bob", "0.1.9"},
		{"Carol", "v0.1.5"},
		{"Dora", "0.10.0"},
		{"Emil", "dev"},
		{"", "0.0.1"},
		{"Fred", "8dbdcad"},
		{"Gina", "0.3.0"},
	}
	w := Warnungen(in)
	if len(w) != 1 || w[0].Level != api.WarnInfo {
		t.Fatalf("%+v", w)
	}
	for _, want := range []string{"Bob (0.1.9)", "Carol (0.1.5)", "ein PC (0.0.1)", "(0.2.0)"} {
		if !strings.Contains(w[0].Text, want) {
			t.Errorf("%q fehlt in %q", want, w[0].Text)
		}
	}
	for _, not := range []string{"Alice", "Dora", "Emil", "Fred", "Gina"} {
		if strings.Contains(w[0].Text, not) {
			t.Errorf("%q gehört nicht in %q", not, w[0].Text)
		}
	}
	for _, m := range []string{"", "dev", "0.2.0-rc.1"} {
		in.ManifestStarter = m
		if w := Warnungen(in); len(w) != 0 {
			t.Errorf("Manifest %q: %+v", m, w)
		}
	}
}

func TestBeitrittsanfragen(t *testing.T) {
	in := gesund()
	if w := Warnungen(in); len(w) != 0 {
		t.Fatal(w)
	}
	in.OffeneBeitritte = 1
	w := Warnungen(in)
	if len(w) != 1 || w[0].Level != api.WarnInfo || !strings.Contains(w[0].Text, "Eine offene Beitrittsanfrage") {
		t.Errorf("eine: %+v", w)
	}
	in.OffeneBeitritte = 3
	w = Warnungen(in)
	if len(w) != 1 || !strings.Contains(w[0].Text, "3 offene Beitrittsanfragen") {
		t.Errorf("drei: %+v", w)
	}
	if Ampel(w) != api.HealthOK {
		t.Errorf("info färbt die Ampel: %q", Ampel(w))
	}
}

func TestAufraeumenUndBudget(t *testing.T) {
	in := gesund()
	in.AufraeumenFehler = "Rezept nicht lesbar"
	in.UeberBudget = true
	w := Warnungen(in)
	if !hat(w, api.WarnFehler, "Aufräumen") || !hat(w, api.WarnWarnung, "Budget") {
		t.Fatalf("%+v", w)
	}
}

// Die Liste ist nach Stufe geordnet (Fehler zuerst), die Ampel die höchste
// Stufe.
func TestSortierungUndAmpel(t *testing.T) {
	in := gesund()
	in.OffeneBeitritte = 2                   // info
	in.SicherungBekannt = false              // warnung
	in.Frei = 1 * gib                        // fehler
	in.Host.LastBeat = jetzt.Add(-time.Hour) // fehler
	w := Warnungen(in)
	var stufen []string
	for _, x := range w {
		stufen = append(stufen, x.Level)
	}
	if got := strings.Join(stufen, " "); got != "fehler fehler warnung info" {
		t.Fatalf("Reihenfolge %q: %+v", got, w)
	}
	if Ampel(w) != api.HealthFehler {
		t.Errorf("Ampel %q", Ampel(w))
	}
	if Ampel(w[2:]) != api.HealthWarnung || Ampel(w[3:]) != api.HealthOK || Ampel(nil) != api.HealthOK {
		t.Errorf("Ampel-Stufen: %q %q %q", Ampel(w[2:]), Ampel(w[3:]), Ampel(nil))
	}
}

func TestFormate(t *testing.T) {
	for in, want := range map[int64]string{0: "0 KB", 1500: "1 KB", 5 << 20: "5 MB", 3 << 30: "3,0 GB", 5<<30 + 1<<29: "5,5 GB", 2 << 40: "2,0 TB"} {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{45 * time.Second: "45 s", 3 * time.Minute: "3 Min.", 5 * time.Hour: "5 Std."} {
		if got := Dauer(in); got != want {
			t.Errorf("Dauer(%v) = %q, want %q", in, got, want)
		}
	}
}
