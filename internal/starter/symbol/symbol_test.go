package symbol

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestSymbolMatchesReference vergleicht mit den Pixeln, die der Entwurf
// (render in symbol-entwurf.html, mit Node ausgeführt) liefert: 16 und 32
// Pixel Bild für Bild, 48, 64, 256 und alle Zustandspunkte über SHA-256.
func TestSymbolMatchesReference(t *testing.T) {
	f, err := os.Open("testdata/referenz.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	next := func() string {
		if !sc.Scan() {
			t.Fatal("referenz.txt endet zu früh")
		}
		return sc.Text()
	}
	letters := map[string]byte{}
	checked := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		switch fs[0] {
		case "farbe":
			letters[fs[2]] = fs[1][0]
		case "bild", "hash":
			n, err := strconv.Atoi(fs[1])
			if err != nil {
				t.Fatalf("Zeile %q: %v", line, err)
			}
			got := rows(t, renderRef(n, fs[2]), letters)
			name := fmt.Sprintf("%d %s", n, fs[2])
			if fs[0] == "bild" {
				for y := 0; y < n; y++ {
					if want := next(); got[y] != want {
						t.Errorf("%s: Zeile %d\n  Go:      %s\n  Entwurf: %s", name, y, got[y], want)
					}
				}
			} else {
				sum := sha256.Sum256([]byte(strings.Join(got, "\n")))
				if h := hex.EncodeToString(sum[:]); h != fs[3] {
					t.Errorf("%s: SHA-256 %s, Entwurf %s", name, h, fs[3])
				}
			}
			checked++
		default:
			t.Fatalf("unbekannte Zeile %q", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 17 {
		t.Fatalf("nur %d Bilder geprüft", checked)
	}
}

func renderRef(n int, state string) *image.NRGBA {
	if state == "-" {
		return Render(n)
	}
	return RenderBadge(n, rgb(state))
}

// rows schreibt ein Bild als Zeilen aus Buchstaben je Farbe (wie
// testdata/referenz.mjs).
func rows(t *testing.T, img *image.NRGBA, letters map[string]byte) []string {
	t.Helper()
	n := img.Rect.Dx()
	out := make([]string, n)
	for y := 0; y < n; y++ {
		var b strings.Builder
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			key := "transparent"
			if c.A != 0 {
				if c.A != 0xff {
					t.Fatalf("Pixel %d,%d halbdurchsichtig: %v", x, y, c)
				}
				key = fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
			}
			l, ok := letters[key]
			if !ok {
				t.Fatalf("Pixel %d,%d: Farbe %s nicht im Entwurf", x, y, key)
			}
			b.WriteByte(l)
		}
		out[y] = b.String()
	}
	return out
}
