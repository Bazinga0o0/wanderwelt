package fenster

import (
	"context"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/op"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// beispielGesundheit ist eine Gesundheit mit allen Werten.
func beispielGesundheit(jetzt time.Time) *api.Health {
	return &api.Health{
		Version:    "0.1.5",
		Started:    jetzt.Add(-50 * time.Hour),
		UptimeS:    int64((50*time.Hour + 20*time.Minute) / time.Second),
		Disk:       &api.HealthDisk{FreeBytes: 12 << 30, TotalBytes: 80 << 30},
		StoreBytes: 3 << 30, StoreKnown: true,
		WorldID: "w-2026-11-03", HeadGeneration: 215, HeadCreated: jetzt.Add(-3 * time.Minute),
		Host:            &api.HealthHost{MinecraftName: "Lukas", MachineID: "pc-lukas-0123456789", LastBeat: jetzt.Add(-12 * time.Second)},
		Starters:        []api.HealthStarter{{MinecraftName: "Lukas", MachineID: "pc-l", Version: "0.1.5", Seen: jetzt.Add(-12 * time.Second)}, {MachineID: "pc-x", Version: "0.1.2", Seen: jetzt.Add(-2 * time.Hour)}},
		OpenJoins:       2,
		Backup:          api.HealthBackup{Known: true, Time: jetzt.Add(-9 * 24 * time.Hour), Target: "/root/backup-1"},
		Prune:           &api.HealthPrune{Time: jetzt.Add(-20 * time.Minute)},
		ManifestStarter: "0.1.5",
		Status:          api.HealthWarnung,
		Warnings: []api.HealthWarning{
			{Level: api.WarnWarnung, Text: "Die letzte Sicherung ist 9 Tage alt (älter als 7 Tage)."},
			{Level: api.WarnInfo, Text: "2 offene Beitrittsanfragen warten auf eine Entscheidung."},
		},
	}
}

func alsText(ks []zustandKarte) string {
	var sb strings.Builder
	for _, k := range ks {
		sb.WriteString("[" + k.titel + "]\n" + strings.Join(k.zeilen, "\n") + "\n")
	}
	return sb.String()
}

// Die Karten zeigen alle Werte der Gesundheit auf Deutsch.
func TestZustandKarten(t *testing.T) {
	jetzt := time.Date(2026, 11, 26, 14, 0, 0, 0, time.Local)
	h := beispielGesundheit(jetzt)
	ks := zustandKarten(h, jetzt)
	text := alsText(ks)
	var titel []string
	for _, k := range ks {
		titel = append(titel, k.titel)
	}
	if got := strings.Join(titel, ", "); got != "Koordinator, Speicherplatz, Weltstand, Host, Starter-Versionen, Beitrittsanfragen, Wartung, Letzte Sicherung, Aufräumen des Speichers" {
		t.Errorf("Karten: %s", got)
	}
	for _, want := range []string{
		"Version 0.1.5", "läuft seit 2 Tage 2 Std.",
		"12 GB frei von 80 GB (15 %)", "Speicher der Stände: 3,0 GB",
		"Stand 215 der Welt w-2026-11-03", "letzter Upload vor 3 Min.",
		"Lukas (PC pc-lukas…)", "letztes Lebenszeichen vor 12 s",
		"Im Manifest: 0.1.5", "Lukas: 0.1.5 (gesehen gerade eben)", "ein PC: 0.1.2 (gesehen vor 2 Std.)",
		"2 offen – im Reiter Mitglieder bearbeiten",
		"[Wartung]\nkeine",
		"vor 9 Tagen", "nach /root/backup-1",
		"gelaufen vor 20 Min.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%q fehlt in:\n%s", want, text)
		}
	}
}

func TestZustandKartenLeer(t *testing.T) {
	jetzt := time.Date(2026, 11, 26, 14, 0, 0, 0, time.Local)
	text := alsText(zustandKarten(&api.Health{Status: api.HealthOK}, jetzt))
	for _, want := range []string{
		"Version unbekannt", "[Speicherplatz]\nunbekannt", "wird noch ermittelt",
		"Noch keine Welt hochgeladen.", "Niemand hostet gerade.", "Im Manifest: keine Angabe",
		"noch kein PC gemeldet", "[Beitrittsanfragen]\nkeine offen", "[Letzte Sicherung]\nunbekannt",
		"noch nicht gelaufen",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%q fehlt in:\n%s", want, text)
		}
	}
}

