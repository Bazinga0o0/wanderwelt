package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

var (
	fortSince = time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	fortJetzt = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
)

func hostMerker() *merker {
	return &merker{Since: fortSince, World: "w1", Rolle: fensterapi.RolleHost}
}

func fensterStatus(opensIn, untilIn int64) *api.Status {
	return &api.Status{Resume: &api.Resume{Since: fortSince, OpensInS: opensIn, UntilInS: untilIn}}
}

// Die Entscheidung zu einem frischen Status: Bewerbung nur ohne Wartung und
// mit Fenster zur Kennung des Merkers; ein Koordinator ohne Fenster (älter,
// verfallen) oder eine andere Wartung verwirft den Merker; ein Mitspieler
// merkt sich die Wartung, sobald der Status sie nennt.
func TestFortPruefe(t *testing.T) {
	wartung := &api.Maintenance{Reason: "Update", Since: fortSince}
	andere := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name      string
		m         *merker
		st        *api.Status
		mitspielt bool
		want      fortAktion
	}{
		{"Fenster zur Kennung", hostMerker(), fensterStatus(60, 600), false, fortStarten},
		{"Fenster schon begonnen", hostMerker(), fensterStatus(0, 300), false, fortStarten},
		{"älterer Koordinator ohne Fenster", hostMerker(), &api.Status{}, false, fortWeg},
		{"Fenster einer anderen Wartung", &merker{Since: andere}, fensterStatus(60, 600), false, fortWeg},
		{"ohne Merker nichts", nil, fensterStatus(60, 600), false, fortNichts},
		{"Wartung läuft, Host-Merker bleibt", hostMerker(), &api.Status{Maintenance: wartung}, false, fortNichts},
		{"Wartung läuft, Mitspieler merkt", nil, &api.Status{Maintenance: wartung}, true, fortMitspiel},
		{"Mitspieler hat schon", hostMerker(), &api.Status{Maintenance: wartung}, true, fortNichts},
		{"Wartung läuft, kein Mitspieler, kein Merker", nil, &api.Status{Maintenance: wartung}, false, fortNichts},
		{"Merker einer früheren Wartung", &merker{Since: andere}, &api.Status{Maintenance: wartung}, false, fortWeg},
		{"Wartung ohne Kennung merkt nicht", nil, &api.Status{Maintenance: &api.Maintenance{Reason: "x"}}, true, fortNichts},
		{"Status unbekannt", hostMerker(), nil, false, fortNichts},
	} {
		if got, why := fortPruefe(c.m, c.st, c.mitspielt); got != c.want {
			t.Errorf("%s: %v (%s), erwartet %v", c.name, got, why, c.want)
		}
	}
}

// Kurz vor der Bewerbung gelten die Bedingungen noch einmal: kein anderer
// Host, keine Übergabe, Fenster nicht verfallen, Wartung vorbei, Minecraft
// nicht vom Nutzer beendet.
func TestFortBedingungen(t *testing.T) {
	m := *hostMerker()
	bis := fortJetzt.Add(10 * time.Minute)
	ok := fensterStatus(0, 300)
	with := func(f func(*api.Status)) *api.Status {
		s := *ok
		f(&s)
		return &s
	}
	for _, c := range []struct {
		name string
		st   *api.Status
		now  time.Time
		mc   bool
		fail string // "" = bewerben
	}{
		{"alles gut", ok, fortJetzt, false, ""},
		{"anderer Host", with(func(s *api.Status) { s.Host = &api.HostInfo{Email: "x@y"} }), fortJetzt, false, "hostet schon"},
		{"Übergabe läuft", with(func(s *api.Status) { s.Handover = &api.HandoverState{State: api.HandoverReady} }), fortJetzt, false, "hostet schon"},
		{"Wartung wieder an", with(func(s *api.Status) { s.Maintenance = &api.Maintenance{Since: fortSince} }), fortJetzt, false, "Wartung"},
		{"Fenster weg", &api.Status{}, fortJetzt, false, "gilt nicht"},
		{"andere Wartung", &api.Status{Resume: &api.Resume{Since: fortSince.Add(time.Hour)}}, fortJetzt, false, "gilt nicht"},
		{"verfallen", ok, bis, false, "verfallen"},
		{"Minecraft beendet", ok, fortJetzt, true, "Minecraft"},
		{"Status unbekannt", nil, fortJetzt, false, "unbekannt"},
	} {
		err := fortBedingungen(m, c.st, bis, c.now, c.mc)
		switch {
		case c.fail == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.fail != "" && (err == nil || !strings.Contains(err.Error(), c.fail)):
			t.Errorf("%s: %v, erwartet Text mit %q", c.name, err, c.fail)
		case err != nil && !strings.Contains(err.Error(), errFortsetzenEntfaellt.Error()):
			t.Errorf("%s: kein errFortsetzenEntfaellt: %v", c.name, err)
		}
	}
}

