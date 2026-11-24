package fenster

import (
	"context"
	"encoding/json"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/op"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// aktionsQuelle merkt sich Aktionen (statt der Steuerung).
type aktionsQuelle struct {
	mu       sync.Mutex
	aktionen []string
}

func (q *aktionsQuelle) Zustand(context.Context) (*fensterapi.Zustand, error) { return nil, nil }
func (q *aktionsQuelle) Staende(context.Context) (*api.GenerationsResponse, error) {
	return nil, nil
}
func (q *aktionsQuelle) Speicherstaende(context.Context) ([]fensterapi.Speicherstand, error) {
	return nil, nil
}
func (q *aktionsQuelle) Log(context.Context, int) (string, error)                 { return "", nil }
func (q *aktionsQuelle) Mitglieder(context.Context) ([]api.MemberInfo, error)     { return nil, nil }
func (q *aktionsQuelle) Beitritte(context.Context) ([]api.JoinRequestInfo, error) { return nil, nil }
func (q *aktionsQuelle) Aktion(_ context.Context, name string, body any) error {
	b, _ := json.Marshal(body)
	q.mu.Lock()
	q.aktionen = append(q.aktionen, name+" "+string(b))
	q.mu.Unlock()
	return nil
}

func (q *aktionsQuelle) liste() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.aktionen...)
}

// TestBedienung bedient das Fenster wie mit der Tastatur (Tab, Enter,
// Escape) über Gios Eingabe-Router: Reiter wechseln, Spielen, Rückfrage
// abbrechen und bestätigen.
func TestBedienung(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	z := beispielZustand(jetzt, true, false) // Admin, niemand hostet
	a := ansicht{s: schnappschuss{verb: verbunden, runde: 1, z: z, staende: beispielStaende(jetzt), staendeAb: jetzt}}
	u := testOberflaeche(t, false, a, jetzt)
	q := &aktionsQuelle{}
	u.d.c = q

	var r input.Router
	var ops op.Ops
	bild := func() {
		gtx := testKontext(&ops, image.Pt(900, 620), 1, jetzt)
		gtx.Source = r.Source()
		u.layout(gtx)
		r.Frame(&ops)
	}
	vor := func(n int) {
		for range n {
			r.MoveFocus(key.FocusForward)
		}
	}
	warteAktionen := func(n int) []string {
		t.Helper()
		ende := time.Now().Add(2 * time.Second)
		for len(q.liste()) < n && time.Now().Before(ende) {
			time.Sleep(5 * time.Millisecond)
		}
		u.d.abarbeiten()
		return q.liste()
	}

	bild()
	// Reihenfolge: Start, Welt, Hosten, Mitglieder, Log, dann „Spielen“.
	vor(6)
	r.ClickFocus()
	bild()
	if got := warteAktionen(1); len(got) != 1 || got[0] != "play null" {
		t.Fatalf("Spielen: Aktionen %q", got)
	}

	// Reiter Welt (zweiter Reiter); frischer Router, Fokus von vorn.
	r = input.Router{}
	bild()
	vor(2)
	r.ClickFocus()
	bild()
	if u.reiter != reiterWelt {
		t.Fatalf("Reiter nach Klick: %v, want Welt", u.reiter)
	}
	u.d.mu.Lock()
	dr := u.d.reiter
	u.d.mu.Unlock()
	if dr != reiterWelt {
		t.Errorf("Datenschicht kennt Reiter %v, want Welt", dr)
	}

	// Rückfrage: Escape bricht ab, nichts wird geschickt.
	g := beispielStaende(jetzt)
	u.welt.frageZurueckholen(u, g.Worlds[0], g.Worlds[0].Generations[2], false, jetzt)
	bild()
	r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	bild()
	if u.rf != nil {
		t.Fatal("Escape schließt die Rückfrage nicht")
	}
	if got := q.liste(); len(got) != 1 {
		t.Fatalf("nach Abbrechen: Aktionen %q", got)
	}

	// Rückfrage bestätigen: Knöpfe der Rückfrage sind die einzigen mit
	// Fokus (der Rest ist gesperrt): Abbrechen, Zurückholen.
	u.welt.frageZurueckholen(u, g.Worlds[0], g.Worlds[0].Generations[2], false, jetzt)
	r = input.Router{}
	bild()
	vor(2)
	if !r.Source().Focused(&u.rfJa) {
		t.Fatal("Fokus nach zweimal Tab nicht auf „Zurückholen“ (nur die Rückfrage darf Fokus bekommen)")
	}
	r.ClickFocus()
	bild()
	got := warteAktionen(2)
	if len(got) != 2 || got[1] != `zurueckholen {"world_id":"w-2026-11-03","number":410}` {
		t.Fatalf("Zurückholen: Aktionen %q", got)
	}
	if u.rf != nil {
		t.Error("Rückfrage nach Bestätigen noch offen")
	}
}

