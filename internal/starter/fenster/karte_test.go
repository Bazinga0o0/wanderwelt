package fenster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/op"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte/kartetest"
)

const karteTestToken = "karte-test-token-0123456789"

// karteTestQuelle ist die Instanz der Tests: Aktionen wie aktionsQuelle, Karte
// direkt aus einem client.Client gegen den Fake-Koordinator (ohne die
// Durchreiche der Instanz, die internal/starter/app testet). Immer als Zeiger
// benutzen: Die Sitzung vergleicht Quellen.
type karteTestQuelle struct {
	*aktionsQuelle
	c *client.Client
}

func (q *karteTestQuelle) KarteManifest(ctx context.Context, welt string) (*karte.Manifest, error) {
	return q.c.KarteManifest(ctx, welt)
}

func (q *karteTestQuelle) KarteKachel(ctx context.Context, welt string, k karte.Kachel, etag string) (karte.Antwort, error) {
	return q.c.KarteKachel(ctx, welt, k, etag)
}

// karteStub antwortet immer mit einem festen Fehler.
type karteStub struct {
	*aktionsQuelle
	err error
}

func (q *karteStub) KarteManifest(context.Context, string) (*karte.Manifest, error) {
	return nil, q.err
}
func (q *karteStub) KarteKachel(context.Context, string, karte.Kachel, string) (karte.Antwort, error) {
	return karte.Antwort{}, q.err
}

// karteZustand: ein Mitglied (admin: Admin) in der Welt w1 mit Kartenstand.
func karteZustand(jetzt time.Time, admin bool) *fensterapi.Zustand {
	z := beispielZustand(jetzt, admin, false)
	z.Status.Worlds = []api.GroupWorld{{ID: "w1", Name: "Survival", HeadGeneration: 412,
		Karte: &api.KarteInfo{Version: 1, Source: "s", Generated: jetzt.Add(-7 * time.Minute), Tiles: 9, Levels: []int{0, 1, 2, 3, 4, 5, 6}}}}
	z.Welt = fensterapi.WeltZustand{Aktiv: "w1"}
	z.Gruppen = []fensterapi.GruppeEintrag{{ID: "wanderwelt.example.de", Titel: "Wandergruppe", Aktiv: true}}
	return z
}

// karteTest ist ein Fenster auf dem Reiter Karte mit eigenem Eingabe-Router.
type karteTest struct {
	t     *testing.T
	u     *oberflaeche
	r     input.Router
	ops   op.Ops
	groes image.Point
	skala float32
	jetzt time.Time
}

func neuerKarteTest(t *testing.T, z *fensterapi.Zustand, quelle quelle, dunkel bool) *karteTest {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	kt := &karteTest{t: t, groes: image.Pt(900, 620), skala: 1, jetzt: time.Now()}
	kt.u = testOberflaeche(t, dunkel, ansicht{s: schnappschuss{verb: verbunden, runde: 1, z: z}, reiter: reiterKarte}, kt.jetzt)
	kt.u.d.c = quelle
	t.Cleanup(kt.u.beenden)
	return kt
}

// bild zeichnet ein Bild (mit Eingaben des Routers).
func (kt *karteTest) bild() {
	gtx := testKontext(&kt.ops, kt.groes, kt.skala, kt.jetzt)
	gtx.Source = kt.r.Source()
	if dims := kt.u.layout(gtx); dims.Size != kt.groes {
		kt.t.Fatalf("Größe %v, want %v", dims.Size, kt.groes)
	}
	kt.r.Frame(&kt.ops)
}

// bisBild zeichnet Bilder, bis f wahr ist (höchstens 5 s).
func (kt *karteTest) bisBild(was string, f func() bool) {
	kt.t.Helper()
	for ende := time.Now().Add(5 * time.Second); time.Now().Before(ende); time.Sleep(3 * time.Millisecond) {
		kt.bild()
		if f() {
			return
		}
	}
	kt.t.Fatalf("Zeitüberschreitung: %s", was)
}

func (kt *karteTest) stand() karteStand {
	if kt.u.karte.sitz == nil {
		return karteStand{}
	}
	return kt.u.karte.sitz.stand()
}

