package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCommittedFilesCurrent: Die eingecheckten Dateien in cmd/wanderwelt
// entsprechen dem aktuellen Symbol (sonst go run ./tools/makeicon).
func TestCommittedFilesCurrent(t *testing.T) {
	ico, syso, err := build()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{icoName: ico, sysoName: syso} {
		got, err := os.ReadFile(filepath.Join("..", "..", "cmd", "wanderwelt", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s veraltet – go run ./tools/makeicon", name)
		}
	}
}
