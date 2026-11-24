package lease

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func TestIncomingGrantPromoteDrop(t *testing.T) {
	m, clk, _ := newManager(t)
	a, b, c3 := candidate(1), candidate(2), candidate(3)

	// Ohne Haupt-Zuschlag gibt es keinen wartenden.
	if _, err := m.GrantIncoming(b); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("GrantIncoming ohne Host: %v, erwartet ErrLeaseLost", err)
	}
	ga := requestHost(t, m, clk, a)
	gb, err := m.GrantIncoming(b)
	if err != nil {
		t.Fatalf("GrantIncoming: %v", err)
	}
	if gb.LeaseID <= ga.LeaseID {
		t.Fatalf("wartende LeaseID %d, Haupt %d: erwartet eigene, größere", gb.LeaseID, ga.LeaseID)
	}
	// Nur ein wartender.
	if _, err := m.GrantIncoming(c3); !errors.Is(err, ErrBusy) {
		t.Fatalf("zweiter GrantIncoming: %v, erwartet ErrBusy", err)
	}
	// Current und Info melden weiter den Haupt-Zuschlag.
	if host, id, ok := m.Current(); !ok || id != ga.LeaseID || host.MachineID != a.MachineID {
		t.Fatalf("Current = %v %d %v", host, id, ok)
	}
	if info, ok := m.Info(); !ok || info.LeaseID != ga.LeaseID {
		t.Fatalf("Info = %+v %v", info, ok)
	}
	if in, ok := m.Incoming(); !ok || in.LeaseID != gb.LeaseID || in.AccountID != b.AccountID {
		t.Fatalf("Incoming = %+v %v", in, ok)
	}
	// Heartbeat, Valid, Report gelten für den wartenden.
	if !m.Valid(gb.LeaseID, b.MachineID) || m.Valid(gb.LeaseID, a.MachineID) {
		t.Fatal("Valid für den wartenden Zuschlag falsch")
	}
	m.Report(gb.LeaseID, []string{"x"}, true)
	if in, _ := m.Incoming(); !in.Ready {
		t.Fatal("Report(ready) des wartenden nicht übernommen")
	}
	if info, _ := m.Info(); info.Ready {
		t.Fatal("Report des wartenden darf den Haupt-Zuschlag nicht berühren")
	}
	if err := m.Heartbeat(gb.LeaseID, b.MachineID); err != nil {
		t.Fatalf("Heartbeat wartender: %v", err)
	}
	// Befördern geht nicht, solange der Haupt-Zuschlag gilt.
	if ok, err := m.Promote(); ok || err != nil {
		t.Fatalf("Promote bei gültigem Host = %v, %v", ok, err)
	}
	// Rückgabe des Haupt-Zuschlags lässt den wartenden stehen; währenddessen
	// vergibt Request keinen neuen Zuschlag.
	if err := m.Release(ga.LeaseID, a.MachineID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, _, ok := m.Current(); ok {
		t.Fatal("Haupt-Zuschlag nach Release noch da")
	}
	if g, err := m.Request(context.Background(), c3); err != nil || g.Role != api.RoleDenied {
		t.Fatalf("Request bei wartendem ohne Haupt-Zuschlag = %+v, %v; erwartet denied", g, err)
	}
	if ok, err := m.Promote(); !ok || err != nil {
		t.Fatalf("Promote = %v, %v", ok, err)
	}
	if _, id, ok := m.Current(); !ok || id != gb.LeaseID {
		t.Fatalf("Current nach Promote = %d %v, erwartet %d", id, ok, gb.LeaseID)
	}
	if info, _ := m.Info(); !info.Ready {
		t.Fatal("Bereitschaft ging bei Promote verloren")
	}
	if _, ok := m.Incoming(); ok {
		t.Fatal("Incoming nach Promote noch da")
	}

	// Drop: wartenden verwerfen, Haupt-Zuschlag bleibt; Rückgabe durch den
	// wartenden selbst wirkt genauso.
	gc, err := m.GrantIncoming(a)
	if err != nil {
		t.Fatalf("GrantIncoming 2: %v", err)
	}
	if err := m.DropIncoming(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Incoming(); ok || m.Valid(gc.LeaseID, a.MachineID) {
		t.Fatal("wartender nach DropIncoming noch gültig")
	}
	if !m.Valid(gb.LeaseID, b.MachineID) {
		t.Fatal("DropIncoming berührte den Haupt-Zuschlag")
	}
	gd, _ := m.GrantIncoming(a)
	if err := m.Release(gd.LeaseID, a.MachineID); err != nil {
		t.Fatalf("Release des wartenden: %v", err)
	}
	if _, ok := m.Incoming(); ok || !m.Valid(gb.LeaseID, b.MachineID) {
		t.Fatal("Release des wartenden: falscher Zustand")
	}
}

func TestIncomingExpiry(t *testing.T) {
	a, b := candidate(1), candidate(2)

	// Haupt-Zuschlag läuft ab, der wartende lebt weiter: Expire meldet nur
	// den Haupt-Zuschlag, Promote geht.
	m, clk, _ := newManager(t)
	requestHost(t, m, clk, a)
	gb, err := m.GrantIncoming(b)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(LeaseTTL - 10*time.Second)
	if err := m.Heartbeat(gb.LeaseID, b.MachineID); err != nil {
		t.Fatal(err)
	}
	clk.Advance(11 * time.Second)
	if expired, acc := m.Expire(); !expired || acc != a.AccountID {
		t.Fatalf("Expire = %v %d", expired, acc)
	}
	if _, ok := m.Incoming(); !ok {
		t.Fatal("wartender mit dem Haupt-Zuschlag verfallen")
	}
	if ok, _ := m.Promote(); !ok {
		t.Fatal("Promote nach Ablauf des Haupt-Zuschlags schlug fehl")
	}

	// Beide ohne Lebenszeichen: beide verfallen, Promote geht nicht.
	m2, clk2, _ := newManager(t)
	requestHost(t, m2, clk2, a)
	if _, err := m2.GrantIncoming(b); err != nil {
		t.Fatal(err)
	}
	clk2.Advance(LeaseTTL + time.Second)
	if _, ok := m2.Incoming(); ok {
		t.Fatal("abgelaufener wartender gilt noch")
	}
	if ok, _ := m2.Promote(); ok {
		t.Fatal("Promote eines abgelaufenen wartenden")
	}
	if expired, acc := m2.Expire(); !expired || acc != a.AccountID {
		t.Fatalf("Expire beider = %v %d", expired, acc)
	}

	// Nur der wartende läuft ab: Haupt-Zuschlag lebt, Expire meldet nichts.
	m3, clk3, _ := newManager(t)
	g3 := requestHost(t, m3, clk3, a)
	if _, err := m3.GrantIncoming(b); err != nil {
		t.Fatal(err)
	}
	clk3.Advance(LeaseTTL - 5*time.Second)
	if err := m3.Heartbeat(g3.LeaseID, a.MachineID); err != nil {
		t.Fatal(err)
	}
	clk3.Advance(10 * time.Second)
	if expired, _ := m3.Expire(); expired {
		t.Fatal("Expire meldete den lebenden Haupt-Zuschlag")
	}
	if _, ok := m3.Incoming(); ok {
		t.Fatal("abgelaufener wartender noch da")
	}
	if _, err := m3.GrantIncoming(b); err != nil {
		t.Fatalf("neuer wartender nach Ablauf des alten: %v", err)
	}
}

func TestIncomingSurvivesRestart(t *testing.T) {
	m1, clk, path := newManager(t)
	a, b := candidate(1), candidate(2)
	ga := requestHost(t, m1, clk, a)
	gb, err := m1.GrantIncoming(b)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(30 * time.Second)
	if err := m1.Heartbeat(gb.LeaseID, b.MachineID); err != nil {
		t.Fatal(err)
	}
	m2, err := New(path, clk)
	if err != nil {
		t.Fatal(err)
	}
	if in, ok := m2.Incoming(); !ok || in.LeaseID != gb.LeaseID {
		t.Fatalf("Incoming nach Neustart = %+v %v", in, ok)
	}
	if _, id, ok := m2.Current(); !ok || id != ga.LeaseID {
		t.Fatalf("Current nach Neustart = %d %v", id, ok)
	}
	// Die Verlängerung des wartenden steht auf der Platte: 70 s später gilt
	// er (100 s nach Vergabe), der Haupt-Zuschlag (nur 90 s) nicht mehr.
	clk.Advance(70 * time.Second)
	m3, err := New(path, clk)
	if err != nil {
		t.Fatal(err)
	}
	if !m3.Valid(gb.LeaseID, b.MachineID) || m3.Valid(ga.LeaseID, a.MachineID) {
		t.Fatal("Gültigkeit nach Neustart falsch")
	}
	if ok, _ := m3.Promote(); !ok {
		t.Fatal("Promote nach Neustart schlug fehl")
	}
	// LeaseIDs steigen weiter.
	if err := m3.Release(gb.LeaseID, b.MachineID); err != nil {
		t.Fatal(err)
	}
	if g := requestHost(t, m3, clk, a); g.LeaseID <= gb.LeaseID {
		t.Fatalf("LeaseID %d nach Neustart, erwartet > %d", g.LeaseID, gb.LeaseID)
	}
}