func (kt *karteTest) zustand(k karte.Kachel) karte.Zustand {
	if kt.u.karte.sitz == nil {
		return karte.Fehlt
	}
	_, z := kt.u.karte.sitz.lader.Bild(k)
	return z
}

// Kachelsatz mit negativen Koordinaten: Stufe 0 x, y ∈ −1…1, Stufe 1 x, y ∈ −1…0.
func kartenSatz(t *testing.T, k *kartetest.Koordinator) {
	t.Helper()
	seed := 0
	for z, bereich := range [][2]int{{-1, 1}, {-1, 0}, {-1, 0}} {
		for y := bereich[0]; y <= bereich[1]; y++ {
			for x := bereich[0]; x <= bereich[1]; x++ {
				seed++
				k.Setze(karte.Kachel{Z: z, X: x, Y: y}, kartetest.PNG(t, seed))
			}
		}
	}
}

func neuerKarteTestMitKoordinator(t *testing.T, dunkel bool) (*karteTest, *kartetest.Koordinator) {
	t.Helper()
	k := kartetest.Neu(t, karteTestToken, "w1")
	kartenSatz(t, k)
	q := &karteTestQuelle{aktionsQuelle: &aktionsQuelle{}, c: client.New(&group.Group{Coordinator: k.URL(), Token: karteTestToken})}
	kt := neuerKarteTest(t, karteZustand(time.Now(), false), q, dunkel)
	return kt, k
}

func goroutinenMit(teil string) int {
	var b bytes.Buffer
	pprof.Lookup("goroutine").WriteTo(&b, 2)
	n := 0
	for _, g := range strings.Split(b.String(), "\n\n") {
		if strings.Contains(g, teil) {
			n++
		}
	}
	return n
}

// ---- Reiter-Sichtbarkeit ----

func TestReiterKarteSichtbarkeit(t *testing.T) {
	jetzt := time.Now()
	ohne := func(f func(z *fensterapi.Zustand)) *fensterapi.Zustand {
		z := karteZustand(jetzt, false)
		f(z)
		return z
	}
	for _, c := range []struct {
		name string
		z    *fensterapi.Zustand
		want bool
	}{
		{"Mitglied mit Karte", karteZustand(jetzt, false), true},
		{"Admin mit Karte", karteZustand(jetzt, true), true},
		{"Status ohne karte", ohne(func(z *fensterapi.Zustand) { z.Status.Worlds[0].Karte = nil }), false},
		{"andere Welt gewählt, dort keine Karte", ohne(func(z *fensterapi.Zustand) {
			z.Status.Worlds = append(z.Status.Worlds, api.GroupWorld{ID: "w2", Name: "Zwei"})
			z.Welt.Aktiv = "w2"
		}), false},
		{"andere Welt gewählt, dort Karte", ohne(func(z *fensterapi.Zustand) {
			z.Status.Worlds = append(z.Status.Worlds, api.GroupWorld{ID: "w2", Name: "Zwei", Karte: &api.KarteInfo{Version: 1, Tiles: 1}})
			z.Welt.Aktiv = "w2"
		}), true},
		{"keine Welt gewählt (älterer Koordinator)", ohne(func(z *fensterapi.Zustand) { z.Welt.Aktiv = "" }), false},
		{"Welt nicht in der Liste", ohne(func(z *fensterapi.Zustand) { z.Welt.Aktiv = "w9" }), false},
		{"kein Status", ohne(func(z *fensterapi.Zustand) { z.Status = nil }), false},
		{"kein Mitglied (Me fehlt)", ohne(func(z *fensterapi.Zustand) { z.Me = nil }), false},
		{"kein Zustand", nil, false},
	} {
		admin := c.z != nil && c.z.Me != nil && c.z.Me.Admin
		if got := reiterZulaessig(reiterKarte, admin, c.z); got != c.want {
			t.Errorf("%s: reiterZulaessig = %v, want %v", c.name, got, c.want)
		}
		if c.z == nil {
			continue
		}
		// Im Fenster: Ohne Kartenstand springt der Reiter auf Start zurück.
		kt := neuerKarteTest(t, c.z, nil, false)
		kt.bild()
		if got := kt.u.reiter == reiterKarte; got != c.want {
			t.Errorf("%s: Reiter bleibt Karte = %v, want %v", c.name, got, c.want)
		}
	}
	if reiterNamen[reiterKarte] != "Karte" || reiterKarte != reiterServer+1 {
		t.Errorf("Reiter Karte: Name %q, Stelle %d von %d (soll ganz hinten stehen)", reiterNamen[reiterKarte], reiterKarte, anzahlReiter)
	}
	for r := range anzahlReiter {
		if reiterNamen[r] == "" {
			t.Errorf("Reiter %d hat keinen Namen", r)
		}
	}
	if nurAdmin(reiterKarte) {
		t.Error("Die Karte ist für alle Mitglieder, nicht nur für Admins")
	}
}

