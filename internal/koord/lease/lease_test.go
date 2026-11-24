package lease

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// fakeClock ist eine von Hand gestellte Uhr. After-Kanäle feuern erst,
// wenn Advance die Zeit über ihren Zeitpunkt schiebt.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	at := c.now.Add(d)
	if d <= 0 {
		ch <- at
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: at, ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	rest := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.now) {
			rest = append(rest, w)
			continue
		}
		w.ch <- c.now
	}
	c.waiters = rest
}

func newManager(t *testing.T) (*Manager, *fakeClock, string) {
	t.Helper()
	clk := newFakeClock()
	path := filepath.Join(t.TempDir(), "lease.json")
	m, err := New(path, clk)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, clk, path
}

func candidate(n int) Candidate {
	return Candidate{
		Email:         fmt.Sprintf("p%d@x.de", n),
		MinecraftName: fmt.Sprintf("Spieler%d", n),
		MachineID:     fmt.Sprintf("pc-%d", n),
		AccountID:     int64(100 + n),
		Score:         api.Score{UploadMbps: 50, CPUScore: 1000, RAMFreeMB: 16000, RAMTotalMB: 32000, XmxMB: 6000},
	}
}

// waitCandidates wartet, bis im offenen Sammelfenster n Anfragen stehen.
func waitCandidates(t *testing.T, m *Manager, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		got := 0
		if m.win != nil {
			got = len(m.win.entries)
		}
		m.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Sammelfenster erreicht nicht %d Anfragen", n)
}

type result struct {
	g   Grant
	err error
}

// requestHost holt über Request und Sammelfenster einen Zuschlag für c.
func requestHost(t *testing.T, m *Manager, clk *fakeClock, c Candidate) Grant {
	t.Helper()
	ch := make(chan result, 1)
	go func() {
		g, err := m.Request(context.Background(), c)
		ch <- result{g, err}
	}()
	waitCandidates(t, m, 1)
	clk.Advance(Window)
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Request: %v", r.err)
		}
		if r.g.Role != api.RoleHost {
			t.Fatalf("Request: Role %q, erwartet host", r.g.Role)
		}
		return r.g
	case <-time.After(5 * time.Second):
		t.Fatal("Request kehrt nach Ende des Sammelfensters nicht zurück")
		return Grant{}
	}
}

func TestConcurrentRequestsYieldOneHost(t *testing.T) {
	m, clk, _ := newManager(t)

	const n = 10
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		c := candidate(i)
		go func() {
			g, err := m.Request(context.Background(), c)
			results <- result{g, err}
		}()
	}
	waitCandidates(t, m, n)
	clk.Advance(Window)

	var hosts, joins int
	var leaseIDs []int64
	var hostInfos []api.HostInfo
	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("Request: %v", r.err)
			}
			switch r.g.Role {
			case api.RoleHost:
				hosts++
			case api.RoleJoin:
				joins++
			default:
				t.Fatalf("unerwartete Role %q", r.g.Role)
			}
			leaseIDs = append(leaseIDs, r.g.LeaseID)
			hostInfos = append(hostInfos, r.g.Host)
		case <-time.After(5 * time.Second):
			t.Fatal("nicht alle Anfragen beantwortet")
		}
	}
	if hosts != 1 || joins != n-1 {
		t.Fatalf("hosts=%d joins=%d, erwartet 1 und %d", hosts, joins, n-1)
	}
	for i := range leaseIDs {
		if leaseIDs[i] == 0 || leaseIDs[i] != leaseIDs[0] || hostInfos[i] != hostInfos[0] {
			t.Fatalf("Antworten verweisen nicht auf denselben Zuschlag: %v %v", leaseIDs, hostInfos)
		}
	}
	host, id, ok := m.Current()
	if !ok || id != leaseIDs[0] || *host != hostInfos[0] {
		t.Fatalf("Current() = %v %d %v, erwartet %v %d", host, id, ok, hostInfos[0], leaseIDs[0])
	}

	// Kommt jemand, während ein Host existiert, ist er sofort Mitspieler.
	g, err := m.Request(context.Background(), candidate(99))
	if err != nil || g.Role != api.RoleJoin || g.LeaseID != leaseIDs[0] {
		t.Fatalf("Request bei bestehendem Host = %+v, %v; erwartet sofort join", g, err)
	}
}

