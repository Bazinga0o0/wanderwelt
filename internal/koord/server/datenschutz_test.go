package server_test

// Datenschutz: E-Mail-Adressen sieht nur der Serverbetreiber. Diese Tests
// fragen alle Endpunkte mit einem Mitglied und mit Admins ab, solange der
// Speicher voller Mitglieder, Anfragen, Gäste und Banns ist, und suchen in
// jeder Antwort nach fremden Adressen.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/kennung"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
)

const (
	emailD     = "dora@example.org" // Mitglied mit Namen, keine Rollen
	emailEmil  = "emil.privat@geheim.example.net"
	emailFritz = "fritz.p@anfrage.example.com"
)

var (
	userD    = who{emailD, 404}
	guestEml = who{emailEmil, 505}
)

// alleAdressen sind alle Adressen, die in den Daten des Koordinators stehen.
var alleAdressen = []string{emailA, emailB, emailC, emailD, emailEmil, emailFritz, stranger.email}

// datenschutzEnv: Mitglieder Alice (Admin, hostet), Bob, Carol (Admin ohne
// Minecraft-Namen), Dora; offene Beitrittsanfragen von Emil und Fritz; ein
// Gast und ein Bann, die Carol eingetragen hat.
func newDatenschutzEnv(t *testing.T) *hoEnv {
	t.Helper()
	e := newRestoreEnvWith(t, func(c *config.Config) {
		c.PortHolder.HandoverPorts = []int{portR1, portR2}
		c.Members = append(c.Members,
			config.Member{Email: emailC, Admin: true},
			config.Member{Email: emailD, Minecraft: "Dora"})
		jetzt := time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)
		joins := config.JoinEntries{
			strings.ToLower(emailEmil):  {Email: emailEmil, Minecraft: "Emil_P", Requested: jetzt, State: config.JoinStateOpen},
			strings.ToLower(emailFritz): {Email: emailFritz, Minecraft: "Bob", Requested: jetzt.Add(time.Minute), State: config.JoinStateOpen}, // Name schon vergeben
		}
		if err := config.SaveJoins(filepath.Join(c.DataDir, config.JoinsFile), joins); err != nil {
			t.Fatal(err)
		}
		lists := config.ServerLists{
			Guests: []config.Guest{{Name: "Gast_Eins", UUID: "cccccccc-cccc-cccc-cccc-cccccccccccc", By: emailC, Added: jetzt}},
			Bans:   []config.Ban{{Name: "Troll_X", UUID: "dddddddd-dddd-dddd-dddd-dddddddddddd", By: emailC, Added: jetzt}},
		}
		if err := config.SaveServerLists(filepath.Join(c.DataDir, config.ServerListsFile), lists); err != nil {
			t.Fatal(err)
		}
	})
	e.ph.take()
	return &hoEnv{e}
}

// fremdeAdressen prüft body auf jede Adresse außer der von viewer und auf
// jedes „@“ außerhalb dieser eigenen Adresse; sie liefert die Funde.
func fremdeAdressen(body []byte, viewer string) []string {
	text := strings.ToLower(string(body))
	text = strings.ReplaceAll(text, strings.ToLower(viewer), "<eigene>")
	var funde []string
	for _, a := range alleAdressen {
		if !strings.EqualFold(a, viewer) && strings.Contains(text, strings.ToLower(a)) {
			funde = append(funde, a)
		}
	}
	if strings.Contains(text, "@") {
		funde = append(funde, "ein „@“ (fremde Adresse?)")
	}
	return funde
}

func TestFremdeAdressenWerdenGefunden(t *testing.T) {
	// Die Prüfung selbst: Sie darf eine eigene Adresse durchlassen und muss
	// jede fremde finden (sonst wäre jeder Test mit ihr wertlos).
	if f := fremdeAdressen([]byte(`{"email":"`+emailA+`"}`), emailA); len(f) != 0 {
		t.Errorf("eigene Adresse gemeldet: %v", f)
	}
	if f := fremdeAdressen([]byte(`{"x":"Bob@Example.org"}`), emailA); len(f) == 0 {
		t.Error("fremde Adresse nicht gefunden")
	}
	if f := fremdeAdressen([]byte(`{"x":"jemand@anderswo.test"}`), emailA); len(f) == 0 {
		t.Error("unbekannte Adresse (@) nicht gefunden")
	}
}

