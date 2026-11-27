package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const testToken = "tok-GEHEIM-1234"

// joinCoord ist ein Fake-Koordinator für die Einrichtung ohne Gruppendatei.
type joinCoord struct {
	mu          sync.Mutex
	member      bool
	state       string // Stand der Beitrittsanfrage (api.Join…)
	acceptAfter int    // nach so vielen GET /v1/join (nur bei „offen“) angenommen; 0: nie
	declineNext bool   // der nächste GET /v1/join meldet „abgelehnt“
	polls       int
	posts       []string
	postCodes   []int // Antworten auf POST /v1/join der Reihe nach (letzte wiederholt); leer: 201
	tokens      []string

	revokeAtPoll  int // ab diesem GET /v1/join (1-basiert) ist der Token ungültig; 0: nie
	failPolls     int // so viele GET /v1/join antworten zuerst mit 500
	memberAfterMe int // /v1/me meldet erst ab dem (n+1). Aufruf „Mitglied“ (bei angenommener Anfrage)
	meCalls       int
	revoked       bool
	noJoin        bool // alter Koordinator: /v1/join gibt Nicht-Mitgliedern 403
}

func (j *joinCoord) handler(w http.ResponseWriter, r *http.Request) {
	j.mu.Lock()
	defer j.mu.Unlock()
	auth := r.Header.Get("Authorization")
	j.tokens = append(j.tokens, auth)
	if r.URL.Path == "/v1/join" && r.Method == http.MethodGet && j.revokeAtPoll > 0 && j.polls+1 >= j.revokeAtPoll {
		j.revoked = true
	}
	if auth != "Bearer "+testToken || j.revoked {
		w.WriteHeader(401)
		return
	}
	jsonErr := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if j.noJoin && r.URL.Path == "/v1/join" {
		jsonErr(403, "Konto gehört nicht zur Gruppe")
		return
	}
	switch {
	case r.URL.Path == "/v1/me":
		j.meCalls++
		if !j.member && !(j.memberAfterMe > 0 && j.meCalls > j.memberAfterMe) {
			jsonErr(403, "Konto gehört nicht zur Gruppe")
			return
		}
		json.NewEncoder(w).Encode(api.Me{Email: "neu@example.org", MinecraftName: "Alex_1"})
	case r.URL.Path == "/v1/join" && r.Method == http.MethodGet:
		j.polls++
		if j.polls > 1 && j.polls <= j.failPolls+1 { // die erste Abfrage (Start) geht durch
			jsonErr(500, "kaputt")
			return
		}
		if j.declineNext && j.state == api.JoinOpen {
			j.state = api.JoinDeclined
			j.declineNext = false
		}
		if j.state == api.JoinOpen && j.acceptAfter > 0 && j.polls >= j.acceptAfter {
			j.state, j.member = api.JoinAccepted, true
		}
		st := j.state
		if st == "" {
			st = api.JoinNone
		}
		json.NewEncoder(w).Encode(api.JoinStatus{State: st})
	case r.URL.Path == "/v1/join" && r.Method == http.MethodPost:
		var req api.JoinRequest
		json.NewDecoder(r.Body).Decode(&req)
		j.posts = append(j.posts, req.MinecraftName)
		code := 201
		if len(j.postCodes) > 0 {
			code = j.postCodes[min(len(j.posts), len(j.postCodes))-1]
		}
		switch code {
		case 201:
			j.state = api.JoinOpen
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(api.JoinStatus{State: api.JoinOpen})
		case 400:
			jsonErr(400, "Mojang kennt den Namen nicht")
		case 409:
			jsonErr(409, "Name vergeben")
		case 429:
			jsonErr(429, "zu viele offene Anfragen")
		default:
			jsonErr(code, "Fehler")
		}
	default:
		http.NotFound(w, r)
	}
}

// dialogs sammelt, was die Fake-Dialoge zeigen, und liefert die Antworten.
type dialogs struct {
	mu       sync.Mutex
	menu     []tokenChoice // Auswahl der Reihe nach; danach „abbrechen“
	tokens   []string      // Token-Eingaben der Reihe nach; danach „abbrechen“
	names    []string      // Namenseingaben
	confirm  []bool        // Antworten auf confirm
	nameOK   bool          // Antwort auf „Du spielst Minecraft als …“
	urls     []string
	prepared int // Aufrufe von prepareWait
	undone   int // Aufrufe von undoPrepareWait
	errs     []string
	infos    []string
	asked    []string // Texte von confirm und Eingaben (für „kein Token im Text“)
}

