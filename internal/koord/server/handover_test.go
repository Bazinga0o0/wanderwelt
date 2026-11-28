package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
)

const (
	pcA1   = "pc-a1" // Alice, erster PC
	pcA2   = "pc-a2" // Alice, zweiter PC
	pcB    = "pc-b"  // Bob
	portR1 = 15566
	portR2 = 15567
)

// hoEnv ist ein Koordinator mit Ersatz-Ports für die Übergabe.
type hoEnv struct {
	*restoreEnv
}

func newHoEnv(t *testing.T) *hoEnv {
	t.Helper()
	e := newRestoreEnvWith(t, func(c *config.Config) {
		c.PortHolder.HandoverPorts = []int{portR1, portR2}
	})
	e.ph.take() // der erste Takt hat die Ersatz-Ports geräumt
	return &hoEnv{e}
}

// hostWithPlayers lässt as hosten, reicht Stand 1 ein und meldet den Server
// mit den Spielern Alice und Bob als laufend.
func (e *hoEnv) hostWithPlayers(as who, machine string) int64 {
	e.t.Helper()
	lid := e.host(as, machine)
	e.commit(as, machine, lid, 1, true, manifestMC)
	e.hb(as, machine, lid, true, "Alice", "Bob")
	return lid
}

// hb schickt ein Lebenszeichen und liefert Status und Antwort.
func (e *hoEnv) hb(as who, machine string, leaseID int64, ready bool, players ...string) (int, api.HeartbeatResponse) {
	e.t.Helper()
	var resp api.HeartbeatResponse
	code := call(e.t, e.base, as, http.MethodPost, "/v1/lease/heartbeat",
		api.HeartbeatRequest{LeaseID: leaseID, MachineID: machine, Ready: ready, Players: players}, &resp)
	return code, resp
}

func (e *hoEnv) mustHB(as who, machine string, leaseID int64, ready bool, players ...string) api.HeartbeatResponse {
	e.t.Helper()
	code, resp := e.hb(as, machine, leaseID, ready, players...)
	if code != http.StatusOK {
		e.t.Fatalf("Lebenszeichen von %s/%d: %d", machine, leaseID, code)
	}
	return resp
}

func (e *hoEnv) present(as who, machine string, joined bool) api.PresenceResponse {
	e.t.Helper()
	var resp api.PresenceResponse
	req := api.PresenceRequest{MachineID: machine, Joined: joined,
		Score: api.Score{UploadMbps: 50, CPUScore: 1000, RAMFreeMB: 16000, RAMTotalMB: 32000, XmxMB: 6000}}
	if code := call(e.t, e.base, as, http.MethodPost, "/v1/presence", req, &resp); code != http.StatusOK {
		e.t.Fatalf("Bereitschaft: %d", code)
	}
	return resp
}

func (e *hoEnv) start(as who, machine string, leaseID int64) (int, api.HandoverStartResponse) {
	e.t.Helper()
	var resp api.HandoverStartResponse
	code := call(e.t, e.base, as, http.MethodPost, "/v1/handover/start",
		api.HandoverStartRequest{LeaseID: leaseID, MachineID: machine}, &resp)
	return code, resp
}

func (e *hoEnv) mustStart(as who, machine string, leaseID int64) api.HandoverStartResponse {
	e.t.Helper()
	code, resp := e.start(as, machine, leaseID)
	if code != http.StatusOK {
		e.t.Fatalf("Start der Übergabe: %d", code)
	}
	return resp
}

// readyReq meldet den aktuellen Head als hochgeladenen Stand.
func (e *hoEnv) readyReq(as who, machine string, leaseID, hoID int64) int {
	e.t.Helper()
	st := getStatus(e.t, e.base, as)
	return call(e.t, e.base, as, http.MethodPost, "/v1/handover/ready", api.HandoverReadyRequest{
		HandoverID: hoID, LeaseID: leaseID, MachineID: machine, WorldID: st.WorldID, Generation: st.HeadGeneration}, nil)
}

func (e *hoEnv) mustReady(as who, machine string, leaseID, hoID int64) {
	e.t.Helper()
	if code := e.readyReq(as, machine, leaseID, hoID); code != http.StatusNoContent {
		e.t.Fatalf("ready: %d", code)
	}
}

