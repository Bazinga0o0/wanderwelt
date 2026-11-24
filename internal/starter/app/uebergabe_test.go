package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

// fakeHostRun ersetzt host.Run: merkt die Deps und läuft, bis ctx endet.
func fakeHostRun(t *testing.T) chan host.Deps {
	t.Helper()
	got := make(chan host.Deps, 4)
	old, oldPrep := hostRun, takeoverPrepare
	hostRun = func(ctx context.Context, d host.Deps) error {
		got <- d
		<-ctx.Done()
		return nil
	}
	takeoverPrepare = func(a *App, ctx context.Context) (playInput, error) {
		return playInput{c: a.c, g: a.g, st: a.st, m: &api.Manifest{MinecraftVersion: "26.2"},
			profile: api.Profile{XmxMB: 4096}}, nil
	}
	t.Cleanup(func() { hostRun, takeoverPrepare = old, oldPrep })
	return got
}

func takeoverApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	a := busyApp(t, nil)
	a.st = &group.State{MachineID: "pc-admin"}
	return a
}

// Ein Angebot startet eine Host-Sitzung mit Zuschlag, Stand und Ersatz-Port
// aus dem Angebot; ein zweites Angebot während der Sitzung wird nicht
// angenommen.
func TestOfferStartsHostWithReplacementPort(t *testing.T) {
	notes := countNotify(t)
	got := fakeHostRun(t)
	a := takeoverApp(t)
	var joinCanceled atomic.Bool
	a.joinCancel = func() { joinCanceled.Store(true) }
	a.phase = phJoined

	o := &api.HandoverOffer{HandoverID: 7, LeaseID: 41, WorldID: "w-1", HeadGeneration: 12, Port: 15566,
		From: api.HostInfo{MinecraftName: "Mara"}, MinecraftAddress: "mc.example.org"}
	a.takeOver(o)
	var d host.Deps
	select {
	case d = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("keine Host-Sitzung gestartet")
	}
	if d.TunnelPort != 15566 || d.LeaseID != 41 || d.Base != 12 || d.BaseWorldID != "w-1" {
		t.Errorf("Deps: Port %d, Zuschlag %d, Base %d/%s", d.TunnelPort, d.LeaseID, d.Base, d.BaseWorldID)
	}
	if d.Handover == nil || d.OnHandover == nil {
		t.Error("Übergabe-Haken fehlen")
	}
	if !joinCanceled.Load() {
		t.Error("Mitspiel-Beobachtung läuft weiter")
	}
	waitFor(t, "Hinweis", func() bool { return len(notes()) > 0 })
	if n := notes(); !strings.Contains(n[0], "Du übernimmst das Hosten von Mara") {
		t.Errorf("Hinweise: %q", n)
	}
	waitFor(t, "Aktion frei", func() bool { return busyOf(a) == "" })
	if !a.isHosting() {
		t.Fatal("hostet nicht")
	}
	a.takeOver(&api.HandoverOffer{HandoverID: 8, LeaseID: 42, Port: 15567})
	select {
	case <-got:
		t.Fatal("zweites Angebot angenommen")
	case <-time.After(100 * time.Millisecond):
	}
	a.endHosting("Test")
	waitFor(t, "Sitzung zu", func() bool { return !a.isHosting() && strings.Contains(strings.Join(notes(), "|"), "Hosten beendet") })
}

// Ein Angebot bei laufender Aktion wird nicht angenommen.
func TestOfferIgnoredWhileBusy(t *testing.T) {
	countNotify(t)
	got := fakeHostRun(t)
	a := takeoverApp(t)
	a.busy = "Spielen"
	a.takeOver(&api.HandoverOffer{HandoverID: 1, LeaseID: 2})
	select {
	case <-got:
		t.Fatal("Angebot trotz laufender Aktion angenommen")
	case <-time.After(100 * time.Millisecond):
	}
	if busyOf(a) != "Spielen" {
		t.Fatalf("Aktion: %q", busyOf(a))
	}
}

