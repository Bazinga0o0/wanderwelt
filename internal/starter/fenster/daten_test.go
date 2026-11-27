package fenster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// warteAuf prüft cond alle 5 ms bis frist.
func warteAuf(t *testing.T, frist time.Duration, was string, cond func() bool) {
	t.Helper()
	ende := time.Now().Add(frist)
	for !cond() {
		if time.Now().After(ende) {
			t.Fatalf("nach %v nicht erreicht: %s", frist, was)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDatenOhneInstanz: Ohne control.json startet das Fenster die Instanz
// einmal, wartet startWarten und zeigt dann „läuft nicht“ – ohne
// Abbruch: Die Abfrage läuft weiter, und eine später erscheinende Instanz
// wird gefunden. Endet sie, steht wieder „läuft nicht“ da, ohne erneutes
// Starten.
func TestDatenOhneInstanz(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)

	var starts atomic.Int32
	var gemalt atomic.Int32
	d := neueDaten(func() error { starts.Add(1); return nil }, func() { gemalt.Add(1) })
	d.takt, d.startWarten = 10*time.Millisecond, 150*time.Millisecond
	d.autoStart = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ende := make(chan struct{})
	go func() { d.lauf(ctx); close(ende) }()

	warteAuf(t, time.Second, "Instanz gestartet", func() bool { return starts.Load() == 1 })
	if v := d.lesen().verb; v != startet && v != laeuftNicht {
		t.Fatalf("nach dem Starten: verb %v, want startet", v)
	}
	warteAuf(t, 2*time.Second, "läuft nicht", func() bool { return d.lesen().verb == laeuftNicht })
	if s := d.lesen(); s.startFehl == "" || s.z != nil {
		t.Errorf("läuft nicht: startFehl %q, z %v", s.startFehl, s.z)
	}
	time.Sleep(100 * time.Millisecond) // weitere Runden
	if n := starts.Load(); n != 1 {
		t.Errorf("Instanz %d-mal gestartet, want 1", n)
	}
	if gemalt.Load() == 0 {
		t.Errorf("Fenster nie neu gezeichnet")
	}
	select {
	case <-ende:
		t.Fatal("Abfrage beendet sich ohne Instanz")
	default:
	}

	// Kaputte control.json: weiter „läuft nicht“, kein Abbruch.
	os.WriteFile(filepath.Join(home, "control.json"), []byte("{kaputt"), 0o600)
	time.Sleep(50 * time.Millisecond)
	if v := d.lesen().verb; v != laeuftNicht {
		t.Fatalf("kaputte control.json: verb %v", v)
	}

	// „Starten“ von Hand: sofort „startet“; ein zweiter Klick startet nicht
	// noch einmal. Dann die Attrappe als Instanz.
	d.starten()
	if v := d.lesen().verb; v != startet {
		t.Fatalf("nach Starten: verb %v", v)
	}
	d.starten()
	warteAuf(t, time.Second, "zweiter Start", func() bool { return starts.Load() >= 2 })
	time.Sleep(30 * time.Millisecond)
	if n := starts.Load(); n != 2 {
		t.Fatalf("Doppelklick auf Starten: %d Starts, want 2 (automatisch + einmal von Hand)", n)
	}
	a := starteAttrappe(t, home)
	warteAuf(t, 2*time.Second, "verbunden", func() bool { s := d.lesen(); return s.verb == verbunden && s.z != nil })
	if s := d.lesen(); s.startFehl != "" || s.z.Version != "0.9.3" {
		t.Errorf("verbunden: startFehl %q, Version %q", s.startFehl, s.z.Version)
	}

	// Instanz endet bei offenem Fenster.
	a.srv.Close()
	warteAuf(t, 2*time.Second, "läuft nicht nach Ende der Instanz", func() bool { return d.lesen().verb == laeuftNicht })
	if s := d.lesen(); s.z != nil || s.startFehl != "" {
		t.Errorf("nach Ende: z %v, startFehl %q (alte Daten oder falscher Fehler)", s.z, s.startFehl)
	}
	time.Sleep(50 * time.Millisecond)
	if n := starts.Load(); n != 2 {
		t.Errorf("Instanz %d-mal gestartet, want 2 (einmal automatisch, einmal von Hand)", n)
	}

	// Eine Aktion ohne Instanz meldet ErrLaeuftNicht, ohne Panik.
	var aktErr error
	d.aktion("play", nil, func(err error) { aktErr = err })
	d.abarbeiten()
	if !errors.Is(aktErr, fensterapi.ErrLaeuftNicht) {
		t.Errorf("Aktion ohne Instanz: %v", aktErr)
	}

	cancel()
	select {
	case <-ende:
	case <-time.After(time.Second):
		t.Fatal("Abfrage endet nicht mit dem Kontext")
	}
}

// TestDatenPolling: Zustand im festen Takt (1,5 s, im Test verkürzt);
// Stände nur bei sichtbarem Reiter „Welt“ und höchstens alle staendeTakt;
// Log, Mitglieder und Speicherstände nur, wo sie gebraucht werden.
func TestDatenPolling(t *testing.T) {
	if zustandTakt != 1500*time.Millisecond || staendeTakt != 30*time.Second {
		t.Fatalf("Takte %v/%v, want 1,5 s/30 s", zustandTakt, staendeTakt)
	}
	if d := neueDaten(nil, nil); d.takt != zustandTakt || d.staendeTakt != staendeTakt {
		t.Fatalf("neueDaten: Takte %v/%v", d.takt, d.staendeTakt)
	}

	home := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)
	a := starteAttrappe(t, home)

	d := neueDaten(func() error { t.Error("Instanz gestartet, obwohl sie läuft"); return nil }, nil)
	const takt = 40 * time.Millisecond
	d.takt = takt
	d.autoStart = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.lauf(ctx)

	time.Sleep(10*takt + takt/2)
	n := a.anzahl("/v1/zustand")
	if n < 8 || n > 13 {
		t.Errorf("Zustand %d-mal in 10,5 Takten, want etwa 11", n)
	}
	for _, p := range []string{"/v1/staende", "/v1/log", "/v1/mitglieder", "/v1/speicherstaende"} {
		if c := a.anzahl(p); c != 0 {
			t.Errorf("Reiter Start: %s %d-mal geholt", p, c)
		}
	}

	// Reiter Welt: Stände sofort, dann nicht wieder vor staendeTakt (30 s).
	d.setzeReiter(reiterWelt)
	warteAuf(t, time.Second, "Stände geholt", func() bool { return a.anzahl("/v1/staende") == 1 })
	time.Sleep(5 * takt)
	d.setzeReiter(reiterStart)
	time.Sleep(2 * takt)
	d.setzeReiter(reiterWelt)
	time.Sleep(3 * takt)
	if c := a.anzahl("/v1/staende"); c != 1 {
		t.Errorf("Stände %d-mal in weniger als 30 s, want 1", c)
	}
	if s := d.lesen(); s.staende == nil || len(s.staende.Worlds) != 2 {
		t.Errorf("Stände nicht im Schnappschuss: %+v", s.staende)
	}
	// Speicherstände erst, wenn „Welt übernehmen“ aufgeklappt ist.
	if c := a.anzahl("/v1/speicherstaende"); c != 0 {
		t.Errorf("Speicherstände ohne Aufklappen %d-mal geholt", c)
	}
	d.setzeSavesWunsch(true)
	warteAuf(t, time.Second, "Speicherstände geholt", func() bool { return a.anzahl("/v1/speicherstaende") == 1 })

	// Nach einer Aktion (Zurückholen) wird die Liste neu geholt.
	d.neuLaden(true, false, false)
	warteAuf(t, time.Second, "Stände neu geholt", func() bool { return a.anzahl("/v1/staende") == 2 })

	// Kürzerer Stände-Takt: regelmäßig, solange Welt sichtbar ist.
	d.mu.Lock()
	d.staendeTakt = 3 * takt
	d.mu.Unlock()
	vor := a.anzahl("/v1/staende")
	time.Sleep(10 * takt)
	if c := a.anzahl("/v1/staende") - vor; c < 2 || c > 5 {
		t.Errorf("Stände bei Takt 3: %d-mal in 10 Takten", c)
	}
	d.setzeReiter(reiterStart)
	time.Sleep(2 * takt)
	vor = a.anzahl("/v1/staende")
	time.Sleep(8 * takt)
	if c := a.anzahl("/v1/staende") - vor; c != 0 {
		t.Errorf("Stände auf Reiter Start %d-mal geholt", c)
	}

	// Log nur auf dem Reiter Log (300 Zeilen), Mitglieder nur auf ihrem.
	d.setzeReiter(reiterLog)
	// Auf den Schnappschuss warten, nicht nur auf die Anfrage (sonst ein Wettlauf).
	warteAuf(t, time.Second, "Log geholt", func() bool { return a.anzahl("/v1/log") >= 1 && d.lesen().log != "" })
	if s := d.lesen(); !strings.Contains(s.log, "Tray bereit") {
		t.Errorf("Log nicht im Schnappschuss: %q (Fehler %q)", s.log, s.logErr)
	}
	if c := a.anzahl("/v1/mitglieder"); c != 0 {
		t.Errorf("Mitglieder auf Reiter Log %d-mal geholt", c)
	}
	d.setzeReiter(reiterMitglieder)
	warteAuf(t, time.Second, "Mitglieder geholt", func() bool { return a.anzahl("/v1/mitglieder") >= 1 })

	// Minimiert: Die Abfrage ruht.
	var mini atomic.Bool
	d.mu.Lock()
	d.minimiert = mini.Load
	d.mu.Unlock()
	mini.Store(true)
	time.Sleep(2 * takt)
	vor = a.anzahl("/v1/zustand")
	time.Sleep(6 * takt)
	if c := a.anzahl("/v1/zustand") - vor; c != 0 {
		t.Errorf("minimiert: Zustand %d-mal geholt", c)
	}
	mini.Store(false)
	d.gezeichnet()
	warteAuf(t, time.Second, "Abfrage nach dem Wiederherstellen", func() bool { return a.anzahl("/v1/zustand") > vor })

	// Aktion: Körper und Fehlertext kommen an.
	a.mu.Lock()
	a.fehler["rolle"] = "Lukas ist der einzige Admin."
	a.mu.Unlock()
	fertig := make(chan error, 2)
	d.aktion("zurueckholen", map[string]any{"world_id": "w", "number": 3}, func(err error) { fertig <- err })
	d.aktion("rolle", rolleAuftrag{Email: "lukas@example.de"}, func(err error) { fertig <- err })
	warteAuf(t, 2*time.Second, "Aktionen fertig", func() bool {
		d.abarbeiten()
		return len(fertig) == 2
	})
	var fehler []string
	for range 2 {
		if err := <-fertig; err != nil {
			fehler = append(fehler, err.Error())
		}
	}
	if len(fehler) != 1 || fehler[0] != "Lukas ist der einzige Admin." {
		t.Errorf("Fehler der Aktionen: %q", fehler)
	}
	a.mu.Lock()
	akt := strings.Join(a.aktionen, "\n")
	a.mu.Unlock()
	if !strings.Contains(akt, `zurueckholen {"number":3,"world_id":"w"}`) || !strings.Contains(akt, `rolle {"email":"lukas@example.de","admin":false,"op":false}`) {
		t.Errorf("Aktionen: %s", akt)
	}
}

// TestDatenOhneBild: Zeichnet die Oberfläche nach einem Neuzeichnen-Wunsch
// bildFrist lang kein Bild (sie hängt), ruht die Abfrage; das nächste Bild
// setzt sie fort.
func TestDatenOhneBild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)
	a := starteAttrappe(t, home)
	var wuensche atomic.Int32
	d := neueDaten(func() error { return nil }, func() { wuensche.Add(1) })
	const takt = 20 * time.Millisecond
	d.takt, d.bildFrist = takt, 10*takt
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.lauf(ctx)

	warteAuf(t, time.Second, "Neuzeichnen gewünscht", func() bool { return wuensche.Load() > 0 })
	time.Sleep(d.bildFrist + 3*takt)
	vor := a.anzahl("/v1/zustand")
	time.Sleep(8 * takt)
	if n := a.anzahl("/v1/zustand") - vor; n != 0 {
		t.Fatalf("ohne Bild weiter abgefragt: %d", n)
	}
	d.gezeichnet()
	warteAuf(t, time.Second, "Abfrage nach dem Bild", func() bool { return a.anzahl("/v1/zustand") > vor })
	// Mit Bildern nach jedem Wunsch läuft sie weiter.
	d.mu.Lock()
	d.neuZeichnen = func() { go d.gezeichnet() }
	d.mu.Unlock()
	vor = a.anzahl("/v1/zustand")
	time.Sleep(d.bildFrist + 5*takt)
	if n := a.anzahl("/v1/zustand") - vor; n < 8 {
		t.Fatalf("mit Bildern nur %d Abfragen", n)
	}
}
