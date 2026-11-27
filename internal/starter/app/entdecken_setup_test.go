package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// setupStubs ersetzt die Dialoge der Einrichtung durch Antworten aus Listen.
type setupStubs struct {
	menu       []string
	server     []string
	hints      []string
	token      string
	picks      []int
	items      [][]string
	tokenAsked int
	// tokenSteps: Antworten des Token-Menüs nach der Server-Adresse (leer: „Token einfügen“, wenn ein Token da ist, sonst Abbruch); opened: geöffnete Webseiten.
	tokenSteps []tokenChoice
	opened     []string
}

func stubSetup(t *testing.T, web string, s *setupStubs) {
	t.Helper()
	oMenu, oServer, oPick, oWeb, oToken, oTMenu, oOpen := setupMenu, askTunnelServer, pickGroup, webFuerServer, tokenEntry, tokenMenu, openURL
	t.Cleanup(func() {
		setupMenu, askTunnelServer, pickGroup, webFuerServer, tokenEntry, tokenMenu, openURL = oMenu, oServer, oPick, oWeb, oToken, oTMenu, oOpen
	})
	setupMenu = func() (string, bool) {
		if len(s.menu) == 0 {
			return "", false
		}
		c := s.menu[0]
		s.menu = s.menu[1:]
		return c, true
	}
	askTunnelServer = func(def, hint string) (string, bool) {
		s.hints = append(s.hints, hint)
		if len(s.server) == 0 {
			return "", false
		}
		v := s.server[0]
		s.server = s.server[1:]
		return v, true
	}
	pickGroup = func(items []string) (int, bool) {
		s.items = append(s.items, items)
		if len(s.picks) == 0 {
			return 0, false
		}
		i := s.picks[0]
		s.picks = s.picks[1:]
		return i, true
	}
	webFuerServer = func(string) string { return web }
	tokenEntry = func(string) (string, bool) {
		s.tokenAsked++
		return s.token, s.token != ""
	}
	tokenMenu = func(string) (tokenChoice, bool) {
		if len(s.tokenSteps) > 0 {
			c := s.tokenSteps[0]
			s.tokenSteps = s.tokenSteps[1:]
			return c, true
		}
		if s.token == "" {
			return 0, false // Abbruch
		}
		return choicePaste, true
	}
	openURL = func(u string) error {
		s.opened = append(s.opened, u)
		return nil
	}
}

// Wer noch kein Konto hat (öffentlicher Download), bekommt nach der
// Server-Adresse die Webseite des Tunnel-Servers geöffnet (Konto anlegen,
// Token erstellen) und fügt dann den Token ein. „Gruppendatei“ führt zurück
// zur Server-Frage mit einem Hinweis.
func TestEntdeckenKontoAnlegenOeffnetWebseite(t *testing.T) {
	w := neueEntdeckWelt(t)
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	k := neuerKoord(t, "Wandergruppe", api.YouNone)
	w.web.setze(k)
	s := &setupStubs{
		server:     []string{"tunnel.example.org", "tunnel.example.org"},
		token:      entdeckToken,
		picks:      []int{0},
		tokenSteps: []tokenChoice{choiceFile, choiceAccount, choiceToken, choicePaste},
	}
	stubSetup(t, w.web.ts.URL, s)
	a := busyApp(t, nil)
	inv, token, err := a.entdeckenSetup(context.Background())
	if err != nil || inv == nil || token != entdeckToken {
		t.Fatalf("%+v %q %v", inv, token, err)
	}
	if len(s.opened) != 2 || s.opened[0] != w.web.ts.URL || s.opened[1] != w.web.ts.URL {
		t.Errorf("geöffnet: %v, erwartet zweimal %s", s.opened, w.web.ts.URL)
	}
	// Die zweite Server-Frage zeigt den Hinweis auf die Gruppendatei.
	if len(s.hints) < 2 || !strings.Contains(s.hints[1], "Gruppendatei") {
		t.Errorf("Hinweise: %q", s.hints)
	}
	if strings.Contains(strings.Join(s.hints, "|"), entdeckToken) || strings.Contains(w.logs.String(), entdeckToken) {
		t.Error("Token geleakt")
	}
}

