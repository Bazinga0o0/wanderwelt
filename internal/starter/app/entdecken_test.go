package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const entdeckToken = "SEHR-GEHEIMER-TOKEN-4711"

// koordFake ist der Koordinator einer Gruppe: /v1/info und /v1/join.
type koordFake struct {
	ts      *httptest.Server
	mu      sync.Mutex
	you     string
	title   string
	members int
	joins   []string
	auth    []string
	joinSt  string // Antwort auf POST /v1/join ("" = open)
	infoRaw string // statt der normalen Antwort
	joinErr int
}

func neuerKoord(t *testing.T, title, you string) *koordFake {
	t.Helper()
	k := &koordFake{you: you, title: title, members: 4}
	k.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.auth = append(k.auth, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/v1/info":
			if k.infoRaw != "" {
				w.Write([]byte(k.infoRaw))
				return
			}
			json.NewEncoder(w).Encode(api.GroupInfo{Title: k.title, Description: "Beschreibung " + k.title, Members: k.members, You: k.you})
		case r.URL.Path == "/v1/join" && r.Method == http.MethodPost:
			if k.joinErr != 0 {
				http.Error(w, `{"error":"geht nicht"}`, k.joinErr)
				return
			}
			var req api.JoinRequest
			json.NewDecoder(r.Body).Decode(&req)
			k.joins = append(k.joins, req.MinecraftName)
			st := k.joinSt
			if st == "" {
				st = api.JoinOpen
			}
			if st == api.JoinOpen {
				k.you = api.YouRequested
			} else {
				k.you = api.YouMember
			}
			json.NewEncoder(w).Encode(api.JoinStatus{State: st})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(k.ts.Close)
	return k
}

// verzeichnisFake ist die Webseite des tunnel-Servers.
type verzeichnisFake struct {
	ts        *httptest.Server
	mu        sync.Mutex
	eintraege []map[string]string
	status    int
	hits      atomic.Int32
	auth      string
}

func neuesVerzeichnis(t *testing.T) *verzeichnisFake {
	t.Helper()
	v := &verzeichnisFake{}
	v.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.hits.Add(1)
		v.mu.Lock()
		defer v.mu.Unlock()
		v.auth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/directory" {
			http.NotFound(w, r)
			return
		}
		if v.status != 0 {
			http.Error(w, "nein", v.status)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"services": v.eintraege})
	}))
	t.Cleanup(v.ts.Close)
	return v
}

func (v *verzeichnisFake) setze(ks ...*koordFake) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.eintraege = nil
	for i, k := range ks {
		v.eintraege = append(v.eintraege, map[string]string{"name": fmt.Sprintf("g%d", i), "title": "Verz-Titel " + k.title,
			"description": "Verz-Beschreibung", "url": k.ts.URL})
	}
}

// entdeckWelt: Gruppe A (lokal, aktiv), tunnel-Webseite web, Koordinatoren.
type entdeckWelt struct {
	a    *App
	web  *verzeichnisFake
	ka   *koordFake
	logs *bytes.Buffer
}

func neueEntdeckWelt(t *testing.T) *entdeckWelt {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	t.Setenv("WANDERWELT_MINECRAFT_DIR", t.TempDir())
	w := &entdeckWelt{web: neuesVerzeichnis(t), ka: neuerKoord(t, "Eigene", api.YouMember), logs: &bytes.Buffer{}}
	log.SetOutput(w.logs)
	t.Cleanup(func() { log.SetOutput(testLogSenke{}) })
	g := &group.Group{Coordinator: w.ka.ts.URL, TunnelServer: "127.0.0.1:1", Token: entdeckToken, WebURL: w.web.ts.URL}
	if err := group.AddGroup(g, true); err != nil {
		t.Fatal(err)
	}
	probeAnzeige(t)
	w.a = busyApp(t, nil)
	// Hinweise (go notify) laufen asynchron: erst nach ihnen die Anzeige zurücksetzen.
	t.Cleanup(func() { w.a.ent.hinweise.Wait() })
	return w
}

type testLogSenke struct{}

func (testLogSenke) Write(p []byte) (int, error) { return len(p), nil }

