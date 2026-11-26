package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// bufConn puffert, was die Gegenseite schickt, damit Available stimmt (wie
// PeekNamedPipe bei der echten Pipe).
type bufConn struct {
	net.Conn
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
	err  error
}

func newBufConn(c net.Conn) *bufConn {
	b := &bufConn{Conn: c}
	b.cond = sync.NewCond(&b.mu)
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, err := c.Read(tmp)
			b.mu.Lock()
			b.buf = append(b.buf, tmp[:n]...)
			if err != nil {
				b.err = err
			}
			b.cond.Broadcast()
			b.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return b
}

func (b *bufConn) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.buf) == 0 && b.err == nil {
		b.cond.Wait()
	}
	if len(b.buf) == 0 {
		return 0, b.err
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func (b *bufConn) Available() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) == 0 && b.err != nil {
		return 0, b.err
	}
	return len(b.buf), nil
}

type recvd struct {
	op      uint32
	payload []byte
}

// fakeDiscord ist die Gegenseite: antwortet auf Handshake mit READY und auf
// Befehle mit einer Bestätigung; merkt sich alle Rahmen.
type fakeDiscord struct {
	mu      sync.Mutex
	dials   int
	failing bool
	frames  []recvd
	conns   []net.Conn
	notify  chan struct{}
}

func newFake() *fakeDiscord { return &fakeDiscord{notify: make(chan struct{}, 1000)} }

func (f *fakeDiscord) dial(context.Context) (Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.failing {
		return nil, errors.New("keine Pipe")
	}
	c, s := net.Pipe()
	f.conns = append(f.conns, s)
	go f.serve(s)
	return newBufConn(c), nil
}

func (f *fakeDiscord) serve(s net.Conn) {
	for {
		op, p, err := readFrame(s)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.frames = append(f.frames, recvd{op, p})
		f.mu.Unlock()
		f.notify <- struct{}{}
		var m struct{ Cmd string }
		_ = json.Unmarshal(p, &m)
		evt := `{"cmd":"` + m.Cmd + `","evt":null}`
		if op == opHandshake {
			evt = `{"cmd":"DISPATCH","evt":"READY"}`
		}
		if writeFrame(s, opFrame, []byte(evt)) != nil {
			return
		}
	}
}

// push schickt Discord-seitig ein Ereignis an den Starter.
func (f *fakeDiscord) push(t *testing.T, payload string) {
	t.Helper()
	f.mu.Lock()
	s := f.conns[len(f.conns)-1]
	f.mu.Unlock()
	if err := writeFrame(s, opFrame, []byte(payload)); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeDiscord) all() []recvd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recvd(nil), f.frames...)
}

// cmds liefert die Rahmen mit diesem Befehl.
func (f *fakeDiscord) cmds(cmd string) [][]byte {
	var out [][]byte
	for _, fr := range f.all() {
		var m struct{ Cmd string }
		if fr.op == opFrame && json.Unmarshal(fr.payload, &m) == nil && m.Cmd == cmd {
			out = append(out, fr.payload)
		}
	}
	return out
}

func (f *fakeDiscord) waitCmds(t *testing.T, cmd string, n int) [][]byte {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if got := f.cmds(cmd); len(got) >= n {
			return got
		}
		select {
		case <-f.notify:
		case <-deadline:
			t.Fatalf("nur %d von %d Rahmen %s", len(f.cmds(cmd)), n, cmd)
		}
	}
}

func (f *fakeDiscord) kill() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
}

func opts(f *fakeDiscord) Options {
	return Options{ClientID: "4711", Dial: f.dial, Backoff: 20 * time.Millisecond, MaxBackoff: 80 * time.Millisecond,
		IOTimeout: time.Second, PollEvery: 10 * time.Millisecond}
}