// bedienTest ist eine Oberfläche mit Eingabe-Router und Aktions-Attrappe.
type bedienTest struct {
	t     *testing.T
	u     *oberflaeche
	q     *aktionsQuelle
	r     input.Router
	ops   op.Ops
	jetzt time.Time
}

func neuerBedienTest(t *testing.T, s schnappschuss, r reiter) *bedienTest {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	b := &bedienTest{t: t, q: &aktionsQuelle{}, jetzt: jetzt}
	b.u = testOberflaeche(t, false, ansicht{s: s, reiter: r}, jetzt)
	b.u.d.c = b.q
	return b
}

func (b *bedienTest) bild() {
	gtx := testKontext(&b.ops, image.Pt(900, 620), 1, b.jetzt)
	gtx.Source = b.r.Source()
	b.u.layout(gtx)
	b.r.Frame(&b.ops)
}

func (b *bedienTest) warte(n int) []string {
	b.t.Helper()
	ende := time.Now().Add(2 * time.Second)
	for len(b.q.liste()) < n && time.Now().Before(ende) {
		time.Sleep(5 * time.Millisecond)
	}
	b.u.d.abarbeiten()
	return b.q.liste()
}

// „Spiel für mich beenden“ beim Hosten fragt erst nach (Text wie im Tray,
// mit dem Hinweis zum Dauerbetrieb); erst „Beenden“ schickt die Aktion.
func TestBeendenFragtNach(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	z := beispielZustand(jetzt, false, true)
	z.Hosting, z.Phase, z.Profil.Dauerbetrieb = true, "Hostet", true
	b := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z}, reiterStart)
	b.bild()
	b.u.start.beenden.Click()
	b.bild()
	if b.u.rf == nil {
		t.Fatal("keine Rückfrage vor dem Beenden")
	}
	for _, want := range []string{"online sind: Mara, Jonas, Lukas", "Dauerbetrieb ist an: Er pausiert danach", "Jetzt beenden?"} {
		if !strings.Contains(b.u.rf.text, want) {
			t.Errorf("Rückfrage ohne %q: %q", want, b.u.rf.text)
		}
	}
	if b.u.rf.nein != "Weiterhosten" || b.u.rf.ja != "Beenden" {
		t.Errorf("Knöpfe %q/%q", b.u.rf.ja, b.u.rf.nein)
	}
	b.bild()
	if got := b.q.liste(); len(got) != 0 {
		t.Fatalf("vor dem Bestätigen geschickt: %q", got)
	}
	b.u.rfJa.Click()
	b.bild()
	if got := b.warte(1); len(got) != 1 || got[0] != "beenden null" {
		t.Fatalf("nach Bestätigen: %q", got)
	}

	// Mitspielen: ohne Rückfrage (wie im Tray).
	z2 := beispielZustand(jetzt, false, true)
	b2 := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z2}, reiterStart)
	b2.bild()
	b2.u.start.beenden.Click()
	b2.bild()
	if b2.u.rf != nil {
		t.Fatal("Rückfrage beim Mitspielen")
	}
	if got := b2.warte(1); len(got) != 1 || got[0] != "beenden null" {
		t.Fatalf("Mitspielen beenden: %q", got)
	}
}