func TestSetupMenuWege(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	s := &setupStubs{}
	stubSetup(t, "", s)
	cases := map[string]error{menuEntdecken: errEntdeckenWahl, menuDatei: zenity.ErrExtraButton}
	for choice, want := range cases {
		s.menu = []string{choice}
		if _, err := askGroupCode(""); err != want {
			t.Errorf("%s: %v", choice, err)
		}
	}
	s.menu = nil // Abbruch
	if _, err := askGroupCode(""); err != zenity.ErrCanceled {
		t.Errorf("Abbruch: %v", err)
	}
}

// Der ganze Weg: Entdecken wählen, Server und Token angeben, Gruppe aus dem
// Verzeichnis wählen; die Einrichtung fragt den Token nicht noch einmal und
// legt gruppen/<id>/gruppe.yaml samt web_url an.
func TestEinrichtungUeberEntdecken(t *testing.T) {
	w := neueEntdeckWelt(t)
	// Frische Einrichtung: keine Gruppe.
	for _, l := range group.List() {
		_ = l
	}
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	k := neuerKoord(t, "Wandergruppe", api.YouMember)
	k2 := neuerKoord(t, "Andere", api.YouNone)
	w.web.setze(k2, k) // Reihenfolge: gewählt wird die zweite (Index 1)
	// /v1/me des gewählten Koordinators: wir sind Mitglied.
	me := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me" {
			json.NewEncoder(wr).Encode(api.Me{Email: "x@example.org", MinecraftName: "Steve_1"})
			return
		}
		k.ts.Config.Handler.ServeHTTP(wr, r)
	}))
	t.Cleanup(me.Close)
	w.web.mu.Lock()
	w.web.eintraege[1]["url"] = me.URL
	w.web.mu.Unlock()
	s := &setupStubs{menu: []string{menuEntdecken}, server: []string{"tunnel.example.org:7443"}, token: entdeckToken, picks: []int{1}}
	stubSetup(t, w.web.ts.URL, s)
	a := busyApp(t, nil)
	if !a.needsGroupCodeOffer() {
		t.Fatal("ohne Gruppe soll die Einrichtung fragen")
	}
	inv, err := a.groupCodeOffer()
	if err != nil || inv == nil {
		t.Fatalf("%v %v", inv, err)
	}
	if inv.Coordinator != me.URL || inv.TunnelServer != "tunnel.example.org:7443" || inv.WebURL != w.web.ts.URL {
		t.Fatalf("Adressen: %+v", inv)
	}
	if len(s.items) != 1 || len(s.items[0]) != 2 || !strings.Contains(s.items[0][0], "Andere") || !strings.Contains(s.items[0][1], "Wandergruppe – 4 Mitgl. (Mitglied)") {
		t.Errorf("Auswahl: %q", s.items)
	}
	a.invite = inv
	tokenAsked := s.tokenAsked
	g, m, err := a.chooseGroupInvite(context.Background())
	if err != nil || m == nil || g.Token != entdeckToken {
		t.Fatalf("%+v %v %v", g, m, err)
	}
	if s.tokenAsked != tokenAsked {
		t.Error("der Token wurde noch einmal gefragt")
	}
	if a.presetToken != "" {
		t.Error("presetToken bleibt stehen")
	}
	id := mustID(t, me.URL)
	if group.ActiveID() != id {
		t.Fatalf("aktiv %q", group.ActiveID())
	}
	loaded, err := group.LoadGroup(group.GroupPath())
	if err != nil || loaded.Token != entdeckToken || loaded.WebURL != w.web.ts.URL || loaded.TunnelServer != "tunnel.example.org:7443" {
		t.Errorf("Gruppendatei: %+v %v", loaded, err)
	}
	if strings.Contains(w.logs.String(), entdeckToken) {
		t.Error("Token im Log")
	}
}