// Minecraft zählt nur, wenn es beim Merken lief und jetzt nachweislich nicht
// mehr; nicht feststellbar oder nie gelaufen (Dauerbetrieb-PC) ist kein Abbruch.
func TestMcBeendet(t *testing.T) {
	old := minecraftLaeuft
	t.Cleanup(func() { minecraftLaeuft = old })
	for _, c := range []struct {
		lief, running, known, want bool
	}{
		{true, false, true, true},
		{true, true, true, false},
		{true, false, false, false},
		{false, false, true, false},
	} {
		minecraftLaeuft = func() (bool, bool) { return c.running, c.known }
		if got := mcBeendet(merker{SpielLief: c.lief}); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// Der Merker steht im Gruppenordner und übersteht einen Neustart des
// Starters; ein Host löst den Mitspieler ab, nicht umgekehrt; „verwerfen“
// löscht die Datei.
func TestFortMerkerPersistenz(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	old := minecraftLaeuft
	minecraftLaeuft = func() (bool, bool) { return true, true }
	t.Cleanup(func() { minecraftLaeuft = old })

	a := &App{ctx: t.Context()}
	a.fortMerken(fensterapi.RolleMitspieler, "w1", fortSince)
	if _, err := os.Stat(fortPath()); err != nil {
		t.Fatalf("Datei fehlt: %v", err)
	}
	a.fortMerken(fensterapi.RolleHost, "w1", fortSince)
	b := &App{ctx: t.Context()} // neuer Start liest die Datei
	z := b.fortZustand()
	if z == nil || z.Rolle != fensterapi.RolleHost {
		t.Fatalf("nach Neustart: %+v", z)
	}
	if !b.fort.m.SpielLief {
		t.Error("SpielLief nicht gemerkt")
	}
	b.fortMerken(fensterapi.RolleMitspieler, "w1", fortSince) // darf den Host nicht ablösen
	if b.fortZustand().Rolle != fensterapi.RolleHost {
		t.Error("Mitspieler hat den Host abgelöst")
	}
	b.fortMerken(fensterapi.RolleHost, "w1", time.Time{}) // ohne Kennung nichts
	if !b.fortVerwerfen("Test") || b.fortPending() {
		t.Fatal("nicht verworfen")
	}
	if _, err := os.Stat(fortPath()); err == nil {
		t.Error("Datei nach dem Verwerfen noch da")
	}
	if b.fortVerwerfen("noch einmal") {
		t.Error("zweites Verwerfen meldet etwas")
	}
	// eine kaputte Datei gilt als kein Merker
	if err := os.WriteFile(fortPath(), []byte("{kaputt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := (&App{ctx: t.Context()}); c.fortPending() {
		t.Error("kaputte Datei gilt als Merker")
	}
}

// fakeFortPlay ersetzt die Bewerbung: zählt Aufrufe, liefert die fp der
// Aufrufe, wartet auf gate oder ctx.
type fakeFortPlay struct {
	calls atomic.Int32
	gate  chan struct{}
	last  atomic.Pointer[fortPlay]
}

func installFakeFortPlay(t *testing.T) *fakeFortPlay {
	t.Helper()
	f := &fakeFortPlay{gate: make(chan struct{})}
	old := fortPlayFn
	fortPlayFn = func(_ *App, ctx context.Context, _ int, _ bool, fp *fortPlay) error {
		f.calls.Add(1)
		f.last.Store(fp)
		select {
		case <-f.gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { fortPlayFn = old })
	return f
}

func fortApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	return &App{ctx: t.Context(), c: replayCoord(t)}
}

// Ein Merker plus Fenster zur selben Wartung startet genau eine Bewerbung
// (Aktion „Fortsetzen“ belegt, Beginn nach der Uhr des Starters); nach ihr
// ist der Merker verbraucht; ein zweiter Status startet nichts mehr.
func TestFortNoteStartetEinmalUndVerbraucht(t *testing.T) {
	f := installFakeFortPlay(t)
	a := fortApp(t)
	a.fortMerken(fensterapi.RolleHost, "w1", fortSince)
	old := fortNow
	fortNow = func() time.Time { return fortJetzt }
	t.Cleanup(func() { fortNow = old })

	if !a.fortNote(fensterStatus(60, 600)) {
		t.Fatal("fortNote meldet kein Fortsetzen")
	}
	waitFor(t, "Bewerbung", func() bool { return f.calls.Load() == 1 })
	if b := busyOf(a); b != fortLabel {
		t.Fatalf("Aktion %q", b)
	}
	fp := f.last.Load()
	if fp == nil || !fp.opens.Equal(fortJetzt.Add(60*time.Second)) || !fp.bis.Equal(fortJetzt.Add(600*time.Second)) || fp.m.World != "w1" {
		t.Fatalf("Bewerbung: %+v", fp)
	}
	if z := a.fortZustand(); z == nil || !z.Laeuft || !z.Ab.Equal(fp.opens) {
		t.Fatalf("Fensterzustand: %+v", z)
	}
	a.fortNote(fensterStatus(55, 595)) // läuft schon: keine zweite
	time.Sleep(30 * time.Millisecond)
	if f.calls.Load() != 1 {
		t.Fatalf("%d Bewerbungen", f.calls.Load())
	}
	close(f.gate)
	waitFor(t, "Ende", func() bool { return busyOf(a) == "" })
	if a.fortPending() {
		t.Error("Merker nach der Bewerbung noch da")
	}
	a.fortNote(fensterStatus(0, 500))
	time.Sleep(30 * time.Millisecond)
	if f.calls.Load() != 1 {
		t.Fatalf("zweite Bewerbung nach dem Verbrauchen: %d", f.calls.Load())
	}
}

// Ohne passendes Fenster (älterer Koordinator, andere Wartung, Wartung läuft
// noch) startet nichts; im ersten Fall ist der Merker weg, im letzten bleibt er.
func TestFortNoteOhneFensterStartetNichts(t *testing.T) {
	f := installFakeFortPlay(t)
	a := fortApp(t)
	a.fortMerken(fensterapi.RolleMitspieler, "", fortSince)

	if !a.fortNote(&api.Status{Maintenance: &api.Maintenance{Since: fortSince}}) {
		t.Error("während der Wartung gilt der Merker")
	}
	if !a.fortPending() || f.calls.Load() != 0 {
		t.Fatal("Merker oder Start während der Wartung falsch")
	}
	if a.fortNote(&api.Status{}) { // Wartung vorbei, aber kein Fenster: alter Koordinator
		t.Error("meldet Fortsetzen ohne Fenster")
	}
	if a.fortPending() || f.calls.Load() != 0 {
		t.Fatal("Merker ohne Fenster nicht verworfen")
	}
}

// Läuft schon etwas (hier: ein anderer Vorgang), wartet die Bewerbung auf den
// nächsten Status; hostet dieser PC schon, ist der Merker weg.
func TestFortStartWartetAufFreienStarter(t *testing.T) {
	f := installFakeFortPlay(t)
	a := fortApp(t)
	a.fortMerken(fensterapi.RolleHost, "", fortSince)
	a.busy = "Spielen"
	a.fortNote(fensterStatus(0, 500))
	time.Sleep(30 * time.Millisecond)
	if f.calls.Load() != 0 || !a.fortPending() {
		t.Fatal("startete trotz laufender Aktion oder verwarf den Merker")
	}
	a.mu.Lock()
	a.busy = ""
	a.hostCancel = func() {}
	a.mu.Unlock()
	a.fortNote(fensterStatus(0, 500))
	if a.fortPending() || f.calls.Load() != 0 {
		t.Fatal("hostet schon: Merker nicht verworfen")
	}
}

// „Fortsetzen abbrechen“ stoppt die laufende Bewerbung und löscht den Merker;
// ein ausdrückliches „Spielen“ tut dasselbe und startet dann normal.
func TestFortAbbrechenUndSpielenErsetzen(t *testing.T) {
	notes := countNotify(t)
	f := installFakeFortPlay(t)
	a := fortApp(t)
	a.fortMerken(fensterapi.RolleHost, "", fortSince)
	a.fortNote(fensterStatus(60, 600))
	waitFor(t, "Bewerbung", func() bool { return f.calls.Load() == 1 })

	a.fortAbbrechen()
	waitFor(t, "Ende der Bewerbung", func() bool { return busyOf(a) == "" })
	if a.fortPending() || a.fortRunning() {
		t.Fatal("Merker oder Bewerbung nach dem Abbrechen noch da")
	}
	waitFor(t, "Hinweis", func() bool { return len(notes()) == 1 })
	if !strings.Contains(notes()[0], "nicht fort") {
		t.Errorf("Hinweis %q", notes()[0])
	}
	a.fortAbbrechen() // ohne Merker: kein zweiter Hinweis
	time.Sleep(30 * time.Millisecond)
	if len(notes()) != 1 {
		t.Errorf("Hinweise %q", notes())
	}
}

// Der Dauerbetrieb startet nicht von selbst, solange der Beginn des Fensters
// noch aussteht (sonst gewänne der erste Takt das Sammelfenster allein);
// danach verhält er sich wie bisher.
func TestFortBlocksAutoBisBeginn(t *testing.T) {
	a := fortApp(t)
	old := fortNow
	now := fortJetzt
	fortNow = func() time.Time { return now }
	t.Cleanup(func() { fortNow = old })
	if a.fortBlocksAuto() {
		t.Fatal("ohne Fenster gesperrt")
	}
	a.fortNote(fensterStatus(60, 600))
	if !a.fortBlocksAuto() {
		t.Fatal("vor dem Beginn nicht gesperrt")
	}
	a.status = &api.Status{} // sicher niemand hostet
	a.dauer.nextTry = time.Time{}
	if a.autoPlay() {
		t.Fatal("autoPlay vor dem Beginn des Fensters")
	}
	now = fortJetzt.Add(61 * time.Second)
	if a.fortBlocksAuto() {
		t.Fatal("nach dem Beginn noch gesperrt")
	}
}

// Kurz vor der Bewerbung wartet der Starter auf den Beginn und prüft dann
// mit frischem Status: hostet inzwischen jemand, entfällt sie; sonst geht
// sie los.
func TestFortWarten(t *testing.T) {
	var hosted atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := fensterStatus(0, 500)
		if hosted.Load() {
			st.Host = &api.HostInfo{Email: "x@y", MinecraftName: "X"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st)
	}))
	t.Cleanup(ts.Close)
	c := client.New(&group.Group{Coordinator: ts.URL})
	a := &App{ctx: t.Context(), c: c}

	oldNow, oldAfter := fortNow, fortAfter
	fortNow = func() time.Time { return fortJetzt }
	var waited atomic.Int64
	ch := make(chan time.Time, 1)
	fortAfter = func(d time.Duration) <-chan time.Time { waited.Store(int64(d)); return ch }
	t.Cleanup(func() { fortNow, fortAfter = oldNow, oldAfter })

	fp := &fortPlay{m: *hostMerker(), opens: fortJetzt.Add(40 * time.Second), bis: fortJetzt.Add(600 * time.Second)}
	errc := make(chan error, 1)
	go func() { errc <- a.fortWarten(t.Context(), c, fp) }()
	waitFor(t, "Warten", func() bool { return waited.Load() != 0 })
	if d := time.Duration(waited.Load()); d != 40*time.Second {
		t.Fatalf("wartet %v, erwartet 40 s", d)
	}
	select {
	case err := <-errc:
		t.Fatalf("kehrte vor dem Beginn zurück: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	ch <- fortJetzt
	if err := <-errc; err != nil {
		t.Fatalf("keine Bewerbung trotz freiem Status: %v", err)
	}

	hosted.Store(true)
	fp.opens = fortJetzt // schon begonnen: kein Warten
	if err := a.fortWarten(t.Context(), c, fp); err == nil || !strings.Contains(err.Error(), "hostet schon") {
		t.Fatalf("mit Host: %v", err)
	}
}

// Abwärtskompatibilität: Ohne Fenster trägt weder der Status noch die
// Anfrage ein neues Feld (ältere Koordinatoren lehnen unbekannte Felder ab;
// ältere Starter sehen nichts Neues); ein Status eines alten Koordinators
// liest sich ohne Fenster und verwirft den Merker.
func TestFortFelderAbwaertskompatibel(t *testing.T) {
	b, _ := json.Marshal(api.Status{})
	if strings.Contains(string(b), "resume") {
		t.Errorf("Status ohne Fenster: %s", b)
	}
	b, _ = json.Marshal(api.PlayRequest{MachineID: "pc"})
	if strings.Contains(string(b), "resume") {
		t.Errorf("Anfrage ohne Resume: %s", b)
	}
	b, _ = json.Marshal(api.PlayRequest{MachineID: "pc", Resume: true})
	if !strings.Contains(string(b), `"resume":true`) {
		t.Errorf("Anfrage mit Resume: %s", b)
	}
	var alt api.Status
	if err := json.Unmarshal([]byte(`{"host_ready":false,"players":[],"head_generation":3,"world_id":"w"}`), &alt); err != nil || alt.Resume != nil {
		t.Fatalf("alter Status: %v %+v", err, alt.Resume)
	}
	if got, _ := fortPruefe(hostMerker(), &alt, false); got != fortWeg {
		t.Errorf("alter Koordinator: %v, erwartet fortWeg", got)
	}
}

// Ein Mitspieler, dessen Status die Wartung nennt, merkt sie sich mit der
// Welt, die er beobachtet; ein Host nie als Mitspieler.
func TestFortNoteMerktMitspieler(t *testing.T) {
	a := fortApp(t)
	st := &api.Status{Maintenance: &api.Maintenance{Reason: "Update", Since: fortSince}}
	a.fortNote(st)
	if a.fortPending() {
		t.Fatal("Merker ohne Mitspielen")
	}
	a.mu.Lock()
	a.phase, a.joinWorld = phJoined, "w2"
	a.mu.Unlock()
	a.fortNote(st)
	z := a.fortZustand()
	if z == nil || z.Rolle != fensterapi.RolleMitspieler || a.fort.m.World != "w2" || !a.fort.m.Since.Equal(fortSince) {
		t.Fatalf("Mitspieler-Merker: %+v %+v", z, a.fort.m)
	}
}

// Der Text im Fenster nennt Rolle und Frist und enthält nur Klartext.
func TestFortsetzenText(t *testing.T) {
	titel, txt := fensterapi.FortsetzenText(&fensterapi.Fortsetzen{Rolle: fensterapi.RolleHost, Ab: fortJetzt, Bis: fortJetzt.Add(10 * time.Minute)}, fortJetzt)
	if titel == "" || !strings.Contains(txt, "gehostet") || !strings.Contains(txt, "Spätestens bis") {
		t.Errorf("%q %q", titel, txt)
	}
	_, txt = fensterapi.FortsetzenText(&fensterapi.Fortsetzen{Rolle: fensterapi.RolleMitspieler}, fortJetzt)
	if !strings.Contains(txt, "mitgespielt") || strings.Contains(txt, "Spätestens") {
		t.Errorf("%q", txt)
	}
}
