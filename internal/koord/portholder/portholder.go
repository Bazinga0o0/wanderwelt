// Package portholder setzt über die Dienst-Schnittstelle von tunneld den
// Halter des öffentlichen Minecraft-Ports. Ein neuer Halter schließt jede
// alte Sitzung auf dem Port; das ist die harte Sperre gegen zwei Hosts.
//
// Schnittstelle (nur Loopback, Header "Authorization: Bearer <Geheimnis>"):
// PUT {api}/v1/tcp-ports/{port}/holder mit {"account_id": N} setzt den
// Halter, DELETE räumt ihn.
package portholder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrUnknownAccount: tunneld kennt das Konto nicht oder hat es noch nicht
// freigeschaltet (PUT antwortet 404). Ein neuer Versuch hilft nicht.
var ErrUnknownAccount = errors.New("tunnel-Konto nicht freigeschaltet oder unbekannt")

// Client setzt und räumt den Halter eines Ports.
type Client interface {
	// Set macht Konto accountID zum Halter von port.
	Set(ctx context.Context, port int, accountID int64) error
	// Clear räumt den Halter von port. Gibt es keinen, ist das kein Fehler.
	Clear(ctx context.Context, port int) error
}

// timeout begrenzt jeden Aufruf; der Koordinator hält dabei seine Sperre
// für Zuschlag und Port.
const timeout = 10 * time.Second

// New liefert einen Client für die Dienst-Schnittstelle unter api (etwa
// "http://127.0.0.1:7447") mit dem Dienst-Geheimnis secret.
func New(api, secret string) Client {
	return &httpClient{
		base:   strings.TrimRight(strings.TrimSpace(api), "/"),
		secret: secret,
		http: &http.Client{
			Timeout: timeout,
			// Eine Dienst-Schnittstelle leitet nicht um; folgen hieße, das
			// Geheimnis womöglich anderswohin zu schicken.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Noop tut nichts; für den Betrieb ohne tunneld (port_holder.api leer).
func Noop() Client { return noop{} }

type noop struct{}

func (noop) Set(context.Context, int, int64) error { return nil }
func (noop) Clear(context.Context, int) error      { return nil }

type httpClient struct {
	base   string
	secret string
	http   *http.Client
}

func (c *httpClient) Set(ctx context.Context, port int, accountID int64) error {
	if accountID <= 0 {
		return fmt.Errorf("portholder: Port %d: keine Konto-ID für den Halter", port)
	}
	body, err := json.Marshal(struct {
		AccountID int64 `json:"account_id"`
	}{accountID})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, port, body)
}

func (c *httpClient) Clear(ctx context.Context, port int) error {
	return c.do(ctx, http.MethodDelete, port, nil)
}

func (c *httpClient) do(ctx context.Context, method string, port int, body []byte) error {
	url := fmt.Sprintf("%s/v1/tcp-ports/%d/holder", c.base, port)
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return fmt.Errorf("portholder: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("portholder: %s Port %d: %w", method, port, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return nil
	case method == http.MethodDelete && resp.StatusCode == http.StatusNotFound:
		return nil // kein Halter gesetzt: schon geräumt
	case method == http.MethodPut && resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("portholder: %s Port %d: %w (%s)", method, port, ErrUnknownAccount, errorText(msg))
	}
	return fmt.Errorf("portholder: %s Port %d: %s: %s", method, port, resp.Status, errorText(msg))
}

// errorText macht aus dem Antwortkörper eine kurze Meldung: das Feld
// "error" aus JSON, sonst der Anfang des Texts.
func errorText(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	s := strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
	if len(s) > 200 {
		cut := 200
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + " …"
	}
	if s == "" {
		return "ohne Text"
	}
	return s
}
