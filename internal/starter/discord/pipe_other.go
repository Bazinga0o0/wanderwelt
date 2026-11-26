//go:build !windows

package discord

import (
	"context"
	"errors"
)

// dialPipe: Discord-Anzeige gibt es nur unter Windows.
func dialPipe(context.Context) (Conn, error) {
	return nil, errors.New("Discord-Anzeige nur unter Windows")
}