// Ein Klick auf den Reiter wirkt nur mit Kartenstand; die Tab-Folge zählt ihn mit.
func TestReiterKarteKlickUndTabFolge(t *testing.T) {
	jetzt := time.Now()
	for _, c := range []struct {
		name  string
		admin bool
		karte bool
		tabs  int // Reiter in der Leiste
	}{
		{"Mitglied mit Karte", false, true, 7},
		{"Mitglied ohne Karte", false, false, 6},
		{"Admin mit Karte", true, true, 10},
		{"Admin ohne Karte", true, false, 9},
	} {
		z := karteZustand(jetzt, c.admin)
		if !c.karte {
			z.Status.Worlds[0].Karte = nil
		}
		b := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z}, reiterStart)
		b.bild()
		for range c.tabs {
			b.r.MoveFocus(key.FocusForward)
		}
		// Der letzte Reiter der Leiste hat den Fokus; ist es der Reiter Karte?
		if got := b.r.Source().Focused(&b.u.tabs[reiterKarte]); got != c.karte {
			t.Errorf("%s: nach %d× Tab Fokus auf „Karte“ = %v, want %v", c.name, c.tabs, got, c.karte)
		}
		b.u.tabs[reiterKarte].Click()
		b.bild()
		if got := b.u.reiter == reiterKarte; got != c.karte {
			t.Errorf("%s: Klick auf „Karte“ wechselt = %v, want %v", c.name, got, c.karte)
		}
	}
}

// ---- Ablauf mit Fake-Koordinator ----

