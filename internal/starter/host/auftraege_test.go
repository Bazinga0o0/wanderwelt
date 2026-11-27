package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
)

// Banns kommen wie Whitelist und Ops über MembersVersion: aufgehobene zuerst
// (pardon), neue zuletzt (ban, mit festem Grund); ein gebannter Gast fällt
// zugleich von der Whitelist.
func TestMembersVersionAppliesBans(t *testing.T) {
	co := newCoord(t)
	p := func(id, name string) api.Player {
		return api.Player{UUID: "00000000-0000-4000-8000-0000000000" + id, Name: name}
	}
	alex, carl, troll := p("0a", "Alex"), p("0c", "Carl"), p("0e", "Troll")
	co.lists.Store(&hostLists{whitelist: []api.Player{alex, carl}, ops: []api.Player{alex}, bans: []api.Player{troll}})
	co.hbResp.Store(&api.HeartbeatResponse{MembersVersion: 1})
	e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)
	if c := e.srv.cmdsAfterStart(); len(c) != 0 {
		t.Fatalf("Befehle %q, erwartet keine", c)
	}
	co.lists.Store(&hostLists{whitelist: []api.Player{alex}, ops: []api.Player{alex}, bans: []api.Player{carl}})
	co.hbResp.Store(&api.HeartbeatResponse{MembersVersion: 2})
	e.drv.advanceTo(50 * time.Second)
	want := []string{
		"pardon Troll",
		"whitelist remove Carl",
		"kick Carl Du bist nicht mehr Mitglied der Gruppe.",
		"ban Carl Von einem Admin gebannt.",
	}
	if c := e.srv.cmdsAfterStart(); !slices.Equal(c, want) {
		t.Fatalf("Befehle\n%q\nerwartet\n%q", c, want)
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// Ein Name, der kein Minecraft-Name ist, bekommt auch beim Bannen keinen
// Befehl (er könnte Argumente einschleusen).
func TestBanCommandsSkipBadNames(t *testing.T) {
	old := memberLists{whitelist: map[string]string{}, ops: map[string]string{}, bans: map[string]string{}}
	next := memberLists{whitelist: map[string]string{}, ops: map[string]string{},
		bans: map[string]string{"a": "Gut_1", "b": "Böse stop", "c": "x;op y"}}
	if got := memberCommands(old, next, ""); !slices.Equal(got, []string{"ban Gut_1 Von einem Admin gebannt."}) {
		t.Fatalf("Befehle %q", got)
	}
	if got := memberCommands(next, old, ""); !slices.Equal(got, []string{"pardon Gut_1"}) {
		t.Fatalf("Befehle %q", got)
	}
}

// Ein Admin schickt über den Koordinator einen Befehl; die laufende Sitzung
// holt ihn ab, führt ihn über die eigene Verbindung (Isolated) aus und
// meldet die Antwort. Gesperrte Befehle prüft die Sitzung selbst noch
// einmal; die Spielregeln liest sie aus „help gamerule“.
func TestAdminAuftraegeLaufenUeberDieSitzung(t *testing.T) {
	co := newCoord(t)
	co.hbResp.Store(&api.HeartbeatResponse{MembersVersion: 1})
	e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
	// Aufträge gibt es nur für einen Zuschlag mit Server (nicht nur zum
	// Hochladen): den Test-Zuschlag zurückgeben und regulär spielen
	// (Sammelfenster, 5 s).
	if err := co.c.Release(t.Context(), api.ReleaseRequest{LeaseID: e.lease, MachineID: hostMachine}); err != nil {
		t.Fatal(err)
	}
	play, err := co.c.Play(t.Context(), api.PlayRequest{MachineID: hostMachine, BaseWorldID: e.head.WorldID, BaseGeneration: e.head.Number})
	if err != nil || play.Role != api.RoleHost {
		t.Fatalf("Spielen: %+v, %v", play, err)
	}
	e.lease, e.d.LeaseID = play.LeaseID, play.LeaseID
	e.srv.isoResp = func(cmd string) string {
		switch cmd {
		case "help gamerule":
			return "/gamerule keep_inventory [<value>]/gamerule minecraft:keep_inventory [<value>]/gamerule random_tick_speed [<value>]"
		case "gamerule keep_inventory":
			return "Gamerule keep_inventory is currently set to: false"
		case "gamerule random_tick_speed":
			return "Gamerule random_tick_speed is currently set to: 3"
		case "gamerule keep_inventory true":
			return "Gamerule keep_inventory is now set to: true"
		}
		return "[Rcon] " + cmd
	}
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second) // Server läuft, „bereit“ ist gemeldet

	admin := client.New(e.d.Group)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	res, err := admin.ServerCommand(ctx, "say hallo")
	if err != nil || !res.OK || res.Output != "[Rcon] say hallo" {
		t.Fatalf("Befehl: %+v, %v", res, err)
	}
	rules, err := admin.Gamerules(ctx)
	if err != nil || len(rules.Rules) != 2 || rules.Rules[0] != (api.Gamerule{Name: "keep_inventory", Value: "false"}) {
		t.Fatalf("Spielregeln: %+v, %v", rules, err)
	}
	set, err := admin.SetGamerule(ctx, api.GameruleSet{Name: "keep_inventory", Value: "true"})
	if err != nil || !set.OK {
		t.Fatalf("Spielregel setzen: %+v, %v", set, err)
	}
	if _, err := admin.ServerCommand(ctx, "stop"); err == nil {
		t.Fatalf("stop angenommen")
	}
	got := e.srv.isolatedCmds()
	want := []string{"say hallo", "help gamerule", "gamerule keep_inventory", "gamerule random_tick_speed", "gamerule keep_inventory true"}
	if !slices.Equal(got, want) {
		t.Fatalf("über Isolated\n%q\nerwartet\n%q", got, want)
	}
	// Die Verbindung des Starters (Command) hat davon nichts gesehen.
	for _, c := range e.srv.cmdsAfterStart() {
		if strings.Contains(c, "hallo") || strings.HasPrefix(c, "gamerule") {
			t.Fatalf("Admin-Befehl %q lief über die Verbindung des Starters", c)
		}
	}

	// Auch was am Koordinator vorbeikäme, prüft die Sitzung selbst.
	solo := &Session{d: e.d}
	out := solo.execJob(ctx, api.HostJob{ID: 99, Kind: api.JobCommand, Command: "execute run stop"})
	if out.OK || !strings.Contains(out.Output, "gesperrt") {
		t.Fatalf("gesperrter Auftrag: %+v", out)
	}
	out = solo.execJob(ctx, api.HostJob{ID: 98, Kind: api.JobGamerule, Name: "keep_inventory", Value: "true run stop"})
	if out.OK {
		t.Fatalf("Spielregel mit Befehl im Wert: %+v", out)
	}
	if got := e.srv.isolatedCmds(); len(got) != len(want) {
		t.Fatalf("gesperrte Aufträge liefen doch: %q", got[len(want):])
	}

	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	// Nach dem Ende nimmt niemand mehr Aufträge an.
	if _, err := admin.ServerCommand(ctx, "say spät"); err == nil {
		t.Fatalf("Befehl nach dem Ende angenommen")
	} else if se, ok := errors.AsType[*client.StatusError](err); !ok || se.StatusCode != 409 {
		t.Fatalf("Befehl nach dem Ende: %v", err)
	}
}