// Beenden als Host mit Mitspielern gibt die Welt weiter (Signal auf
// Deps.Handover, Server läuft weiter); ohne Mitspieler oder mit „ohne
// Weitergabe“ endet er wie heute.
func TestEndPlayingHandoverSignal(t *testing.T) {
	countNotify(t)
	oldConfirm := confirm
	t.Cleanup(func() { confirm = oldConfirm })
	var asked []string
	confirm = func(text, ja, nein string) bool { asked = append(asked, text); return true }

	me := &api.Me{Email: "lukas@example.org", MinecraftName: "Lukas"}
	status := func(players ...string) *api.Status {
		return &api.Status{Host: &api.HostInfo{Email: "lukas@example.org"}, Players: players}
	}
	for _, tc := range []struct {
		name      string
		players   []string
		plain     bool
		wantSig   bool
		wantStop  bool
		wantAsked string
	}{
		{"mit Mitspielern", []string{"Lukas", "Mara"}, false, true, false, "an den besten PC der Mitspieler übergeben"},
		{"allein", []string{"Lukas"}, false, false, true, "Der Server stoppt dann für alle"},
		{"ohne Weitergabe", []string{"Lukas", "Mara"}, true, false, true, "Der Server stoppt dann für alle"},
	} {
		asked = nil
		a := takeoverApp(t)
		a.me, a.status = me, status(tc.players...)
		var stopped atomic.Bool
		a.hostCancel = func() { stopped.Store(true) }
		ho := a.uebergabe.newSession()
		a.endPlayingMode(true, !tc.plain)
		sig := false
		select {
		case <-ho:
			sig = true
		default:
		}
		if sig != tc.wantSig || stopped.Load() != tc.wantStop {
			t.Errorf("%s: Signal %v, gestoppt %v", tc.name, sig, stopped.Load())
		}
		if len(asked) != 1 || !strings.Contains(asked[0], tc.wantAsked) {
			t.Errorf("%s: Rückfrage %q", tc.name, asked)
		}
	}

	// Rückfrage verneint: nichts passiert.
	confirm = func(string, string, string) bool { return false }
	a := takeoverApp(t)
	a.me, a.status = me, status("Lukas", "Mara")
	a.hostCancel = func() { t.Error("gestoppt") }
	ho := a.uebergabe.newSession()
	a.endPlayingMode(true, true)
	select {
	case <-ho:
		t.Error("Signal trotz Nein")
	default:
	}
}

// OnHandover: Hinweise und Merker für das Ende der Sitzung.
func TestOnHandoverNotices(t *testing.T) {
	notes := countNotify(t)
	a := takeoverApp(t)
	a.uebergabe.newSession()
	a.onHandover(api.HandoverPreparing, "Mara")
	a.onHandover(api.HandoverReady, "Mara")
	if g := a.uebergabe.endSession(); g != "Mara" {
		t.Fatalf("übergeben an %q", g)
	}
	a.uebergabe.newSession()
	a.onHandover(api.HandoverReady, "Mara")
	a.onHandover(api.HandoverFailed, "Mara")
	a.onHandover(stateNobody, "")
	if g := a.uebergabe.endSession(); g != "" {
		t.Fatalf("nach gescheitert übergeben an %q", g)
	}
	time.Sleep(50 * time.Millisecond)
	all := strings.Join(notes(), "|")
	for _, want := range []string{"Übergabe an Mara …", "Übergabe gescheitert – Server stoppt normal.", "Niemand kann übernehmen – Server stoppt normal."} {
		if !strings.Contains(all, want) {
			t.Errorf("Hinweis %q fehlt: %s", want, all)
		}
	}
}