// Nach dem Speichern springen Formular (Hosten) und Schalter (Mitglieder)
// nicht auf den alten Stand zurück, solange der Schnappschuss ihn noch zeigt.
func TestGespeichertBleibt(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	z := beispielZustand(jetzt, true, false)
	alt := z.Profil
	b := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z, mitglieder: beispielMitglieder()}, reiterHosten)
	b.bild()
	// Schalter Dauerbetrieb per Tastatur: fünf Reiter, C2ME, Dauerbetrieb.
	for range 7 {
		b.r.MoveFocus(key.FocusForward)
	}
	if !b.r.Source().Focused(&b.u.hosten.dauer) {
		t.Fatal("Fokus nicht auf dem Schalter Dauerbetrieb")
	}
	b.r.ClickFocus()
	b.bild()
	if !b.u.hosten.form.Dauerbetrieb {
		t.Fatal("Schalter Dauerbetrieb setzt das Formular nicht")
	}
	b.u.hosten.form.XmxMB = 8192
	b.u.hosten.speichern.Click()
	b.bild()
	got := b.warte(1)
	if len(got) != 1 || !strings.Contains(got[0], `"xmx_mb":8192`) || !strings.Contains(got[0], `"dauerbetrieb":true`) {
		t.Fatalf("profil: %q", got)
	}
	for range 3 {
		b.bild() // Schnappschuss zeigt noch das alte Profil
	}
	if f := b.u.hosten.form; f.XmxMB != 8192 || !f.Dauerbetrieb || !b.u.hosten.dauer.Value {
		t.Fatalf("Formular sprang zurück: %+v", f)
	}
	// Die Instanz meldet das neue Profil: Merker erledigt; ändert es sich
	// danach von außen, folgt das Formular.
	neu := alt
	neu.XmxMB, neu.Dauerbetrieb = 8192, true
	z2 := *z
	z2.Profil = neu
	b.u.d.mu.Lock()
	b.u.d.s.z = &z2
	b.u.d.mu.Unlock()
	b.bild()
	z3 := z2
	z3.Profil.XmxMB = 4096
	b.u.d.mu.Lock()
	b.u.d.s.z = &z3
	b.u.d.mu.Unlock()
	b.bild()
	if b.u.hosten.form.XmxMB != 4096 {
		t.Fatalf("Formular folgt dem Zustand nicht mehr: %+v", b.u.hosten.form)
	}

	// Mitglieder: Admin-Schalter von Jonas (erster nach Namen) umlegen.
	b.u.reiter = reiterMitglieder
	b.r = input.Router{}
	b.bild()
	for range 7 { // fünf Reiter, „ändern“ von Jonas, dann sein Admin-Schalter
		b.r.MoveFocus(key.FocusForward)
	}
	b.r.ClickFocus()
	b.bild()
	got = b.warte(2)
	if len(got) != 2 || got[1] != `rolle {"email":"jonas.berger@example.de","admin":true,"op":false}` {
		t.Fatalf("rolle: %q", got)
	}
	for range 3 {
		b.bild() // Liste zeigt Jonas noch ohne Admin
	}
	if !b.u.mitgl.admin["jonas.berger@example.de"].Value {
		t.Fatal("Admin-Schalter sprang zurück")
	}
}

// „Speichern“ im Hosten-Formular nimmt den Dauerbetrieb aus dem neuesten
// Zustand, solange der Schalter im Formular nicht umgelegt wurde: Ein
// Umschalten im Tray bleibt erhalten. Wurde er umgelegt, gilt das Formular.
func TestSpeichernNimmtNeuestenDauerbetrieb(t *testing.T) {
	if p := speicherProfil(api.Profile{XmxMB: 8192}, api.Profile{Dauerbetrieb: true}, false); !p.Dauerbetrieb || p.XmxMB != 8192 {
		t.Errorf("unverändert: %+v", p)
	}
	if p := speicherProfil(api.Profile{Dauerbetrieb: false}, api.Profile{Dauerbetrieb: true}, true); p.Dauerbetrieb {
		t.Errorf("umgelegt: %+v", p)
	}

	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	z := beispielZustand(jetzt, true, false)
	z.Profil.Dauerbetrieb = false
	b := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z, mitglieder: beispielMitglieder()}, reiterHosten)
	b.bild()
	b.u.hosten.form.XmxMB = 8192 // im Formular geändert
	b.bild()
	// Der Tray schaltet den Dauerbetrieb ein.
	z2 := *z
	z2.Profil.Dauerbetrieb = true
	b.u.d.mu.Lock()
	b.u.d.s.z = &z2
	b.u.d.mu.Unlock()
	b.bild()
	b.u.hosten.speichern.Click()
	b.bild()
	got := b.warte(1)
	if len(got) != 1 || !strings.Contains(got[0], `"xmx_mb":8192`) || !strings.Contains(got[0], `"dauerbetrieb":true`) {
		t.Fatalf("profil nach Tray-Umschalten: %q", got)
	}
}

