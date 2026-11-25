//go:build !windows

package fenster

import (
	"context"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// Run gibt es nur unter Windows.
func Run(ctx context.Context, exe, version string) error { return winsys.ErrUnsupported }
