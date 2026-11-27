package benachrichtigung

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Grenzen und Standardwerte des Senders.
const (
	// MinAbstand: höchstens eine Nachricht je 5 s (Discord begrenzt Webhooks
	// ohnehin; so bleiben wir weit darunter).
	MinAbstand = 5 * time.Second
	// MaxWarteschlange: so viele Nachrichten warten höchstens; ist sie voll,
	// fällt die älteste weg.
	MaxWarteschlange = 20
	// MaxWarten: so lange wartet der Sender höchstens auf Retry-After.
	MaxWarten = 2 * time.Minute
	// Versuche je Nachricht (der erste Versuch zählt mit).
	Versuche = 3
	// standardWarten gilt, wenn Discord keine Zeit nennt.
	standardWarten = 5 * time.Second
	// antwortMax: So viel der Antwort wird gelesen.
	antwortMax = 4 << 10
	// zustellFrist: Obergrenze für einen einzelnen Aufruf.
	zustellFrist = 15 * time.Second
)

// Doer ist der HTTP-Client des Senders (*http.Client); Tests setzen einen
// eigenen ein.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// SenderOptionen konfigurieren einen Sender; fehlende Werte nehmen die
// Standardwerte.
type SenderOptionen struct {
	Webhook          Webhook
	Client           Doer
	Log              *slog.Logger
	MinAbstand       time.Duration
	MaxWarteschlange int
	MaxWarten        time.Duration
	Versuche         int
	// Schlafen wartet d oder bis ctx endet (Fehler dann ctx.Err); Jetzt ist
	// die Uhr. Nur für Tests.
	Schlafen func(ctx context.Context, d time.Duration) error
	Jetzt    func() time.Time
}

// Sender schickt Nachrichten ratenbegrenzt an einen Webhook. Senden kehrt
// sofort zurück; Lauf arbeitet die Warteschlange ab.
type Sender struct {
	url      string
	hc       Doer
	log      *slog.Logger
	abstand  time.Duration
	maxQ     int
	maxWarte time.Duration
	versuche int
	schlafen func(ctx context.Context, d time.Duration) error
	jetzt    func() time.Time

	mu      sync.Mutex
	q       []string
	wake    chan struct{}
	zuletzt time.Time // Zeit des letzten Versuchs
}

// NeuerSender baut den Sender.
func NeuerSender(o SenderOptionen) *Sender {
	s := &Sender{
		url:      o.Webhook.Adresse(),
		hc:       o.Client,
		log:      o.Log,
		abstand:  o.MinAbstand,
		maxQ:     o.MaxWarteschlange,
		maxWarte: o.MaxWarten,
		versuche: o.Versuche,
		schlafen: o.Schlafen,
		jetzt:    o.Jetzt,
		wake:     make(chan struct{}, 1),
	}
	if s.hc == nil {
		s.hc = &http.Client{
			Timeout: zustellFrist,
			// Nie woandershin folgen: Die Adresse gehört genau diesem Webhook.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.abstand <= 0 {
		s.abstand = MinAbstand
	}
	if s.maxQ <= 0 {
		s.maxQ = MaxWarteschlange
	}
	if s.maxWarte <= 0 {
		s.maxWarte = MaxWarten
	}
	if s.versuche <= 0 {
		s.versuche = Versuche
	}
	if s.schlafen == nil {
		s.schlafen = schlafen
	}
	if s.jetzt == nil {
		s.jetzt = time.Now
	}
	return s
}

func schlafen(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Senden reiht text ein und kehrt sofort zurück. Ist die Warteschlange voll,
// fällt die älteste Nachricht weg.
func (s *Sender) Senden(text string) {
	s.mu.Lock()
	if len(s.q) >= s.maxQ {
		s.q = s.q[1:]
		s.log.Debug("Discord-Webhook: Warteschlange voll, älteste Nachricht verworfen")
	}
	s.q = append(s.q, text)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Wartend ist die Zahl der Nachrichten in der Warteschlange.
func (s *Sender) Wartend() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.q)
}

func (s *Sender) holen() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.q) == 0 {
		return "", false
	}
	t := s.q[0]
	s.q = s.q[1:]
	return t, true
}

