package fenster

import (
	"context"
	"errors"
	"testing"
	"time"
)

// berichtQuelle antwortet auf „fehlerbericht“ erst, wenn gate geschlossen
// wird (nil: sofort), mit err.
type berichtQuelle struct {
	*aktionsQuelle
	gate chan struct{}
	err  error
}

func (q *berichtQuelle) Aktion(ctx context.Context, name string, body any) error {
	q.aktionsQuelle.Aktion(ctx, name, body)
	if q.gate != nil {
		<-q.gate
	}
	return q.err
}

// Der Knopf „Fehlerbericht erstellen“ im Reiter Log schickt die Aktion genau
// einmal (auch bei mehrfachem Klicken, solange sie läuft), zeigt danach den
// Hinweis, und ein Fehler der Instanz erscheint als Fehlertext.
func TestFehlerberichtKnopf(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	z := beispielZustand(jetzt, false, true)
	neu := func() (*bedienTest, *berichtQuelle) {
		b := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z, log: "zeile 1\n", logAb: jetzt}, reiterLog)
		q := &berichtQuelle{aktionsQuelle: b.q, gate: make(chan struct{})}
		b.u.d.c = q
		return b, q
	}

	b, q := neu()
	b.bild()
	b.u.log.bericht.Click()
	b.bild()
	if !b.u.log.berichtLaeuft {
		t.Fatal("Knopf läuft nicht an")
	}
	b.u.log.bericht.Click() // läuft noch: wird ignoriert
	b.bild()
	if got := b.warte(1); len(got) != 1 || got[0] != "fehlerbericht null" {
		t.Fatalf("Aktionen %q, erwartet genau einmal fehlerbericht", got)
	}
	close(q.gate)
	for i := 0; i < 200 && !b.u.log.berichtOK; i++ {
		time.Sleep(5 * time.Millisecond)
		b.bild()
	}
	if b.u.log.berichtLaeuft || !b.u.log.berichtOK || b.u.log.fehler != "" {
		t.Fatalf("nach dem Erstellen: läuft %v, ok %v, Fehler %q", b.u.log.berichtLaeuft, b.u.log.berichtOK, b.u.log.fehler)
	}
	if got := b.q.liste(); len(got) != 1 {
		t.Fatalf("Aktionen %q", got)
	}

	// Fehler der Instanz (409 „läuft schon“, Ziel nicht schreibbar …).
	b2, q2 := neu()
	q2.err = errors.New("Der Fehlerbericht wird gerade erstellt.")
	close(q2.gate)
	b2.bild()
	b2.u.log.bericht.Click()
	b2.bild()
	for i := 0; i < 200 && b2.u.log.fehler == ""; i++ {
		time.Sleep(5 * time.Millisecond)
		b2.bild()
	}
	if b2.u.log.berichtOK || b2.u.log.berichtLaeuft || b2.u.log.fehler != "Der Fehlerbericht wird gerade erstellt." {
		t.Fatalf("nach dem Fehler: ok %v, läuft %v, Fehler %q", b2.u.log.berichtOK, b2.u.log.berichtLaeuft, b2.u.log.fehler)
	}
	// Danach geht ein neuer Versuch wieder.
	q2.err = nil
	b2.u.log.bericht.Click()
	b2.bild()
	for i := 0; i < 200 && !b2.u.log.berichtOK; i++ {
		time.Sleep(5 * time.Millisecond)
		b2.bild()
	}
	if !b2.u.log.berichtOK || b2.u.log.fehler != "" {
		t.Fatalf("zweiter Versuch: ok %v, Fehler %q", b2.u.log.berichtOK, b2.u.log.fehler)
	}
}
