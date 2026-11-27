package fensterapi

import (
	"context"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Gesundheit holt die Gesundheit des Koordinators (nur für Admins; sonst
// Fehler mit dem Text der Instanz).
func (c *Client) Gesundheit(ctx context.Context) (*api.Health, error) {
	var r api.Health
	if err := c.getJSON(ctx, "/v1/gesundheit", &r); err != nil {
		return nil, err
	}
	return &r, nil
}