// Lauf stellt Nachrichten zu, bis ctx endet. Aufrufen in eigener Goroutine.
func (s *Sender) Lauf(ctx context.Context) {
	for {
		text, ok := s.holen()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		if err := s.zustellen(ctx, text); err != nil && ctx.Err() != nil {
			return
		}
	}
}

// zustellen schickt text, mit Abstand zum letzten Versuch und mit
// Wiederholung bei 429, 5xx und Verbindungsfehlern. Fehler stehen nur im
// Debug-Log; der Rückgabewert ist nur ctx.Err.
func (s *Sender) zustellen(ctx context.Context, text string) error {
	for versuch := 1; versuch <= s.versuche; versuch++ {
		if !s.zuletzt.IsZero() {
			if err := s.schlafen(ctx, s.abstand-s.jetzt().Sub(s.zuletzt)); err != nil {
				return err
			}
		}
		s.zuletzt = s.jetzt()
		warten, wiederholen := s.einmal(ctx, text, versuch)
		if !wiederholen || versuch == s.versuche {
			return ctx.Err()
		}
		if err := s.schlafen(ctx, min(warten, s.maxWarte)); err != nil {
			return err
		}
	}
	return nil
}

// nachricht ist der Körper eines Webhook-Aufrufs. allowed_mentions mit leerem
// parse verbietet jede Erwähnung (@everyone, @here, Rollen, Mitglieder).
type nachricht struct {
	Content         string          `json:"content"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

// einmal schickt text einmal. wiederholen: Es lohnt ein neuer Versuch nach
// warten.
func (s *Sender) einmal(ctx context.Context, text string, versuch int) (warten time.Duration, wiederholen bool) {
	body, err := json.Marshal(nachricht{Content: kuerze(text), AllowedMentions: allowedMentions{Parse: []string{}}})
	if err != nil {
		return 0, false
	}
	cctx, cancel := context.WithTimeout(ctx, zustellFrist)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		s.log.Debug("Discord-Webhook: Anfrage nicht gebaut")
		return 0, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wanderwelt-koordinator")
	resp, err := s.hc.Do(req)
	if err != nil {
		// net/http schreibt die Adresse in jeden Fehler: nur die Ursache zeigen.
		s.log.Debug("Discord-Webhook: Verbindung fehlgeschlagen", "versuch", versuch, "ursache", ursache(err))
		return standardWarten * time.Duration(versuch), ctx.Err() == nil
	}
	defer resp.Body.Close()
	antwort, _ := io.ReadAll(io.LimitReader(resp.Body, antwortMax))
	switch {
	case resp.StatusCode/100 == 2:
		return 0, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 5:
		w := retryAfter(resp.Header.Get("Retry-After"), antwort)
		if w <= 0 {
			w = standardWarten * time.Duration(versuch)
		}
		s.log.Debug("Discord-Webhook: später noch einmal", "status", resp.StatusCode, "versuch", versuch, "warten", w)
		return w, true
	}
	s.log.Debug("Discord-Webhook: abgelehnt, Nachricht verworfen", "status", resp.StatusCode)
	return 0, false
}

// ursache ist der Text der Ursache eines Verbindungsfehlers ohne die Adresse.
func ursache(err error) string {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err.Error()
	}
	return err.Error()
}

// retryAfter liest die Wartezeit aus dem Kopf Retry-After (Sekunden, auch
// mit Nachkommastellen) oder, wenn der fehlt, aus „retry_after“ im Körper
// (Sekunden); 0, wenn keine genannt ist.
func retryAfter(kopf string, body []byte) time.Duration {
	if f, err := strconv.ParseFloat(strings.TrimSpace(kopf), 64); err == nil && f > 0 && !math.IsInf(f, 0) {
		return sekunden(f)
	}
	var b struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &b) == nil && b.RetryAfter > 0 && !math.IsInf(b.RetryAfter, 0) {
		return sekunden(b.RetryAfter)
	}
	return 0
}

func sekunden(f float64) time.Duration {
	if f > float64(time.Hour/time.Second) {
		f = float64(time.Hour / time.Second)
	}
	return time.Duration(f * float64(time.Second))
}