func TestZustandKartenBesonderes(t *testing.T) {
	jetzt := time.Date(2026, 11, 26, 14, 0, 0, 0, time.Local)
	h := beispielGesundheit(jetzt)
	h.Host.UploadOnly = true
	h.Maintenance = &api.Maintenance{Reason: "Update", Since: jetzt.Add(-time.Minute), StopAt: jetzt.Add(4 * time.Minute)}
	h.Prune = &api.HealthPrune{Time: jetzt.Add(-time.Hour), Probe: true, Generations: 1, Blobs: 7, Bytes: 5 << 20}
	h.Disk.TotalBytes = 0
	text := alsText(zustandKarten(h, jetzt))
	for _, want := range []string{
		"nur zum Hochladen", "Grund: Update", "der Host stoppt in 4 Min.",
		"Probe während der Wartung vor 1 Std.", "würde 1 Stand und 7 Blobs (5 MB) löschen", "[Speicherplatz]\n12 GB frei\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%q fehlt in:\n%s", want, text)
		}
	}
	h.Prune = &api.HealthPrune{Time: jetzt.Add(-time.Hour), Error: "Rezept nicht lesbar"}
	text = alsText(zustandKarten(h, jetzt))
	if !strings.Contains(text, "fehlgeschlagen vor 1 Std.") || !strings.Contains(text, "Rezept nicht lesbar") {
		t.Errorf("Aufräum-Fehler:\n%s", text)
	}
}

func TestAmpelUndLaufzeit(t *testing.T) {
	for status, want := range map[string]string{api.HealthOK: "OK", api.HealthWarnung: "Warnung", api.HealthFehler: "Fehler", "neu": "Warnung", "": "Warnung"} {
		if got, _ := ampelText(status); got != want {
			t.Errorf("Ampel %q: %q, want %q", status, got, want)
		}
	}
	if _, art := ampelText(api.HealthOK); art != meldungInfo {
		t.Errorf("OK: Art %v", art)
	}
	if _, art := ampelText(api.HealthFehler); art != meldungFehler {
		t.Errorf("Fehler: Art %v", art)
	}
	for in, want := range map[int64]string{-5: "0 s", 42: "42 s", 180: "3 Min.", 3*3600 + 5*60: "3 Std. 5 Min.", 24 * 3600: "1 Tag 0 Std.", 3*86400 + 7200: "3 Tage 2 Std."} {
		if got := laufzeit(in); got != want {
			t.Errorf("laufzeit(%d) = %q, want %q", in, got, want)
		}
	}
	if warnungsArt(api.WarnFehler) != meldungFehler || warnungsArt(api.WarnWarnung) != meldungWarn || warnungsArt(api.WarnInfo) != meldungInfo || warnungsArt("x") != meldungInfo {
		t.Error("Warnstufen falsch zugeordnet")
	}
}

// Den Reiter „Zustand“ gibt es nur für Admins; ein Nicht-Admin fällt auf
// Start zurück.
func TestReiterZustandNurAdmins(t *testing.T) {
	jetzt := time.Now()
	var ops op.Ops
	for _, admin := range []bool{true, false} {
		a := ansicht{s: schnappschuss{verb: verbunden, z: beispielZustand(jetzt, admin, true)}, reiter: reiterZustand}
		u := testOberflaeche(t, false, a, jetzt)
		u.layout(testKontext(&ops, image.Pt(900, 620), 1, jetzt))
		if got := u.reiter == reiterZustand; got != admin {
			t.Errorf("admin=%v: Reiter bleibt %v", admin, u.reiter)
		}
	}
	if reiterNamen[reiterZustand] != "Zustand" || reiterZustand != anzahlReiter-1 {
		t.Errorf("Reiter Zustand: Name %q, Stelle %d von %d (soll hinten angehängt sein)", reiterNamen[reiterZustand], reiterZustand, anzahlReiter)
	}
}