func TestKarteAblauf(t *testing.T) {
	kt, k := neuerKarteTestMitKoordinator(t, false)
	kt.bild() // öffnet die Sitzung
	if kt.u.karte.sitz == nil {
		t.Fatal("keine Sitzung auf dem Reiter Karte")
	}
	kt.bisBild("Manifest da", func() bool { return kt.stand().manifest != nil })
	m := kt.stand().manifest
	if m.Anzahl() != 9+4+4 {
		t.Fatalf("Manifest nennt %d Kacheln", m.Anzahl())
	}
	// Eingepasst: Die ganze Karte (3×3 Kacheln = 768 Blöcke) liegt im Bild;
	// geladen werden nur die sichtbaren Kacheln der gewählten Stufe, nicht alle 17.
	kt.bisBild("sichtbare Kacheln geladen", func() bool {
		for _, kachel := range kt.u.karte.bild.sichtbar {
			if kt.zustand(kachel) != karte.Fertig {
				return false
			}
		}
		return len(kt.u.karte.bild.sichtbar) > 0
	})
	b := &kt.u.karte.bild
	if b.sicht.Zoom <= 0 || b.sicht.Zoom > 1 || b.stufe != b.sicht.Stufe(m.Stufen()) {
		t.Errorf("Ansicht nach dem Einpassen: %+v, Stufe %d", b.sicht, b.stufe)
	}
	if n := k.KachelAbrufe(); n != len(b.sichtbar) {
		t.Errorf("%d Kachel-Abrufe für %d sichtbare Kacheln", n, len(b.sichtbar))
	}
	for _, kachel := range b.sichtbar {
		if kachel.Z != b.stufe {
			t.Errorf("sichtbare Kachel %v nicht auf Stufe %d", kachel, b.stufe)
		}
	}
	if k.Abrufe("manifest.json") != 1 {
		t.Errorf("%d Manifest-Abrufe, want 1", k.Abrufe("manifest.json"))
	}
	if txt := kt.u.karte.statusText(kt.jetzt, kt.stand(), kt.u.karte.sitz); !strings.HasPrefix(txt, "Stand: gerade eben") || !strings.Contains(txt, "Zoom") {
		t.Errorf("Statusleiste: %q", txt)
	}

	// Hineinzoomen auf 100 %: Stufe 0, andere Kacheln.
	for b.sicht.Zoom < 1 {
		b.zoomMitte(knopfSchritt, m)
	}
	b.sicht.Zoom = 1
	kt.bisBild("Stufe 0 geladen", func() bool {
		if b.stufe != 0 {
			return false
		}
		for _, kachel := range b.sichtbar {
			if kt.zustand(kachel) != karte.Fertig {
				return false
			}
		}
		return true
	})
	vorher := k.KachelAbrufe()

	// Neu laden mit einer geänderten Kachel: Nur ihr Hash ist neu, nur sie wird neu geholt.
	var geaendert karte.Kachel
	for _, kachel := range b.sichtbar {
		if kachel.X == 0 && kachel.Y == 0 {
			geaendert = kachel
		}
	}
	if geaendert == (karte.Kachel{}) && !contains(b.sichtbar, karte.Kachel{}) {
		t.Fatalf("Kachel 0/0/0 nicht sichtbar: %v", b.sichtbar)
	}
	alt, _ := m.Hash(karte.Kachel{})
	k.Setze(karte.Kachel{}, kartetest.PNG(t, 4242))
	k.Erzeugt(kt.jetzt.Add(-2*time.Minute-30*time.Second), "test-2")
	kt.u.karte.neu.Click()
	kt.bisBild("neues Manifest und neue Kachel", func() bool {
		neu := kt.stand().manifest
		if neu == nil || neu == m {
			return false
		}
		h, _ := neu.Hash(karte.Kachel{})
		_, z := kt.u.karte.sitz.lader.Bild(karte.Kachel{})
		return h != alt && z == karte.Fertig
	})
	if n := k.KachelAbrufe() - vorher; n != 1 {
		t.Errorf("%d Kachel-Abrufe nach Neu laden mit einer geänderten Kachel, want 1", n)
	}
	if k.Abrufe("manifest.json") != 2 {
		t.Errorf("%d Manifest-Abrufe, want 2", k.Abrufe("manifest.json"))
	}
	if txt := kt.u.karte.statusText(kt.jetzt.Add(0), kt.stand(), kt.u.karte.sitz); !strings.Contains(txt, "Stand: vor 2 Min.") {
		t.Errorf("Statusleiste nach Neu laden: %q", txt)
	}
	// Ein Manifest ohne Änderung tauscht nichts aus (keine neuen Kachel-Abrufe).
	vor := k.KachelAbrufe()
	kt.u.karte.neu.Click()
	kt.bisBild("drittes Manifest", func() bool { return k.Abrufe("manifest.json") == 3 })
	time.Sleep(30 * time.Millisecond)
	kt.bild()
	if k.KachelAbrufe() != vor {
		t.Error("unverändertes Manifest löste Kachel-Abrufe aus")
	}
	if k.OhneToken() != 0 {
		t.Errorf("%d Anfragen ohne Token", k.OhneToken())
	}
}

func contains(ks []karte.Kachel, k karte.Kachel) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