func pop[T any](d *dialogs, s *[]T) (T, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var zero T
	if len(*s) == 0 {
		return zero, false
	}
	v := (*s)[0]
	*s = (*s)[1:]
	return v, true
}

func (d *dialogs) note(s *[]string, text string) {
	d.mu.Lock()
	*s = append(*s, text)
	d.mu.Unlock()
}

// stubJoinUI ersetzt alle Dialoge und stellt Home und Minecraft-Ordner auf
// Temp-Verzeichnisse. wait: das Wartefenster (nil: kein Fenster).
func stubJoinUI(t *testing.T, d *dialogs, wait zenity.ProgressDialog) {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	t.Setenv("WANDERWELT_MINECRAFT_DIR", t.TempDir())
	oldMenu, oldTE, oldURL, oldWait, oldPoll := tokenMenu, tokenEntry, openURL, openJoinWait, joinPollInterval
	oldPrep, oldUndo, oldDelay := prepareWait, undoPrepareWait, memberRetryDelay
	oldErr, oldInfo, oldConfirm, oldNC, oldNE := showError, showInfo, confirm, nameConfirm, nameEntry
	t.Cleanup(func() {
		tokenMenu, tokenEntry, openURL, openJoinWait, joinPollInterval = oldMenu, oldTE, oldURL, oldWait, oldPoll
		prepareWait, undoPrepareWait, memberRetryDelay = oldPrep, oldUndo, oldDelay
		showError, showInfo, confirm, nameConfirm, nameEntry = oldErr, oldInfo, oldConfirm, oldNC, oldNE
	})
	tokenMenu = func(text string) (tokenChoice, bool) {
		d.note(&d.asked, text)
		return pop(d, &d.menu)
	}
	tokenEntry = func(text string) (string, bool) {
		d.note(&d.asked, text)
		return pop(d, &d.tokens)
	}
	openURL = func(u string) error { d.note(&d.urls, u); return nil }
	openJoinWait = func() (zenity.ProgressDialog, error) {
		if wait == nil {
			return nil, errors.New("kein Fenster im Test")
		}
		return wait, nil
	}
	joinPollInterval = 5 * time.Millisecond
	memberRetryDelay = time.Millisecond
	undoPrepareWait = func() { d.mu.Lock(); d.undone++; d.mu.Unlock() }
	prepareWait = func() { d.mu.Lock(); d.prepared++; d.mu.Unlock() }
	showError = func(text string) { d.note(&d.errs, text) }
	showInfo = func(text string) { d.note(&d.infos, text) }
	confirm = func(text, yes, no string) bool {
		d.note(&d.asked, text)
		v, _ := pop(d, &d.confirm)
		return v
	}
	nameConfirm = func(text string) bool { d.note(&d.asked, text); return d.nameOK }
	nameEntry = func(text string) (string, bool) {
		d.note(&d.asked, text)
		return pop(d, &d.names)
	}
}

func joinTestApp(t *testing.T, srv *httptest.Server) *App {
	t.Helper()
	return &App{invite: &group.Invite{
		Coordinator:  srv.URL,
		TunnelServer: "tunnel.example.org:7443",
		WebURL:       "https://tunnel.example.org",
		TokenURL:     "https://tunnel.example.org/token",
	}}
}

// captureLog fängt das Log ab (der Token darf nie darin stehen).
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func noSecret(t *testing.T, d *dialogs, logs *bytes.Buffer) {
	t.Helper()
	all := strings.Join(d.errs, "\n") + strings.Join(d.infos, "\n") + strings.Join(d.asked, "\n") + logs.String()
	if strings.Contains(all, testToken) {
		t.Fatalf("Token in Dialog oder Log:\n%s", all)
	}
}

func newJoinWait() *fakeProgress {
	return &fakeProgress{max: 100, done: make(chan struct{})}
}

