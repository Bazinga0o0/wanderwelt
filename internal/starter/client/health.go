package client

import (
	"context"
	"net/http"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Health holt die Gesundheit des Koordinators (GET /v1/health; nur Admins).
// Ein älterer Koordinator kennt den Endpunkt nicht: ErrNotSupported.
func (c *Client) Health(ctx context.Context) (*api.Health, error) {
	var out api.Health
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/health", nil, &out); err != nil {
		return nil, notSupported(err, true)
	}
	return &out, nil
}
