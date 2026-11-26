package wartung

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// Zustandsmaschine: Aus → Ansage → Gestoppt, Frist 0 ist sofort Gestoppt,
// eine neue Frist ersetzt die alte, Since bleibt.
func TestPhasen(t *testing.T) {
	var z Zustand
	if z.Phase(t0) != Aus {
		t.Fatal("Nullwert muss Aus sein")
	}
	z, err := Starten(z, t0, 60*time.Second, "Update")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dt   time.Duration
		want Phase
		rest time.Duration
	}{
		{0, Ansage, 60 * time.Second},
		{59*time.Second + 500*time.Millisecond, Ansage, time.Second},
		{60 * time.Second, Gestoppt, 0},
		{time.Hour, Gestoppt, 0},
	} {
		if got := z.Phase(t0.Add(c.dt)); got != c.want {
			t.Errorf("+%v: Phase %v, erwartet %v", c.dt, got, c.want)
		}
		if got := z.Rest(t0.Add(c.dt)); got != c.rest {
			t.Errorf("+%v: Rest %v, erwartet %v", c.dt, got, c.rest)
		}
	}
	z2, _ := Starten(z, t0.Add(10*time.Second), 0, "")
	if z2.Phase(t0.Add(10*time.Second)) != Gestoppt || !z2.Since.Equal(t0) || z2.Grund != StandardGrund {
		t.Errorf("Neustart der Frist: %+v", z2)
	}
	if _, err := Starten(Zustand{}, t0, -time.Second, "x"); err == nil {
		t.Error("negative Frist angenommen")
	}
	if _, err := Starten(Zustand{}, t0, 0, strings.Repeat("ä", MaxGrund+1)); err == nil {
		t.Error("zu langer Grund angenommen")
	}
}

// Persistenz: Laden nach Speichern liefert dasselbe, ohne Datei „aus“,
// Speichern von „aus“ löscht, eine kaputte Datei ist ein Fehler.
func TestPersistenz(t *testing.T) {
	dir := t.TempDir()
	if z, err := Laden(dir); err != nil || z.Aktiv {
		t.Fatalf("ohne Datei: %+v, %v", z, err)
	}
	z, _ := Starten(Zustand{}, t0, 30*time.Second, "Umzug")
	if err := Speichern(dir, z); err != nil {
		t.Fatal(err)
	}
	got, err := Laden(dir)
	if err != nil || got != z {
		t.Fatalf("Laden: %+v, %v (erwartet %+v)", got, err, z)
	}
	if err := Speichern(dir, Zustand{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Laden(dir); got.Aktiv {
		t.Fatal("Datei nach „ende“ noch aktiv")
	}
	if err := Speichern(dir, Zustand{}); err != nil {
		t.Fatalf("zweites Löschen: %v", err)
	}
	if err := schreiben(Pfad(dir), "kaputt"); err != nil {
		t.Fatal(err)
	}
	if _, err := Laden(dir); err == nil {
		t.Fatal("kaputte Datei ohne Fehler gelesen")
	}
}

func TestBericht(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := BerichtLesen(dir); ok || err != nil {
		t.Fatal("Bericht ohne Datei")
	}
	b := Bericht{Time: t0, Since: t0, Zuschlag: "Alice", Uploads: 2}
	if err := BerichtSchreiben(dir, b); err != nil {
		t.Fatal(err)
	}
	got, ok, err := BerichtLesen(dir)
	if err != nil || !ok || !got.Time.Equal(t0) || got.Zuschlag != "Alice" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if h := got.Haengt(); !strings.Contains(h, "Alice hostet noch") || !strings.Contains(h, "2 Upload") {
		t.Errorf("Haengt: %q", h)
	}
}