// Wer den Reiter verlässt, beendet die Sitzung: keine Goroutine bleibt, die
// Ansicht wird gemerkt.
func TestKarteSitzungEndetSauber(t *testing.T) {
	kt, _ := neuerKarteTestMitKoordinator(t, false)
	kt.bisBild("Manifest da", func() bool { return kt.stand().manifest != nil })
	kt.bild()
	if goroutinenMit("fenster.(*karteSitzung).lauf") != 1 {
		t.Fatal("die Sitzung hat keine Goroutine")
	}
	b := &kt.u.karte.bild
	b.sicht.Zoom = 0.77
	b.sicht.X, b.sicht.Z = 100, -50
	kt.u.reiter = reiterStart
	kt.bild()
	if kt.u.karte.sitz != nil {
		t.Fatal("Sitzung nach Reiterwechsel noch offen")
	}
	for _, teil := range []string{"fenster.(*karteSitzung).lauf", "karte.(*Lader", "karte.(*karteSitzung"} {
		if n := goroutinenMit(teil); n != 0 {
			t.Errorf("%d Goroutinen mit %q bleiben nach dem Schließen", n, teil)
		}
	}
	// Zurück auf die Karte: dieselbe Ansicht.
	kt.u.reiter = reiterKarte
	kt.bild()
	if got := kt.u.karte.bild.sicht; math.Abs(got.Zoom-0.77) > 1e-9 || got.X != 100 || got.Z != -50 {
		t.Errorf("Ansicht nach Rückkehr: %+v", got)
	}
	// Fenster schließt: ebenfalls sauber, und doppeltes Schließen ist harmlos.
	kt.u.beenden()
	kt.u.beenden()
	if n := goroutinenMit("fenster.(*karteSitzung).lauf"); n != 0 {
		t.Errorf("%d Goroutinen nach dem Schließen des Fensters", n)
	}
}

// Das Manifest wird im Takt geprüft, solange der Reiter offen ist, danach nicht mehr.
func TestKarteManifestTakt(t *testing.T) {
	alt := karteManifestTakt
	karteManifestTakt = 15 * time.Millisecond
	t.Cleanup(func() { karteManifestTakt = alt })
	kt, k := neuerKarteTestMitKoordinator(t, false)
	kt.bisBild("mindestens vier Prüfungen", func() bool { return k.Abrufe("manifest.json") >= 4 })
	kt.u.reiter = reiterStart
	kt.bild()
	n := k.Abrufe("manifest.json")
	time.Sleep(100 * time.Millisecond)
	if got := k.Abrufe("manifest.json"); got != n {
		t.Errorf("Manifest wird nach dem Schließen weiter geprüft (%d → %d)", n, got)
	}
	// Ein gleiches Manifest lädt nichts nach: nur die sichtbaren Kacheln einmal.
	if k.KachelAbrufe() > 12 {
		t.Errorf("%d Kachel-Abrufe trotz unverändertem Manifest", k.KachelAbrufe())
	}
}

// Minimiertes Fenster: keine Prüfung.
func TestKarteMinimiertPrueftNicht(t *testing.T) {
	alt := karteManifestTakt
	karteManifestTakt = 10 * time.Millisecond
	t.Cleanup(func() { karteManifestTakt = alt })
	kt, k := neuerKarteTestMitKoordinator(t, false)
	kt.u.d.minimiert = func() bool { return true }
	kt.bild()
	time.Sleep(80 * time.Millisecond)
	if n := k.Abrufe("manifest.json"); n != 0 {
		t.Errorf("%d Abrufe bei minimiertem Fenster", n)
	}
}

// ---- Fehlerzustände ----

