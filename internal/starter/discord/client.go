package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// Conn ist die Verbindung zu Discord. Available meldet, wie viele Bytes zum
// Lesen bereitliegen, ohne zu blockieren: Die Windows-Pipe ist ein
// synchrones Handle, ein wartendes Lesen würde das Schreiben blockieren;
// deshalb wird nur gelesen, wenn etwas da ist (oder nach einem Befehl auf
// die Antwort gewartet wird).
type Conn interface {
	io.ReadWriteCloser
	Available() (int, error)
}

// Dial öffnet die Verbindung zu Discord (Pipe). Fehler heißt: Discord läuft
// nicht.
type Dial func(ctx context.Context) (Conn, error)

// Options für New; Nullwerte ergeben das Verhalten im Starter.
type Options struct {
	ClientID string
	Dial     Dial // Standard: Named Pipe (nur Windows)
	// Logf: Debug-Log; nil: nichts.
	Logf func(format string, args ...any)
	// OnJoin bekommt das Beitritts-Geheimnis, wenn jemand bei einem Freund
	// auf „Beitreten“ klickt. Es kommt aus dem Lese-Thread des Pakets und
	// muss sofort zurückkehren; das Geheimnis ist unbekannte Eingabe.
	OnJoin func(secret string)
	// Backoff: erste Wartezeit nach einem Fehler (Standard 5 s), verdoppelt
	// bis MaxBackoff (Standard 60 s).
	Backoff, MaxBackoff time.Duration
	// IOTimeout: so lange darf ein Austausch mit Discord dauern (Standard 5 s).
	IOTimeout time.Duration
	// PollEvery: so oft wird nach Ereignissen gesehen (Standard 250 ms).
	PollEvery time.Duration
}

// Enabled: false bei fehlender Client-ID ("" oder "0").
func Enabled(clientID string) bool { return clientID != "" && clientID != "0" }