func TestHandshakeUndAktivitaet(t *testing.T) {
	f := newFake()
	p := New(opts(f))
	defer p.Close()
	start := time.Unix(1_800_000_000, 0)
	p.Set(Activity{State: "mit 3 Freunden", Start: start})
	acts := f.waitCmds(t, "SET_ACTIVITY", 1)

	fr := f.all()
	if fr[0].op != opHandshake {
		t.Fatalf("erster Rahmen op %d", fr[0].op)
	}
	var hs map[string]any
	_ = json.Unmarshal(fr[0].payload, &hs)
	if hs["v"] != float64(1) || hs["client_id"] != "4711" {
		t.Errorf("Handshake: %s", fr[0].payload)
	}
	sub := f.cmds("SUBSCRIBE")
	if len(sub) != 1 || !strings.Contains(string(sub[0]), `"ACTIVITY_JOIN"`) {
		t.Errorf("Abo: %q", sub)
	}

	var cmd struct {
		Cmd  string `json:"cmd"`
		Args struct {
			PID      int `json:"pid"`
			Activity struct {
				Details    string `json:"details"`
				State      string `json:"state"`
				Timestamps struct {
					Start int64 `json:"start"`
				} `json:"timestamps"`
				Assets struct {
					LargeImage string `json:"large_image"`
				} `json:"assets"`
				Buttons []struct{ Label, URL string } `json:"buttons"`
				Party   *json.RawMessage              `json:"party"`
				Secrets *json.RawMessage              `json:"secrets"`
			} `json:"activity"`
		} `json:"args"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(acts[0], &cmd); err != nil {
		t.Fatal(err)
	}
	a := cmd.Args.Activity
	if cmd.Nonce == "" || cmd.Args.PID == 0 {
		t.Errorf("Befehl: %s", acts[0])
	}
	if a.Details != "wanderwelt" || a.State != "mit 3 Freunden" || a.Timestamps.Start != start.Unix() ||
		a.Assets.LargeImage != "wanderwelt-discord-1024" || len(a.Buttons) != 1 ||
		a.Buttons[0].Label != "wanderwelt herunterladen" || a.Buttons[0].URL != "https://github.com/Bazinga0o0/wanderwelt/releases/latest" {
		t.Errorf("Aktivität: %s", acts[0])
	}
	if a.Party != nil || a.Secrets != nil {
		t.Errorf("Party/Geheimnis ohne JoinSecret: %s", acts[0])
	}
}

func TestPartyUndGeheimnis(t *testing.T) {
	f := newFake()
	p := New(opts(f))
	defer p.Close()
	p.Set(Activity{State: "x", PartyID: "wanderwelt-ab12", PartySize: 3, PartyMax: 4, JoinSecret: "w1.abc"})
	acts := f.waitCmds(t, "SET_ACTIVITY", 1)
	var m struct {
		Args struct {
			Activity struct {
				Party struct {
					ID   string `json:"id"`
					Size []int  `json:"size"`
				} `json:"party"`
				Secrets struct {
					Join string `json:"join"`
				} `json:"secrets"`
			} `json:"activity"`
		} `json:"args"`
	}
	if err := json.Unmarshal(acts[0], &m); err != nil {
		t.Fatal(err)
	}
	a := m.Args.Activity
	if a.Party.ID != "wanderwelt-ab12" || len(a.Party.Size) != 2 || a.Party.Size[0] != 3 || a.Party.Size[1] != 4 || a.Secrets.Join != "w1.abc" {
		t.Errorf("Party: %s", acts[0])
	}
}

func TestNurBeiAenderungSenden(t *testing.T) {
	f := newFake()
	p := New(opts(f))
	defer p.Close()
	a := Activity{State: "ohne Freunde :(", Start: time.Unix(1_800_000_000, 0)}
	p.Set(a)
	f.waitCmds(t, "SET_ACTIVITY", 1)
	p.Set(a) // gleich
	p.Set(a)
	time.Sleep(100 * time.Millisecond)
	if n := len(f.cmds("SET_ACTIVITY")); n != 1 {
		t.Fatalf("%d Aktivitäten, erwartet 1", n)
	}
	p.Set(Activity{State: "mit 1 Freund", Start: a.Start})
	f.waitCmds(t, "SET_ACTIVITY", 2)
}

func nullActivity(b []byte) bool {
	var m struct {
		Args struct {
			Activity *json.RawMessage `json:"activity"`
		} `json:"args"`
	}
	return json.Unmarshal(b, &m) == nil && (m.Args.Activity == nil || string(*m.Args.Activity) == "null")
}

func TestClearSendetNullUndOhneVerbindungNichts(t *testing.T) {
	f := newFake()
	p := New(opts(f))
	defer p.Close()
	p.Clear() // nie verbunden: kein Verbindungsversuch
	time.Sleep(60 * time.Millisecond)
	f.mu.Lock()
	d := f.dials
	f.mu.Unlock()
	if d != 0 {
		t.Fatalf("%d Verbindungsversuche ohne Aktivität", d)
	}
	p.Set(Activity{State: "x"})
	f.waitCmds(t, "SET_ACTIVITY", 1)
	p.Clear()
	acts := f.waitCmds(t, "SET_ACTIVITY", 2)
	if !nullActivity(acts[1]) {
		t.Errorf("Clear: %s", acts[1])
	}
}

func TestCloseLoeschtAktivitaet(t *testing.T) {
	f := newFake()
	p := New(opts(f))
	p.Set(Activity{State: "x"})
	f.waitCmds(t, "SET_ACTIVITY", 1)
	p.Close()
	acts := f.waitCmds(t, "SET_ACTIVITY", 2)
	if !nullActivity(acts[1]) {
		t.Errorf("Close sendete: %s", acts[1])
	}
}

func TestWiederverbindenMitBackoff(t *testing.T) {
	f := newFake()
	f.failing = true
	p := New(opts(f))
	defer p.Close()
	p.Set(Activity{State: "x"})
	time.Sleep(250 * time.Millisecond)
	f.mu.Lock()
	d := f.dials
	f.failing = false
	f.mu.Unlock()
	// 20+40+80+80 ms: einige Versuche, aber nicht im Dauerfeuer.
	if d < 3 || d > 8 {
		t.Errorf("%d Versuche in 250 ms", d)
	}
	f.waitCmds(t, "SET_ACTIVITY", 1) // verbindet sich, sobald Discord da ist
}

func TestVerbindungsabbruchNeuAufbauen(t *testing.T) {
	f := newFake()
	p := New(opts(f))
	defer p.Close()
	p.Set(Activity{State: "a"})
	f.waitCmds(t, "SET_ACTIVITY", 1)
	f.kill() // Discord neu gestartet
	p.Set(Activity{State: "b"})
	f.waitCmds(t, "SET_ACTIVITY", 2)
	n := 0
	for _, fr := range f.all() {
		if fr.op == opHandshake {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d Handshakes, erwartet 2", n)
	}
}

func TestOhneClientIDKeinVerbindungsversuch(t *testing.T) {
	for _, id := range []string{"", "0"} {
		f := newFake()
		o := opts(f)
		o.ClientID = id
		p := New(o)
		p.Listen()
		p.Set(Activity{State: "x"})
		p.Clear()
		p.Close()
		if f.dials != 0 {
			t.Errorf("ClientID %q: %d Versuche", id, f.dials)
		}
	}
	if !Enabled(ClientID) {
		t.Error("eingetragene ClientID gilt als aus")
	}
}

func TestJoinEreignis(t *testing.T) {
	f := newFake()
	got := make(chan string, 4)
	o := opts(f)
	o.OnJoin = func(s string) { got <- s }
	p := New(o)
	defer p.Close()
	p.Listen() // ohne Aktivität verbinden und abonnieren
	f.waitCmds(t, "SUBSCRIBE", 1)

	f.push(t, `{"cmd":"DISPATCH","evt":"ACTIVITY_JOIN","data":{"secret":"w1.geheim"}}`)
	select {
	case s := <-got:
		if s != "w1.geheim" {
			t.Errorf("Geheimnis %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("kein ACTIVITY_JOIN angekommen")
	}

	// Andere Ereignisse und leere Geheimnisse lösen nichts aus.
	f.push(t, `{"cmd":"DISPATCH","evt":"ACTIVITY_SPECTATE","data":{"secret":"x"}}`)
	f.push(t, `{"cmd":"DISPATCH","evt":"ACTIVITY_JOIN","data":{"secret":""}}`)
	time.Sleep(100 * time.Millisecond)
	select {
	case s := <-got:
		t.Errorf("unerwartetes Ereignis: %q", s)
	default:
	}
}

func TestJoinEreignisWaehrendAktivitaet(t *testing.T) {
	f := newFake()
	got := make(chan string, 4)
	o := opts(f)
	o.OnJoin = func(s string) { got <- s }
	p := New(o)
	defer p.Close()
	p.Set(Activity{State: "x"})
	f.waitCmds(t, "SET_ACTIVITY", 1)
	f.push(t, `{"cmd":"DISPATCH","evt":"ACTIVITY_JOIN","data":{"secret":"w1.a"}}`)
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("kein ACTIVITY_JOIN")
	}
	p.Set(Activity{State: "y"}) // Schreiben klappt weiterhin
	f.waitCmds(t, "SET_ACTIVITY", 2)
}

func TestPingWirdBeantwortet(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		_ = writeFrame(s, opPing, []byte(`{"n":1}`))
		_ = writeFrame(s, opFrame, []byte(`{"cmd":"SET_ACTIVITY"}`))
	}()
	done := make(chan response, 1)
	go func() { r, _ := readResponse(c, "", nil); done <- r }()
	op, p, err := readFrame(s)
	if err != nil || op != opPong || string(p) != `{"n":1}` {
		t.Fatalf("PONG: %d %s %v", op, p, err)
	}
	if r := <-done; r.Cmd != "SET_ACTIVITY" {
		t.Errorf("Antwort: %+v", r)
	}
}

func TestFriendsText(t *testing.T) {
	for n, want := range map[int]string{-1: "ohne Freunde :(", 0: "ohne Freunde :(", 1: "mit 1 Freund", 2: "mit 2 Freunden", 10: "mit 10 Freunden"} {
		if got := FriendsText(n); got != want {
			t.Errorf("FriendsText(%d) = %q", n, got)
		}
	}
}

func TestZuGrosserRahmen(t *testing.T) {
	c, s := net.Pipe()
	go func() { _, _ = s.Write([]byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0x7f}) }()
	if _, _, err := readFrame(c); err == nil {
		t.Error("riesiger Rahmen akzeptiert")
	}
}

var _ io.Reader = (*bufConn)(nil)
