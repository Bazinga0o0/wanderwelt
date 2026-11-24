package server_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

// restart startet den Koordinator neu: gleiche Platte, gleiche Uhr, gleicher
// Port-Halter-Fake (der erste Takt läuft wie nach jedem Start).
func (e *hoEnv) restart() {
	e.t.Helper()
	cfg := &config.Config{
		DataDir:          e.dir,
		MinecraftAddress: "mc.example.org:15565",
		StorageBudget:    1 << 30,
		SnapshotInterval: 5 * time.Minute,
		ManifestFile:     filepath.Join(e.dir, "manifest.yaml"),
		Members: []config.Member{
			{Email: emailA, Minecraft: "Alice", Admin: true, Op: true},
			{Email: emailB, Minecraft: "Bob"},
		},
	}
	cfg.Auth.Mode = "dev"
	cfg.PortHolder.Port = mcPort
	cfg.PortHolder.HandoverPorts = []int{portR1, portR2}
	lm, err := lease.New(filepath.Join(e.dir, "lease.json"), e.clk)
	if err != nil {
		e.t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(e.dir, "store"), cfg.StorageBudget)
	if err != nil {
		e.t.Fatal(err)
	}
	srv := server.New(cfg, lm, st, e.ph, fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
	srv.SetNow(e.clk.Now)
	ts := httptest.NewServer(srv)
	e.t.Cleanup(ts.Close)
	e.srv, e.lm, e.base = srv, lm, ts.URL
	e.tick()
}

// Neustart nach der Beförderung: Der Ersatz-Port bleibt beim neuen Host
// (kein Clear), nur der unbenutzte wird geräumt; MainPort kommt weiter.
func TestHandoverRestartAfterPromotion(t *testing.T) {
	e := newHoEnv(t)
	lidA := e.hostWithPlayers(userA, pcA1)
	e.present(userB, pcB, true)
	st := e.mustStart(userA, pcA1, lidA)
	e.mustReady(userA, pcA1, lidA, st.HandoverID)
	lidB := e.present(userB, pcB, true).Handover.LeaseID
	e.mustHB(userB, pcB, lidB, true, "Bob")
	e.release(userA, pcA1, lidA)
	e.ph.take()

	e.restart()
	e.wantCalls("clear 15567") // 15566 hält der beförderte Host
	if hb := e.mustHB(userB, pcB, lidB, true, "Bob"); !hb.MainPort {
		t.Fatal("kein MainPort nach dem Neustart")
	}
	// Ende des Zuschlags räumt den Ersatz-Port auch nach dem Neustart.
	e.release(userB, pcB, lidB)
	e.tick()
	e.wantCalls("clear 15565", "clear 15566")
}

// Neustart nach „bereit“: Die Übergabe bleibt, die Rückgabe des alten Hosts
// befördert den wartenden, und der alte Host sieht weiter „bereit“.
func TestHandoverRestartWhenReady(t *testing.T) {
	e := newHoEnv(t)
	lidA := e.hostWithPlayers(userA, pcA1)
	e.present(userB, pcB, true)
	st := e.mustStart(userA, pcA1, lidA)
	e.mustReady(userA, pcA1, lidA, st.HandoverID)
	lidB := e.present(userB, pcB, true).Handover.LeaseID
	e.mustHB(userB, pcB, lidB, true, "Bob")
	e.ph.take()

	e.restart()
	e.wantCalls("clear 15567")
	hb := e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob")
	if hb.Handover == nil || hb.Handover.State != api.HandoverReady || hb.Handover.Address != "mc.example.org:15566" {
		t.Fatalf("nach dem Neustart: %+v", hb.Handover)
	}
	if code := e.commitCode(userA, pcA1, lidA, 3); code != http.StatusConflict {
		t.Fatalf("Commit nach dem Neustart: %d", code)
	}
	if code := e.release(userA, pcA1, lidA); code != http.StatusNoContent {
		t.Fatalf("Rückgabe: %d", code)
	}
	e.wantCalls("set 15565 202")
	if hb := e.mustHB(userB, pcB, lidB, true, "Bob"); !hb.MainPort {
		t.Fatal("kein MainPort nach Neustart und Rückgabe")
	}
}