func (w *entdeckWelt) holen(t *testing.T) fensterapi.Entdecken {
	t.Helper()
	return w.a.entdecken(context.Background(), true)
}

func finde(t *testing.T, e fensterapi.Entdecken, u string) fensterapi.EntdeckenEintrag {
	t.Helper()
	for _, g := range e.Gruppen {
		if g.URL == u {
			return g
		}
	}
	t.Fatalf("%s fehlt in %+v", u, e.Gruppen)
	return fensterapi.EntdeckenEintrag{}
}

func TestEntdeckenListeUndMarken(t *testing.T) {
	w := neueEntdeckWelt(t)
	kb := neuerKoord(t, "Fremde", api.YouNone)
	kc := neuerKoord(t, "Beantragt", api.YouRequested)
	w.web.setze(w.ka, kb, kc)
	e := w.holen(t)
	if len(e.Gruppen) != 3 || len(e.Hinweise) != 0 {
		t.Fatalf("%+v", e)
	}
	a, b, c := finde(t, e, w.ka.ts.URL), finde(t, e, kb.ts.URL), finde(t, e, kc.ts.URL)
	if a.Zustand != fensterapi.EntdeckenMitglied || !a.Lokal {
		t.Errorf("eigene Gruppe: %+v", a)
	}
	if b.Zustand != fensterapi.EntdeckenKeiner || b.Lokal || b.Titel != "Fremde" || b.Mitglieder != 4 || b.Beschreibung != "Beschreibung Fremde" {
		t.Errorf("fremde Gruppe: %+v", b)
	}
	if c.Zustand != fensterapi.EntdeckenAngefragt {
		t.Errorf("angefragt: %+v", c)
	}
	// Token nur als Bearer an Verzeichnis und Koordinatoren, nie im Log.
	if w.web.auth != "Bearer "+entdeckToken {
		t.Errorf("Verzeichnis-Auth: %q", w.web.auth)
	}
	if strings.Contains(w.logs.String(), entdeckToken) {
		t.Error("Token im Log")
	}
	// Nichts wurde doppelt angelegt.
	if len(group.List()) != 1 {
		t.Errorf("Gruppen: %v", group.List())
	}
	// Der Titel der eigenen Gruppe steht für die Auswahl bereit.
	if l, _ := w.a.gruppenListe(); l[0].Titel != "Eigene" {
		t.Errorf("Titel: %+v", l)
	}
}

func TestEntdeckenAngenommeneGruppeWirdAngelegt(t *testing.T) {
	w := neueEntdeckWelt(t)
	kd := neuerKoord(t, "Neue", api.YouMember) // schon angenommen, lokal noch nicht da
	w.web.setze(w.ka, kd)
	e := w.holen(t)
	d := finde(t, e, kd.ts.URL)
	if !d.Lokal || d.Zustand != fensterapi.EntdeckenMitglied {
		t.Fatalf("%+v", d)
	}
	l := group.List()
	if len(l) != 2 || group.ActiveID() != mustID(t, w.ka.ts.URL) {
		t.Fatalf("Gruppen %v aktiv %q", l, group.ActiveID())
	}
	id := mustID(t, kd.ts.URL)
	g, err := group.LoadGroup(group.IDDir(id) + "/gruppe.yaml")
	if err != nil || g.Token != entdeckToken || g.Coordinator != kd.ts.URL || g.TunnelServer != "127.0.0.1:1" || g.WebURL != w.web.ts.URL {
		t.Fatalf("neue Gruppendatei: %+v %v", g, err)
	}
	// Ein zweiter Abruf legt nichts doppelt an.
	w.holen(t)
	if len(group.List()) != 2 {
		t.Errorf("doppelt: %v", group.List())
	}
	if strings.Contains(w.logs.String(), entdeckToken) {
		t.Error("Token im Log")
	}
}

func mustID(t *testing.T, u string) string {
	t.Helper()
	id, err := group.IDFromCoordinator(u)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func beitritt(t *testing.T, a *App, u, name string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"url": u, "name": name})
	rec := httptest.NewRecorder()
	a.entdeckenBeitritt(rec, httptest.NewRequest(http.MethodPost, "/v1/entdeckenbeitritt", nil), body)
	return rec.Code, rec.Body.String()
}

