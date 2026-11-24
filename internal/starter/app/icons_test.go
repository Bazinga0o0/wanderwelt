package app

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"image/png"
	"testing"
)

// icoEntry ist ein Eintrag einer ICO-Datei samt Bilddaten.
type icoEntry struct {
	size int
	data []byte
}

func parseICO(t *testing.T, b []byte) []icoEntry {
	t.Helper()
	if len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		t.Fatalf("kein ICO-Kopf: % x", b[:min(len(b), 6)])
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	var out []icoEntry
	for i := 0; i < n; i++ {
		e := b[6+16*i:]
		size := int(e[0])
		if size == 0 {
			size = 256
		}
		if int(e[1]) != int(e[0]) || binary.LittleEndian.Uint16(e[6:]) != 32 {
			t.Fatalf("Eintrag %d: %d×%d, %d Bit", i, e[0], e[1], binary.LittleEndian.Uint16(e[6:]))
		}
		l, off := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		if int(off+l) > len(b) {
			t.Fatalf("Eintrag %d reicht über das Ende hinaus", i)
		}
		out = append(out, icoEntry{size, b[off : off+l]})
	}
	return out
}

// bmpPixel liest einen Pixel aus einem 32-Bit-Bitmap-Eintrag (Zeilen von
// unten nach oben, BGRA).
func bmpPixel(t *testing.T, e icoEntry, x, y int) color.NRGBA {
	t.Helper()
	if binary.LittleEndian.Uint32(e.data) != 40 ||
		int(int32(binary.LittleEndian.Uint32(e.data[4:]))) != e.size ||
		int(int32(binary.LittleEndian.Uint32(e.data[8:]))) != 2*e.size {
		t.Fatalf("%d px: kein passender BITMAPINFOHEADER", e.size)
	}
	p := e.data[40+((e.size-1-y)*e.size+x)*4:]
	return color.NRGBA{p[2], p[1], p[0], p[3]}
}

// TestPhaseIconHasBadge: Jedes Tray-Symbol hat 16 und 32 Pixel, in der
// Mitte des Punkts die Farbe des Zustands, darum den dunklen Ring, und
// die Ecke bleibt durchsichtig.
func TestPhaseIconHasBadge(t *testing.T) {
	ring := color.NRGBA{0x24, 0x17, 0x0f, 0xff}
	// Mitte des Punkts (N·0,8) und ein Pixel im Ring darüber.
	pos := map[int][4]int{16: {12, 12, 12, 9}, 32: {25, 25, 25, 18}}
	for p, c := range phaseColors {
		want := color.NRGBA{c.R, c.G, c.B, 0xff}
		entries := parseICO(t, phaseIcon(p))
		if len(entries) != 2 || entries[0].size != 16 || entries[1].size != 32 {
			t.Fatalf("%v: Einträge %v, erwartet 16 und 32", p, sizes(entries))
		}
		for _, e := range entries {
			at := pos[e.size]
			if got := bmpPixel(t, e, at[0], at[1]); got != want {
				t.Errorf("%v, %d px: Punkt %v, erwartet %v", p, e.size, got, want)
			}
			if got := bmpPixel(t, e, at[2], at[3]); got != ring {
				t.Errorf("%v, %d px: Ring %v, erwartet %v", p, e.size, got, ring)
			}
			if got := bmpPixel(t, e, 0, 0); got.A != 0 {
				t.Errorf("%v, %d px: Ecke nicht durchsichtig: %v", p, e.size, got)
			}
		}
	}
	if !bytes.Equal(phaseIcon(phase(-1)), phaseIcon(phReady)) {
		t.Error("unbekannter Zustand: nicht das Symbol für „bereit“")
	}
}

// TestAppIconSizes: Das Programmsymbol hat 16, 32, 48, 64 und 256 Pixel
// (256 als PNG) und keinen Zustandspunkt.
func TestAppIconSizes(t *testing.T) {
	entries := parseICO(t, appIconICO())
	got := sizes(entries)
	want := []int{16, 32, 48, 64, 256}
	if len(got) != len(want) {
		t.Fatalf("Größen %v, erwartet %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Größen %v, erwartet %v", got, want)
		}
	}
	for _, e := range entries {
		if e.size == 256 {
			img, err := png.Decode(bytes.NewReader(e.data))
			if err != nil {
				t.Fatalf("256 px: %v", err)
			}
			if b := img.Bounds(); b.Dx() != 256 || b.Dy() != 256 {
				t.Fatalf("256 px: PNG ist %v", b)
			}
			continue
		}
		// Wo im Tray der Punkt sitzt, ist hier der Block (keine Zustandsfarbe).
		px := bmpPixel(t, e, e.size*4/5, e.size*4/5)
		for p, c := range phaseColors {
			if px == (color.NRGBA{c.R, c.G, c.B, 0xff}) {
				t.Errorf("%d px: Pixel am Punkt hat die Farbe von %v", e.size, p)
			}
		}
		if px.A != 0xff {
			t.Errorf("%d px: Pixel am Punkt %v, erwartet den Block", e.size, px)
		}
	}
}

func sizes(es []icoEntry) []int {
	var out []int
	for _, e := range es {
		out = append(out, e.size)
	}
	return out
}