func TestBestPrefersUploadWeighted(t *testing.T) {
	a := Candidate{Email: "a@x.de", Score: api.Score{UploadMbps: 100, CPUScore: 1000, RAMFreeMB: 16000}}
	b := Candidate{Email: "b@x.de", Score: api.Score{UploadMbps: 20, CPUScore: 1500, RAMFreeMB: 32000}}
	if got := Best([]Candidate{a, b}); got != 0 {
		t.Fatalf("Best(A, B) = %d, erwartet 0 (A)", got)
	}
	if got := Best([]Candidate{b, a}); got != 1 {
		t.Fatalf("Best(B, A) = %d, erwartet 1 (A)", got)
	}
	// Knapper Fall, den nur das Verhältnis 0,5 zu 0,35 entscheidet:
	// A = 0,5 + 0,35·2/3 ≈ 0,733 gegen B = 0,5·0,7 + 0,35 = 0,700 (RAM gleich).
	a2 := Candidate{Email: "a@x.de", Score: api.Score{UploadMbps: 100, CPUScore: 1000, RAMFreeMB: 16000}}
	b2 := Candidate{Email: "b@x.de", Score: api.Score{UploadMbps: 70, CPUScore: 1500, RAMFreeMB: 16000}}
	if got := Best([]Candidate{b2, a2}); got != 1 {
		t.Fatalf("Best(B2, A2) = %d, erwartet 1 (A2)", got)
	}
}

func TestBestSkipsBelowMinimumUnlessNoneQualify(t *testing.T) {
	a := Candidate{Email: "a@x.de", Score: api.Score{UploadMbps: 3, CPUScore: 5000, RAMFreeMB: 32000, XmxMB: 6000}}
	b := Candidate{Email: "b@x.de", Score: api.Score{UploadMbps: 10, CPUScore: 1000, RAMFreeMB: 8000, XmxMB: 6000}}
	if got := Best([]Candidate{a, b}); got != 1 {
		t.Fatalf("Best(A Up=3, B Up=10) = %d, erwartet 1 (B)", got)
	}
	// Zu wenig freier RAM (unter Xmx + 1 GB) zählt ebenso als unter dem Minimum.
	c := Candidate{Email: "c@x.de", Score: api.Score{UploadMbps: 500, CPUScore: 5000, RAMFreeMB: 7000, XmxMB: 6000}}
	if got := Best([]Candidate{c, b}); got != 1 {
		t.Fatalf("Best(C RAM knapp, B) = %d, erwartet 1 (B)", got)
	}
	if got := Best([]Candidate{a}); got != 0 {
		t.Fatalf("Best(nur A) = %d, erwartet 0", got)
	}
}

func TestLeaseExpiresAfter90s(t *testing.T) {
	m, clk, _ := newManager(t)
	c := candidate(1)
	g := requestHost(t, m, clk, c)

	clk.Advance(89 * time.Second)
	if !m.Valid(g.LeaseID, c.MachineID) {
		t.Fatal("Zuschlag nach 89 s ungültig")
	}
	clk.Advance(2 * time.Second)
	if m.Valid(g.LeaseID, c.MachineID) {
		t.Fatal("Zuschlag nach 91 s noch gültig")
	}
	if expired, acc := m.Expire(); !expired || acc != c.AccountID {
		t.Fatalf("Expire() = %v, %d; erwartet true, %d", expired, acc, c.AccountID)
	}
	if expired, _ := m.Expire(); expired {
		t.Fatal("Expire() meldet denselben Ablauf zweimal")
	}
	if err := m.Heartbeat(g.LeaseID, c.MachineID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Heartbeat nach Ablauf = %v, erwartet ErrLeaseLost", err)
	}
}