// route ist ein Eintrag der Liste aller Endpunkte: get der Pfad zum
// Abfragen (mit eingesetzten Platzhaltern), sonst warum nicht (skip).
type route struct {
	key  string // „METHODE Muster“ wie im Router
	get  string // konkreter Pfad (nur GET)
	skip string // Begründung, wenn der Endpunkt hier nicht als GET geprüft wird
}

// datenschutzRouten: ALLE Endpunkte des Routers. Ein neuer Endpunkt muss hier
// eingetragen werden (TestRouterUndListeStimmenUeberein): GET zum Abfragen
// mit dem Speicher voller Mitglieder, sonst mit Begründung. Die Antworten
// auf Änderungen prüft TestAntwortenDerAktionen.
var datenschutzRouten = []route{
	{key: "GET /v1/me", get: "/v1/me"},
	{key: "GET /v1/status", get: "/v1/status"},
	{key: "GET /v1/manifest", get: "/v1/manifest"},
	{key: "GET /v1/artifacts/{name}", get: "/v1/artifacts/server.jar"},
	{key: "POST /v1/play", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "POST /v1/lease/heartbeat", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "POST /v1/lease/release", skip: "Aktion, Antwort 204"},
	{key: "POST /v1/presence", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "POST /v1/handover/start", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "POST /v1/handover/ready", skip: "Aktion, Antwort 204"},
	{key: "POST /v1/handover/abort", skip: "Aktion, Antwort 204"},
	{key: "GET /v1/host/settings", get: "/v1/host/settings"},
	{key: "POST /v1/blobs/missing", skip: "Aktion mit Prüfsummen (keine Mitgliedsdaten)"},
	{key: "POST /v1/blobs/fetch", skip: "Aktion mit Prüfsummen (keine Mitgliedsdaten)"},
	{key: "POST /v1/blobs/batch", skip: "Aktion mit Prüfsummen (keine Mitgliedsdaten)"},
	{key: "PUT /v1/blobs/{sha}", skip: "Aktion mit Prüfsummen (keine Mitgliedsdaten)"},
	{key: "GET /v1/blobs/{sha}", get: "/v1/blobs/" + strings.Repeat("ab", 32)},
	{key: "GET /v1/generations", get: "/v1/generations"},
	{key: "POST /v1/restore", skip: "Aktion; Antwort ohne Mitglieder (Stand-Nummern)"},
	{key: "GET /v1/generations/head", get: "/v1/generations/head"},
	{key: "GET /v1/generations/{world}/{n}", get: "/v1/generations/{WELT}/1"}, // {WELT}: Weltkennung des Heads
	{key: "POST /v1/generations", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "PUT /v1/profile/{machine}", skip: "Aktion, Antwort 204"},
	{key: "GET /v1/stats", get: "/v1/stats"},
	{key: "GET /v1/members", get: "/v1/members"},
	{key: "PUT /v1/members/{email}", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "PUT /v1/members/{email}/minecraft", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "POST /v1/me/minecraft", skip: "Aktion; Antwort ist Me (eigene Adresse)"},
	{key: "PUT /v1/storage/budget", skip: "Aktion; Antwort ohne Mitglieder (Speicherzahlen)"},
	{key: "POST /v1/speedtest", skip: "Aktion; Zufallsdaten"},
	{key: "GET /v1/ping", get: "/v1/ping"},
	{key: "POST /v1/join", skip: "Aktion; Antwort ist JoinStatus (ohne Adresse)"},
	{key: "GET /v1/join", get: "/v1/join"},
	{key: "GET /v1/info", get: "/v1/info"},
	{key: "GET /v1/join-requests", get: "/v1/join-requests"},
	{key: "POST /v1/join-requests/{email}/accept", skip: "Aktion; Fehlertexte in TestAntwortenDerAktionen"},
	{key: "POST /v1/join-requests/{email}/decline", skip: "Aktion; Fehlertexte in TestAntwortenDerAktionen"},
	{key: "GET /v1/health", get: "/v1/health"},
	{key: "GET /v1/karte/{welt}/manifest.json", get: "/v1/karte/{WELT}/manifest.json"},
	{key: "GET /v1/karte/{welt}/t/{z}/{x}/{y}", get: "/v1/karte/{WELT}/t/0/0/0"},
	{key: "GET /v1/worlds", get: "/v1/worlds"},
	{key: "POST /v1/worlds", skip: "Aktion; Antwort ist die Welt (ohne Mitglieder)"},
	{key: "PATCH /v1/worlds/{id}", skip: "Aktion; Antwort ist die Welt (ohne Mitglieder)"},
	{key: "DELETE /v1/worlds/{id}", skip: "Aktion, Antwort 204"},
	{key: "GET /v1/server/lists", get: "/v1/server/lists"},
	{key: "POST /v1/server/lists", skip: "Aktion; Antwort in TestAntwortenDerAktionen"},
	{key: "GET /v1/server/gamerules", get: "/v1/server/gamerules"},
	{key: "POST /v1/server/gamerules", skip: "Aktion; Antwort ist Minecraft-Text"},
	{key: "POST /v1/server/command", skip: "Aktion; Antwort ist Minecraft-Text"},
	{key: "POST /v1/host/jobs", skip: "Host-Abruf; Aufträge ohne Mitglieder"},
	{key: "POST /v1/host/jobs/result", skip: "Host-Meldung, Antwort 204"},
	{key: "/", skip: "Auffangroute (404)"},
}