// Token eines Mitglieds: Menü → Webseiten → Token einfügen → gespeichert, ohne
// Token in Dialogen oder Log.
func TestEinrichtungTokenMitglied(t *testing.T) {
	j := &joinCoord{member: true}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choiceAccount, choiceToken, choicePaste}, tokens: []string{"  " + testToken + " "}}
	stubJoinUI(t, d, nil)
	logs := captureLog(t)

	g, me, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if err != nil || me.MinecraftName != "Alex_1" || g.Token != testToken {
		t.Fatalf("g=%v me=%v err=%v", g, me, err)
	}
	if len(d.urls) != 2 || d.urls[0] != "https://tunnel.example.org" || d.urls[1] != "https://tunnel.example.org/token" {
		t.Fatalf("Webseiten: %v", d.urls)
	}
	saved, err := group.LoadGroup(group.GroupPath())
	if err != nil || saved.Token != testToken || saved.Coordinator != srv.URL || saved.TunnelServer != "tunnel.example.org:7443" {
		t.Fatalf("gespeichert: %+v %v", saved, err)
	}
	if len(j.posts) != 0 {
		t.Fatalf("Mitglied darf nicht anfragen: %v", j.posts)
	}
	noSecret(t, d, logs)
}

// 401: Meldung „Token ungültig oder Konto noch nicht freigeschaltet“, nichts
// gespeichert, erneut versuchen möglich.
func TestEinrichtungToken401(t *testing.T) {
	j := &joinCoord{member: true}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste, choicePaste}, tokens: []string{"falscher-token", testToken}}
	stubJoinUI(t, d, nil)
	logs := captureLog(t)

	app := joinTestApp(t, srv)
	if _, err := os.Stat(group.GroupPath()); err == nil {
		t.Fatal("Gruppendatei schon da")
	}
	g, _, err := app.chooseGroupInvite(context.Background())
	if err != nil || g.Token != testToken {
		t.Fatalf("g=%v err=%v", g, err)
	}
	if len(d.errs) != 1 || !strings.Contains(d.errs[0], "Token ungültig oder Konto noch nicht freigeschaltet") {
		t.Fatalf("Meldungen: %q", d.errs)
	}
	saved, _ := group.LoadGroup(group.GroupPath())
	if saved == nil || saved.Token != testToken {
		t.Fatalf("falscher Token gespeichert? %+v", saved)
	}
	noSecret(t, d, logs)
}

// Nur der falsche Token, dann Abbruch: nichts gespeichert.
func TestEinrichtungToken401Abbruch(t *testing.T) {
	j := &joinCoord{}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{"falsch"}}
	stubJoinUI(t, d, nil)

	_, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if !errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(group.GroupPath()); err == nil {
		t.Fatal("Gruppendatei mit ungültigem Token gespeichert")
	}
}

// 403 → Namen aus dem Launcher bestätigt → POST /v1/join → Warten → angenommen,
// ohne Neustart.
func TestEinrichtungBeitrittAngenommen(t *testing.T) {
	j := &joinCoord{acceptAfter: 3}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	wait := newJoinWait()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, nameOK: true}
	stubJoinUI(t, d, wait)
	writeAcc(t, os.Getenv("WANDERWELT_MINECRAFT_DIR"), "launcher_accounts.json",
		`{"activeAccountLocalId":"a","accounts":{"a":{"minecraftProfile":{"id":"u","name":"Alex_1"}}}}`)
	logs := captureLog(t)

	g, me, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if err != nil || g.Token != testToken || me.MinecraftName != "Alex_1" {
		t.Fatalf("g=%v me=%v err=%v", g, me, err)
	}
	if len(j.posts) != 1 || j.posts[0] != "Alex_1" {
		t.Fatalf("Anfragen: %v", j.posts)
	}
	if d.prepared != 1 || len(d.infos) != 1 || !strings.Contains(d.infos[0], "Du bist dabei") {
		t.Fatalf("prepared=%d infos=%q", d.prepared, d.infos)
	}
	select {
	case <-wait.done: // Wartefenster wurde geschlossen
	default:
		t.Fatal("Wartefenster nicht geschlossen")
	}
	// Token ist schon gespeichert (Neustart während des Wartens macht weiter).
	if saved, err := group.LoadGroup(group.GroupPath()); err != nil || saved.Token != testToken {
		t.Fatalf("gespeichert: %v %v", saved, err)
	}
	noSecret(t, d, logs)
}