// B3 (Gesamtlauf): Stürzt der Host ab, gilt sein Zuschlag noch bis zu 90 s.
// Bereit ist er aber nur, solange sein letztes Lebenszeichen (Takt 10 s)
// jünger als 25 s ist – nach zwei verpassten nicht mehr.
func TestReadyNeedsRecentHeartbeat(t *testing.T) {
	m, clk, _ := newManager(t)
	c := candidate(1)
	g := requestHost(t, m, clk, c)
	beat := func(ready bool) {
		t.Helper()
		if err := m.Heartbeat(g.LeaseID, c.MachineID); err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
		m.Report(g.LeaseID, []string{"Alex"}, ready)
	}
	ready := func() bool {
		info, ok := m.Info()
		return ok && info.Ready
	}

	beat(true)
	if !ready() {
		t.Fatal("direkt nach dem Lebenszeichen nicht bereit")
	}
	clk.Advance(24 * time.Second) // ein Lebenszeichen verpasst, das zweite knapp
	if !ready() {
		t.Fatal("nach 24 s ohne Lebenszeichen nicht mehr bereit")
	}
	clk.Advance(time.Second)
	if ready() {
		t.Fatal("nach 25 s ohne Lebenszeichen (zwei verpasst) noch bereit")
	}
	if info, ok := m.Info(); !ok || info.Host.MachineID != c.MachineID {
		t.Fatalf("Zuschlag nach 25 s weg: %+v, %v – er gilt bis zum Ablauf weiter", info, ok)
	}
	// Kommt der Host zurück, ist er mit dem nächsten Lebenszeichen wieder bereit.
	beat(true)
	if !ready() {
		t.Fatal("nach neuem Lebenszeichen nicht bereit")
	}
	// Ein frisches Lebenszeichen ohne ready bleibt nicht bereit.
	beat(false)
	if ready() {
		t.Fatal("Lebenszeichen ready=false, trotzdem bereit")
	}
}

func TestHeartbeatFromOtherMachineSameAccountRejected(t *testing.T) {
	m, clk, _ := newManager(t)
	c := candidate(1)
	g := requestHost(t, m, clk, c)

	other := c.MachineID + "-laptop" // gleiches Konto, gleiche E-Mail, anderer PC
	if err := m.Heartbeat(g.LeaseID, other); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Heartbeat vom anderen PC = %v, erwartet ErrLeaseLost", err)
	}
	if m.Valid(g.LeaseID, other) {
		t.Fatal("Valid() für anderen PC desselben Kontos")
	}
	if err := m.Release(g.LeaseID, other); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Release vom anderen PC = %v, erwartet ErrLeaseLost", err)
	}
	if err := m.Heartbeat(g.LeaseID, c.MachineID); err != nil {
		t.Fatalf("Heartbeat vom Host-PC: %v", err)
	}
	if !m.Valid(g.LeaseID, c.MachineID) {
		t.Fatal("Zuschlag des Host-PCs ungültig")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	m1, clk, path := newManager(t)
	c := candidate(1)
	g := requestHost(t, m1, clk, c)

	m2, err := New(path, clk)
	if err != nil {
		t.Fatalf("New nach Neustart: %v", err)
	}
	host, id, ok := m2.Current()
	if !ok || id != g.LeaseID || *host != g.Host {
		t.Fatalf("Current() nach Neustart = %v %d %v, erwartet %v %d", host, id, ok, g.Host, g.LeaseID)
	}
	clk.Advance(60 * time.Second)
	if err := m2.Heartbeat(g.LeaseID, c.MachineID); err != nil {
		t.Fatalf("Heartbeat nach Neustart: %v", err)
	}
	// Das Lebenszeichen muss selbst einen Neustart überleben: 120 s nach dem
	// Zuschlag, aber nur 60 s nach dem Lebenszeichen ist er noch gültig.
	clk.Advance(60 * time.Second)
	m2, err = New(path, clk)
	if err != nil {
		t.Fatalf("New nach Lebenszeichen: %v", err)
	}
	if !m2.Valid(g.LeaseID, c.MachineID) {
		t.Fatal("Verlängerung durch Lebenszeichen nach Neustart verloren")
	}
	if err := m2.Release(g.LeaseID, c.MachineID); err != nil {
		t.Fatalf("Release nach Neustart: %v", err)
	}
	g2 := requestHost(t, m2, clk, c)
	if g2.LeaseID <= g.LeaseID {
		t.Fatalf("LeaseID nach Neustart %d, erwartet > %d", g2.LeaseID, g.LeaseID)
	}

	// Ein beim Laden schon abgelaufener Zuschlag zählt als kein Host, wird
	// aber einmal von Expire gemeldet (Port-Halter räumen).
	clk.Advance(LeaseTTL + time.Second)
	m3, err := New(path, clk)
	if err != nil {
		t.Fatalf("New nach Ablauf: %v", err)
	}
	if _, _, ok := m3.Current(); ok {
		t.Fatal("abgelaufener Zuschlag nach Neustart noch aktuell")
	}
	if expired, acc := m3.Expire(); !expired || acc != c.AccountID {
		t.Fatalf("Expire() nach Neustart = %v, %d; erwartet true, %d", expired, acc, c.AccountID)
	}
	if g3 := requestHost(t, m3, clk, candidate(2)); g3.LeaseID <= g2.LeaseID {
		t.Fatalf("LeaseID %d, erwartet > %d", g3.LeaseID, g2.LeaseID)
	}
}