// Bereitschaft: nur mit Mitspielen (oder Dauerbetrieb), nur ohne
// KeineUebernahme, Messwert einmal gemessen und gemerkt, Fehler höchstens
// einmal je Minute im Log, Angebot startet den Host.
func TestPresenceTick(t *testing.T) {
	notes := countNotify(t)
	fakeHostRun(t)
	var calls atomic.Int32
	var mu sync.Mutex
	var last api.PresenceRequest
	var offer, fail atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/presence" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if fail.Load() {
			http.Error(w, `{"error":"unbekannt"}`, http.StatusNotFound)
			return
		}
		mu.Lock()
		last = api.PresenceRequest{}
		json.NewDecoder(r.Body).Decode(&last)
		mu.Unlock()
		resp := api.PresenceResponse{}
		if offer.Load() {
			resp.Handover = &api.HandoverOffer{HandoverID: 1, LeaseID: 9, Port: 15566, From: api.HostInfo{MinecraftName: "Mara"}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)

	var measured atomic.Int32
	oldM := measureScore
	measureScore = func(context.Context, *client.Client, int) (api.Score, error) {
		measured.Add(1)
		return api.Score{UploadMbps: 50}, nil
	}
	t.Cleanup(func() { measureScore = oldM })
	var logbuf bytes.Buffer
	log.SetOutput(&logbuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	var now atomic.Int64
	now.Store(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC).UnixNano())
	oldN := presenceNow
	presenceNow = func() time.Time { return time.Unix(0, now.Load()) }
	t.Cleanup(func() { presenceNow = oldN })
	oldAC := onACPower
	onACPower = func() bool { return true }
	t.Cleanup(func() { onACPower = oldAC })

	a := takeoverApp(t)
	a.c = client.New(&group.Group{Coordinator: ts.URL})
	prof := bench.DefaultProfile()
	if err := bench.SaveProfile(prof); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	a.presenceTick(ctx) // weder mitspielend noch Dauerbetrieb
	if calls.Load() != 0 {
		t.Fatal("Bereitschaft ohne Mitspielen")
	}
	a.phase = phJoined
	a.presenceTick(ctx)
	a.presenceTick(ctx)
	mu.Lock()
	ok := last.Joined && last.MachineID == "pc-admin" && last.Score.UploadMbps == 50
	mu.Unlock()
	if calls.Load() != 2 || measured.Load() != 1 || !ok {
		t.Fatalf("Aufrufe %d, Messungen %d, Anfrage %+v", calls.Load(), measured.Load(), last)
	}

	// Auch Stunden später wird nicht mitten im Spiel neu gemessen.
	now.Add(int64(3 * time.Hour))
	a.presenceTick(ctx)
	if measured.Load() != 1 {
		t.Fatalf("%d Messungen", measured.Load())
	}
	calls.Add(-1) // dieser Aufruf zählt für die Prüfungen unten nicht
	a.phase = phReady
	a.dauer.on.Store(true)
	a.presenceTick(ctx)
	mu.Lock()
	joined := last.Joined
	mu.Unlock()
	if calls.Load() != 3 || joined {
		t.Fatalf("Dauerbetrieb: Aufrufe %d, Joined %v", calls.Load(), joined)
	}
	a.busy = "Spielen"
	a.presenceTick(ctx)
	a.busy = ""
	if calls.Load() != 3 {
		t.Fatal("Bereitschaft trotz Aktion")
	}
	prof.KeineUebernahme = true
	bench.SaveProfile(prof)
	a.presenceTick(ctx)
	if calls.Load() != 3 {
		t.Fatal("Bereitschaft trotz KeineUebernahme")
	}
	prof.KeineUebernahme = false
	bench.SaveProfile(prof)

	fail.Store(true)
	for range 5 {
		a.presenceTick(ctx)
	}
	if n := strings.Count(logbuf.String(), "Bereitschaft: Presence"); n != 1 {
		t.Fatalf("%d Logzeilen für Fehler", n)
	}
	now.Add(int64(2 * time.Minute))
	a.presenceTick(ctx)
	if n := strings.Count(logbuf.String(), "Bereitschaft: Presence"); n != 2 {
		t.Fatalf("nach einer Minute %d Logzeilen", n)
	}
	fail.Store(false)

	offer.Store(true)
	a.presenceTick(ctx)
	waitFor(t, "Host gestartet", a.isHosting)
	a.endHosting("Test")
	waitFor(t, "Sitzung zu", func() bool {
		return !a.isHosting() && busyOf(a) == "" && strings.Contains(strings.Join(notes(), "|"), "Hosten beendet")
	})
}