func TestEntdeckenBeitrittAnfragen(t *testing.T) {
	w := neueEntdeckWelt(t)
	kb := neuerKoord(t, "Fremde", api.YouNone)
	w.web.setze(w.ka, kb)
	// Vor dem ersten Abruf kennt die Instanz die Adresse nicht.
	if code, body := beitritt(t, w.a, kb.ts.URL, "Steve_1"); code != http.StatusConflict || !strings.Contains(body, "nicht in der Liste") {
		t.Errorf("ohne Liste: %d %s", code, body)
	}
	w.holen(t)
	if code, _ := beitritt(t, w.a, kb.ts.URL, "x"); code != http.StatusBadRequest {
		t.Errorf("Name zu kurz: %d", code)
	}
	if code, _ := beitritt(t, w.a, kb.ts.URL, "Steve 1"); code != http.StatusBadRequest {
		t.Errorf("Name mit Leerzeichen: %d", code)
	}
	if code, _ := beitritt(t, w.a, "https://fremd.example.org", "Steve_1"); code != http.StatusConflict {
		t.Errorf("nicht gelistete Adresse: %d", code)
	}
	code, body := beitritt(t, w.a, kb.ts.URL, "Steve_1")
	if code != http.StatusOK || !strings.Contains(body, "angefragt") {
		t.Fatalf("Anfrage: %d %s", code, body)
	}
	if len(kb.joins) != 1 || kb.joins[0] != "Steve_1" {
		t.Errorf("Anfragen: %v", kb.joins)
	}
	// Nur der Koordinator der gelisteten Gruppe sah den Token.
	for _, a := range w.ka.auth {
		_ = a
	}
	if e := w.holen(t); finde(t, e, kb.ts.URL).Zustand != fensterapi.EntdeckenAngefragt {
		t.Errorf("danach: %+v", e.Gruppen)
	}
	if len(group.List()) != 1 {
		t.Error("vor der Annahme darf keine Gruppe entstehen")
	}
	// Admin nimmt an: der nächste Abruf legt die Gruppe an.
	kb.mu.Lock()
	kb.you = api.YouMember
	kb.mu.Unlock()
	w.holen(t)
	if len(group.List()) != 2 {
		t.Errorf("nach der Annahme: %v", group.List())
	}
	if strings.Contains(w.logs.String(), entdeckToken) {
		t.Error("Token im Log")
	}
}

func TestEntdeckenBeitrittSofortAngenommenUndFehler(t *testing.T) {
	w := neueEntdeckWelt(t)
	kb := neuerKoord(t, "Fremde", api.YouNone)
	kb.joinSt = api.JoinAccepted
	w.web.setze(w.ka, kb)
	w.holen(t)
	code, body := beitritt(t, w.a, kb.ts.URL, "Steve_1")
	if code != http.StatusOK || !strings.Contains(body, "mitglied") || len(group.List()) != 2 {
		t.Fatalf("%d %s %v", code, body, group.List())
	}
	for status, want := range map[int]string{400: "geht nicht", 403: "Beitrittsanfragen", 404: "Beitrittsanfragen", 409: "schon", 429: "nicht möglich", 503: "ging nicht durch"} {
		w := neueEntdeckWelt(t)
		kb := neuerKoord(t, "F", api.YouNone)
		kb.joinErr = status
		w.web.setze(w.ka, kb)
		w.holen(t)
		code, body := beitritt(t, w.a, kb.ts.URL, "Steve_1")
		if code < 400 || !strings.Contains(body, want) {
			t.Errorf("%d: %d %s", status, code, body)
		}
	}
}

func TestEntdeckenVerzeichnisFehler(t *testing.T) {
	for status, want := range map[int]string{401: "Token nicht", 404: "keine gelisteten", 429: "überlastet", 503: "überlastet", 500: "nicht erreichbar"} {
		w := neueEntdeckWelt(t)
		w.web.status = status
		e := w.holen(t)
		if len(e.Gruppen) != 0 || len(e.Hinweise) != 1 || !strings.Contains(e.Hinweise[0], want) {
			t.Errorf("%d: %+v", status, e)
		}
		if strings.Contains(strings.Join(e.Hinweise, " "), entdeckToken) || strings.Contains(w.logs.String(), entdeckToken) {
			t.Errorf("%d: Token geleakt", status)
		}
	}
}