// Die Ansicht zeichnet in allen Zuständen (laden, Fehler, Daten, Daten mit
// Fehler beim Aktualisieren, alle Ampelfarben, hell und dunkel, schmal und
// breit) ohne Absturz und mit sichtbarem Inhalt.
func TestZustandZeichnet(t *testing.T) {
	jetzt := time.Date(2026, 11, 26, 14, 0, 0, 0, time.Local)
	h := beispielGesundheit(jetzt)
	hFehler := beispielGesundheit(jetzt)
	hFehler.Status = api.HealthFehler
	hFehler.Warnings = append([]api.HealthWarning{{Level: api.WarnFehler, Text: "Nur noch 1,0 GB Speicherplatz frei."}}, hFehler.Warnings...)
	hOK := &api.Health{Status: api.HealthOK, Warnings: []api.HealthWarning{}}
	for name, g := range map[string]gesundheitDaten{
		"laden":          {},
		"fehler":         {err: "Der Koordinator ist zu alt für diese Ansicht", ab: jetzt},
		"daten":          {h: h, ab: jetzt.Add(-10 * time.Second)},
		"fehler-und-alt": {h: h, err: "Zeitüberschreitung", ab: jetzt},
		"ampel-fehler":   {h: hFehler, ab: jetzt},
		"ampel-ok":       {h: hOK, ab: jetzt},
		"holt-gerade":    {h: h, ab: jetzt, laeuft: true},
	} {
		for _, dunkel := range []bool{false, true} {
			for _, breite := range []int{520, 1100} {
				a := ansicht{s: schnappschuss{verb: verbunden, runde: 1, z: beispielZustand(jetzt, true, true), gesundheit: g}, reiter: reiterZustand}
				u := testOberflaeche(t, dunkel, a, jetzt)
				var ops op.Ops
				gtx := testKontext(&ops, image.Pt(breite, 700), 1, jetzt)
				if d := u.layout(gtx); d.Size.X != breite {
					t.Errorf("%s: Größe %v", name, d.Size)
				}
				if u.reiter != reiterZustand {
					t.Errorf("%s: Reiter gewechselt", name)
				}
			}
		}
	}
}

// gesundheitsFake ist eine Quelle, die die Gesundheit liefert und die
// Abrufe zählt.
type gesundheitsFake struct {
	*aktionsQuelle
	mu     sync.Mutex
	admin  bool
	n      int
	fehler error
	h      *api.Health
}

func (q *gesundheitsFake) Zustand(context.Context) (*fensterapi.Zustand, error) {
	return beispielZustand(time.Now(), q.admin, true), nil
}

func (q *gesundheitsFake) Gesundheit(context.Context) (*api.Health, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.n++
	if q.fehler != nil {
		return nil, q.fehler
	}
	h := *q.h
	return &h, nil
}

func (q *gesundheitsFake) abrufe() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n
}

func (q *gesundheitsFake) setze(f func()) {
	q.mu.Lock()
	f()
	q.mu.Unlock()
}