// commitCode reicht Stand k auf dem Head ein und liefert nur den Status.
func (e *hoEnv) commitCode(as who, machine string, leaseID int64, k int) int {
	e.t.Helper()
	st := getStatus(e.t, e.base, as)
	return call(e.t, e.base, as, http.MethodPost, "/v1/generations", api.CommitRequest{
		LeaseID: leaseID, MachineID: machine, BaseWorldID: st.WorldID, BaseGeneration: st.HeadGeneration,
		MinecraftVersion: manifestMC, Files: e.files(k), Players: []string{"Alice"}}, nil)
}

func (e *hoEnv) release(as who, machine string, leaseID int64) int {
	e.t.Helper()
	return call(e.t, e.base, as, http.MethodPost, "/v1/lease/release",
		api.ReleaseRequest{LeaseID: leaseID, MachineID: machine}, nil)
}

func (e *hoEnv) tick() { e.srv.ExpireTick(e.t.Context()) }

// advance lässt die Zeit in 10-s-Schritten vergehen; vor jedem Takt rufen
// beats (Lebenszeichen der noch lebenden Hosts) und der Ablaufschritt.
func (e *hoEnv) advance(d time.Duration, beats func()) {
	e.t.Helper()
	for left := d; left > 0; left -= 10 * time.Second {
		step := min(left, 10*time.Second)
		e.clk.Advance(step)
		if beats != nil {
			beats()
		}
		e.tick()
	}
}

func (e *hoEnv) state() *api.HandoverState { return getStatus(e.t, e.base, userA).Handover }

func (e *hoEnv) wantCalls(want ...string) {
	e.t.Helper()
	got := e.ph.take()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		e.t.Fatalf("Port-Halter-Aufrufe %v, erwartet %v", got, want)
	}
}

