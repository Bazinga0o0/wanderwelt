package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// ListJoinRequests holt die offenen Beitrittsanfragen (GET /v1/join-requests;
// nur Admins, älteste zuerst).
func (c *Client) ListJoinRequests(ctx context.Context) ([]api.JoinRequestInfo, error) {
	var out []api.JoinRequestInfo
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/join-requests", nil, &out); err != nil {
		return nil, notSupported(err, true)
	}
	return out, nil
}

// AcceptJoin nimmt die Anfrage des Kontos email an
// (POST /v1/join-requests/{email}/accept; nur Admins). 404: keine offene
// Anfrage; 409: der Minecraft-Name ist inzwischen vergeben.
func (c *Client) AcceptJoin(ctx context.Context, email string) error {
	return c.decideJoin(ctx, email, "accept")
}

// DeclineJoin lehnt die Anfrage des Kontos email ab
// (POST /v1/join-requests/{email}/decline; nur Admins).
func (c *Client) DeclineJoin(ctx context.Context, email string) error {
	return c.decideJoin(ctx, email, "decline")
}

func (c *Client) decideJoin(ctx context.Context, email, was string) error {
	return notSupported(c.call(ctx, callTimeout, http.MethodPost,
		"/v1/join-requests/"+url.PathEscape(email)+"/"+was, nil, nil), false)
}