func TestEinrichtungEntdeckenFehlerwege(t *testing.T) {
	w := neueEntdeckWelt(t)
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	w.web.status = http.StatusUnauthorized
	s := &setupStubs{server: []string{"https://böse/pfad", "tunnel.example.org:99999", "tunnel.example.org", "tunnel.example.org", "tunnel.example.org"}, token: entdeckToken}
	stubSetup(t, w.web.ts.URL, s)
	a := busyApp(t, nil)
	// 1. ungültige Adressen, 2. Token abgelehnt (401), 3. Verzeichnis leer, danach Abbruch.
	orig, n := askTunnelServer, 0
	askTunnelServer = func(d, h string) (string, bool) {
		if n++; n == 4 { // ab dem vierten Mal antwortet das Verzeichnis, aber leer
			w.web.mu.Lock()
			w.web.status, w.web.eintraege = 0, nil
			w.web.mu.Unlock()
		}
		return orig(d, h)
	}
	_, _, err := a.entdeckenSetup(context.Background())
	if err != errCanceled {
		t.Fatalf("Abbruch erwartet: %v", err)
	}
	h := strings.Join(s.hints, "|")
	for _, want := range []string{"keine Server-Adresse", "Token ungültig", "keine Gruppen auf"} {
		if !strings.Contains(h, want) {
			t.Errorf("Hinweis %q fehlt in %q", want, h)
		}
	}
	if strings.Contains(h, entdeckToken) || strings.Contains(w.logs.String(), entdeckToken) {
		t.Error("Token geleakt")
	}
	// Token leer eingegeben: Abbruch.
	s2 := &setupStubs{server: []string{"tunnel.example.org"}, token: ""}
	stubSetup(t, w.web.ts.URL, s2)
	if _, _, err := a.entdeckenSetup(context.Background()); err != errCanceled {
		t.Errorf("ohne Token: %v", err)
	}
}

func TestEntdeckenServerStandardAusEingebetteterGruppe(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	var def string
	s := &setupStubs{}
	stubSetup(t, "", s)
	askTunnelServer = func(d, hint string) (string, bool) { def = d; return "", false }
	a := busyApp(t, nil)
	a.invite = &group.Invite{Coordinator: "https://c.example.org", TunnelServer: "tunnel.example.org:7443", WebURL: "https://tunnel.example.org"}
	a.entdeckenSetup(context.Background())
	if def != "tunnel.example.org:7443" {
		t.Errorf("Standard: %q", def)
	}
}

func TestBeitrittsLinkBeiMehrerenGruppenZeigtEntdecken(t *testing.T) {
	probeAnzeige(t)
	opened := make(chan struct{}, 4)
	old := openWindowOnJoin
	openWindowOnJoin = func() { opened <- struct{}{} }
	t.Cleanup(func() { openWindowOnJoin = old })

	a := zweiGruppen(t)
	a.onJoinLink()
	<-opened
	if z := a.zustand(); !z.ZeigeEntdecken {
		t.Error("bei zwei Gruppen soll das Fenster zu Entdecken springen")
	}
	// Eine Gruppe: nur das Fenster öffnen.
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	if err := group.AddGroup(&group.Group{Coordinator: "http://127.0.0.1:1", DevIdentityEmail: "x@example.org"}, true); err != nil {
		t.Fatal(err)
	}
	b := busyApp(t, nil)
	b.g = &group.Group{}
	b.onJoinLink()
	<-opened
	if z := b.zustand(); z.ZeigeEntdecken {
		t.Error("bei einer Gruppe nicht")
	}
}

// Wird in „Entdecken“ nur der Servername ohne Port eingegeben, speichert die
// Einrichtung host:port mit dem Standardport des Tunnel-Servers (der
// tunnel-Client verlangt einen Port).
func TestEntdeckenServerOhnePortBekommtStandardPort(t *testing.T) {
	w := neueEntdeckWelt(t)
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	k := neuerKoord(t, "Wandergruppe", api.YouNone)
	w.web.setze(k)
	s := &setupStubs{server: []string{"tunnel.example.org"}, token: entdeckToken, picks: []int{0}}
	stubSetup(t, w.web.ts.URL, s)
	a := busyApp(t, nil)
	inv, _, err := a.entdeckenSetup(context.Background())
	if err != nil || inv == nil {
		t.Fatalf("%+v %v", inv, err)
	}
	if inv.TunnelServer != "tunnel.example.org:7443" {
		t.Errorf("TunnelServer = %q, erwartet tunnel.example.org:7443", inv.TunnelServer)
	}
}
