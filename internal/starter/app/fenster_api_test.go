package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// anzeigeProbe ersetzt die echte Anzeige (zenity: Benachrichtigung,
// Fehler- und Infofenster, Rückfrage) und hält fest, was erschienen wäre.
type anzeigeProbe struct {
	mu       sync.Mutex
	shown    []string // "toast: …", "fehler: …", "info: …"
	confirms []string
}

func probeAnzeige(t *testing.T) *anzeigeProbe {
	t.Helper()
	p := &anzeigeProbe{}
	oldToast, oldErr, oldInfo, oldConfirm, oldProgress := toast, errorBox, infoBox, confirm, openImportProgress
	t.Cleanup(func() {
		toast, errorBox, infoBox, confirm, openImportProgress = oldToast, oldErr, oldInfo, oldConfirm, oldProgress
	})
	toast = func(s string) { p.add("toast: " + s) }
	errorBox = func(s string) { p.add("fehler: " + s) }
	infoBox = func(s string) { p.add("info: " + s) }
	confirm = func(text, yes, no string) bool {
		p.mu.Lock()
		p.confirms = append(p.confirms, text)
		p.mu.Unlock()
		return true
	}
	openImportProgress = func() (zenity.ProgressDialog, error) { return nil, errors.New("kein Fenster im Test") }
	return p
}

func (p *anzeigeProbe) add(s string) {
	p.mu.Lock()
	p.shown = append(p.shown, s)
	p.mu.Unlock()
}

func (p *anzeigeProbe) got() (shown, confirms []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.shown), slices.Clone(p.confirms)
}

// fensterTest ist eine Instanz mit laufender Steuerung samt Fenster-
// Endpunkten und ein verbundenes Fenster (fensterapi-Client).
type fensterTest struct {
	a     *App
	fc    *fensterapi.Client
	host  string // 127.0.0.1:<Port>
	token string

	mu        sync.Mutex
	autostart []bool   // Aufrufe von setAutostart
	opened    []string // Aufrufe von openFolder
}

// startFenster startet die Steuerung einer Instanz mit dem Koordinator-
// Client c (nil: noch keiner geladen) in einem frischen Home() und mit
// eigenem Minecraft-Ordner; Autostart und Explorer sind ersetzt.
func startFenster(t *testing.T, c *client.Client) *fensterTest {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	t.Setenv("WANDERWELT_MINECRAFT_DIR", t.TempDir())
	ft := &fensterTest{}
	oldSet, oldOn, oldOpen := setAutostart, autostartOn, openFolder
	t.Cleanup(func() { setAutostart, autostartOn, openFolder = oldSet, oldOn, oldOpen })
	var an atomic.Bool
	setAutostart = func(on bool) error {
		ft.mu.Lock()
		ft.autostart = append(ft.autostart, on)
		ft.mu.Unlock()
		an.Store(on)
		return nil
	}
	autostartOn = an.Load
	openFolder = func(dir string) error {
		ft.mu.Lock()
		ft.opened = append(ft.opened, dir)
		ft.mu.Unlock()
		return nil
	}
	ft.a = busyApp(t, c)
	cs, err := startControl(ft.a.onControl, ft.a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cs.Close)
	data, err := os.ReadFile(controlPath())
	if err != nil {
		t.Fatal(err)
	}
	var ci fensterapi.ControlInfo
	if err := json.Unmarshal(data, &ci); err != nil {
		t.Fatal(err)
	}
	ft.host, ft.token = fmt.Sprintf("127.0.0.1:%d", ci.Port), ci.Token
	if ft.fc, err = fensterapi.Verbinden(); err != nil {
		t.Fatalf("Verbinden: %v", err)
	}
	return ft
}

// roh schickt eine Anfrage an die Steuerung mit Host-Kopf host und
// Authorization auth ("" = keiner).
func (ft *fensterTest) roh(t *testing.T, method, path, host, auth, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+ft.host+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(data)
}

// mit schickt eine Anfrage wie das Fenster (richtiger Host, Token).
func (ft *fensterTest) mit(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	code, _, data := ft.roh(t, method, path, ft.host, "Bearer "+ft.token, body)
	return code, data
}

