package client

import (
	"context"
	"net/http"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Join stellt als Nicht-Mitglied eine Beitrittsanfrage mit dem Minecraft-
// Namen (POST /v1/join). Fehler als *StatusError: 400 Name ungültig oder
// Mojang kennt ihn nicht, 409 Name vergeben oder schon angefragt (oder das
// Konto ist schon Mitglied), 429 offene Anfrage da, zu viele offene oder
// weniger als 24 h nach einer Ablehnung, 503 Mojang nicht erreichbar.
func (c *Client) Join(ctx context.Context, minecraftName string) (*api.JoinStatus, error) {
	var out api.JoinStatus
	if err := c.call(ctx, callTimeout, http.MethodPost, "/v1/join", api.JoinRequest{MinecraftName: minecraftName}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JoinStatus fragt den Stand der eigenen Beitrittsanfrage (GET /v1/join).
func (c *Client) JoinStatus(ctx context.Context) (*api.JoinStatus, error) {
	var out api.JoinStatus
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/join", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