// Scheitert die Übernahme nach einem Angebot, geht der Zuschlag sofort
// zurück (mit eigener MachineID); dasselbe Angebot wird nicht noch einmal
// angenommen.
func TestFailedTakeoverReleasesLease(t *testing.T) {
	countNotify(t)
	old := takeoverPrepare
	t.Cleanup(func() { takeoverPrepare = old })
	var mu sync.Mutex
	var rel []api.ReleaseRequest
	prepared := atomic.Int32{}
	takeoverPrepare = func(*App, context.Context) (playInput, error) {
		prepared.Add(1)
		return playInput{}, context.DeadlineExceeded
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/lease/release" {
			var q api.ReleaseRequest
			json.NewDecoder(r.Body).Decode(&q)
			mu.Lock()
			rel = append(rel, q)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	a := takeoverApp(t)
	a.c = client.New(&group.Group{Coordinator: ts.URL})
	o := &api.HandoverOffer{HandoverID: 5, LeaseID: 41, WorldID: "w", Port: 15566}
	a.takeOver(o)
	waitFor(t, "Zuschlag zurück", func() bool { mu.Lock(); defer mu.Unlock(); return len(rel) == 1 })
	waitFor(t, "Aktion frei", func() bool { return busyOf(a) == "" })
	mu.Lock()
	if rel[0].LeaseID != 41 || rel[0].MachineID != "pc-admin" {
		t.Errorf("Release %+v", rel[0])
	}
	mu.Unlock()
	a.takeOver(o) // dasselbe Angebot noch einmal
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n := len(rel)
	mu.Unlock()
	if n != 1 || prepared.Load() != 1 || busyOf(a) != "" {
		t.Fatalf("Wiederholung: %d Releases, %d Versuche, Aktion %q", n, prepared.Load(), busyOf(a))
	}
}

// keine_uebernahme geht an den Koordinator, wenn gesetzt; ein älterer
// (400) bekommt das Profil ohne das Feld. Eine geänderte Einstellung im
// Fenster wird sofort gemeldet.
func TestKeineUebernahmeReported(t *testing.T) {
	countNotify(t)
	var mu sync.Mutex
	var bodies []string
	strict := atomic.Bool{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/profile/") {
			http.NotFound(w, r)
			return
		}
		b := new(bytes.Buffer)
		b.ReadFrom(r.Body)
		mu.Lock()
		bodies = append(bodies, b.String())
		mu.Unlock()
		if strict.Load() && strings.Contains(b.String(), "keine_uebernahme") {
			http.Error(w, `{"error":"unknown field"}`, http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	a := takeoverApp(t)
	a.c = client.New(&group.Group{Coordinator: ts.URL})
	p := bench.DefaultProfile()
	if err := bench.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	p.KeineUebernahme = true
	if err := a.saveProfile(p); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Profil gemeldet", func() bool { mu.Lock(); defer mu.Unlock(); return len(bodies) == 1 })
	mu.Lock()
	if !strings.Contains(bodies[0], `"keine_uebernahme":true`) {
		t.Errorf("Profil ohne keine_uebernahme: %s", bodies[0])
	}
	mu.Unlock()

	// Wieder an: sofort gemeldet (Feld fällt weg, omitempty).
	p.KeineUebernahme = false
	a.saveProfile(p)
	waitFor(t, "Profil gemeldet", func() bool { mu.Lock(); defer mu.Unlock(); return len(bodies) == 2 })
	// Anderer Wert ohne Wechsel der Einstellung: nicht gemeldet.
	p.C2ME = !p.C2ME
	a.saveProfile(p)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if len(bodies) != 2 {
		t.Errorf("%d Meldungen", len(bodies))
	}
	mu.Unlock()

	// Älterer Koordinator: zweiter Versuch ohne das Feld.
	strict.Store(true)
	if err := putProfile(t.Context(), a.c, "pc-admin", api.Profile{KeineUebernahme: true}); err != nil {
		t.Fatalf("putProfile: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	n := len(bodies)
	if n != 4 || strings.Contains(bodies[3], "keine_uebernahme") {
		t.Errorf("Wiederholung ohne Feld: %q", bodies[2:])
	}
}

// Während der Übernahme (Download, Welt-Ordner) schickt die App für den
// wartenden Zuschlag Lebenszeichen (ohne Spieler, nicht bereit); sobald die
// Sitzung startet, hört das auf (kein Doppel-Takt).
func TestTakeoverBridgesLease(t *testing.T) {
	notes := countNotify(t)
	got := fakeHostRun(t)
	oldBeat := takeoverBeat
	takeoverBeat = 10 * time.Millisecond
	t.Cleanup(func() { takeoverBeat = oldBeat })
	release := make(chan struct{})
	prep := takeoverPrepare
	takeoverPrepare = func(a *App, ctx context.Context) (playInput, error) {
		<-release
		return prep(a, ctx)
	}
	var mu sync.Mutex
	var beats []api.HeartbeatRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/lease/heartbeat" {
			var q api.HeartbeatRequest
			json.NewDecoder(r.Body).Decode(&q)
			mu.Lock()
			beats = append(beats, q)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	a := takeoverApp(t)
	a.c = client.New(&group.Group{Coordinator: ts.URL})
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(beats) }

	a.takeOver(&api.HandoverOffer{HandoverID: 3, LeaseID: 41, WorldID: "w", Port: 15566})
	waitFor(t, "mehrere Lebenszeichen vor dem Start", func() bool { return count() >= 3 })
	mu.Lock()
	b := beats[0]
	mu.Unlock()
	if b.LeaseID != 41 || b.MachineID != "pc-admin" || b.Ready || len(b.Players) != 0 {
		t.Errorf("Lebenszeichen %+v", b)
	}
	close(release)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("keine Sitzung gestartet")
	}
	n := count()
	time.Sleep(100 * time.Millisecond)
	if count() != n {
		t.Fatalf("Lebenszeichen nach dem Start der Sitzung: %d → %d", n, count())
	}
	a.endHosting("Test")
	waitFor(t, "Sitzung zu", func() bool {
		return !a.isHosting() && busyOf(a) == "" && strings.Contains(strings.Join(notes(), "|"), "Hosten beendet")
	})
}

// Verliert der wartende Zuschlag während der Vorbereitung (409 auf das
// Lebenszeichen), bricht die Übernahme ab: ein Hinweis, kein Release.
func TestBridgeLeaseLostAbortsTakeover(t *testing.T) {
	notes := countNotify(t)
	got := fakeHostRun(t)
	oldBeat := takeoverBeat
	takeoverBeat = 10 * time.Millisecond
	t.Cleanup(func() { takeoverBeat = oldBeat })
	takeoverPrepare = func(a *App, ctx context.Context) (playInput, error) {
		<-ctx.Done()
		return playInput{}, ctx.Err()
	}
	var released, beats atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/lease/heartbeat":
			beats.Add(1)
			http.Error(w, `{"error":"lease"}`, http.StatusConflict)
		case "/v1/lease/release":
			released.Add(1)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ts.Close)
	a := takeoverApp(t)
	a.c = client.New(&group.Group{Coordinator: ts.URL})
	a.takeOver(&api.HandoverOffer{HandoverID: 4, LeaseID: 41})
	waitFor(t, "Übernahme beendet", func() bool { return busyOf(a) == "" })
	waitFor(t, "Hinweis", func() bool { return strings.Contains(strings.Join(notes(), "|"), "Zuschlag") })
	time.Sleep(50 * time.Millisecond)
	if released.Load() != 0 || beats.Load() != 1 || a.isHosting() {
		t.Fatalf("Release %d, Lebenszeichen %d, hostet %v", released.Load(), beats.Load(), a.isHosting())
	}
	select {
	case <-got:
		t.Fatal("Sitzung gestartet")
	default:
	}
	n := 0
	for _, s := range notes() {
		if strings.Contains(s, "Hosten übernehmen:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d Fehlerhinweise: %q", n, notes())
	}
}