// Kompletter Ablauf: Bereitschaft, Start, ready, Angebot, neuer Host bereit,
// Rückgabe, Haupt-Port, Ersatz-Port bis zum Ende des neuen Hosts.
func TestHandoverFullFlow(t *testing.T) {
	e := newHoEnv(t)
	lidA := e.hostWithPlayers(userA, pcA1)
	e.ph.take()

	// Ohne Angebot, solange es keine Übergabe gibt.
	if r := e.present(userB, pcB, true); r.Handover != nil {
		t.Fatalf("Angebot ohne Übergabe: %+v", r.Handover)
	}
	st := e.mustStart(userA, pcA1, lidA)
	if st.To.Email != "" || st.To.ID == "" || st.To.MinecraftName != "Bob" || st.HandoverID == 0 {
		t.Fatalf("Start: %+v", st)
	}
	if s := e.state(); s == nil || s.State != api.HandoverPreparing || s.Address != "" || s.To.Email != "" || s.To.ID == "" {
		t.Fatalf("Status in Vorbereitung: %+v", s)
	}
	if hb := e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob"); hb.Handover == nil || hb.Handover.State != api.HandoverPreparing {
		t.Fatalf("Lebenszeichen in Vorbereitung: %+v", hb.Handover)
	}
	// Bis ready darf der alte Host den Übergabe-Stand einreichen.
	if code := e.commitCode(userA, pcA1, lidA, 2); code != http.StatusOK {
		t.Fatalf("Commit in Vorbereitung: %d", code)
	}
	// Falsche Übergabe-ID und falscher Head werden abgelehnt.
	if code := e.readyReq(userA, pcA1, lidA, st.HandoverID+1); code != http.StatusConflict {
		t.Fatalf("ready mit falscher ID: %d", code)
	}
	if code := call(t, e.base, userA, http.MethodPost, "/v1/handover/ready", api.HandoverReadyRequest{
		HandoverID: st.HandoverID, LeaseID: lidA, MachineID: pcA1, WorldID: e.head().WorldID, Generation: e.head().Number - 1}, nil); code != http.StatusConflict {
		t.Fatalf("ready mit altem Head: %d", code)
	}
	e.wantCalls()

	e.mustReady(userA, pcA1, lidA, st.HandoverID)
	e.wantCalls("set 15566 202")
	if s := e.state(); s == nil || s.State != api.HandoverStarting || s.Address != "" {
		t.Fatalf("Status nach ready: %+v", s)
	}
	// Commit-Sperren: der alte Zuschlag reicht nichts mehr ein.
	if code := e.commitCode(userA, pcA1, lidA, 3); code != http.StatusConflict {
		t.Fatalf("Commit des alten Zuschlags nach ready: %d", code)
	}
	if hb := e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob"); hb.Handover == nil || hb.Handover.State != api.HandoverStarting || hb.MainPort {
		t.Fatalf("Lebenszeichen nach ready: %+v", hb)
	}

	// Das Angebot geht nur an den Kandidaten, bis dessen Lebenszeichen kommt.
	if r := e.present(userA, pcA2, false); r.Handover != nil {
		t.Fatalf("Angebot an den falschen PC: %+v", r.Handover)
	}
	r := e.present(userB, pcB, true)
	o := r.Handover
	if o == nil || o.HandoverID != st.HandoverID || o.Port != portR1 || o.WorldID != e.head().WorldID ||
		o.HeadGeneration != e.head().Number || o.From.Email != "" || o.From.ID == "" || o.From.MachineID != "" ||
		o.MinecraftAddress != "mc.example.org:15565" || o.LeaseID == 0 || o.LeaseID == lidA {
		t.Fatalf("Angebot: %+v", o)
	}
	// Der wartende Zuschlag darf nicht einreichen.
	if code := e.commitCode(userB, pcB, o.LeaseID, 3); code != http.StatusConflict {
		t.Fatalf("Commit des wartenden Zuschlags: %d", code)
	}
	hb := e.mustHB(userB, pcB, o.LeaseID, false, "Bob")
	if hb.MainPort {
		t.Fatal("MainPort vor der Beförderung")
	}
	if r := e.present(userB, pcB, true); r.Handover != nil {
		t.Fatalf("Angebot nach dem ersten Lebenszeichen noch offen: %+v", r.Handover)
	}
	if hb := e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob"); hb.Handover.State != api.HandoverStarting {
		t.Fatalf("vor ready des neuen Hosts: %+v", hb.Handover)
	}

	// Neuer Host bereit: der alte sieht „bereit“ mit Adresse, der Status
	// ohne.
	e.mustHB(userB, pcB, o.LeaseID, true, "Bob")
	hb = e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob")
	if hb.Handover == nil || hb.Handover.State != api.HandoverReady || hb.Handover.Address != "mc.example.org:15566" {
		t.Fatalf("bereit: %+v", hb.Handover)
	}
	if s := e.state(); s == nil || s.State != api.HandoverReady || s.Address != "" {
		t.Fatalf("Status bereit: %+v", s)
	}
	// Auch ein Lebenszeichen ohne ready nimmt „bereit“ nicht zurück.
	e.mustHB(userB, pcB, o.LeaseID, false, "Bob")
	if hb := e.mustHB(userA, pcA1, lidA, true); hb.Handover.State != api.HandoverReady {
		t.Fatalf("bereit nicht dauerhaft: %+v", hb.Handover)
	}
	e.wantCalls()

	// Rückgabe des alten Zuschlags befördert den neuen.
	if code := e.release(userA, pcA1, lidA); code != http.StatusNoContent {
		t.Fatalf("Rückgabe: %d", code)
	}
	e.wantCalls("set 15565 202") // kein Räumen des Haupt-Ports dazwischen
	hb = e.mustHB(userB, pcB, o.LeaseID, true, "Bob")
	if !hb.MainPort || hb.Handover != nil {
		t.Fatalf("Lebenszeichen nach Beförderung: %+v", hb)
	}
	if s := getStatus(t, e.base, userA); s.Handover != nil || s.Host == nil || s.Host.Email != "" || s.Host.MinecraftName != "Bob" {
		t.Fatalf("Status nach Beförderung: %+v", s)
	}
	if code := e.commitCode(userB, pcB, o.LeaseID, 3); code != http.StatusOK {
		t.Fatalf("Commit des beförderten Hosts: %d", code)
	}
	// Der Ersatz-Port bleibt dem neuen Host zugeordnet.
	e.tick()
	e.wantCalls()

	// Ende seines Zuschlags: beide Ports werden geräumt.
	if code := e.release(userB, pcB, o.LeaseID); code != http.StatusNoContent {
		t.Fatalf("Rückgabe des neuen Hosts: %d", code)
	}
	e.tick()
	e.wantCalls("clear 15565", "clear 15566")
}

