package server_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

// endeWartung schreibt den Zustand wie „wartung ende“ und lässt den
// Koordinator im nächsten Takt davon erfahren.
func (e *restoreEnv) endeWartung() {
	e.t.Helper()
	if err := wartung.Speichern(e.dir, wartung.Zustand{}); err != nil {
		e.t.Fatal(err)
	}
	e.srv.ExpireTick(e.t.Context())
}

func (e *restoreEnv) status(as who) api.Status {
	e.t.Helper()
	var st api.Status
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/status", nil, &st); code != http.StatusOK {
		e.t.Fatalf("Status: %d", code)
	}
	return st
}

// playNow: eine Anfrage, die sofort beantwortet wird (denied).
func (e *restoreEnv) playNow(as who, req api.PlayRequest) api.PlayResponse {
	e.t.Helper()
	var resp api.PlayResponse
	if code := call(e.t, e.base, as, http.MethodPost, "/v1/play", req, &resp); code != http.StatusOK {
		e.t.Fatalf("Play: %d", code)
	}
	return resp
}

// Nach einer gestoppten Wartung öffnet „ende“ das Fenster: Status mit
// Kennung und Restzeiten; nach dem Verfall verschwindet es aus dem Status,
// und Bewerbungen mit Resume werden abgelehnt.
func TestFortsetzenFensterOeffnetUndVerfaellt(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	e.startWartung(0, "Update")
	z, _ := wartung.Laden(e.dir)
	if st := e.status(userB); st.Resume != nil || st.Maintenance == nil {
		t.Fatalf("Status in der Wartung: %+v %+v", st.Maintenance, st.Resume)
	}
	e.endeWartung()

	st := e.status(userB)
	if st.Maintenance != nil || st.Resume == nil || !st.Resume.Since.Equal(z.Since) ||
		st.Resume.OpensInS != 60 || st.Resume.UntilInS != 600 {
		t.Fatalf("Status nach dem Ende: %+v %+v", st.Maintenance, st.Resume)
	}
	e.clk.Advance(90 * time.Second)
	if st := e.status(userB); st.Resume == nil || st.Resume.OpensInS != 0 || st.Resume.UntilInS != 510 {
		t.Fatalf("Status nach dem Beginn: %+v", st.Resume)
	}

	e.clk.Advance(510 * time.Second)
	e.srv.ExpireTick(t.Context())
	if st := e.status(userB); st.Resume != nil {
		t.Fatalf("Fenster nach dem Verfall noch im Status: %+v", st.Resume)
	}
	if _, ok, _ := wartung.FortsetzenLaden(e.dir); ok {
		t.Error("Datei des Fensters nach dem Verfall noch da")
	}
	r := e.playNow(userB, api.PlayRequest{MachineID: "pc-b", Resume: true})
	if r.Role != api.RoleDenied || r.Reason != api.ReasonResumeClosed {
		t.Fatalf("Resume nach dem Verfall: %+v", r)
	}
}

// Alle Bewerbungen im Fenster gehen durch das gewöhnliche Sammelfenster: Der
// PC mit der besseren Hardware wird Host, der andere bekommt „join“, auch
// wenn er zuerst kam.
func TestFortsetzenWahlDesBesten(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	e.startWartung(0, "Update")
	e.endeWartung()
	e.clk.Advance(61 * time.Second)

	weak := api.Score{UploadMbps: 20, CPUScore: 500, RAMFreeMB: 8000, RAMTotalMB: 16000, XmxMB: 4000}
	strong := api.Score{UploadMbps: 200, CPUScore: 1500, RAMFreeMB: 24000, RAMTotalMB: 32000, XmxMB: 6000}
	var mu sync.Mutex
	roles := map[string]string{}
	var wg sync.WaitGroup
	send := func(as who, machine string, sc api.Score) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r api.PlayResponse
			call(t, e.base, as, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: machine, Score: sc, Resume: true}, &r)
			mu.Lock()
			roles[machine] = r.Role
			mu.Unlock()
		}()
	}
	send(userA, "pc-a", weak) // der schwächere kommt zuerst
	e.clk.waitWindow(t)
	send(userB, "pc-b", strong)
	time.Sleep(150 * time.Millisecond) // die zweite Anfrage ist im Fenster
	e.clk.Advance(5 * time.Second)
	wg.Wait()
	if roles["pc-b"] != api.RoleHost || roles["pc-a"] != api.RoleJoin {
		t.Fatalf("Rollen %v, erwartet pc-b host und pc-a join", roles)
	}
}

