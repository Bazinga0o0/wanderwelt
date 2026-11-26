package app

import (
	"context"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/discord"
)

func TestDiscordActivityText(t *testing.T) {
	since := time.Unix(1_800_000_000, 0)
	me := &api.Me{MinecraftName: "Leon"}
	cases := []struct {
		name    string
		ph      phase
		players []string
		want    string
		ok      bool
	}{
		{"allein hosten", phHosting, []string{"Leon"}, "ohne Freunde :(", true},
		{"ein Freund", phJoined, []string{"leon", "Anna"}, "mit 1 Freund", true},
		{"drei Freunde", phHosting, []string{"Leon", "A", "B", "C"}, "mit 3 Freunden", true},
		{"bereit", phReady, []string{"A"}, "", false},
		{"Fehler", phError, nil, "", false},
		{"startet", phStarting, nil, "", false},
		{"laedt hoch", phUploading, nil, "", false},
	}
	for _, c := range cases {
		act, ok := discordActivity(c.ph, &api.Status{Players: c.players}, me, since, "", "")
		if ok != c.ok || (ok && (act.State != c.want || !act.Start.Equal(since))) {
			t.Errorf("%s: %+v %v", c.name, act, ok)
		}
	}
	if act, ok := discordActivity(phHosting, nil, nil, since, "", ""); !ok || act.State != "ohne Freunde :(" {
		t.Errorf("ohne Status: %+v %v", act, ok)
	}
}

func TestDiscordTickSetztUndLoescht(t *testing.T) {
	f := &discord.Fake{}
	a := &App{ctx: context.Background(), presence: f, phase: phReady, me: &api.Me{MinecraftName: "Leon"}}
	var since time.Time
	since = a.discordTick(since)
	if !since.IsZero() {
		t.Fatal("Spielzeit ohne Spiel")
	}
	a.phase, a.status = phJoined, &api.Status{Players: []string{"Leon", "Anna"}}
	since = a.discordTick(since)
	first := since
	if first.IsZero() {
		t.Fatal("Spielzeit nicht gestartet")
	}
	a.status = &api.Status{Players: []string{"Leon", "Anna", "Bo"}}
	since = a.discordTick(since)
	if !since.Equal(first) {
		t.Error("Spielzeit neu gestartet")
	}
	a.phase = phReady
	since = a.discordTick(since)
	if !since.IsZero() {
		t.Error("Spielzeit nicht zurückgesetzt")
	}
	sets, clears, _ := f.Snapshot()
	if len(sets) != 2 || sets[0].State != "mit 1 Freund" || sets[1].State != "mit 2 Freunden" || clears != 2 {
		t.Errorf("sets %+v clears %d", sets, clears)
	}
	a.discordClose()
	if _, _, closed := f.Snapshot(); !closed {
		t.Error("nicht geschlossen")
	}
	(&App{}).discordClose() // ohne Anzeige: kein Absturz
}
