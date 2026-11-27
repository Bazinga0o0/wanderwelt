package lease

import (
	"context"
	"slices"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func inWorld(n int, world string) Candidate {
	c := candidate(n)
	c.World = world
	return c
}

// Es läuft immer nur eine Welt: Gegen einen laufenden Host bekommt, wer eine
// andere Welt will, „denied“ mit der laufenden Welt; wer dieselbe will (oder
// keine nennt), wird Mitspieler.
func TestEineWeltGleichzeitigGegenHost(t *testing.T) {
	m, clk, _ := newManager(t)
	ga := requestHost(t, m, clk, inWorld(1, "w1"))
	if ga.World != "w1" {
		t.Fatalf("Zuschlag trägt Welt %q", ga.World)
	}
	if info, ok := m.Info(); !ok || info.World != "w1" {
		t.Fatalf("Info: %+v %v", info, ok)
	}
	g, err := m.Request(context.Background(), inWorld(2, "w2"))
	if err != nil || g.Role != api.RoleDenied || g.Reason != ReasonOtherWorld || g.World != "w1" || g.LeaseID != ga.LeaseID {
		t.Fatalf("andere Welt: %+v %v", g, err)
	}
	for _, c := range []Candidate{inWorld(3, "w1"), inWorld(4, "")} {
		if g, err := m.Request(context.Background(), c); err != nil || g.Role != api.RoleJoin || g.LeaseID != ga.LeaseID {
			t.Fatalf("Mitspieler %q: %+v %v", c.World, g, err)
		}
	}
	// Auch ein Zuschlag nur zum Hochladen hält die Welt (und lässt nichts durch).
	m2, _, _ := newManager(t)
	up, err := m2.Acquire(inWorld(1, "w2"))
	if err != nil || up.World != "w2" {
		t.Fatalf("Acquire: %+v %v", up, err)
	}
	if g, _ := m2.Request(context.Background(), inWorld(2, "w1")); g.Role != api.RoleDenied {
		t.Fatalf("Request gegen Hochladen: %+v", g)
	}
}

// Im Sammelfenster zählt die Welt der ersten Anfrage; wer eine andere will,
// wartet nicht mit und beeinflusst die Wahl nicht.
func TestSammelfensterNurEineWelt(t *testing.T) {
	m, clk, _ := newManager(t)
	chA := make(chan result, 1)
	go func() {
		g, err := m.Request(context.Background(), inWorld(1, "w2"))
		chA <- result{g, err}
	}()
	waitCandidates(t, m, 1)
	// Anderer Wunsch mitten im Fenster: sofort abgelehnt, nicht Teil der Wahl.
	g, err := m.Request(context.Background(), inWorld(2, "w1"))
	if err != nil || g.Role != api.RoleDenied || g.Reason != ReasonOtherWorld || g.World != "w2" {
		t.Fatalf("andere Welt im Fenster: %+v %v", g, err)
	}
	chC := make(chan result, 1)
	go func() {
		g, err := m.Request(context.Background(), inWorld(3, "w2"))
		chC <- result{g, err}
	}()
	waitCandidates(t, m, 2)
	if w := m.BusyWorlds(); !slices.Equal(w, []string{"w2"}) {
		t.Fatalf("BusyWorlds im Fenster: %v", w)
	}
	clk.Advance(Window)
	ra, rc := <-chA, <-chC
	hosts := 0
	for _, r := range []result{ra, rc} {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.g.Role == api.RoleHost {
			hosts++
		}
		if r.g.World != "w2" {
			t.Fatalf("Zuschlag für Welt %q", r.g.World)
		}
	}
	if hosts != 1 {
		t.Fatalf("%d Hosts", hosts)
	}
	if info, _ := m.Info(); info.World != "w2" {
		t.Fatalf("Welt des Hosts: %q", info.World)
	}
}

// Die Übergabe wechselt nur den PC: Der wartende Zuschlag hat dieselbe Welt,
// auch wenn der Kandidat keine nennt; die Welt übersteht Neustart und
// Beförderung.
func TestUebergabeErbtWelt(t *testing.T) {
	m, clk, path := newManager(t)
	requestHost(t, m, clk, inWorld(1, "w2"))
	in, err := m.GrantIncoming(inWorld(2, "w9")) // die Angabe zählt nicht
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := m.Incoming(); info.World != "w2" {
		t.Fatalf("wartender Zuschlag hat Welt %q", info.World)
	}
	if w := m.BusyWorlds(); !slices.Equal(w, []string{"w2", "w2"}) {
		t.Fatalf("BusyWorlds: %v", w)
	}
	// Neustart des Koordinators: Welt steht in der Zustandsdatei.
	m2, err := New(path, clk)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := m2.Info(); info.World != "w2" {
		t.Fatalf("Welt nach Neustart: %q", info.World)
	}
	if err := m2.Release(1, "pc-1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := m2.Promote(); !ok || err != nil {
		t.Fatalf("Promote: %v %v", ok, err)
	}
	if info, _ := m2.Info(); info.LeaseID != in.LeaseID || info.World != "w2" {
		t.Fatalf("nach Promote: %+v", info)
	}
}

// Ein Zuschlag ohne Welt (aus der Zeit vor der Weltliste) lässt jeden
// herein; Zustand ohne „world“ lädt.
func TestZuschlagOhneWeltPasstZuJeder(t *testing.T) {
	m, clk, _ := newManager(t)
	requestHost(t, m, clk, candidate(1))
	if g, _ := m.Request(context.Background(), inWorld(2, "w1")); g.Role != api.RoleJoin {
		t.Fatalf("%+v", g)
	}
	if w := m.BusyWorlds(); !slices.Equal(w, []string{""}) {
		t.Fatalf("BusyWorlds: %v", w)
	}
}
