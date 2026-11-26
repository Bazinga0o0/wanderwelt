package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/discord"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

func validSecret(t *testing.T) string {
	t.Helper()
	s, err := group.EncodeJoin(&group.Invite{Coordinator: "https://koord.example.org", TunnelServer: "tunnel.example.org:7443"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// joinTest stellt Home und Dialoge ein; die Dialoge liefern die Antworten.
func joinTest(t *testing.T) (a *App, shown *[]string) {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	var msgs []string
	oldErr, oldChoice, oldConfirm := showError, askJoinChoice, confirmJoin
	showError = func(s string) { msgs = append(msgs, s) }
	t.Cleanup(func() { showError, askJoinChoice, confirmJoin = oldErr, oldChoice, oldConfirm })
	a = &App{ctx: context.Background(), joinCh: make(chan string, 1), presence: &discord.Fake{}}
	return a, &msgs
}

func TestJoinOfferNurOhneGruppe(t *testing.T) {
	a, _ := joinTest(t)
	if !a.needsJoinOffer() {
		t.Fatal("ohne Gruppe kein Angebot")
	}
	// Gruppendatei vorhanden (auch kaputt): nie anbieten, nie ersetzen.
	if err := os.WriteFile(group.GroupPath(), []byte("coordinator: https://x.example.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.needsJoinOffer() {
		t.Error("Angebot trotz vorhandener Gruppendatei")
	}
	b, _ := joinTest(t)
	b.invite = &group.Invite{Coordinator: "https://x.example.org"}
	if b.needsJoinOffer() {
		t.Error("Angebot trotz Gruppen-Build")
	}
	c, _ := joinTest(t)
	c.presence = nil
	if c.needsJoinOffer() {
		t.Error("Angebot ohne Discord")
	}
}

func TestJoinOfferBestaetigt(t *testing.T) {
	a, _ := joinTest(t)
	a.joinCh <- validSecret(t)
	var seen *group.Invite
	confirmJoin = func(inv *group.Invite) bool { seen = inv; return true }
	askJoinChoice = func() bool { t.Error("gefragt trotz Geheimnis"); return false }
	inv := a.discordJoinOffer(context.Background())
	if inv == nil || seen == nil || inv.Coordinator != "https://koord.example.org" || inv.TunnelServer != "tunnel.example.org:7443" ||
		inv.WebURL != "https://tunnel.example.org" {
		t.Fatalf("Einladung %+v", inv)
	}
	if _, err := os.Stat(group.GroupPath()); err == nil {
		t.Error("gruppe.yaml schon vor der Einrichtung angelegt")
	}
}

func TestJoinOfferAbgelehnt(t *testing.T) {
	a, _ := joinTest(t)
	a.joinCh <- validSecret(t)
	confirmJoin = func(*group.Invite) bool { return false }
	if inv := a.discordJoinOffer(context.Background()); inv != nil {
		t.Errorf("trotz Ablehnung: %+v", inv)
	}
}

func TestJoinOfferMuell(t *testing.T) {
	a, msgs := joinTest(t)
	a.joinCh <- "w1.!!!"
	confirmJoin = func(*group.Invite) bool { t.Error("Bestätigung für Müll"); return true }
	if inv := a.discordJoinOffer(context.Background()); inv != nil {
		t.Errorf("Müll angenommen: %+v", inv)
	}
	if len(*msgs) != 1 || strings.Contains((*msgs)[0], "w1.") {
		t.Errorf("Meldungen: %q", *msgs)
	}
}

func TestJoinOfferWartetAufKlick(t *testing.T) {
	a, _ := joinTest(t)
	askJoinChoice = func() bool { return true }
	confirmJoin = func(*group.Invite) bool { return true }
	old := showJoinWait
	t.Cleanup(func() { showJoinWait = old })
	showJoinWait = func(ctx context.Context) { <-ctx.Done() }
	go func() { time.Sleep(50 * time.Millisecond); a.onDiscordJoin(validSecret(t)) }()
	if inv := a.discordJoinOffer(context.Background()); inv == nil {
		t.Fatal("Klick nicht ausgewertet")
	}
}

func TestJoinOfferDochGruppendatei(t *testing.T) {
	a, _ := joinTest(t)
	askJoinChoice = func() bool { return true }
	old := showJoinWait
	t.Cleanup(func() { showJoinWait = old })
	showJoinWait = func(context.Context) {} // Fenster sofort zu: „Doch mit Gruppendatei“
	if inv := a.discordJoinOffer(context.Background()); inv != nil {
		t.Errorf("%+v", inv)
	}
	askJoinChoice = func() bool { return false }
	if inv := a.discordJoinOffer(context.Background()); inv != nil {
		t.Errorf("%+v", inv)
	}
}

func TestJoinMitGruppeOeffnetNurFenster(t *testing.T) {
	a, _ := joinTest(t)
	opened := make(chan struct{}, 1)
	old := openWindowOnJoin
	t.Cleanup(func() { openWindowOnJoin = old })
	openWindowOnJoin = func() { opened <- struct{}{} }
	g := &group.Group{Coordinator: "https://alt.example.org", TunnelServer: "alt.example.org:1", Token: "geheim"}
	a.g = g
	a.onDiscordJoin(validSecret(t))
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("Fenster nicht geöffnet")
	}
	if len(a.joinCh) != 0 {
		t.Error("Geheimnis trotz Gruppe vorgemerkt")
	}
	if a.g != g || g.Coordinator != "https://alt.example.org" || a.invite != nil {
		t.Error("vorhandene Gruppe verändert")
	}
	if _, err := os.Stat(group.GroupPath()); err == nil {
		t.Error("gruppe.yaml geschrieben")
	}
}

func TestJoinSecretOhneTokenUndNurWennMoeglich(t *testing.T) {
	a, _ := joinTest(t)
	if s, _ := a.joinSecret(); s != "" {
		t.Error("Geheimnis ohne Gruppe")
	}
	a.g = &group.Group{Coordinator: "https://koord.example.org", TunnelServer: "tunnel.example.org:7443", Token: "supergeheim"}
	s, party := a.joinSecret()
	if s == "" || !strings.HasPrefix(party, "wanderwelt-") || strings.Contains(s, "supergeheim") {
		t.Fatalf("Geheimnis %q Party %q", s, party)
	}
	inv, err := group.DecodeJoin(s)
	if err != nil || inv.Coordinator != "https://koord.example.org" {
		t.Errorf("Rückweg: %+v %v", inv, err)
	}
	// Dev-Modus ohne Tunnel-Server, oder http: kein Geheimnis.
	a.g = &group.Group{Coordinator: "http://127.0.0.1:8080", DevIdentityEmail: "x@example.org"}
	if s, _ := a.joinSecret(); s != "" {
		t.Errorf("Geheimnis für Dev-Gruppe: %q", s)
	}
}

func TestDiscordActivityPartyMitGeheimnis(t *testing.T) {
	since := time.Unix(1_800_000_000, 0)
	me := &api.Me{MinecraftName: "Leon"}
	act, ok := discordActivity(phHosting, &api.Status{Players: []string{"Leon", "A", "B"}}, me, since, "w1.x", "wanderwelt-ab")
	if !ok || act.JoinSecret != "w1.x" || act.PartyID != "wanderwelt-ab" || act.PartySize != 3 || act.PartyMax <= act.PartySize {
		t.Errorf("%+v", act)
	}
	act, _ = discordActivity(phHosting, nil, nil, since, "", "")
	if act.JoinSecret != "" || act.PartyMax != 0 {
		t.Errorf("Party ohne Geheimnis: %+v", act)
	}
}

func TestDiscordPartyMitgliederzahl(t *testing.T) {
	since := time.Unix(1_800_000_000, 0)
	me := &api.Me{MinecraftName: "Leon"}
	players := []string{"Leon", "A"} // N = 2
	for _, c := range []struct{ members, wantMax int }{
		{6, 6}, // echte Zahl
		{0, 3}, // älterer Koordinator: N+1
		{1, 3}, // Zahl kleiner als N: mindestens N+1
		{2, 3}, // alle online: trotzdem Platz für Neue
	} {
		act, _ := discordActivity(phHosting, &api.Status{Players: players, Members: c.members}, me, since, "w1.x", "p")
		if act.PartySize != 2 || act.PartyMax != c.wantMax {
			t.Errorf("Members %d: %d von %d, erwartet 2 von %d", c.members, act.PartySize, act.PartyMax, c.wantMax)
		}
	}
}