func fehlerText(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Fehler string `json:"fehler"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("Fehlerantwort %q ist kein {\"fehler\": …}: %v", body, err)
	}
	return e.Fehler
}

// Ohne das Token aus control.json oder mit einem anderen Host-Kopf als
// 127.0.0.1:<Port> lehnt die Steuerung jeden neuen Endpunkt mit 403 ab, und
// es passiert nichts.
func TestFensterAPIToken(t *testing.T) {
	probeAnzeige(t)
	ft := startFenster(t, nil)
	profil := `{"xmx_mb":4096,"view_distance":10,"simulation_distance":8,"dauerbetrieb":true}`
	endpoints := []struct{ method, path, body string }{
		{"GET", "/v1/zustand", ""},
		{"GET", "/v1/staende", ""},
		{"GET", "/v1/speicherstaende", ""},
		{"GET", "/v1/log?zeilen=5", ""},
		{"GET", "/v1/mitglieder", ""},
		{"POST", "/v1/play", ""},
		{"POST", "/v1/beenden", ""},
		{"POST", "/v1/import", `{"dir":"C:\\x"}`},
		{"POST", "/v1/profil", profil},
		{"POST", "/v1/autostart", `{"an":true}`},
		{"POST", "/v1/zurueckholen", `{"world_id":"w1","number":3}`},
		{"POST", "/v1/speicher", `{"budget_bytes":21474836480}`},
		{"POST", "/v1/rolle", `{"email":"bob@example.org","admin":true,"op":true}`},
		{"POST", "/v1/logordner", ""},
	}
	port := strings.TrimPrefix(ft.host, "127.0.0.1:")
	cases := []struct{ name, host, auth string }{
		{"ohne Token", ft.host, ""},
		{"falsches Token", ft.host, "Bearer " + strings.Repeat("0", len(ft.token))},
		{"Token mit Anhang", ft.host, "Bearer " + ft.token + "0"},
		{"Token ohne Bearer", ft.host, ft.token},
		{"leeres Token", ft.host, "Bearer "},
		{"Host localhost", "localhost:" + port, "Bearer " + ft.token},
		{"Host anderer Port", "127.0.0.1:1", "Bearer " + ft.token},
		{"Host ohne Port", "127.0.0.1", "Bearer " + ft.token},
	}
	for _, ep := range endpoints {
		for _, tc := range cases {
			if code, _, body := ft.roh(t, ep.method, ep.path, tc.host, tc.auth, ep.body); code != http.StatusForbidden {
				t.Errorf("%s %s, %s: %d %q, erwartet 403", ep.method, ep.path, tc.name, code, body)
			}
		}
	}

	// Nichts davon hat gewirkt.
	select {
	case <-ft.a.playCh:
		t.Error("play ohne Token angenommen")
	default:
	}
	ft.mu.Lock()
	if len(ft.autostart) != 0 || len(ft.opened) != 0 {
		t.Errorf("Autostart %v, Ordner geöffnet %v ohne Token", ft.autostart, ft.opened)
	}
	ft.mu.Unlock()
	if _, err := os.Stat(bench.ProfilePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("profil.yaml ohne Token geschrieben (%v)", err)
	}
	if ft.a.hinweise.fensterOffen(time.Now()) {
		t.Error("Zustand ohne Token zählt als Abfrage des Fensters")
	}

	// Gegenprobe: mit Token und richtigem Host antworten sie.
	if code, body := ft.mit(t, "GET", "/v1/zustand", ""); code != http.StatusOK {
		t.Fatalf("Zustand mit Token: %d %q", code, body)
	}
	if code, _ := ft.mit(t, "POST", "/v1/autostart", `{"an":true}`); code != http.StatusOK {
		t.Fatalf("autostart mit Token: %d", code)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if !reflect.DeepEqual(ft.autostart, []bool{true}) {
		t.Fatalf("Autostart-Aufrufe %v", ft.autostart)
	}
}

// „Spielen“ und „Spiel für mich beenden“ aus dem Fenster laufen über
// dieselben Methoden wie die Einträge im Tray-Menü – „beenden“ ohne die
// Rückfrage des Trays (das Fenster fragt selbst).
func TestFensterAktionenWieTray(t *testing.T) {
	p := probeAnzeige(t)
	ft := startFenster(t, nil)
	a := ft.a

	// play: an die Tray-Schleife, die dafür a.Play() ausführt wie beim Klick.
	if code, body := ft.mit(t, "POST", "/v1/play", ""); code != http.StatusAccepted {
		t.Fatalf("play: %d %q, erwartet 202", code, body)
	}
	select {
	case <-a.playCh:
	default:
		t.Fatal("play nicht an die Tray-Schleife übergeben")
	}

	// beenden als Host (Dauerbetrieb an): Hosten endet, Dauerbetrieb pausiert.
	hosting := func(x *App) *atomic.Int32 {
		var n atomic.Int32
		x.mu.Lock()
		x.hostCancel, x.phase = func() { n.Add(1) }, phHosting
		x.mu.Unlock()
		x.dauer.on.Store(true)
		return &n
	}
	tray := busyApp(t, nil)
	trayCancel := hosting(tray)
	tray.stopPlaying()
	fensterCancel := hosting(a)
	a.mu.Lock()
	a.hostAddr = "mc.example.org:25565"
	a.mu.Unlock()
	if z, err := ft.fc.Zustand(t.Context()); err != nil || z.Phase != "Hostet" || !z.Hosting || z.HostAddr != "mc.example.org:25565" {
		t.Fatalf("Zustand als Host: %+v, %v", z, err)
	}
	if code, body := ft.mit(t, "POST", "/v1/beenden", ""); code != http.StatusAccepted {
		t.Fatalf("beenden als Host: %d %q, erwartet 202", code, body)
	}
	waitFor(t, "Fenster beendet das Hosten", func() bool { return fensterCancel.Load() == 1 })
	for name, x := range map[string]*App{"Tray": tray, "Fenster": a} {
		x.dauer.mu.Lock()
		paused := x.dauer.paused
		x.dauer.mu.Unlock()
		x.mu.Lock()
		ph := x.phase
		x.mu.Unlock()
		if !paused || ph != phUploading {
			t.Errorf("%s: Dauerbetrieb pausiert %v, Zustand %v; erwartet pausiert und „lädt hoch“", name, paused, ph)
		}
	}
	if trayCancel.Load() != 1 {
		t.Fatal("Tray beendet das Hosten nicht")
	}
	if _, confirms := p.got(); len(confirms) != 1 || !strings.Contains(confirms[0], "Du hostest gerade") {
		t.Fatalf("Rückfragen %q, erwartet genau die des Trays", confirms)
	}

	// beenden als Mitspieler: Beobachtung des Hosts endet, wieder bereit.
	joined := func(x *App) *atomic.Int32 {
		var n atomic.Int32
		x.mu.Lock()
		x.hostCancel, x.phase, x.joinCancel = nil, phJoined, func() { n.Add(1) }
		x.mu.Unlock()
		return &n
	}
	trayJoin := joined(tray)
	tray.stopPlaying()
	fensterJoin := joined(a)
	if code, body := ft.mit(t, "POST", "/v1/beenden", ""); code != http.StatusAccepted {
		t.Fatalf("beenden als Mitspieler: %d %q, erwartet 202", code, body)
	}
	waitFor(t, "Fenster beendet das Mitspielen", func() bool { return fensterJoin.Load() == 1 })
	for name, x := range map[string]*App{"Tray": tray, "Fenster": a} {
		waitFor(t, name+" bereit", func() bool {
			x.mu.Lock()
			defer x.mu.Unlock()
			return x.phase == phReady && x.joinCancel == nil
		})
	}
	if trayJoin.Load() != 1 {
		t.Fatal("Tray beendet das Mitspielen nicht")
	}
	waitFor(t, "derselbe Hinweis von Tray und Fenster", func() bool {
		shown, _ := p.got()
		n := 0
		for _, s := range shown {
			if strings.HasPrefix(s, "toast: Okay. Minecraft selbst") {
				n++
			}
		}
		return n == 2
	})

	// Weder Host noch Mitspieler: abgelehnt.
	if code, body := ft.mit(t, "POST", "/v1/beenden", ""); code != http.StatusConflict || fehlerText(t, body) == "" {
		t.Fatalf("beenden ohne Spiel: %d %q, erwartet 409 mit Text", code, body)
	}
}

// Ein ungültiges Leistungsprofil aus dem Fenster wird mit der Meldung der
// Profilprüfung abgelehnt, profil.yaml bleibt unverändert. Schaltet das
// Profil den Dauerbetrieb ein, geht das wie der Haken im Tray.
func TestProfilUngueltig(t *testing.T) {
	p := probeAnzeige(t)
	oldAC := onACPower
	t.Cleanup(func() { onACPower = oldAC })
	onACPower = func() bool { return false } // kein Takt mit Koordinator
	ft := startFenster(t, nil)
	ctx := t.Context()

	valid := api.Profile{XmxMB: 4096, ViewDistance: 11, SimulationDistance: 7, C2ME: true, C2METhreads: 3}
	if err := ft.fc.Aktion(ctx, "profil", valid); err != nil {
		t.Fatalf("gültiges Profil: %v", err)
	}
	if got, err := bench.LoadProfile(); err != nil || got != valid {
		t.Fatalf("gespeichert %+v, %v; erwartet %+v", got, err, valid)
	}
	if z, err := ft.fc.Zustand(ctx); err != nil || z.Profil != valid {
		t.Fatalf("Zustand.Profil %+v, %v", z.Profil, err)
	}
	before, err := os.ReadFile(bench.ProfilePath())
	if err != nil {
		t.Fatal(err)
	}

	bad := valid
	bad.XmxMB = 512
	data, _ := json.Marshal(bad)
	code, body := ft.mit(t, "POST", "/v1/profil", string(data))
	if code != http.StatusBadRequest || !strings.Contains(fehlerText(t, body), "xmx_mb 512 ist zu klein") {
		t.Fatalf("Xmx 512: %d %q, erwartet 400 mit der Meldung der Profilprüfung", code, body)
	}
	if err := ft.fc.Aktion(ctx, "profil", bad); err == nil || !strings.Contains(err.Error(), "xmx_mb 512") {
		t.Fatalf("Fenster bekommt %v", err)
	}
	if code, _ := ft.mit(t, "POST", "/v1/profil", "kein json"); code != http.StatusBadRequest {
		t.Fatalf("kaputter Körper: %d, erwartet 400", code)
	}
	if after, _ := os.ReadFile(bench.ProfilePath()); !bytes.Equal(after, before) {
		t.Fatalf("profil.yaml verändert:\n%s", after)
	}

	// Dauerbetrieb an: Pause und Wartezeit vorbei, Hinweis wie im Tray.
	a := ft.a
	a.dauer.mu.Lock()
	a.dauer.paused, a.dauer.fails, a.dauer.nextTry, a.dauer.notified = true, 3, time.Now().Add(time.Hour), true
	a.dauer.mu.Unlock()
	on := valid
	on.Dauerbetrieb = true
	if err := ft.fc.Aktion(ctx, "profil", on); err != nil {
		t.Fatalf("Dauerbetrieb an: %v", err)
	}
	if !a.dauer.on.Load() {
		t.Fatal("Dauerbetrieb nicht übernommen")
	}
	if got, err := bench.LoadProfile(); err != nil || !got.Dauerbetrieb {
		t.Fatalf("profil.yaml: %+v, %v", got, err)
	}
	waitFor(t, "Pause und Wartezeit vorbei", func() bool {
		a.dauer.mu.Lock()
		defer a.dauer.mu.Unlock()
		return !a.dauer.paused && a.dauer.fails == 0 && a.dauer.nextTry.IsZero() && !a.dauer.notified
	})
	waitFor(t, "Hinweis", func() bool {
		shown, _ := p.got()
		return slices.ContainsFunc(shown, func(s string) bool { return strings.HasPrefix(s, "toast: Dauerbetrieb an") })
	})
	if z, err := ft.fc.Zustand(ctx); err != nil || !z.Profil.Dauerbetrieb {
		t.Fatalf("Zustand.Profil.Dauerbetrieb nach dem Einschalten: %+v, %v", z.Profil, err)
	}
	// Dasselbe Profil noch einmal: kein Umschalten, kein Hinweis.
	shownBefore, _ := p.got()
	if err := ft.fc.Aktion(ctx, "profil", on); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if shown, _ := p.got(); len(shown) != len(shownBefore) {
		t.Fatalf("Hinweise ohne Umschalten: %q", shown[len(shownBefore):])
	}
}

// Die Instanz hält die letzten 20 Hinweise, Seq aufsteigend.
func TestHinweisPuffer(t *testing.T) {
	probeAnzeige(t)
	ft := startFenster(t, nil)
	fensterInstanz.Store(ft.a)
	t.Cleanup(func() { fensterInstanz.Store(nil) })
	for i := 1; i <= 21; i++ {
		notify(fmt.Sprintf("Hinweis %d", i))
	}
	z, err := ft.fc.Zustand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(z.Hinweise) != 20 {
		t.Fatalf("%d Hinweise, erwartet 20", len(z.Hinweise))
	}
	for i, h := range z.Hinweise {
		if h.Seq != int64(i+2) || h.Text != fmt.Sprintf("Hinweis %d", i+2) || h.Art != "info" || h.Zeit.IsZero() {
			t.Fatalf("Hinweis %d = %+v, erwartet Seq %d „Hinweis %d“", i, h, i+2, i+2)
		}
	}
}

// Fragt das Fenster den Zustand ab, erscheinen Meldungen und Hinweise 5 s
// lang nur dort; danach wieder als Dialog bzw. Benachrichtigung.
func TestDialogeBeiAktivemFenster(t *testing.T) {
	p := probeAnzeige(t)
	ft := startFenster(t, nil)
	a := ft.a
	fensterInstanz.Store(a)
	t.Cleanup(func() { fensterInstanz.Store(nil) })
	t0 := time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)
	var now atomic.Int64
	now.Store(t0.UnixNano())
	oldNow := fensterNow
	t.Cleanup(func() { fensterNow = oldNow })
	fensterNow = func() time.Time { return time.Unix(0, now.Load()) }

	// Fenster zu: wie bisher.
	notify("eins")
	showError("zwei")
	showInfo("drei")
	want := []string{"toast: eins", "fehler: zwei", "info: drei"}
	if shown, _ := p.got(); !reflect.DeepEqual(shown, want) {
		t.Fatalf("ohne Fenster angezeigt %q, erwartet %q", shown, want)
	}

	// Fenster fragt ab: knapp 5 s lang nur Hinweise.
	if _, err := ft.fc.Zustand(t.Context()); err != nil {
		t.Fatal(err)
	}
	now.Store(t0.Add(5*time.Second - time.Millisecond).UnixNano())
	notify("vier")
	showError("fünf")
	showInfo("sechs")
	a.endWith(errorDialog("sieben"))
	a.fail("Spielen", errors.New("acht"))
	waitFor(t, "Hinweis zum Fehler", func() bool { return len(hinweiseOf(a)) == 8 })
	if shown, _ := p.got(); !reflect.DeepEqual(shown, want) {
		t.Fatalf("bei offenem Fenster angezeigt %q", shown[len(want):])
	}
	got := map[string]string{}
	for _, h := range hinweiseOf(a) {
		got[h.Text] = h.Art
	}
	wantArt := map[string]string{"eins": "info", "zwei": "fehler", "drei": "info", "vier": "info",
		"fünf": "fehler", "sechs": "info", "sieben": "fehler", "Spielen: acht": "fehler"}
	if !reflect.DeepEqual(got, wantArt) {
		t.Fatalf("Hinweise %v, erwartet %v", got, wantArt)
	}

	// 5 s nach der Abfrage: wieder Dialoge.
	now.Store(t0.Add(5 * time.Second).UnixNano())
	notify("neun")
	showError("zehn")
	want = append(want, "toast: neun", "fehler: zehn")
	if shown, _ := p.got(); !reflect.DeepEqual(shown, want) {
		t.Fatalf("nach der Frist angezeigt %q, erwartet %q", shown, want)
	}
	if n := len(hinweiseOf(a)); n != 10 {
		t.Fatalf("%d Hinweise, erwartet 10", n)
	}
}

func hinweiseOf(a *App) []fensterapi.Hinweis {
	a.hinweise.mu.Lock()
	defer a.hinweise.mu.Unlock()
	return slices.Clone(a.hinweise.list)
}

// Ein älterer Koordinator kennt die neuen Endpunkte nicht (404 vom
// Auffang-Handler): Das Fenster bekommt „Der Koordinator ist zu alt für
// diese Ansicht“. Eine 404 mit eigener Meldung (unbekannter Stand) bleibt
// diese Meldung; ein neuer Koordinator wird durchgereicht.
func TestKoordinatorZuAlt(t *testing.T) {
	probeAnzeige(t)
	var mu sync.Mutex
	var seen []string
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/restore":
			reply(w, http.StatusNotFound, map[string]string{"error": "Stand w1/9 gibt es nicht"})
			return
		case "GET /v1/generations":
			// Etwa ein Proxy davor: 404 ohne die Meldung des Koordinators.
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		reply(w, http.StatusNotFound, map[string]string{"error": "unbekannter Endpunkt " + r.Method + " " + r.URL.Path})
	}))
	defer old.Close()
	ft := startFenster(t, client.New(&group.Group{Coordinator: old.URL, DevIdentityEmail: "admin@example.org"}))
	ctx := t.Context()
	const zuAlt = "Der Koordinator ist zu alt für diese Ansicht"

	code, body := ft.mit(t, "GET", "/v1/staende", "")
	if code/100 != 4 || fehlerText(t, body) != zuAlt {
		t.Fatalf("Stände: %d %q, erwartet 4xx mit {\"fehler\": %q}", code, body, zuAlt)
	}
	if _, err := ft.fc.Staende(ctx); err == nil || err.Error() != zuAlt {
		t.Fatalf("Fenster: Staende = %v", err)
	}
	if _, err := ft.fc.Mitglieder(ctx); err == nil || err.Error() != zuAlt {
		t.Fatalf("Fenster: Mitglieder = %v", err)
	}
	if err := ft.fc.Aktion(ctx, "speicher", api.BudgetRequest{BudgetBytes: 20 << 30}); err == nil || err.Error() != zuAlt {
		t.Fatalf("Fenster: speicher = %v", err)
	}
	if err := ft.fc.Aktion(ctx, "rolle", map[string]any{"email": "bob@example.org", "admin": true, "op": false}); err == nil || err.Error() != zuAlt {
		t.Fatalf("Fenster: rolle = %v", err)
	}
	if err := ft.fc.Aktion(ctx, "zurueckholen", api.RestoreRequest{WorldID: "w1", Number: 9}); err == nil || err.Error() != "Stand w1/9 gibt es nicht" {
		t.Fatalf("Fenster: zurueckholen = %v", err)
	}
	mu.Lock()
	wantSeen := []string{
		"GET /v1/generations", "GET /v1/generations", "GET /v1/members",
		`PUT /v1/storage/budget {"budget_bytes":21474836480}`,
		`PUT /v1/members/bob@example.org {"admin":true,"op":false}`,
		`POST /v1/restore {"world_id":"w1","number":9}`,
	}
	if !reflect.DeepEqual(seen, wantSeen) {
		t.Errorf("beim Koordinator angekommen:\n%q\nerwartet\n%q", seen, wantSeen)
	}
	mu.Unlock()

	// Neuer Koordinator: Antworten kommen unverändert beim Fenster an.
	gens := api.GenerationsResponse{Worlds: []api.WorldInfo{{WorldID: "w2", Active: true, Generations: []api.GenerationInfo{
		{WorldID: "w2", Number: 8, HostName: "Lukas", Players: []string{"Bob"}, Size: 4711, MinecraftVersion: "26.2",
			RestoredFrom: &api.GenRef{WorldID: "w2", Number: 3}}}}}, Storage: &api.StorageInfo{UsedBytes: 5, BudgetBytes: 10}}
	members := []api.MemberInfo{{Email: "bob@example.org", MinecraftName: "Bob", Op: true, Online: true}}
	newer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/generations":
			reply(w, http.StatusOK, gens)
		case "GET /v1/members":
			reply(w, http.StatusOK, members)
		case "POST /v1/restore":
			reply(w, http.StatusOK, api.RestoreResponse{Pending: true})
		default:
			reply(w, http.StatusNotFound, map[string]string{"error": "unbekannter Endpunkt " + r.Method + " " + r.URL.Path})
		}
	}))
	defer newer.Close()
	ft.a.mu.Lock()
	ft.a.c = client.New(&group.Group{Coordinator: newer.URL, DevIdentityEmail: "admin@example.org"})
	ft.a.mu.Unlock()
	if got, err := ft.fc.Staende(ctx); err != nil || !reflect.DeepEqual(*got, gens) {
		t.Fatalf("Staende = %+v, %v", got, err)
	}
	if got, err := ft.fc.Mitglieder(ctx); err != nil || !reflect.DeepEqual(got, members) {
		t.Fatalf("Mitglieder = %+v, %v", got, err)
	}
	if code, body := ft.mit(t, "POST", "/v1/zurueckholen", `{"world_id":"w2","number":3}`); code != http.StatusOK ||
		strings.TrimSpace(body) != `{"applied":false,"pending":true}` {
		t.Fatalf("zurueckholen: %d %q", code, body)
	}
}

// „Welt übernehmen“ aus dem Fenster nimmt nur einen Ordner direkt im
// saves-Ordner von Minecraft (bereinigt, unter Windows ohne Rücksicht auf
// Groß- und Kleinschreibung).
func TestImportNurAusSaves(t *testing.T) {
	p := probeAnzeige(t)
	c := importCoordinator(t)
	ft := startFenster(t, c)
	saves := savesDir()
	writeSave(t, saves, "Meine Welt", levelDat("26.2", false, testUUID), true)
	writeSave(t, saves, "Alt", levelDat("1.21.4", false, testUUID), false)

	// Die Liste fürs Fenster: übernehmbar oder mit Grund.
	list, err := ft.fc.Speicherstaende(t.Context())
	if err != nil || len(list) != 2 {
		t.Fatalf("Speicherstaende = %+v, %v", list, err)
	}
	byFolder := map[string]fensterapi.Speicherstand{}
	for _, s := range list {
		byFolder[s.Folder] = s
	}
	if s := byFolder["Meine Welt"]; s.Problem != "" || !s.PlayerFile || s.Version != "26.2" || s.LevelName != "Testwelt" || s.Size == 0 {
		t.Errorf("Meine Welt: %+v, erwartet übernehmbar", s)
	}
	if s := byFolder["Alt"]; !strings.Contains(s.Problem, "1.21.4") || s.PlayerFile {
		t.Errorf("Alt: %+v, erwartet Grund mit der Version", s)
	}
	sep := string(filepath.Separator)
	mc := filepath.Dir(saves)

	accept := map[string]string{
		filepath.Join(saves, "Meine Welt"):                   filepath.Join(saves, "Meine Welt"),
		saves + sep + "Meine Welt" + sep:                     filepath.Join(saves, "Meine Welt"),
		saves + sep + "x" + sep + ".." + sep + "Meine Welt":  filepath.Join(saves, "Meine Welt"),
		mc + sep + "saves" + sep + "." + sep + "Andere Welt": filepath.Join(saves, "Andere Welt"),
	}
	if runtime.GOOS == "windows" {
		accept[strings.ToUpper(saves)+sep+"Meine Welt"] = strings.ToUpper(saves) + sep + "Meine Welt"
	}
	for in, want := range accept {
		if got, ok := saveDirIn(saves, in); !ok || got != want {
			t.Errorf("saveDirIn(%q) = %q, %v; erwartet %q", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"",
		"Meine Welt",
		saves,
		saves + sep,
		mc,
		filepath.Join(saves, "Meine Welt", "region"),
		saves + sep + ".." + sep + "Meine Welt",
		saves + sep + "Meine Welt" + sep + "..",
		filepath.Join(t.TempDir(), "saves", "Meine Welt"),
		filepath.Join(mc, "saves2", "Meine Welt"),
	} {
		if got, ok := saveDirIn(saves, in); ok {
			t.Errorf("saveDirIn(%q) = %q, erwartet abgelehnt", in, got)
		}
	}

	// Außerhalb: 400, nichts beginnt.
	outside := t.TempDir()
	writeSave(t, outside, "Fremd", levelDat("26.2", false, testUUID), true)
	body, _ := json.Marshal(importRequest{Dir: filepath.Join(outside, "Fremd")})
	if code, resp := ft.mit(t, "POST", "/v1/import", string(body)); code != http.StatusBadRequest || fehlerText(t, resp) == "" {
		t.Fatalf("import von außerhalb: %d %q, erwartet 400", code, resp)
	}
	if b := busyOf(ft.a); b != "" {
		t.Fatalf("nach Ablehnung läuft %q", b)
	}

	// Gewollter Pfad: Ordner aus saves wird übernommen. Die Antwort (202)
	// kommt, während das Übernehmen noch läuft; ein zweites wartet nicht.
	ft.a.mu.Lock()
	ft.a.st, ft.a.hc = &group.State{MachineID: "pc-admin"}, worldsync.LoadHashCache()
	ft.a.mu.Unlock()
	gate := make(chan struct{})
	oldOpen := worldOpenAfterCopy
	t.Cleanup(func() { worldOpenAfterCopy = oldOpen })
	worldOpenAfterCopy = func(string) bool { <-gate; return false }
	body, _ = json.Marshal(importRequest{Dir: filepath.Join(saves, "Meine Welt")})
	if code, resp := ft.mit(t, "POST", "/v1/import", string(body)); code != http.StatusAccepted {
		close(gate)
		t.Fatalf("import aus saves: %d %q, erwartet 202", code, resp)
	}
	code, resp := ft.mit(t, "POST", "/v1/import", string(body))
	busy := busyOf(ft.a)
	close(gate)
	if code != http.StatusConflict || !strings.Contains(fehlerText(t, resp), "Welt übernehmen") || busy != "Welt übernehmen" {
		t.Fatalf("zweites import während des ersten: %d %q (läuft: %q), erwartet 409", code, resp, busy)
	}
	waitFor(t, "Ergebnis", func() bool {
		shown, _ := p.got()
		return slices.ContainsFunc(shown, func(s string) bool { return strings.Contains(s, "ist jetzt die Gruppenwelt") })
	})
	if head, err := c.Head(t.Context()); err != nil || head == nil || head.Number != 1 {
		t.Fatalf("Head nach dem Übernehmen: %+v, %v", head, err)
	}
}

// /v1/log liefert die letzten Zeilen als Text: ohne Angabe 300, höchstens
// 2000.
func TestFensterLog(t *testing.T) {
	probeAnzeige(t)
	ft := startFenster(t, nil)
	// Noch kein Log und ein Log, das wie HTML aussieht: trotzdem Text.
	for _, content := range []string{"", "<html><body>Zeile 1\n"} {
		if content != "" {
			if err := os.MkdirAll(filepath.Dir(LogPath()), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(LogPath(), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		code, hdr, body := ft.roh(t, "GET", "/v1/log", ft.host, "Bearer "+ft.token, "")
		if code != http.StatusOK || hdr.Get("Content-Type") != "text/plain; charset=utf-8" || body != content {
			t.Fatalf("Log %q: %d %q %q", content, code, hdr.Get("Content-Type"), body)
		}
	}
	var b strings.Builder
	for i := 1; i <= 2500; i++ {
		fmt.Fprintf(&b, "Zeile %d\n", i)
	}
	if err := os.WriteFile(LogPath(), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := func(path string) []string {
		t.Helper()
		code, hdr, body := ft.roh(t, "GET", path, ft.host, "Bearer "+ft.token, "")
		if code != http.StatusOK || hdr.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%s: %d %q (%s)", path, code, hdr.Get("Content-Type"), body)
		}
		return strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	}
	for path, want := range map[string][2]string{
		"/v1/log":             {"Zeile 2201", "Zeile 2500"}, // 300
		"/v1/log?zeilen=1":    {"Zeile 2500", "Zeile 2500"},
		"/v1/log?zeilen=2000": {"Zeile 501", "Zeile 2500"},
		"/v1/log?zeilen=2001": {"Zeile 501", "Zeile 2500"},
	} {
		got := lines(path)
		if got[0] != want[0] || got[len(got)-1] != want[1] {
			t.Errorf("%s: %d Zeilen %q … %q, erwartet %q … %q", path, len(got), got[0], got[len(got)-1], want[0], want[1])
		}
	}
	if got, err := ft.fc.Log(t.Context(), 2); err != nil || got != "Zeile 2499\nZeile 2500\n" {
		t.Fatalf("Fenster: Log = %q, %v", got, err)
	}
	for _, q := range []string{"0", "-1", "zwei"} {
		if code, _ := ft.mit(t, "GET", "/v1/log?zeilen="+q, ""); code != http.StatusBadRequest {
			t.Errorf("zeilen=%s: %d, erwartet 400", q, code)
		}
	}
}
