package app

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/discord"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

func validCode(t *testing.T) string {
	t.Helper()
	s, err := group.EncodeJoin(&group.Invite{Coordinator: "https://koord.example.org", TunnelServer: "tunnel.example.org:7443"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// codeTest stellt Home und Dialoge ein.
func codeTest(t *testing.T) *App {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	oldAsk, oldConfirm, oldOpen := askGroupCode, confirmJoin, openWindowOnJoin
	t.Cleanup(func() { askGroupCode, confirmJoin, openWindowOnJoin = oldAsk, oldConfirm, oldOpen })
	askGroupCode = func(string) (string, error) {
		t.Error("unerwartet nach dem Code gefragt")
		return "", zenity.ErrCanceled
	}
	confirmJoin = func(*group.Invite) bool { t.Error("unerwartete Bestätigung"); return false }
	return &App{presence: &discord.Fake{}}
}

func TestCodeAngebotNurOhneGruppe(t *testing.T) {
	a := codeTest(t)
	if !a.needsGroupCodeOffer() {
		t.Fatal("ohne Gruppe kein Angebot")
	}
	// Gruppendatei vorhanden (auch kaputt): nie anbieten, nie ersetzen.
	if err := os.WriteFile(group.GroupPath(), []byte("coordinator: https://x.example.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.needsGroupCodeOffer() {
		t.Error("Angebot trotz vorhandener Gruppendatei")
	}
	b := codeTest(t)
	b.invite = &group.Invite{Coordinator: "https://x.example.org"}
	if b.needsGroupCodeOffer() {
		t.Error("Angebot trotz Gruppen-Build")
	}
}

func TestCodeBestaetigt(t *testing.T) {
	a := codeTest(t)
	askGroupCode = func(string) (string, error) { return "  " + validCode(t) + "\r\n", nil } // mit Leerraum eingefügt
	var seen *group.Invite
	confirmJoin = func(inv *group.Invite) bool { seen = inv; return true }
	inv, err := a.groupCodeOffer()
	if err != nil || inv == nil || seen == nil || inv.Coordinator != "https://koord.example.org" ||
		inv.TunnelServer != "tunnel.example.org:7443" || inv.WebURL != "https://tunnel.example.org" {
		t.Fatalf("Einladung %+v %v", inv, err)
	}
	if _, err := os.Stat(group.GroupPath()); err == nil {
		t.Error("gruppe.yaml schon vor der Einrichtung angelegt")
	}
}

func TestCodeAbgelehntFragtErneut(t *testing.T) {
	a := codeTest(t)
	calls := 0
	askGroupCode = func(string) (string, error) {
		calls++
		if calls == 1 {
			return validCode(t), nil
		}
		return "", zenity.ErrExtraButton // dann doch die Gruppendatei
	}
	confirmJoin = func(*group.Invite) bool { return false }
	inv, err := a.groupCodeOffer()
	if inv != nil || err != nil || calls != 2 {
		t.Errorf("%+v %v nach %d Fragen", inv, err, calls)
	}
}

func TestCodeMuellWirdAbgelehnt(t *testing.T) {
	c := validCode(t)
	bad := []string{
		"", "   ", "w1.", "w1.!!!", "foo", "https://evil.example.org",
		c + "\n" + c,                     // zwei Codes
		c + "x y",                        // Anhang
		strings.Repeat("w1.A", 400),      // überlang
		strings.Repeat("A", 100000),      // riesig
		"w1.aGVsbG8",                     // gültiges Base64, aber keine Adressen
		"w1." + strings.Repeat("A", 200), // zu lang für einen Code
		"w1.%0Ahttps",                    // Prozent-Kodierung
		c[:10] + "\n" + c[10:],           // Zeilenumbruch mittendrin
	}
	for _, s := range bad {
		if inv, err := ParseGroupCode(s); err == nil {
			t.Errorf("%q angenommen: %+v", s, inv)
		}
	}
	// Böse Adressen lassen sich gar nicht erst kodieren.
	for _, inv := range []group.Invite{
		{Coordinator: "http://koord.example.org", TunnelServer: "t.example.org:1"},
		{Coordinator: "https://u:p@koord.example.org", TunnelServer: "t.example.org:1"},
		{Coordinator: "https://koord.example.org/pfad", TunnelServer: "t.example.org:1"},
	} {
		if s, err := group.EncodeJoin(&inv); err == nil {
			t.Errorf("Kodieren akzeptiert %+v: %s", inv, s)
		}
	}

	a := codeTest(t)
	calls := 0
	var hints []string
	askGroupCode = func(h string) (string, error) {
		calls++
		hints = append(hints, h)
		if calls < 3 {
			return "w1.!!!", nil
		}
		return "", zenity.ErrCanceled
	}
	inv, err := a.groupCodeOffer()
	if inv != nil || !errors.Is(err, errCanceled) {
		t.Errorf("%+v %v", inv, err)
	}
	if hints[0] != "" || !strings.Contains(hints[1], "ungültig") || strings.Contains(hints[1], "w1.!!!") {
		t.Errorf("Hinweise: %q", hints)
	}
}

func TestLinkArgumentWirdNieAusgewertet(t *testing.T) {
	for _, s := range []string{
		"wanderwelt://beitreten", "WANDERWELT://beitreten", "wanderwelt:beitreten", "wanderwelt://",
		"wanderwelt://beitreten?s=" + validCode(t), "wanderwelt://beitreten/../../x", "wanderwelt://beitreten\r\nx",
		"wanderwelt://" + strings.Repeat("A", 100000), "wanderwelt:%00%0d%0a",
	} {
		if !IsJoinLinkArg(s) {
			t.Errorf("%q nicht als Link erkannt", s)
		}
	}
	for _, s := range []string{"", "play", "fenster", "discord-1://x", "xwanderwelt://", "https://wanderwelt://", "wanderwel", "import"} {
		if IsJoinLinkArg(s) {
			t.Errorf("%q als Link erkannt", s)
		}
	}
}

func TestLinkMitGruppeOeffnetNurFensterUndBremst(t *testing.T) {
	a := codeTest(t)
	opened := make(chan struct{}, 4)
	openWindowOnJoin = func() { opened <- struct{}{} }
	g := &group.Group{Coordinator: "https://alt.example.org", TunnelServer: "alt.example.org:1", Token: "geheim"}
	a.g = g
	a.onJoinLink()
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("Fenster nicht geöffnet")
	}
	a.onJoinLink() // sofort wieder: ignoriert
	a.onJoinLink()
	select {
	case <-opened:
		t.Error("Link wiederholt das Fenster ohne Pause")
	case <-time.After(100 * time.Millisecond):
	}
	if a.g != g || g.Coordinator != "https://alt.example.org" || a.invite != nil {
		t.Error("vorhandene Gruppe verändert")
	}
	if _, err := os.Stat(group.GroupPath()); err == nil {
		t.Error("gruppe.yaml geschrieben")
	}
}

func TestLinkOhneGruppeTutNichts(t *testing.T) {
	a := codeTest(t)
	openWindowOnJoin = func() { t.Error("Fenster ohne Gruppe") }
	a.onJoinLink()
	time.Sleep(50 * time.Millisecond)
}

func TestGruppencodeOhneTokenUndNurWennMoeglich(t *testing.T) {
	a := codeTest(t)
	if s := a.groupCode(); s != "" {
		t.Error("Code ohne Gruppe")
	}
	if a.partyID() != "" {
		t.Error("Party ohne Gruppe")
	}
	a.g = &group.Group{Coordinator: "https://koord.example.org", TunnelServer: "tunnel.example.org:7443", Token: "supergeheim"}
	s := a.groupCode()
	if s == "" || strings.Contains(s, "supergeheim") || len(s) > group.MaxJoinSecret {
		t.Fatalf("Code %q", s)
	}
	if !strings.HasPrefix(a.partyID(), "wanderwelt-") {
		t.Errorf("Party %q", a.partyID())
	}
	inv, err := ParseGroupCode(s)
	if err != nil || inv.Coordinator != "https://koord.example.org" || inv.TunnelServer != "tunnel.example.org:7443" {
		t.Errorf("Rückweg: %+v %v", inv, err)
	}
	// Mit Gruppen-Build: Webseite und Token-Adresse (öffentlich) wandern mit.
	a.invite = &group.Invite{WebURL: "https://web.example.org", TokenURL: "https://web.example.org/token"}
	if inv, err := ParseGroupCode(a.groupCode()); err != nil || inv.WebURL != "https://web.example.org" || inv.TokenURL != "https://web.example.org/token" {
		t.Errorf("Build-Adressen: %+v %v", inv, err)
	}
	// Dev-Modus ohne Tunnel-Server, oder http: kein Code.
	a.invite = nil
	a.g = &group.Group{Coordinator: "http://127.0.0.1:8080", DevIdentityEmail: "x@example.org"}
	if s := a.groupCode(); s != "" {
		t.Errorf("Code für Dev-Gruppe: %q", s)
	}
}

func TestDiscordActivityPartyOhneGeheimnis(t *testing.T) {
	since := time.Unix(1_800_000_000, 0)
	me := &api.Me{MinecraftName: "Leon"}
	act, ok := discordActivity(phHosting, &api.Status{Players: []string{"Leon", "A", "B"}}, me, since, "wanderwelt-ab")
	if !ok || act.PartyID != "wanderwelt-ab" || act.PartySize != 3 || act.PartyMax <= act.PartySize {
		t.Errorf("%+v", act)
	}
	act, _ = discordActivity(phHosting, nil, nil, since, "")
	if act.PartyMax != 0 {
		t.Errorf("Party ohne Gruppe: %+v", act)
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
		act, _ := discordActivity(phHosting, &api.Status{Players: players, Members: c.members}, me, since, "p")
		if act.PartySize != 2 || act.PartyMax != c.wantMax {
			t.Errorf("Members %d: %d von %d, erwartet 2 von %d", c.members, act.PartySize, act.PartyMax, c.wantMax)
		}
	}
}