// routerRouten liest alle HandleFunc-Muster aus dem Quelltext des Routers.
func routerRouten(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile(`\.HandleFunc\("([^"]+)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllSubmatch(b, -1) {
			out = append(out, string(m[1]))
		}
	}
	slices.Sort(out)
	if len(out) < 20 {
		t.Fatalf("nur %d Routen im Quelltext gefunden: Suche kaputt?", len(out))
	}
	return out
}

// Meldet jeden Pfad, den der Router hat und die Liste nicht kennt (und
// umgekehrt): Ein neuer Endpunkt kann so nicht am Datenschutz-Test vorbei.
func TestRouterUndListeStimmenUeberein(t *testing.T) {
	var liste []string
	for _, r := range datenschutzRouten {
		liste = append(liste, r.key)
		if (r.get == "") == (r.skip == "") {
			t.Errorf("%s: genau eines von get und skip angeben", r.key)
		}
		if r.get != "" && !strings.HasPrefix(r.key, "GET ") {
			t.Errorf("%s: get nur bei GET", r.key)
		}
	}
	slices.Sort(liste)
	for _, k := range routerRouten(t) {
		if !slices.Contains(liste, k) {
			t.Errorf("Der Router hat %q, datenschutzRouten nicht: Endpunkt eintragen (GET zum Abfragen oder skip mit Begründung)", k)
		}
	}
	router := routerRouten(t)
	for _, k := range liste {
		if !slices.Contains(router, k) {
			t.Errorf("datenschutzRouten hat %q, der Router nicht mehr: Eintrag streichen", k)
		}
	}
}

// Kein Endpunkt, den ein Mitglied oder Admin abfragen kann, nennt eine fremde
// Adresse: nicht im Status (auch nicht, während jemand hostet und eine
// Übergabe läuft), nicht in Mitgliederliste, Anfragen, Stand-Liste, Serverlisten,
// Statistik, Gesundheit, Welten, Info.
func TestKeineFremdeEmailInGETAntworten(t *testing.T) {
	t.Cleanup(server.SetJobWait(200 * time.Millisecond)) // Spielregeln fragen den Host; hier antwortet keiner
	e := newDatenschutzEnv(t)
	lidA := e.hostWithPlayers(userA, pcA1)
	e.present(userB, pcB, true)
	e.present(userD, "pc-d", true)
	e.mustStart(userA, pcA1, lidA) // Übergabe läuft: Status trägt das Ziel
	e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob")

	world := getStatus(t, e.base, userB).WorldID
	// Admin ohne Minecraft-Namen: Carols Rollen sind nur ihre; sie kann
	// trotzdem alles abfragen.
	viewers := []who{userA, userB, userC, userD}
	for _, r := range datenschutzRouten {
		if r.get == "" {
			continue
		}
		path := strings.ReplaceAll(r.get, "{WELT}", world)
		for _, v := range viewers {
			code, body := rohGET(t, e.base, v, path)
			if f := fremdeAdressen(body, v.email); len(f) > 0 {
				t.Errorf("%s als %s (%d): fremde Adresse in der Antwort: %v\n%s", path, v.email, code, f, body)
			}
		}
	}
	// Ein Nicht-Mitglied: nur die Beitritts-Endpunkte antworten; auch dort
	// steht nichts Fremdes.
	for _, path := range []string{"/v1/join", "/v1/info", "/v1/me", "/v1/status"} {
		code, body := rohGET(t, e.base, guestEml, path)
		if f := fremdeAdressen(body, guestEml.email); len(f) > 0 {
			t.Errorf("%s als Nicht-Mitglied (%d): %v\n%s", path, code, f, body)
		}
	}

	// Gegenprobe: Die eigene Adresse darf drinstehen, und der Host sieht
	// seine eigene im Status.
	_, body := rohGET(t, e.base, userA, "/v1/status")
	if !bytes.Contains(body, []byte(emailA)) {
		t.Errorf("Der Host sieht seine eigene Adresse nicht im Status: %s", body)
	}
	_, body = rohGET(t, e.base, userB, "/v1/status")
	if bytes.Contains(body, []byte(emailA)) {
		t.Errorf("Bob sieht Alices Adresse im Status: %s", body)
	}
}

func rohGET(t *testing.T, base string, as who, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	setIdentity(req, as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// Die Antworten der Aktionen (Spielen, Übergabe, Rollen, Namen, Anfragen,
// Listen, Stände) nennen ebenfalls nie eine fremde Adresse, auch nicht in
// Fehlertexten.
func TestAntwortenDerAktionen(t *testing.T) {
	e := newDatenschutzEnv(t)
	key := func(email string) string { return e.srv.IDOf(email) }
	prufe := func(was string, as who, code int, body []byte) {
		t.Helper()
		if f := fremdeAdressen(body, as.email); len(f) > 0 {
			t.Errorf("%s als %s (%d): fremde Adresse: %v\n%s", was, as.email, code, f, body)
		}
	}

	// Spielen: Alice hostet; Bob und Dora stoßen dazu (Antwort „join“).
	lidA := e.hostWithPlayers(userA, pcA1)
	for _, v := range []who{userB, userD} {
		code, body := e.raw(v, http.MethodPost, "/v1/play", api.PlayRequest{MachineID: "pc-" + v.email[:1]})
		prufe("play", v, code, body)
		var resp api.PlayResponse
		if err := json.Unmarshal(body, &resp); err != nil || resp.Role != api.RoleJoin ||
			resp.Host == nil || resp.Host.Email != "" || resp.Host.ID != key(emailA) {
			t.Errorf("play als %s: %s, erwartet join mit Host nur mit Name und Kennung", v.email, body)
		}
	}
	// Übergabe: Start (Antwort an Alice), Lebenszeichen, Bereitschaft, Angebot
	// (an Bob), Status.
	e.present(userB, pcB, true)
	var start api.HandoverStartResponse
	code, body := e.raw(userA, http.MethodPost, "/v1/handover/start", api.HandoverStartRequest{LeaseID: lidA, MachineID: pcA1})
	prufe("handover/start", userA, code, body)
	if err := json.Unmarshal(body, &start); err != nil || start.To.Email != "" || start.To.ID != key(emailB) {
		t.Errorf("handover/start: %s", body)
	}
	b, _ := json.Marshal(e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob"))
	prufe("heartbeat", userA, 200, b)
	e.mustReady(userA, pcA1, lidA, start.HandoverID)
	offer := e.present(userB, pcB, true)
	b, _ = json.Marshal(offer)
	prufe("presence", userB, 200, b)
	if offer.Handover == nil || offer.Handover.From.Email != "" || offer.Handover.From.ID != key(emailA) {
		t.Errorf("Angebot: %+v, erwartet Alice nur mit Name und Kennung", offer.Handover)
	}
	// Stand einreichen (Antwort an den Host): die eigene Adresse darf stehen.
	code, body = e.raw(userA, http.MethodPost, "/v1/generations", struct{}{})
	prufe("generations (ungültig)", userA, code, body)

	// Admin-Aktionen mit Kennung: Rolle, Name, Anfragen.
	code, body = e.raw(userA, http.MethodPut, "/v1/members/"+key(emailB), api.RoleRequest{Op: true})
	prufe("PUT members", userA, code, body)
	if code != http.StatusOK {
		t.Errorf("Rolle über die Kennung: %d %s", code, body)
	}
	code, body = e.raw(userA, http.MethodPut, "/v1/members/"+key(emailD)+"/minecraft", api.MinecraftNameRequest{MinecraftName: "Alice"})
	prufe("PUT members/minecraft (vergeben)", userA, code, body)
	// Fremde Adresse im Pfad: kein Ratespiel („gibt es das Mitglied?“),
	// immer 404, egal ob es sie gibt.
	for _, em := range []string{emailB, "gibtsnicht@example.org", strings.ToUpper(emailB)} {
		code, body = e.raw(userA, http.MethodPut, "/v1/members/"+em, api.RoleRequest{Admin: true})
		prufe("PUT members/<fremde Adresse>", userA, code, body)
		if code != http.StatusNotFound {
			t.Errorf("Rolle über fremde Adresse %s: %d, erwartet 404", em, code)
		}
	}
	if code, _ = e.raw(userB, http.MethodGet, "/v1/me", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	var bob api.Me
	call(t, e.base, userB, http.MethodGet, "/v1/me", nil, &bob)
	if bob.Admin {
		t.Error("Bob wurde über eine fremde Adresse zum Admin")
	}
	// Die eigene Adresse geht (ältere Starter schicken sie für die eigene Zeile).
	if code, body = e.raw(userA, http.MethodPut, "/v1/members/"+emailA, api.RoleRequest{Admin: true, Op: true}); code != http.StatusOK {
		t.Errorf("Rolle über die eigene Adresse: %d %s", code, body)
	}
	// Anfragen: Fritz will „Bob“ heißen – Annehmen scheitert, der Text nennt
	// weder Bobs noch Fritz' Adresse.
	code, body = e.raw(userA, http.MethodPost, "/v1/join-requests/"+key(emailFritz)+"/accept", nil)
	prufe("accept (Name vergeben)", userA, code, body)
	if code != http.StatusConflict || !bytes.Contains(body, []byte("Bob")) {
		t.Errorf("accept bei vergebenem Namen: %d %s, erwartet 409 mit Bobs Namen", code, body)
	}
	// Eine fremde Adresse nimmt der Pfad der Anfragen ebenfalls nicht.
	code, body = e.raw(userA, http.MethodPost, "/v1/join-requests/"+emailEmil+"/accept", nil)
	prufe("accept über fremde Adresse", userA, code, body)
	if code != http.StatusNotFound {
		t.Errorf("accept über Adresse: %d, erwartet 404", code)
	}
	code, body = e.raw(userA, http.MethodPost, "/v1/join-requests/"+key(emailEmil)+"/decline", nil)
	prufe("decline", userA, code, body)
	if code != http.StatusNoContent {
		t.Errorf("decline über Kennung: %d %s", code, body)
	}
	// Serverlisten: Carol bannt, Alice sieht Carol als „Mitglied <Kennung>“
	// (Carol hat keinen Minecraft-Namen), nie als Adresse.
	code, body = e.raw(userC, http.MethodPost, "/v1/server/lists", api.ServerListChange{List: api.ListBans, Action: api.ListAdd, Name: "Alice"})
	prufe("server/lists (Bann)", userC, code, body)
	code, body = e.raw(userA, http.MethodGet, "/v1/server/lists", nil)
	prufe("server/lists", userA, code, body)
	if !bytes.Contains(body, []byte("Mitglied "+key(emailC))) {
		t.Errorf("server/lists: Carol sollte als „Mitglied %s“ erscheinen: %s", key(emailC), body)
	}
	// Den einzigen Admin ... (Fehlertext nennt Namen oder Kennung): Alice und
	// Carol sind Admins, erst Carol, dann Alice abwählen.
	e.raw(userA, http.MethodPut, "/v1/members/"+key(emailC), api.RoleRequest{})
	code, body = e.raw(userA, http.MethodPut, "/v1/members/"+key(emailA), api.RoleRequest{Op: true})
	prufe("einziger Admin", userA, code, body)
}

// Kennungen: stabil, unabhängig von Groß-/Kleinschreibung, je Schlüssel
// anders; der Schlüssel liegt 0600 in data_dir und steht nie im Log oder in
// einer Antwort.
func TestKennungImKoordinator(t *testing.T) {
	log := captureLog(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	a := newMemberEnv(t, dirA, gib)
	b := newMemberEnv(t, dirB, gib)
	if a.srv.IDOf(emailB) != a.srv.IDOf("BOB@Example.ORG") {
		t.Error("die Kennung hängt von der Schreibweise ab")
	}
	if a.srv.IDOf(emailB) == a.srv.IDOf(emailC) {
		t.Error("zwei Adressen, eine Kennung")
	}
	if a.srv.IDOf(emailB) == b.srv.IDOf(emailB) {
		t.Error("gleiche Kennung bei verschiedenen Schlüsseln (je Server ein Schlüssel)")
	}
	if !kennung.IstID(a.srv.IDOf(emailB)) {
		t.Errorf("Kennung %q hat nicht die Form 16 Hex-Zeichen", a.srv.IDOf(emailB))
	}
	// Die Liste nennt dieselben Kennungen.
	bekannt := []string{a.srv.IDOf(emailA), a.srv.IDOf(emailB), a.srv.IDOf(emailC)}
	for _, m := range a.members(userA) {
		if !slices.Contains(bekannt, m.ID) {
			t.Errorf("Mitglied %+v ohne bekannte Kennung", m)
		}
	}
	// Der Schlüssel: Datei 0600 in data_dir, Inhalt nie in Log und Antworten.
	checkMode600(t, filepath.Join(dirA, kennung.Datei))
	secret := strings.TrimSpace(readFile(t, filepath.Join(dirA, kennung.Datei)))
	if len(secret) < 32 {
		t.Fatalf("Schlüsseldatei: %q", secret)
	}
	a.host()
	for _, p := range []string{"/v1/members", "/v1/status", "/v1/me", "/v1/health", "/v1/join-requests"} {
		if _, body := rohGET(t, a.base, userA, p); bytes.Contains(body, []byte(secret)) {
			t.Errorf("%s enthält den Schlüssel", p)
		}
	}
	if strings.Contains(log.String(), secret) {
		t.Error("Der Schlüssel steht im Log")
	}
	// Ein Neustart auf demselben data_dir behält den Schlüssel (und damit
	// die Kennungen).
	a2 := newMemberEnv(t, dirA, gib)
	if a2.srv.IDOf(emailB) != a.srv.IDOf(emailB) {
		t.Error("Kennung ändert sich beim Neustart")
	}
}