// Die Gesundheit wird nur bei offenem Reiter „Zustand“ geholt, sofort beim
// Öffnen, danach alle gesundheitTakt (hier verkürzt), auf Wunsch
// („Aktualisieren“) sofort; nie für Nicht-Admins; ein Fehler lässt die
// letzten Werte stehen.
func TestDatenGesundheit(t *testing.T) {
	if gesundheitTakt != 30*time.Second {
		t.Fatalf("Takt %v, want 30 s", gesundheitTakt)
	}
	if d := neueDaten(nil, nil); d.gesundheitTakt != 0 {
		t.Fatalf("neueDaten setzt gesundheitTakt auf %v (soll bei 0 = 30 s bleiben)", d.gesundheitTakt)
	}
	q := &gesundheitsFake{aktionsQuelle: &aktionsQuelle{}, admin: true, h: beispielGesundheit(time.Now())}
	d := neueDaten(func() error { return nil }, nil)
	const takt = 20 * time.Millisecond
	d.takt = takt
	d.gesundheitTakt = 40 * takt // 0,8 s
	d.verbinde = func() (quelle, error) { return q, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.lauf(ctx)

	// Auf Start nichts.
	time.Sleep(8 * takt)
	if n := q.abrufe(); n != 0 {
		t.Fatalf("auf dem Reiter Start %d Abrufe", n)
	}
	// Öffnen: sofort einmal, danach nicht wieder vor dem Takt.
	d.setzeReiter(reiterZustand)
	warteAuf(t, time.Second, "Gesundheit geholt", func() bool { return q.abrufe() == 1 })
	if s := d.lesen(); s.gesundheit.h == nil || s.gesundheit.h.Version != "0.1.5" || s.gesundheit.ab.IsZero() || s.gesundheit.err != "" {
		t.Fatalf("Schnappschuss: %+v", s.gesundheit)
	}
	time.Sleep(15 * takt)
	if n := q.abrufe(); n != 1 {
		t.Fatalf("%d Abrufe vor Ablauf des Takts, want 1", n)
	}
	// Automatisch nach dem Takt.
	warteAuf(t, 2*time.Second, "automatische Aktualisierung", func() bool { return q.abrufe() >= 2 })

	// „Aktualisieren“: sofort, auch mitten im Takt.
	vor := q.abrufe()
	d.gesundheitNeuLaden()
	warteAuf(t, time.Second, "Aktualisieren", func() bool { return q.abrufe() > vor })

	// Fehler: die alten Werte bleiben, der Fehler steht daneben; danach
	// wieder gesund.
	q.setze(func() { q.fehler = errors.New("Der Koordinator ist nicht erreichbar.") })
	d.gesundheitNeuLaden()
	warteAuf(t, time.Second, "Fehler gemerkt", func() bool { return d.lesen().gesundheit.err != "" })
	if s := d.lesen(); s.gesundheit.h == nil || s.gesundheit.h.Version != "0.1.5" {
		t.Errorf("alte Werte nach Fehler: %+v", s.gesundheit.h)
	}
	q.setze(func() { q.fehler = nil; q.h.Version = "0.1.6" })
	d.gesundheitNeuLaden()
	warteAuf(t, time.Second, "wieder gesund", func() bool {
		s := d.lesen()
		return s.gesundheit.err == "" && s.gesundheit.h != nil && s.gesundheit.h.Version == "0.1.6"
	})

	// Reiter verlassen: keine Abrufe mehr.
	d.setzeReiter(reiterStart)
	time.Sleep(4 * takt)
	vor = q.abrufe()
	time.Sleep(60 * takt) // länger als der Takt
	if n := q.abrufe(); n != vor {
		t.Errorf("%d Abrufe auf dem Reiter Start", n-vor)
	}
}

// ohneGesundheit ist ein Admin-Zustand ohne Fähigkeit, die Gesundheit zu holen.
type ohneGesundheit struct{ *aktionsQuelle }

func (ohneGesundheit) Zustand(context.Context) (*fensterapi.Zustand, error) {
	return beispielZustand(time.Now(), true, true), nil
}

// Für Nicht-Admins und für Quellen ohne Gesundheit wird nichts geholt.
func TestDatenGesundheitNurAdmins(t *testing.T) {
	q := &gesundheitsFake{aktionsQuelle: &aktionsQuelle{}, admin: false, h: beispielGesundheit(time.Now())}
	d := neueDaten(func() error { return nil }, nil)
	const takt = 20 * time.Millisecond
	d.takt = takt
	d.verbinde = func() (quelle, error) { return q, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.lauf(ctx)
	d.setzeReiter(reiterZustand)
	warteAuf(t, time.Second, "verbunden", func() bool { s := d.lesen(); return s.verb == verbunden && s.z != nil })
	time.Sleep(10 * takt)
	if n := q.abrufe(); n != 0 {
		t.Errorf("Nicht-Admin: %d Abrufe", n)
	}

	// Eine Quelle ohne Gesundheit (andere Fakes) wird nicht angefasst.
	d2 := neueDaten(func() error { return nil }, nil)
	d2.takt = takt
	d2.verbinde = func() (quelle, error) { return ohneGesundheit{&aktionsQuelle{}}, nil }
	d2.reiter = reiterZustand
	d2.runde(ctx) // darf nicht abstürzen
	if s := d2.lesen(); s.gesundheit.h != nil || !s.gesundheit.ab.IsZero() {
		t.Errorf("Quelle ohne Gesundheit: %+v", s.gesundheit)
	}
}
