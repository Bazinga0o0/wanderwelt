//go:build !windows

package host

import (
	"os"
	"testing"
)

// checkOwnerOnly: nur der Eigentümer darf lesen und schreiben.
func checkOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s: Rechte %v, erwartet 0600", path, info.Mode().Perm())
	}
}

// konsolenHelfer gibt es nur unter Windows.
func konsolenHelfer() { os.Exit(0) }