func TestKarteFehlerzustaende(t *testing.T) {
	kt, k := neuerKarteTestMitKoordinator(t, false)
	k.OhneKarte(true)
	kt.bisBild("Fehler da", func() bool { return kt.stand().fehler != nil })
	if st := kt.stand(); st.manifest != nil || !errors.Is(st.fehler, karte.ErrNochNichtErzeugt) {
		t.Errorf("ohne Karte: %+v", st)
	}
	titel, text := karteFehlerText(kt.stand().fehler)
	if titel != "Die Karte ist noch nicht erzeugt" || !strings.Contains(text, "von selbst") {
		t.Errorf("Text: %q / %q", titel, text)
	}
	// Die Karte erscheint später; „Neu laden“ holt sie.
	k.OhneKarte(false)
	kt.u.karte.neu.Click()
	kt.bisBild("Karte da", func() bool { return kt.stand().manifest != nil && kt.stand().fehler == nil })

	// Koordinator fällt aus: Die Karte bleibt, eine Warnung steht darüber.
	k.Ausfall(true)
	kt.u.karte.neu.Click()
	kt.bisBild("Aktualisierung fehlgeschlagen", func() bool { return kt.stand().fehler != nil })
	if kt.stand().manifest == nil {
		t.Error("die Karte verschwindet beim Ausfall")
	}
	if titel, _ := karteFehlerText(kt.stand().fehler); titel != "Der Koordinator ist nicht erreichbar" {
		t.Errorf("Titel bei Ausfall: %q", titel)
	}
	k.Ausfall(false)
	kt.u.karte.neu.Click()
	kt.bisBild("wieder gut", func() bool { return kt.stand().fehler == nil })

	for name, c := range map[string]struct {
		err   error
		titel string
	}{
		"ungültig":    {fmt.Errorf("%w: Hash der Kachel 0/0/0", karte.ErrManifestUngueltig), "Die Karte ist ungültig"},
		"Instanz weg": {fensterapi.ErrLaeuftNicht, "wanderwelt antwortet nicht"},
		"sonst":       {errors.New("Zeitüberschreitung"), "Der Koordinator ist nicht erreichbar"},
	} {
		titel, text := karteFehlerText(c.err)
		if titel != c.titel || text == "" {
			t.Errorf("%s: %q / %q", name, titel, text)
		}
		// Als Meldung im leeren Reiter: lässt sich zeichnen.
		kt2 := neuerKarteTest(t, karteZustand(time.Now(), false), &karteStub{aktionsQuelle: &aktionsQuelle{}, err: c.err}, false)
		kt2.bisBild("Fehler "+name, func() bool { return kt2.stand().fehler != nil })
		if st := kt2.stand(); st.manifest != nil {
			t.Errorf("%s: Manifest trotz Fehler", name)
		}
	}
	if _, text := karteFehlerText(fmt.Errorf("%w: Hash der Kachel 0/0/0", karte.ErrManifestUngueltig)); !strings.Contains(text, "Hash der Kachel") {
		t.Error("Meldung zu ungültigem Manifest nennt den Grund nicht")
	}
}

// ---- Eingaben ----

func zeiger(kind pointer.Kind, pos f32.Point) pointer.Event {
	e := pointer.Event{Kind: kind, Source: pointer.Mouse, Position: pos, PointerID: 1}
	if kind == pointer.Press {
		e.Buttons = pointer.ButtonPrimary
	}
	return e
}