func TestEntdeckenUeberlastetPausiert(t *testing.T) {
	w := neueEntdeckWelt(t)
	w.web.status = http.StatusTooManyRequests
	w.holen(t)
	n := w.web.hits.Load()
	e := w.holen(t) // zweiter Abruf: das Verzeichnis wird in Ruhe gelassen
	if w.web.hits.Load() != n || len(e.Hinweise) != 1 || !strings.Contains(e.Hinweise[0], "überlastet") {
		t.Errorf("Pause: Treffer %d -> %d, %+v", n, w.web.hits.Load(), e)
	}
}

func TestEntdeckenFrischerStandWirdGemerkt(t *testing.T) {
	w := neueEntdeckWelt(t)
	w.web.setze(w.ka)
	w.holen(t)
	n := w.web.hits.Load()
	w.a.entdecken(context.Background(), false)
	if w.web.hits.Load() != n {
		t.Error("frischer Stand soll nicht neu geholt werden")
	}
}

func TestEntdeckenMuellInfoUndVerzeichnis(t *testing.T) {
	w := neueEntdeckWelt(t)
	kb := neuerKoord(t, "Muell", api.YouNone)
	kb.infoRaw = `{"title":"x\u0000y","members":1,"you":"keiner"}`
	w.web.setze(w.ka, kb)
	e := w.holen(t)
	b := finde(t, e, kb.ts.URL)
	if b.Zustand != fensterapi.EntdeckenUnbekannt || b.Fehler == "" || b.Titel != "Verz-Titel Muell" {
		t.Errorf("%+v", b)
	}
	// Fremde Adressen im Verzeichnis werden verworfen; es geht nie ein Token dorthin.
	fremd := neuerKoord(t, "Fremd", api.YouNone)
	w.web.mu.Lock()
	w.web.eintraege = []map[string]string{{"name": "x", "title": "Böse", "url": "https://evil.example.org"},
		{"name": "y", "title": "Böse2", "url": "http://" + strings.Replace(mustHost(t, fremd.ts.URL), "127.0.0.1", "localhost", 1)}}
	w.web.mu.Unlock()
	e = w.holen(t)
	if len(e.Gruppen) != 0 {
		t.Errorf("fremde Adressen angenommen: %+v", e.Gruppen)
	}
	if len(fremd.auth) != 0 {
		t.Errorf("Token an fremden Host geschickt: %v", fremd.auth)
	}
}

func mustHost(t *testing.T, u string) string {
	t.Helper()
	p, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return p.Host
}

func TestEntdeckenOhneTunnelZugang(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	a := busyApp(t, nil)
	e := a.entdecken(context.Background(), true)
	if len(e.Gruppen) != 0 || len(e.Hinweise) != 1 {
		t.Errorf("%+v", e)
	}
}

// Ein Wechsel auf die neu angelegte Gruppe richtet sie ein (eigene PC-Kennung,
// Profil des PCs), ohne die alte zu berühren.
func TestWechselAufEntdeckteGruppe(t *testing.T) {
	w := neueEntdeckWelt(t)
	kd := neuerKoord(t, "Neue", api.YouMember)
	w.web.setze(w.ka, kd)
	st, _ := group.LoadState()
	st.MachineID, st.InstalledVersion = "pc-alt", "1"
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	w.holen(t)
	if err := w.a.load(); err != nil {
		t.Fatal(err)
	}
	if err := w.a.switchGroup(mustID(t, kd.ts.URL)); err != nil {
		t.Fatal(err)
	}
	if w.a.machineID == "" || w.a.machineID == "pc-alt" {
		t.Errorf("PC-Kennung: %q", w.a.machineID)
	}
	old, _ := group.LoadStateOf(mustID(t, w.ka.ts.URL))
	if old.MachineID != "pc-alt" {
		t.Errorf("alte Gruppe verändert: %+v", old)
	}
}
