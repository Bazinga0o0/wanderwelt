//go:build !windows

package group

import "os"

// restrictToOwner: Außerhalb von Windows genügt Modus 0600.
func restrictToOwner(path string) error { return os.Chmod(path, 0o600) }
