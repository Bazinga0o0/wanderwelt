package karte

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testPNG erzeugt eine 256×256-Kachel mit Palette (16 Farben); seed macht
// den Inhalt (und damit den Hash) verschieden.
func testPNG(t testing.TB, seed int) []byte {
	t.Helper()
	pal := make(color.Palette, 16)
	for i := range pal {
		pal[i] = color.RGBA{R: uint8(i * 16), G: uint8((i*7 + seed*13) % 256), B: uint8(255 - i*16), A: 255}
	}
	img := image.NewPaletted(image.Rect(0, 0, KachelPx, KachelPx), pal)
	for y := range KachelPx {
		for x := range KachelPx {
			img.SetColorIndex(x, y, uint8((x/16+y/16+seed)%16))
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// testManifest baut ein Manifest aus Kacheln: Schlüssel → PNG-Bytes.
func testManifest(t testing.TB, generated time.Time, kacheln map[string][]byte) *Manifest {
	t.Helper()
	m, err := LeseManifest(testManifestJSON(t, generated, kacheln))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testManifestJSON(t testing.TB, generated time.Time, kacheln map[string][]byte) []byte {
	t.Helper()
	tiles := map[string]Eintrag{}
	for k, d := range kacheln {
		tiles[k] = Eintrag{SHA256: HashVon(d), Size: int64(len(d))}
	}
	b, err := json.Marshal(map[string]any{
		"format": 1, "dimension": "overworld", "tile_size": 256, "levels": []int{0, 1, 2, 3, 4, 5, 6},
		"source": fmt.Sprint("test-", generated.Unix()), "generated": generated, "tiles": tiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bis wartet höchstens 5 s, bis f wahr ist.
func bis(t testing.TB, was string, f func() bool) {
	t.Helper()
	for ende := time.Now().Add(5 * time.Second); time.Now().Before(ende); time.Sleep(2 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("Zeitüberschreitung: %s", was)
}

func writeFile(dir, name string, data []byte) error {
	return os.WriteFile(filepath.Join(dir, name), data, 0o600)
}