// Mit Mitspielern fragt „Spiel für mich beenden“ nach der Übergabe;
// „Hosten ohne Weitergabe beenden“ nur beim Hosten, mit der alten Rückfrage
// und eigener Aktion; eine laufende Übergabe steht im Start-Reiter.
func TestBeendenUebergabeUndOhneWeitergabe(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	z := beispielZustand(jetzt, false, true)
	z.Hosting, z.Phase = true, "Hostet"
	z.Status.Handover = &api.HandoverState{State: api.HandoverStarting, To: api.HostInfo{MinecraftName: "Mara"}}
	if !gibtWeiter(z) {
		t.Fatal("gibtWeiter: andere Spieler sind online")
	}
	b := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z}, reiterStart)
	b.bild()
	b.u.start.beenden.Click()
	b.bild()
	if b.u.rf == nil || !strings.Contains(b.u.rf.text, "an den besten PC der Mitspieler übergeben") {
		t.Fatalf("Rückfrage ohne Übergabe: %+v", b.u.rf)
	}
	b.u.rfJa.Click()
	b.bild()
	if got := b.warte(1); len(got) != 1 || got[0] != "beenden null" {
		t.Fatalf("Aktion: %q", got)
	}

	b2 := neuerBedienTest(t, schnappschuss{verb: verbunden, runde: 1, z: z}, reiterStart)
	b2.bild()
	b2.u.start.beendenOhne.Click()
	b2.bild()
	if b2.u.rf == nil || strings.Contains(b2.u.rf.text, "übergeben") || !strings.Contains(b2.u.rf.text, "Der Server stoppt dann für alle") {
		t.Fatalf("Rückfrage ohne Weitergabe: %+v", b2.u.rf)
	}
	b2.u.rfJa.Click()
	b2.bild()
	if got := b2.warte(1); len(got) != 1 || got[0] != "beendenohne null" {
		t.Fatalf("Aktion: %q", got)
	}

	// Allein im Spiel: keine Übergabe in der Rückfrage.
	z.Status.Players = []string{"Lukas"}
	if gibtWeiter(z) {
		t.Fatal("gibtWeiter ohne Mitspieler")
	}
	if strings.Contains(beendenFrage(z, gibtWeiter(z)), "übergeben") {
		t.Fatal("Übergabe in der Rückfrage ohne Mitspieler")
	}
}

// Beitrittsanfragen: Annehmen und Ablehnen fragen erst nach; erst die
// Bestätigung schickt die Aktion mit der richtigen E-Mail, und die Anfrage
// bleibt danach verborgen, bis die Liste sie nicht mehr zeigt.
func TestBeitrittsanfragenBedienung(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	z := beispielZustand(jetzt, true, true)
	s := schnappschuss{verb: verbunden, runde: 1, z: z, mitglieder: beispielMitglieder(), anfragen: beispielAnfragen(jetzt)}
	b := neuerBedienTest(t, s, reiterMitglieder)
	b.bild()

	// Fokus: fünf Reiter, dann „Annehmen“ der ersten Anfrage.
	for range 6 {
		b.r.MoveFocus(key.FocusForward)
	}
	b.r.ClickFocus()
	b.bild()
	if b.u.rf == nil || b.u.rf.ja != "Annehmen" || !strings.Contains(b.u.rf.text, "Tim_Redstone") {
		t.Fatalf("keine Rückfrage zum Annehmen: %+v", b.u.rf)
	}
	if got := b.q.liste(); len(got) != 0 {
		t.Fatalf("vor der Bestätigung schon Aktionen: %q", got)
	}
	b.u.rf.beiJa()
	b.u.rf = nil
	got := b.warte(1)
	if len(got) != 1 || got[0] != `beitritt {"email":"tim@example.de","annehmen":true}` {
		t.Fatalf("Aktionen: %q", got)
	}
	b.bild()
	if sicht := b.u.mitgl.anfr.sichtbar(s.anfragen); len(sicht) != 1 || sicht[0].Email == "tim@example.de" {
		t.Fatalf("bearbeitete Anfrage noch sichtbar: %+v", sicht)
	}
	// Die Liste ohne sie: Merker wird aufgeräumt.
	b.u.mitgl.anfr.sichtbar(s.anfragen[1:])
	if b.u.mitgl.anfr.erledigt["tim@example.de"] {
		t.Error("Merker bleibt, obwohl die Anfrage aus der Liste verschwand")
	}

	// Ablehnen: Rückfrage mit Warnfarbe, Aktion mit annehmen=false.
	b.u.mitgl.anfr.klick(&b.u.mitgl.anfr.ablehnen, "tim@example.de").Click()
	b.bild()
	if b.u.rf == nil || !b.u.rf.gefahr || b.u.rf.ja != "Ablehnen" {
		t.Fatalf("keine Rückfrage zum Ablehnen: %+v", b.u.rf)
	}
	b.u.rf.beiJa()
	got = b.warte(2)
	if len(got) != 2 || got[1] != `beitritt {"email":"tim@example.de","annehmen":false}` {
		t.Fatalf("Aktionen: %q", got)
	}
}