func TestLeaseIDsStrictlyIncrease(t *testing.T) {
	m, clk, _ := newManager(t)
	c := candidate(1)
	g1 := requestHost(t, m, clk, c)
	if err := m.Release(g1.LeaseID, c.MachineID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, _, ok := m.Current(); ok {
		t.Fatal("nach Release noch ein Host")
	}
	g2 := requestHost(t, m, clk, c)
	if g2.LeaseID <= g1.LeaseID {
		t.Fatalf("LeaseID %d nach %d, erwartet größer", g2.LeaseID, g1.LeaseID)
	}
	clk.Advance(LeaseTTL + time.Second)
	m.Expire()
	g3 := requestHost(t, m, clk, candidate(2))
	if g3.LeaseID <= g2.LeaseID {
		t.Fatalf("LeaseID %d nach %d, erwartet größer", g3.LeaseID, g2.LeaseID)
	}
}

func TestAcquireOnlyWhenIdle(t *testing.T) {
	m, clk, _ := newManager(t)
	a, b := candidate(1), candidate(2)

	g, err := m.Acquire(a)
	if err != nil || g.Role != api.RoleHost || g.LeaseID == 0 {
		t.Fatalf("Acquire ohne Host = %+v, %v; erwartet host", g, err)
	}
	if _, err := m.Acquire(b); !errors.Is(err, ErrBusy) {
		t.Fatalf("Acquire bei bestehendem Host = %v, erwartet ErrBusy", err)
	}
	if err := m.Release(g.LeaseID, a.MachineID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Offenes Sammelfenster: Acquire darf sich nicht an der Wahl vorbeidrängen.
	ch := make(chan result, 1)
	go func() {
		g, err := m.Request(context.Background(), b)
		ch <- result{g, err}
	}()
	waitCandidates(t, m, 1)
	if _, err := m.Acquire(a); !errors.Is(err, ErrBusy) {
		t.Fatalf("Acquire bei offenem Sammelfenster = %v, erwartet ErrBusy", err)
	}
	clk.Advance(Window)
	select {
	case r := <-ch:
		if r.err != nil || r.g.Role != api.RoleHost || r.g.Host.MachineID != b.MachineID {
			t.Fatalf("Request = %+v, %v; erwartet host für B", r.g, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Request kehrt nicht zurück")
	}
}