// Abgelehnt: Meldung, bei „Beenden“ endet die Einrichtung.
func TestEinrichtungBeitrittAbgelehnt(t *testing.T) {
	j := &joinCoord{declineNext: true}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Alex_1"}, confirm: []bool{false}}
	stubJoinUI(t, d, newJoinWait())

	_, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if !errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
	if len(d.asked) == 0 || !strings.Contains(strings.Join(d.asked, "\n"), "abgelehnt") {
		t.Fatalf("keine Ablehnungsmeldung: %q", d.asked)
	}
}

// Abgelehnt, dann erneut anfragen: 429 (24 h) wird als Meldung gezeigt.
func TestEinrichtungErneutAnfragenZuFrueh(t *testing.T) {
	j := &joinCoord{state: api.JoinDeclined, postCodes: []int{429}}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Alex_1"}, confirm: []bool{true}}
	stubJoinUI(t, d, nil)

	_, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if !errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
	if len(d.errs) != 1 || !strings.Contains(d.errs[0], "zu viele offene Anfragen") {
		t.Fatalf("Meldungen: %q", d.errs)
	}
}

// Name schon vergeben/angefragt (409) und von Mojang abgelehnt (400): erneut
// fragen, dann geht der dritte Name durch.
func TestEinrichtungNameVergeben(t *testing.T) {
	j := &joinCoord{postCodes: []int{409, 400, 201}, acceptAfter: 1}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Vergeben_1", "Unbekannt", "Frei_3"}}
	stubJoinUI(t, d, newJoinWait())

	_, me, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if err != nil || me == nil {
		t.Fatalf("me=%v err=%v", me, err)
	}
	if got := strings.Join(j.posts, ","); got != "Vergeben_1,Unbekannt,Frei_3" {
		t.Fatalf("Anfragen: %s", got)
	}
	asked := strings.Join(d.asked, "\n")
	if !strings.Contains(asked, "gehört schon einem Mitglied oder ist schon angefragt") || !strings.Contains(asked, "Mojang kennt den Namen nicht") {
		t.Fatalf("Hinweise zu den Namen fehlen:\n%s", asked)
	}
}

// Ungültige Eingaben (Länge, Zeichen) gehen nicht an den Koordinator.
func TestEinrichtungNameUngueltigLokal(t *testing.T) {
	j := &joinCoord{acceptAfter: 1}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"ab", "mit Leerzeichen", "Gut_Name"}}
	stubJoinUI(t, d, newJoinWait())

	if _, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(j.posts) != 1 || j.posts[0] != "Gut_Name" {
		t.Fatalf("Anfragen: %v", j.posts)
	}
}

// Name beim Erkennen „stimmt nicht“: Eingabe statt des erkannten Namens.
func TestEinrichtungErkannterNameAbgelehnt(t *testing.T) {
	j := &joinCoord{acceptAfter: 1}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Anderer_2"}, nameOK: false}
	stubJoinUI(t, d, newJoinWait())
	writeAcc(t, os.Getenv("WANDERWELT_MINECRAFT_DIR"), "launcher_accounts.json",
		`{"activeAccountLocalId":"a","accounts":{"a":{"minecraftProfile":{"id":"u","name":"Alex_1"}}}}`)

	if _, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(j.posts) != 1 || j.posts[0] != "Anderer_2" {
		t.Fatalf("Anfragen: %v", j.posts)
	}
}

// Neustart während des Wartens: gruppe.yaml mit Token liegt da, die Anfrage
// ist offen – keine zweite Anfrage, kein Token-Dialog.
func TestEinrichtungWeiterBeimWarten(t *testing.T) {
	j := &joinCoord{state: api.JoinOpen, acceptAfter: 2}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{}
	stubJoinUI(t, d, newJoinWait())
	if err := group.SaveGroup(joinTestApp(t, srv).invite.WithToken(testToken)); err != nil {
		t.Fatal(err)
	}

	_, me, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if err != nil || me == nil {
		t.Fatalf("me=%v err=%v", me, err)
	}
	if len(j.posts) != 0 || len(d.asked) != 0 {
		t.Fatalf("Anfragen %v, Dialoge %q", j.posts, d.asked)
	}
}