// Ohne vorherige Wartung gibt es kein Fenster; ein gewöhnliches „Spielen“
// ist davon nie betroffen.
func TestFortsetzenOhneFensterAbgelehntSpielenBleibt(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	if r := e.playNow(userB, api.PlayRequest{MachineID: "pc-b", Resume: true}); r.Role != api.RoleDenied || r.Reason != api.ReasonResumeClosed {
		t.Fatalf("Resume ohne Fenster: %+v", r)
	}
	if e.host(userB, "pc-b") == 0 { // gewöhnliches Spielen geht
		t.Fatal("kein Zuschlag")
	}
}

// Beendet der Admin die Wartung noch in der Ansage-Frist, hat niemand
// gestoppt: kein Fenster. Eine neue Wartung schließt ein offenes Fenster.
func TestFortsetzenNurNachStoppUndNeueWartungSchliesst(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	e.startWartung(time.Minute, "Update")
	e.endeWartung()
	if st := e.status(userB); st.Resume != nil {
		t.Fatalf("Fenster nach Ende in der Ansage-Frist: %+v", st.Resume)
	}

	e.startWartung(0, "Update 2")
	e.endeWartung()
	if st := e.status(userB); st.Resume == nil {
		t.Fatal("kein Fenster nach gestoppter Wartung")
	}
	e.startWartung(time.Hour, "Update 3")
	if st := e.status(userB); st.Resume != nil || st.Maintenance == nil {
		t.Fatalf("Fenster neben neuer Wartung: %+v %+v", st.Maintenance, st.Resume)
	}
	if _, ok, _ := wartung.FortsetzenLaden(e.dir); ok {
		t.Error("Datei des Fensters trotz neuer Wartung noch da")
	}
}

// Das Fenster steht in data_dir und übersteht einen Neustart des
// Koordinators; ein kaputtes wird beim Start verworfen.
func TestFortsetzenUeberlebtNeustart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	z, _ := wartung.Starten(wartung.Zustand{}, now.Add(-time.Hour), 0, "Update")
	f, ok := wartung.NeuesFortsetzen(z, now)
	if !ok {
		t.Fatal("kein Fenster")
	}
	if err := wartung.FortsetzenSpeichern(dir, f); err != nil {
		t.Fatal(err)
	}
	srv, _, _, _ := newServerIn(t, dir, "dev", fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
	hs := httptest.NewServer(srv)
	defer hs.Close()
	var st api.Status
	if code := call(t, hs.URL, userB, http.MethodGet, "/v1/status", nil, &st); code != http.StatusOK || st.Resume == nil || !st.Resume.Since.Equal(z.Since) {
		t.Fatalf("Status nach Neustart: %d %+v", code, st.Resume)
	}

	dir2 := t.TempDir()
	if err := os.WriteFile(wartung.FortsetzenPfad(dir2), []byte("{kaputt"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv2, _, _, _ := newServerIn(t, dir2, "dev", fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
	hs2 := httptest.NewServer(srv2)
	defer hs2.Close()
	var st2 api.Status
	call(t, hs2.URL, userB, http.MethodGet, "/v1/status", nil, &st2)
	if st2.Resume != nil {
		t.Fatalf("kaputtes Fenster gilt: %+v", st2.Resume)
	}
	if _, err := os.Stat(wartung.FortsetzenPfad(dir2)); err == nil {
		t.Error("kaputte Datei nicht gelöscht")
	}
}

// Abwärtskompatibilität: Ohne Fenster trägt der Status kein Feld "resume"
// (ältere Starter sehen nichts Neues), und eine Anfrage ohne Resume
// schickt das Feld nicht mit.
func TestFortsetzenFelderSindOmitempty(t *testing.T) {
	e := newRestoreEnv(t)
	code, body := e.raw(userB, http.MethodGet, "/v1/status", nil)
	if code != http.StatusOK || strings.Contains(string(body), `"resume"`) {
		t.Fatalf("Status ohne Fenster: %d %s", code, body)
	}
}