func TestKarteZiehenUndZoomen(t *testing.T) {
	kt, _ := neuerKarteTestMitKoordinator(t, false)
	kt.bisBild("Manifest da", func() bool { return kt.stand().manifest != nil })
	kt.bild()
	b := &kt.u.karte.bild
	b.sicht = karte.Ansicht{X: 128, Z: 128, Zoom: 1}
	kt.bild()
	vor := b.sicht
	// Ziehen: Der Inhalt folgt der Hand (nach rechts unten → Mitte wandert nach links oben).
	kt.r.Queue(zeiger(pointer.Press, f32.Pt(300, 200)))
	kt.bild()
	ziehen := zeiger(pointer.Move, f32.Pt(340, 230)) // mit gedrückter Taste macht Gio daraus „Drag“
	ziehen.Buttons = pointer.ButtonPrimary
	kt.r.Queue(ziehen)
	kt.bild()
	kt.r.Queue(zeiger(pointer.Release, f32.Pt(340, 230)))
	kt.bild()
	if got := b.sicht; math.Abs(got.X-(vor.X-40)) > 1e-6 || math.Abs(got.Z-(vor.Z-30)) > 1e-6 || got.Zoom != 1 {
		t.Errorf("Ansicht nach Ziehen: %+v, vorher %+v", got, vor)
	}
	if b.ziehen {
		t.Error("Ziehen nach Loslassen noch aktiv")
	}
	// Ohne gedrückte Taste bewegt Ziehen nichts (nur Mausposition).
	vor = b.sicht
	kt.r.Queue(zeiger(pointer.Move, f32.Pt(500, 250)))
	kt.bild()
	if b.sicht != vor {
		t.Errorf("Bewegung ohne Taste verschob die Karte: %+v", b.sicht)
	}
	// Gio meldet Positionen im Koordinatensystem des Kartenbereichs (nicht des
	// Fensters); maßgeblich ist, was das Widget bekommen hat.
	pos := b.maus
	bx, bz, ok := b.mausBlock()
	if !ok {
		t.Fatal("keine Mauskoordinate")
	}
	wx, wz := b.sicht.BlockBei(float64(pos.X), float64(pos.Y), b.groesse)
	if bx != int(math.Floor(wx)) || bz != int(math.Floor(wz)) {
		t.Errorf("Mauskoordinate %d, %d, want %v, %v", bx, bz, wx, wz)
	}
	if pos.X >= 500 || pos.Y >= 250 || pos.X < 400 {
		t.Errorf("Mausposition im Kartenbereich %v (Fenster 500, 250)", pos)
	}
	if txt := kt.u.karte.statusText(kt.jetzt, kt.stand(), kt.u.karte.sitz); !strings.Contains(txt, fmt.Sprintf("x %s, z %s", minusText(bx), minusText(bz))) {
		t.Errorf("Statusleiste ohne Mauskoordinate: %q", txt)
	}
	// Mausrad nach oben (negativ) vergrößert um die Mausposition: Der Block darunter bleibt.
	vx, vz := b.sicht.BlockBei(float64(pos.X), float64(pos.Y), b.groesse)
	e := zeiger(pointer.Scroll, f32.Pt(500, 250))
	e.Scroll = f32.Pt(0, -120)
	kt.r.Queue(e)
	kt.bild()
	if got := b.sicht.Zoom; math.Abs(got-radSchritt) > 1e-9 {
		t.Errorf("Zoom nach einer Raststufe: %v, want %v", got, radSchritt)
	}
	nx, nz := b.sicht.BlockBei(float64(pos.X), float64(pos.Y), b.groesse)
	if math.Abs(nx-vx) > 1e-6 || math.Abs(nz-vz) > 1e-6 {
		t.Errorf("Block unter der Maus: (%v, %v) → (%v, %v)", vx, vz, nx, nz)
	}
	// Nach unten verkleinert; bis an die Grenzen, nicht darüber.
	for range 60 {
		e := zeiger(pointer.Scroll, f32.Pt(500, 250))
		e.Scroll = f32.Pt(0, 120)
		kt.r.Queue(e)
		kt.bild()
	}
	if b.sicht.Zoom != karte.MinZoom {
		t.Errorf("Zoom unten = %v, want %v", b.sicht.Zoom, karte.MinZoom)
	}
	for range 80 {
		e := zeiger(pointer.Scroll, f32.Pt(500, 250))
		e.Scroll = f32.Pt(0, -120)
		kt.r.Queue(e)
		kt.bild()
	}
	if b.sicht.Zoom != karte.MaxZoom {
		t.Errorf("Zoom oben = %v, want %v", b.sicht.Zoom, karte.MaxZoom)
	}
	// Maus verlässt die Karte: keine Koordinate mehr.
	kt.r.Queue(zeiger(pointer.Leave, f32.Pt(-1, -1)))
	kt.bild()
	if _, _, ok := b.mausBlock(); ok {
		t.Error("Mauskoordinate nach Verlassen")
	}
}