// Zwei Übergaben hintereinander: die zweite nimmt den anderen Ersatz-Port,
// und der alte Ersatz-Port wird mit dem Ende des alten Hosts geräumt.
func TestHandoverSecondUsesOtherPort(t *testing.T) {
	e := newHoEnv(t)
	lidA := e.hostWithPlayers(userA, pcA1)
	e.present(userB, pcB, true)
	h1 := e.mustStart(userA, pcA1, lidA)
	e.mustReady(userA, pcA1, lidA, h1.HandoverID)
	lidB := e.present(userB, pcB, true).Handover.LeaseID
	e.mustHB(userB, pcB, lidB, true, "Bob")
	e.release(userA, pcA1, lidA)
	e.mustHB(userB, pcB, lidB, true, "Alice", "Bob")
	e.ph.take()

	// Zweite Übergabe von Bob an Alices zweiten PC.
	e.present(userA, pcA2, true)
	h2 := e.mustStart(userB, pcB, lidB)
	if h2.To.Email != "" || h2.To.MinecraftName != "Alice" {
		t.Fatalf("zweiter Kandidat: %+v", h2.To)
	}
	e.mustReady(userB, pcB, lidB, h2.HandoverID)
	e.wantCalls("set 15567 101")
	o := e.present(userA, pcA2, true).Handover
	if o == nil || o.Port != portR2 {
		t.Fatalf("zweites Angebot: %+v", o)
	}
	e.mustHB(userA, pcA2, o.LeaseID, true, "Alice")
	e.release(userB, pcB, lidB)
	e.wantCalls("set 15565 101", "clear 15566") // Bobs Ersatz-Port endet mit seinem Zuschlag
	if hb := e.mustHB(userA, pcA2, o.LeaseID, true, "Alice"); !hb.MainPort {
		t.Fatal("kein MainPort für den zweiten neuen Host")
	}
	e.tick()
	e.wantCalls()
}

// Kein Kandidat, nicht eingerichtet, kein Host, schon eine Übergabe, ein
// Zurückholen steht aus.
func TestHandoverStartRefusals(t *testing.T) {
	e := newHoEnv(t)
	lidA := e.hostWithPlayers(userA, pcA1)

	if code, _ := e.start(userA, pcA1, lidA); code != http.StatusConflict {
		t.Fatalf("Start ohne Kandidat: %d", code)
	}
	// Nur der Inhaber des Haupt-Zuschlags.
	e.present(userB, pcB, true)
	for _, c := range []struct {
		as      who
		machine string
		lease   int64
	}{{userB, pcB, lidA}, {userA, pcA2, lidA}, {userA, pcA1, lidA + 7}} {
		if code, _ := e.start(c.as, c.machine, c.lease); code != http.StatusConflict {
			t.Fatalf("Start von %s/%s/%d: %d", c.as.email, c.machine, c.lease, code)
		}
	}
	// Veraltete Bereitschaft zählt nicht (> 15 s).
	e.clk.Advance(16 * time.Second)
	e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob")
	if code, _ := e.start(userA, pcA1, lidA); code != http.StatusConflict {
		t.Fatalf("Start mit veralteter Bereitschaft: %d", code)
	}
	// Der PC des Hosts ist kein Kandidat.
	e.present(userA, pcA1, true)
	if code, _ := e.start(userA, pcA1, lidA); code != http.StatusConflict {
		t.Fatalf("Start mit nur dem eigenen PC: %d", code)
	}
	// Profil mit „keine Übernahme“.
	if code := call(t, e.base, userB, http.MethodPut, "/v1/profile/"+pcB, api.Profile{KeineUebernahme: true}, nil); code != http.StatusNoContent {
		t.Fatalf("Profil: %d", code)
	}
	e.present(userB, pcB, true)
	if code, _ := e.start(userA, pcA1, lidA); code != http.StatusConflict {
		t.Fatalf("Start mit KeineUebernahme: %d", code)
	}
	call(t, e.base, userB, http.MethodPut, "/v1/profile/"+pcB, api.Profile{}, nil)
	e.present(userB, pcB, true)

	// Während der Übergabe wird ein Zurückholen abgelehnt.
	e.mustStart(userA, pcA1, lidA)
	if code, _ := e.start(userA, pcA1, lidA); code != http.StatusConflict {
		t.Fatalf("zweiter Start: %d", code)
	}
	if code, msg := e.restoreError(userA, api.GenRef{WorldID: e.head().WorldID, Number: 1}); code != http.StatusConflict || msg == "" {
		t.Fatalf("Zurückholen während der Übergabe: %d %q", code, msg)
	}

	// Steht ein Zurückholen aus, gibt es keine Übergabe.
	e3 := newHoEnv(t)
	lid3 := e3.hostWithPlayers(userA, pcA1)
	e3.present(userB, pcB, true)
	if code, resp := e3.restore(userA, api.GenRef{WorldID: e3.head().WorldID, Number: 1}); code != http.StatusOK || !resp.Pending {
		t.Fatalf("Zurückholen: %d %+v", code, resp)
	}
	if code, _ := e3.start(userA, pcA1, lid3); code != http.StatusConflict {
		t.Fatalf("Start bei ausstehendem Zurückholen: %d", code)
	}

	// Ohne handover_ports gibt es keine Übergabe.
	e2 := newRestoreEnv(t)
	lid2 := e2.host(userA, pcA1)
	if code := call(t, e2.base, userB, http.MethodPost, "/v1/presence", api.PresenceRequest{MachineID: pcB}, nil); code != http.StatusOK {
		t.Fatalf("Bereitschaft: %d", code)
	}
	if code := call(t, e2.base, userA, http.MethodPost, "/v1/handover/start", api.HandoverStartRequest{LeaseID: lid2, MachineID: pcA1}, nil); code != http.StatusConflict {
		t.Fatalf("Start ohne handover_ports: %d", code)
	}
}