// New liefert die Anzeige. Ohne gültige Client-ID ein Noop.
func New(o Options) Presence {
	if !Enabled(o.ClientID) {
		return Noop{}
	}
	if o.Dial == nil {
		o.Dial = dialPipe
	}
	if o.Backoff <= 0 {
		o.Backoff = 5 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 60 * time.Second
	}
	if o.IOTimeout <= 0 {
		o.IOTimeout = 5 * time.Second
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 250 * time.Millisecond
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	c := &client{o: o, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go c.run()
	return c
}

type client struct {
	o Options

	mu     sync.Mutex
	want   *Activity // gewünschte Anzeige; nil: keine
	listen bool

	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	// nur run:
	conn    Conn
	sent    string // JSON der zuletzt gesendeten Aktivität ("null": gelöscht)
	nonce   int
	backoff time.Duration
	lastErr string
}

func (c *client) Set(a Activity) { c.update(func() { c.want = &a }) }
func (c *client) Clear()         { c.update(func() { c.want = nil }) }
func (c *client) Listen()        { c.update(func() { c.listen = true }) }

func (c *client) update(f func()) {
	c.mu.Lock()
	f()
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Close löscht die Aktivität und wartet kurz auf das Ende.
func (c *client) Close() {
	c.Clear()
	c.stopOnce.Do(func() { close(c.stop) })
	select {
	case <-c.done:
	case <-time.After(2 * c.o.IOTimeout):
	}
}

func (c *client) run() {
	defer close(c.done)
	poll := time.NewTicker(c.o.PollEvery)
	defer poll.Stop()
	var retry <-chan time.Time
	for {
		var err error
		select {
		case <-c.stop:
			if err := c.apply(false); err != nil { // Schluss: löschen, wenn verbunden
				c.o.Logf("Discord: %v", err)
			}
			c.drop()
			return
		case <-c.wake:
			err = c.apply(true)
		case <-retry:
			err = c.apply(true)
		case <-poll.C:
			if c.conn == nil {
				continue
			}
			err = c.pollEvents()
		}
		if err != nil {
			if msg := err.Error(); msg != c.lastErr { // dieselbe Meldung nicht jede Minute
				c.lastErr = msg
				c.o.Logf("Discord: %v", err)
			}
			c.drop()
			if c.backoff == 0 {
				c.backoff = c.o.Backoff
			} else {
				c.backoff = min(2*c.backoff, c.o.MaxBackoff)
			}
			retry = time.After(c.backoff)
			continue
		}
		c.backoff, retry, c.lastErr = 0, nil, ""
	}
}

func (c *client) drop() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.sent = ""
}

// pollEvents liest, was Discord von sich aus geschickt hat (Ereignisse,
// PING, CLOSE).
func (c *client) pollEvents() error {
	for {
		n, err := c.conn.Available()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		r, ok, err := readOne(c.conn)
		if err != nil {
			return err
		}
		if ok && r.Cmd == "DISPATCH" {
			c.event(r)
		}
	}
}

// event wertet ein Ereignis von Discord aus.
func (c *client) event(r response) {
	if r.Evt == evtJoin && c.o.OnJoin != nil && r.Data.Secret != "" {
		c.o.OnJoin(r.Data.Secret)
	}
}

// apply bringt Discord auf den gewünschten Stand. connect: bei Bedarf
// verbinden (sonst nur über eine bestehende Verbindung senden).
func (c *client) apply(connect bool) error {
	c.mu.Lock()
	var want *Activity
	if c.want != nil {
		w := *c.want
		w.Start = w.Start.Truncate(time.Second)
		want = &w
	}
	listen := c.listen
	c.mu.Unlock()
	key, _ := json.Marshal(want)
	if c.conn == nil && (want == nil && !listen || !connect) {
		return nil // nichts zu löschen, nichts zu hören
	}
	if string(key) == c.sent {
		return nil // unverändert
	}
	if c.conn == nil {
		if err := c.connect(); err != nil {
			return err
		}
	}
	c.nonce++
	p, err := activityPayload(want, os.Getpid(), strconv.Itoa(c.nonce))
	if err != nil {
		return err
	}
	r, err := c.exchange(opFrame, p, "")
	if err != nil {
		return err
	}
	if r.Evt == "ERROR" {
		// Discord lehnt die Aktivität ab (nicht die Verbindung): nicht wiederholen.
		c.o.Logf("Discord: SET_ACTIVITY abgelehnt: %s", r.Data.Message)
	}
	c.sent = string(key)
	return nil
}

func (c *client) connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.o.IOTimeout)
	defer cancel()
	conn, err := c.o.Dial(ctx)
	if err != nil {
		return err
	}
	c.conn = conn
	r, err := c.exchange(opHandshake, handshakePayload(c.o.ClientID), "READY")
	if err != nil {
		return fmt.Errorf("Handshake: %w", err)
	}
	if r.Evt != "READY" {
		return fmt.Errorf("Handshake: unerwartete Antwort %q %s", r.Evt, r.Data.Message)
	}
	c.nonce++
	r, err = c.exchange(opFrame, subscribePayload(evtJoin, strconv.Itoa(c.nonce)), "")
	if err != nil {
		return fmt.Errorf("Abo ACTIVITY_JOIN: %w", err)
	}
	if r.Evt == "ERROR" {
		c.o.Logf("Discord: ACTIVITY_JOIN nicht abonniert: %s", r.Data.Message)
	}
	c.o.Logf("Discord verbunden")
	return nil
}

// exchange schreibt einen Rahmen und liest die Antwort (wantEvt: siehe
// readResponse), höchstens IOTimeout lang. Zeitüberschreitung: Fehler; der
// Aufrufer verwirft die Verbindung.
func (c *client) exchange(op uint32, payload []byte, wantEvt string) (response, error) {
	type result struct {
		r   response
		err error
	}
	conn := c.conn
	ch := make(chan result, 1)
	go func() {
		if err := writeFrame(conn, op, payload); err != nil {
			ch <- result{err: err}
			return
		}
		r, err := readResponse(conn, wantEvt, c.event)
		ch <- result{r, err}
	}()
	select {
	case res := <-ch:
		return res.r, res.err
	case <-time.After(c.o.IOTimeout):
		return response{}, fmt.Errorf("keine Antwort von Discord")
	}
}