// 429 ohne eigene offene Anfrage: Meldung mit dem Text des Koordinators.
func TestEinrichtungAnfrage429(t *testing.T) {
	j := &joinCoord{postCodes: []int{429}}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Alex_1"}}
	stubJoinUI(t, d, nil)

	_, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if !errors.Is(err, errCanceled) || len(d.errs) != 1 || !strings.Contains(d.errs[0], "zu viele offene Anfragen") {
		t.Fatalf("err=%v errs=%q", err, d.errs)
	}
}

// „Später fortsetzen“ im Wartefenster: Autostart/Verknüpfung sind vorher
// eingetragen, Hinweis, kein Fehler (errLater → Exit 0); die Gruppendatei
// bleibt für den nächsten Start.
func TestEinrichtungWartenAbbrechen(t *testing.T) {
	j := &joinCoord{state: api.JoinOpen}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	wait := newJoinWait()
	wait.Close() // der Nutzer hat das Fenster geschlossen
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}}
	stubJoinUI(t, d, wait)
	joinPollInterval = time.Hour

	_, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if !errors.Is(err, errLater) || errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
	if d.prepared != 1 || d.undone != 0 {
		t.Fatalf("Autostart/Verknüpfung: prepared=%d undone=%d", d.prepared, d.undone)
	}
	if len(d.infos) != 1 || !strings.Contains(d.infos[0], "startet mit Windows wieder und meldet sich, sobald ein Admin dich angenommen hat") {
		t.Fatalf("Hinweis: %q", d.infos)
	}
	if saved, err := group.LoadGroup(group.GroupPath()); err != nil || saved.Token != testToken {
		t.Fatalf("Gruppendatei fehlt: %v %v", saved, err)
	}
}

// Menü abbrechen und Token-Eingabe abbrechen (zurück ins Menü).
func TestEinrichtungAbbrechen(t *testing.T) {
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: nil}
	stubJoinUI(t, d, nil)
	app := &App{invite: &group.Invite{Coordinator: "http://127.0.0.1:1", TunnelServer: "t:1", WebURL: "https://x.example.org"}}
	if _, _, err := app.chooseGroupInvite(context.Background()); !errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
}

// Ein ungeeigneter Token (Leerzeichen/Zeilenumbruch) kommt nie zum Koordinator.
func TestEinrichtungTokenFormPruefung(t *testing.T) {
	j := &joinCoord{member: true}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{"", "zwei Woerter", testToken}}
	stubJoinUI(t, d, nil)

	if _, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, a := range j.tokens {
		if a != "Bearer "+testToken {
			t.Fatalf("falscher Token gesendet: %q", a)
		}
	}
}

// textProgress merkt sich die Texte des Wartefensters.
type textProgress struct {
	*fakeProgress
	mu    sync.Mutex
	texts []string
}

func (p *textProgress) Text(s string) error {
	p.mu.Lock()
	p.texts = append(p.texts, s)
	p.mu.Unlock()
	return nil
}

// Token während des Wartens entzogen (401): Fenster zu, Meldung, Token neu
// eingeben lassen (hier: Abbruch im Menü).
func TestEinrichtungTokenEntzogBeimWarten(t *testing.T) {
	j := &joinCoord{state: api.JoinOpen, revokeAtPoll: 1}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	wait := newJoinWait()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}}
	stubJoinUI(t, d, wait)
	// Erste Abfrage (awaitJoin) muss noch durch, erst das Warten sieht 401.
	j.revokeAtPoll = 2

	_, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if !errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
	if len(d.errs) != 1 || !strings.Contains(d.errs[0], "Token ungültig oder Konto noch nicht freigeschaltet") {
		t.Fatalf("Meldungen: %q", d.errs)
	}
	select {
	case <-wait.done:
	default:
		t.Fatal("Wartefenster nicht geschlossen")
	}
}

