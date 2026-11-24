package symbol

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

// AppICO ist das Programmsymbol ohne Zustandspunkt in allen AppSizes.
func AppICO() []byte {
	imgs := make([]*image.NRGBA, len(AppSizes))
	for i, n := range AppSizes {
		imgs[i] = Render(n)
	}
	return ICO(imgs...)
}

// BadgeICO ist das Tray-Symbol: 16 und 32 Pixel mit Zustandspunkt in c.
func BadgeICO(c color.Color) []byte {
	return ICO(RenderBadge(16, c), RenderBadge(32, c))
}

// PNG kodiert ein Bild als PNG.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic("symbol: PNG: " + err.Error()) // schreibt nur in den Speicher
	}
	return b.Bytes()
}

// ICO baut eine ICO-Datei mit je einem Eintrag pro Bild (quadratisch):
// bis 255 Pixel als 32-Bit-Bitmap mit Alpha, ab 256 Pixel als PNG.
func ICO(imgs ...*image.NRGBA) []byte {
	data := make([][]byte, len(imgs))
	for i, img := range imgs {
		if img.Rect.Dx() >= 256 {
			data[i] = PNG(img)
		} else {
			data[i] = icoBitmap(img)
		}
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(imgs))})
	offset := 6 + 16*len(imgs)
	for i, img := range imgs {
		s := img.Rect.Dx()
		dim := byte(s)
		if s >= 256 {
			dim = 0
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, uint16(1))  // Ebenen
		binary.Write(&out, binary.LittleEndian, uint16(32)) // Bit pro Pixel
		binary.Write(&out, binary.LittleEndian, uint32(len(data[i])))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(data[i])
	}
	for _, d := range data {
		out.Write(d)
	}
	return out.Bytes()
}

// icoBitmap: BITMAPINFOHEADER, BGRA-Pixel von unten nach oben, leere
// AND-Maske (die Transparenz steckt im Alpha).
func icoBitmap(img *image.NRGBA) []byte {
	s := img.Rect.Dx()
	var b bytes.Buffer
	maskRow := ((s + 31) / 32) * 4
	hdr := struct {
		Size                   uint32
		Width, Height          int32
		Planes, BitCount       uint16
		Compression, SizeImage uint32
		XPels, YPels           int32
		ClrUsed, ClrImportant  uint32
	}{40, int32(s), int32(2 * s), 1, 32, 0, uint32(s*s*4 + maskRow*s), 0, 0, 0, 0}
	binary.Write(&b, binary.LittleEndian, hdr)
	for y := s - 1; y >= 0; y-- {
		for x := 0; x < s; x++ {
			c := img.NRGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, maskRow*s))
	return b.Bytes()
}
