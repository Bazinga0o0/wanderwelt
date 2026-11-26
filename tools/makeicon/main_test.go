package main

import (
	"bytes"
	"image/png"
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

// TestDiscordPNG: 1024x1024, nicht leer.
func TestDiscordPNG(t *testing.T) {
	b := discordPNGBytes()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if r := img.Bounds(); r.Dx() != 1024 || r.Dy() != 1024 {
		t.Errorf("Größe %v", r)
	}
	if _, _, _, a := img.At(512, 512).RGBA(); a == 0 {
		t.Error("Mitte leer")
	}
}