// Dauerhafte Fehler beim Abfragen: nach 3 in Folge sagt es das Wartefenster,
// es versucht weiter und stellt den Text nach Erfolg wieder her.
func TestEinrichtungWartenFehlerHinweis(t *testing.T) {
	j := &joinCoord{state: api.JoinOpen, acceptAfter: 8, failPolls: 4}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	wait := &textProgress{fakeProgress: newJoinWait()}
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}}
	stubJoinUI(t, d, wait)

	_, me, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if err != nil || me == nil {
		t.Fatalf("me=%v err=%v", me, err)
	}
	wait.mu.Lock()
	defer wait.mu.Unlock()
	n := len(wait.texts)
	if n != 3 || !strings.Contains(wait.texts[1], "klappt gerade nicht") || strings.Contains(wait.texts[2], "klappt gerade nicht") {
		t.Fatalf("Texte: %q", wait.texts)
	}
	for _, txt := range wait.texts {
		kurzeZeile(t, "Wartefenster", txt) // eine kurze Zeile: das Fenster ist unter Windows fest (dialoge.go)
	}
}

// Angenommen, der Koordinator bestätigt die Mitgliedschaft erst beim dritten
// Versuch: kein Abbruch.
func TestEinrichtungAngenommenMitgliedVerzoegert(t *testing.T) {
	j := &joinCoord{state: api.JoinAccepted, memberAfterMe: 3}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}}
	stubJoinUI(t, d, nil)

	_, me, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if err != nil || me == nil {
		t.Fatalf("me=%v err=%v", me, err)
	}
}

// Endet die Einrichtung endgültig ohne Mitgliedschaft (abgelehnt, nicht erneut
// angefragt), werden Autostart/Verknüpfung zurückgenommen; bei „Später
// fortsetzen“ bleiben sie.
func TestEinrichtungAutostartZurueck(t *testing.T) {
	j := &joinCoord{declineNext: true}
	srv := httptest.NewServer(http.HandlerFunc(j.handler))
	defer srv.Close()
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Alex_1"}, confirm: []bool{false}}
	stubJoinUI(t, d, newJoinWait())
	if _, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background()); !errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
	if d.prepared != 1 || d.undone != 1 {
		t.Fatalf("prepared=%d undone=%d", d.prepared, d.undone)
	}
}

// Alter Koordinator ohne Beitrittsanfragen (403 auf /v1/join): klare Meldung;
// „Abbrechen“ endet, „Gruppendatei wählen“ führt zur Gruppendatei (auch mit
// schon gespeichertem Token). Keine Endlosschleife.
func TestEinrichtungAlterKoordinator(t *testing.T) {
	old := &joinCoord{noJoin: true}
	srv := httptest.NewServer(http.HandlerFunc(old.handler))
	defer srv.Close()

	// Abbrechen.
	d := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Alex_1"}, confirm: []bool{false}}
	stubJoinUI(t, d, nil)
	_, _, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if !errors.Is(err, errCanceled) {
		t.Fatalf("err=%v", err)
	}
	if len(d.asked) == 0 || !strings.Contains(strings.Join(d.asked, "\n"), "kann noch keine Beitrittsanfragen – bitte den Admin um eine Gruppendatei") {
		t.Fatalf("Meldung fehlt: %q", d.asked)
	}
	if len(old.posts) != 0 {
		t.Fatalf("Anfragen: %v", old.posts)
	}

	// Gruppendatei wählen: Der Token ist schon gespeichert, die Datei zeigt
	// auf einen Koordinator, der Mitglieder kennt.
	good := &joinCoord{member: true}
	srv2 := httptest.NewServer(http.HandlerFunc(good.handler))
	defer srv2.Close()
	d2 := &dialogs{menu: []tokenChoice{choicePaste}, tokens: []string{testToken}, names: []string{"Alex_1"}, confirm: []bool{true}}
	stubJoinUI(t, d2, nil)
	file := filepath.Join(t.TempDir(), "gruppe-anna.yaml")
	os.WriteFile(file, []byte("coordinator: "+srv2.URL+"\ntunnel_server: t:1\ntoken: "+testToken+"\n"), 0o600)
	oldSel := selectGroupFile
	t.Cleanup(func() { selectGroupFile = oldSel })
	calls := 0
	selectGroupFile = func() (string, error) { calls++; return file, nil }
	g, me, err := joinTestApp(t, srv).chooseGroupInvite(context.Background())
	if err != nil || me == nil || g.Coordinator != srv2.URL || calls != 1 {
		t.Fatalf("g=%v me=%v err=%v calls=%d", g, me, err, calls)
	}
}