// Ein Kandidat, der mitspielt (joined), aber nicht in der Spielerliste des
// Hosts steht, wird ignoriert; ein untätiger PC (nicht joined) zählt immer.
func TestHandoverCandidateNeedsPlayerInGame(t *testing.T) {
	e := newHoEnv(t)
	lidA := e.host(userA, pcA1)
	e.commit(userA, pcA1, lidA, 1, true, manifestMC)
	e.mustHB(userA, pcA1, lidA, true, "Alice") // Bob ist nicht auf dem Server

	e.present(userB, pcB, true)
	if code, _ := e.start(userA, pcA1, lidA); code != http.StatusConflict {
		t.Fatalf("Start mit nicht anwesendem Mitspieler: %d", code)
	}
	e.present(userB, pcB, false) // Dauerbetrieb
	if code, st := e.start(userA, pcA1, lidA); code != http.StatusOK || st.To.Email != "" || st.To.MinecraftName != "Bob" {
		t.Fatalf("Start mit untätigem PC: %d %+v", code, st)
	}
}

// Scheitern: neuer Host nicht rechtzeitig, Zeit nach dem Start, Zeit nach dem
// Angebot, Rückgabe und Absturz des wartenden Zuschlags. Danach reicht der
// alte Host wieder ein, der Ersatz-Port ist geräumt, und „gescheitert“
// bleibt 2 min abrufbar.
func TestHandoverFailures(t *testing.T) {
	// start() bis ready+Angebot angenommen; liefert die LeaseID des Wartenden.
	offered := func(e *hoEnv, lidA int64) (int64, int64) {
		st := e.mustStart(userA, pcA1, lidA)
		e.mustReady(userA, pcA1, lidA, st.HandoverID)
		lid := e.present(userB, pcB, true).Handover.LeaseID
		e.mustHB(userB, pcB, lid, false, "Bob")
		return st.HandoverID, lid
	}
	beatA := func(e *hoEnv, lidA int64) func() {
		return func() { e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob") }
	}
	failed := func(e *hoEnv, lidA int64, port bool) {
		t.Helper()
		hb := e.mustHB(userA, pcA1, lidA, true, "Alice", "Bob")
		if hb.Handover == nil || hb.Handover.State != api.HandoverFailed || hb.Handover.Reason == "" {
			t.Fatalf("erwartet gescheitert, bekommen %+v", hb.Handover)
		}
		if code := e.commitCode(userA, pcA1, lidA, 5); code != http.StatusOK {
			t.Fatalf("Commit des alten Zuschlags nach dem Scheitern: %d", code)
		}
		if port {
			e.wantCalls("clear 15566")
		}
		if _, ok := e.lm.Incoming(); ok {
			t.Fatal("wartender Zuschlag nach dem Scheitern noch da")
		}
		// 2 min abrufbar, dann weg.
		e.advance(100*time.Second, beatA(e, lidA))
		if e.state() == nil {
			t.Fatal("gescheitert nach 100 s schon weg")
		}
		e.advance(30*time.Second, beatA(e, lidA))
		if e.state() != nil {
			t.Fatalf("gescheitert nach 130 s noch da: %+v", e.state())
		}
	}
	setup := func() (*hoEnv, int64) {
		e := newHoEnv(t)
		lidA := e.hostWithPlayers(userA, pcA1)
		e.present(userB, pcB, true)
		e.ph.take()
		return e, lidA
	}

	t.Run("5 min ohne ready", func(t *testing.T) {
		e, lidA := setup()
		st := e.mustStart(userA, pcA1, lidA)
		e.advance(290*time.Second, beatA(e, lidA))
		if s := e.state(); s == nil || s.State != api.HandoverPreparing {
			t.Fatalf("nach 290 s: %+v", s)
		}
		e.advance(20*time.Second, beatA(e, lidA))
		if code := e.readyReq(userA, pcA1, lidA, st.HandoverID); code != http.StatusConflict {
			t.Fatalf("ready nach der Frist: %d", code)
		}
		e.wantCalls()
		failed(e, lidA, false)
	})

	t.Run("5 min ohne bereit", func(t *testing.T) {
		e, lidA := setup()
		_, lidB := offered(e, lidA)
		e.ph.take()
		beats := func() {
			beatA(e, lidA)()
			e.mustHB(userB, pcB, lidB, false, "Bob")
		}
		e.advance(290*time.Second, beats)
		if s := e.state(); s == nil || s.State != api.HandoverStarting {
			t.Fatalf("nach 290 s: %+v", s)
		}
		e.advance(20*time.Second, beats)
		failed(e, lidA, true)
		// Der wartende Zuschlag ist verworfen: sein Lebenszeichen wird abgewiesen.
		if code, _ := e.hb(userB, pcB, lidB, true, "Bob"); code != http.StatusConflict {
			t.Fatalf("Lebenszeichen des verworfenen Zuschlags: %d", code)
		}
	})

	t.Run("wartender gibt zurück", func(t *testing.T) {
		e, lidA := setup()
		_, lidB := offered(e, lidA)
		e.ph.take()
		if code := e.release(userB, pcB, lidB); code != http.StatusNoContent {
			t.Fatalf("Rückgabe des wartenden: %d", code)
		}
		e.wantCalls("clear 15566")
		if _, _, ok := e.lm.Current(); !ok {
			t.Fatal("Haupt-Zuschlag wurde mit zurückgegeben")
		}
		failed(e, lidA, false)
	})

	t.Run("wartender stürzt ab", func(t *testing.T) {
		e, lidA := setup()
		offered(e, lidA)
		e.ph.take()
		e.advance(100*time.Second, beatA(e, lidA)) // B schweigt, sein Zuschlag verfällt
		failed(e, lidA, true)
	})

	t.Run("alter Host gibt vor ready zurück", func(t *testing.T) {
		e, lidA := setup()
		st := e.mustStart(userA, pcA1, lidA)
		if code := e.release(userA, pcA1, lidA); code != http.StatusNoContent {
			t.Fatalf("Rückgabe: %d", code)
		}
		e.tick()
		if s := e.state(); s != nil {
			t.Fatalf("Übergabe nach Rückgabe vor ready: %+v", s)
		}
		if _, ok := e.lm.Incoming(); ok {
			t.Fatal("wartender Zuschlag ohne ready")
		}
		if code := e.readyReq(userA, pcA1, lidA, st.HandoverID); code != http.StatusConflict {
			t.Fatalf("ready nach Rückgabe: %d", code)
		}
		for _, c := range e.ph.take() {
			if c == "set 15565 202" {
				t.Fatal("Haupt-Port wurde ohne Übergabe an den Kandidaten gesetzt")
			}
		}
	})
}

// Der alte Host stürzt nach ready ab: nach Ablauf seines Zuschlags wird der
// wartende befördert und bekommt den Haupt-Port.
func TestHandoverOldHostCrashAfterReady(t *testing.T) {
	e := newHoEnv(t)
	lidA := e.hostWithPlayers(userA, pcA1)
	e.present(userB, pcB, true)
	st := e.mustStart(userA, pcA1, lidA)
	e.mustReady(userA, pcA1, lidA, st.HandoverID)
	lidB := e.present(userB, pcB, true).Handover.LeaseID
	e.mustHB(userB, pcB, lidB, true, "Bob")
	e.ph.take()

	// A schweigt; B lebt weiter. Vor dem Ablauf bleibt alles beim Alten.
	beatB := func() { e.mustHB(userB, pcB, lidB, true, "Bob") }
	e.advance(80*time.Second, beatB)
	if _, id, ok := e.lm.Current(); !ok || id != lidA {
		t.Fatalf("vor dem Ablauf: Haupt-Zuschlag %d %v", id, ok)
	}
	if hb := e.mustHB(userB, pcB, lidB, true, "Bob"); hb.MainPort {
		t.Fatal("MainPort vor dem Ablauf")
	}
	e.advance(20*time.Second, beatB)
	e.wantCalls("set 15565 202")
	if _, id, ok := e.lm.Current(); !ok || id != lidB {
		t.Fatalf("nach dem Ablauf: Haupt-Zuschlag %d %v, erwartet %d", id, ok, lidB)
	}
	if hb := e.mustHB(userB, pcB, lidB, true, "Bob"); !hb.MainPort {
		t.Fatal("kein MainPort nach der Beförderung")
	}
	if code := e.commitCode(userB, pcB, lidB, 3); code != http.StatusOK {
		t.Fatalf("Commit des beförderten Hosts: %d", code)
	}
	// Der alte Host merkt es: sein Zuschlag ist weg.
	if code, _ := e.hb(userA, pcA1, lidA, true); code != http.StatusConflict {
		t.Fatalf("Lebenszeichen des alten Hosts: %d", code)
	}
}

// Der alte Host bricht seine Übergabe ab (POST /v1/handover/abort): in der
// Vorbereitung und nach ready. Wartender Zuschlag und Ersatz-Port werden
// geräumt, die Übergabe ist gleich vergessen, eine neue darf beginnen.
func TestHandoverAbort(t *testing.T) {
	abort := func(e *hoEnv, as who, machine string, leaseID, hoID int64) int {
		e.t.Helper()
		return call(e.t, e.base, as, http.MethodPost, "/v1/handover/abort",
			api.HandoverAbortRequest{HandoverID: hoID, LeaseID: leaseID, MachineID: machine}, nil)
	}
	setup := func() (*hoEnv, int64) {
		e := newHoEnv(t)
		lidA := e.hostWithPlayers(userA, pcA1)
		e.present(userB, pcB, true)
		e.ph.take()
		return e, lidA
	}

	t.Run("in der Vorbereitung", func(t *testing.T) {
		e, lidA := setup()
		st := e.mustStart(userA, pcA1, lidA)
		if code := abort(e, userA, pcA1, lidA, st.HandoverID); code != http.StatusNoContent {
			t.Fatalf("abort: %d", code)
		}
		if s := e.state(); s != nil {
			t.Fatalf("Übergabe nach dem Abbruch noch da: %+v", s)
		}
		if code := e.readyReq(userA, pcA1, lidA, st.HandoverID); code != http.StatusConflict {
			t.Fatalf("ready nach dem Abbruch: %d", code)
		}
		// Commit ist erlaubt, eine neue Übergabe auch.
		if code := e.commitCode(userA, pcA1, lidA, 5); code != http.StatusOK {
			t.Fatalf("Commit nach dem Abbruch: %d", code)
		}
		e.present(userB, pcB, true)
		if code, _ := e.start(userA, pcA1, lidA); code != http.StatusOK {
			t.Fatalf("neuer Start nach dem Abbruch: %d", code)
		}
	})

	t.Run("nach ready", func(t *testing.T) {
		e, lidA := setup()
		st := e.mustStart(userA, pcA1, lidA)
		e.mustReady(userA, pcA1, lidA, st.HandoverID)
		lidB := e.present(userB, pcB, true).Handover.LeaseID
		e.mustHB(userB, pcB, lidB, false, "Bob")
		e.ph.take()
		if code := abort(e, userA, pcA1, lidA, st.HandoverID); code != http.StatusNoContent {
			t.Fatalf("abort: %d", code)
		}
		e.wantCalls("clear 15566")
		if _, ok := e.lm.Incoming(); ok {
			t.Fatal("wartender Zuschlag nach dem Abbruch noch da")
		}
		if s := e.state(); s != nil {
			t.Fatalf("Übergabe nach dem Abbruch noch da: %+v", s)
		}
		if code, _ := e.hb(userB, pcB, lidB, true, "Bob"); code != http.StatusConflict {
			t.Fatalf("Lebenszeichen des verworfenen Zuschlags: %d", code)
		}
		if _, _, ok := e.lm.Current(); !ok {
			t.Fatal("Haupt-Zuschlag weg")
		}
		e.present(userB, pcB, true)
		if code, _ := e.start(userA, pcA1, lidA); code != http.StatusOK {
			t.Fatalf("neuer Start nach dem Abbruch: %d", code)
		}
	})

	t.Run("fremd und unbekannt", func(t *testing.T) {
		e, lidA := setup()
		st := e.mustStart(userA, pcA1, lidA)
		// Nicht der Host: abgewiesen, die Übergabe bleibt.
		if code := abort(e, userB, pcB, lidA, st.HandoverID); code != http.StatusConflict {
			t.Fatalf("abort eines Fremden: %d", code)
		}
		if s := e.state(); s == nil || s.State != api.HandoverPreparing {
			t.Fatalf("Übergabe nach fremdem Abbruch: %+v", s)
		}
		// Falsche Übergabe-Nummer: nichts zu tun, nichts passiert.
		if code := abort(e, userA, pcA1, lidA, st.HandoverID+99); code != http.StatusNoContent {
			t.Fatalf("abort mit falscher Nummer: %d", code)
		}
		if s := e.state(); s == nil {
			t.Fatal("Übergabe nach abort mit falscher Nummer weg")
		}
	})
}

// Findet die Übergabe keinen Kandidaten, nennt die Fehlermeldung (und damit
// das Log des Hosts, das mit der Welt wandert) den Grund: veraltet, nicht im
// Spiel, keine Übernahme – nur mit Minecraft-Namen, nie mit E-Mail-Adressen.
func TestHandoverStartRefusalNamesReason(t *testing.T) {
	e := newHoEnv(t)
	lid := e.host(userA, pcA1)
	e.commit(userA, pcA1, lid, 1, true, manifestMC)

	reason := func() string {
		t.Helper()
		code, body := e.raw(userA, http.MethodPost, "/v1/handover/start", api.HandoverStartRequest{LeaseID: lid, MachineID: pcA1})
		if code != http.StatusConflict {
			t.Fatalf("Start: %d %s", code, body)
		}
		var out struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Error
	}

	// Niemand gemeldet.
	if msg := reason(); !strings.Contains(msg, "kein PC hat sich als übernahmebereit gemeldet") {
		t.Errorf("ohne Meldung: %q", msg)
	}
	// Bob meldet sich als Mitspieler, steht aber nicht in der Spielerliste.
	e.mustHB(userA, pcA1, lid, true, "Alice")
	e.present(userB, pcB, true)
	msg := reason()
	for _, want := range []string{"1 PC gemeldet", "nicht in der Spielerliste", "Spielerliste des Hosts: Alice"} {
		if !strings.Contains(msg, want) {
			t.Errorf("nicht im Spiel: %q fehlt in %q", want, msg)
		}
	}
	// Veraltete Meldung.
	e.clk.Advance(20 * time.Second)
	e.mustHB(userA, pcA1, lid, true, "Alice", "Bob")
	if msg := reason(); !strings.Contains(msg, "zu lange nicht gehört") || !strings.Contains(msg, "s gehört") {
		t.Errorf("veraltet: %q", msg)
	}
	// Keine E-Mail-Adresse in der Meldung.
	for _, m := range []string{reason(), msg} {
		if strings.Contains(m, "@") || strings.Contains(m, emailB) {
			t.Errorf("Adresse in der Meldung: %q", m)
		}
	}
}