func TestKarteKnoepfe(t *testing.T) {
	kt, _ := neuerKarteTestMitKoordinator(t, false)
	kt.bisBild("Manifest da", func() bool { return kt.stand().manifest != nil })
	kt.bild()
	b := &kt.u.karte.bild
	b.sicht = karte.Ansicht{X: 5000, Z: 5000, Zoom: 0.5}
	kt.bild() // wird in die Karte geklemmt
	m := kt.stand().manifest
	x0, z0, x1, z1, _ := m.Ausdehnung()
	if b.sicht.X > x1 || b.sicht.Z > z1 || b.sicht.X < x0 || b.sicht.Z < z0 {
		t.Errorf("Mitte außerhalb der Karte: %+v", b.sicht)
	}
	z := b.sicht.Zoom
	kt.u.karte.plus.Click()
	kt.bild()
	if math.Abs(b.sicht.Zoom-z*knopfSchritt) > 1e-9 {
		t.Errorf("Zoom nach +: %v", b.sicht.Zoom)
	}
	kt.u.karte.minus.Click()
	kt.bild()
	kt.u.karte.minus.Click()
	kt.bild()
	if math.Abs(b.sicht.Zoom-z/knopfSchritt) > 1e-9 {
		t.Errorf("Zoom nach −−: %v", b.sicht.Zoom)
	}
	// Zur Mitte: Mitte der Stufe-0-Grenzen (Kacheln −1…1 → Blöcke −256…512 → Mitte 128).
	b.sicht.X, b.sicht.Z = x0, z0
	kt.u.karte.mitte.Click()
	kt.bild()
	if b.sicht.X != 128 || b.sicht.Z != 128 {
		t.Errorf("Mitte nach „Zur Mitte“: %v, %v, want 128, 128", b.sicht.X, b.sicht.Z)
	}
}

// ---- Layout ----

func TestKarteLayout(t *testing.T) {
	for _, dunkel := range []bool{false, true} {
		for _, g := range []struct {
			sz    image.Point
			skala float32
		}{{image.Pt(900, 620), 1}, {image.Pt(720, 520), 1}, {image.Pt(1125, 775), 1.25}} {
			t.Run(fmt.Sprintf("dunkel=%v/%dx%d", dunkel, g.sz.X, g.sz.Y), func(t *testing.T) {
				kt, k := neuerKarteTestMitKoordinator(t, dunkel)
				kt.groes, kt.skala = g.sz, g.skala
				kt.bild() // Sitzung öffnet; noch ohne Karte: „wird geladen“
				kt.bisBild("Karte geladen", func() bool {
					return kt.stand().manifest != nil && len(kt.u.karte.bild.sichtbar) > 0 && kt.zustand(kt.u.karte.bild.sichtbar[0]) == karte.Fertig
				})
				// Meldung statt Karte in einem frischen Fenster, als Admin (zehn Reiter).
				k.OhneKarte(true)
				kt2 := neuerKarteTest(t, karteZustand(time.Now(), true), kt.u.d.c, dunkel)
				kt2.groes, kt2.skala = g.sz, g.skala
				kt2.bisBild("Fehler", func() bool { return kt2.stand().fehler != nil })
				// Warnung über der Karte, wenn die Aktualisierung scheitert.
				k.OhneKarte(false)
				k.Ausfall(true)
				kt.u.karte.neu.Click()
				kt.bisBild("Warnung über der Karte", func() bool { return kt.stand().fehler != nil && kt.stand().manifest != nil })
			})
		}
	}
}

// Drosselt der Koordinator das Manifest (429), wartet die Sitzung kurz und
// zeigt keinen Fehler.
func TestKarteDrosselungIstKeinFehler(t *testing.T) {
	alt := karte.MaxDrosselWarte
	karte.MaxDrosselWarte = 5 * time.Millisecond
	t.Cleanup(func() { karte.MaxDrosselWarte = alt })
	kt, k := neuerKarteTestMitKoordinator(t, false)
	k.Drosseln(3) // Manifest zweimal, dann eine Kachel
	var gesehen bool
	kt.bisBild("Karte geladen", func() bool {
		gesehen = gesehen || kt.stand().fehler != nil
		return kt.stand().manifest != nil && len(kt.u.karte.bild.sichtbar) > 0 && kt.zustand(kt.u.karte.bild.sichtbar[0]) == karte.Fertig
	})
	if gesehen {
		t.Error("Drosselung wurde als Fehler gezeigt")
	}
	if k.Gedrosselt() != 3 {
		t.Errorf("%d×429, want 3", k.Gedrosselt())
	}
	if n, _ := kt.u.karte.sitz.lader.Fehler(); n != 0 {
		t.Errorf("%d Kacheln im Fehlerzustand", n)
	}
}
