package fensterapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Reiter „Server“ (nur Admins): Ansichten und Aktionen der lokalen
// Steuerung, die der Koordinator beantwortet.
//
//	GET  /v1/serverlisten  → api.ServerLists
//	GET  /v1/spielregeln   → api.Gamerules (nur, solange die Welt läuft)
//	POST /v1/serverliste   = api.ServerListChange → api.ServerListResult
//	POST /v1/spielregel    = api.GameruleSet      → api.ServerCommandResult
//	POST /v1/befehl        = {"command": …}       → api.ServerCommandResult

// Namen der Aktionen.
const (
	AktionServerliste = "serverliste"
	AktionSpielregel  = "spielregel"
	AktionBefehl      = "befehl"
)

// ServerListen holt Whitelist, Ops und Banns.
func (c *Client) ServerListen(ctx context.Context) (*api.ServerLists, error) {
	var r api.ServerLists
	if err := c.getJSON(ctx, "/v1/serverlisten", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Spielregeln holt die Spielregeln der laufenden Welt.
func (c *Client) Spielregeln(ctx context.Context) (*api.Gamerules, error) {
	var r api.Gamerules
	if err := c.getJSON(ctx, "/v1/spielregeln", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// AktionAntwort löst die Aktion name aus wie Aktion und liest die
// JSON-Antwort nach out.
func (c *Client) AktionAntwort(ctx context.Context, name string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("Steuerung: %s: %w", name, err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/"+url.PathEscape(name), data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return c.transportError(ctx, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("Steuerung: %s: %w", name, err)
	}
	return nil
}
