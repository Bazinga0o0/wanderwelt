package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Ein kleines NBT-Lesen für level.dat (gzip-komprimiert, Big Endian). Es
// liest den ganzen Baum in Go-Werte:
//
//	Byte → int8, Short → int16, Int → int32, Long → int64,
//	Float → float32, Double → float64, String → string,
//	ByteArray → []byte, IntArray → []int32, LongArray → []int64,
//	List → []any, Compound → map[string]any.
//
// Grenzen gegen kaputte oder böswillige Dateien: höchstens maxNBTBytes
// entpackt, Schachtelungstiefe höchstens maxNBTDepth, Längen nie größer als
// der Rest des erlaubten Umfangs.

const (
	maxNBTBytes = 32 << 20
	maxNBTDepth = 512
)

const (
	tagEnd = iota
	tagByte
	tagShort
	tagInt
	tagLong
	tagFloat
	tagDouble
	tagByteArray
	tagString
	tagList
	tagCompound
	tagIntArray
	tagLongArray
)

var errNBTTooLarge = errors.New("NBT: Datei zu groß")

// readNBTFile liest eine NBT-Datei (gzip oder unkomprimiert) und liefert
// die Wurzel.
func readNBTFile(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	root, err := parseNBTStream(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return root, nil
}

// parseNBTStream erkennt gzip am Kopf und liest dann die Wurzel.
func parseNBTStream(r io.Reader) (map[string]any, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(2)
	if err != nil {
		return nil, fmt.Errorf("NBT: %w", io.ErrUnexpectedEOF)
	}
	var src io.Reader = br
	if head[0] == 0x1f && head[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("NBT: gzip: %w", err)
		}
		defer zr.Close()
		src = zr
	}
	return parseNBT(src)
}

// parseNBT liest unkomprimiertes NBT: ein benanntes Compound als Wurzel.
func parseNBT(r io.Reader) (map[string]any, error) {
	d := &nbtDecoder{r: &io.LimitedReader{R: r, N: maxNBTBytes + 1}}
	t, err := d.u8()
	if err != nil {
		return nil, err
	}
	if t != tagCompound {
		return nil, fmt.Errorf("NBT: Wurzel ist kein Compound (Typ %d)", t)
	}
	if _, err := d.str(); err != nil {
		return nil, err
	}
	v, err := d.payload(tagCompound, 0)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

type nbtDecoder struct {
	r   *io.LimitedReader
	buf [8]byte
}

func (d *nbtDecoder) read(n int) ([]byte, error) {
	if int64(n) > d.r.N {
		return nil, errNBTTooLarge
	}
	var b []byte
	if n <= len(d.buf) {
		b = d.buf[:n]
	} else {
		b = make([]byte, n)
	}
	if _, err := io.ReadFull(d.r, b); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		if d.r.N <= 0 {
			return nil, errNBTTooLarge
		}
		return nil, fmt.Errorf("NBT: %w", err)
	}
	return b, nil
}

func (d *nbtDecoder) u8() (byte, error) {
	b, err := d.read(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (d *nbtDecoder) u16() (uint16, error) {
	b, err := d.read(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (d *nbtDecoder) u32() (uint32, error) {
	b, err := d.read(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (d *nbtDecoder) u64() (uint64, error) {
	b, err := d.read(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

// length liest eine Länge (Int) und prüft sie gegen den Rest des Umfangs;
// elem ist die Mindestgröße eines Elements in Bytes.
func (d *nbtDecoder) length(elem int64) (int, error) {
	u, err := d.u32()
	if err != nil {
		return 0, err
	}
	n := int32(u)
	if n < 0 {
		return 0, fmt.Errorf("NBT: negative Länge %d", n)
	}
	if int64(n)*elem > d.r.N {
		return 0, errNBTTooLarge
	}
	return int(n), nil
}

func (d *nbtDecoder) str() (string, error) {
	n, err := d.u16()
	if err != nil {
		return "", err
	}
	b, err := d.read(int(n))
	if err != nil {
		return "", err
	}
	// Modified UTF-8 von Java; für Namen und Versionen reicht UTF-8.
	return string(bytes.Clone(b)), nil
}

func (d *nbtDecoder) payload(t byte, depth int) (any, error) {
	if depth > maxNBTDepth {
		return nil, errors.New("NBT: zu tief verschachtelt")
	}
	switch t {
	case tagByte:
		b, err := d.u8()
		return int8(b), err
	case tagShort:
		v, err := d.u16()
		return int16(v), err
	case tagInt:
		v, err := d.u32()
		return int32(v), err
	case tagLong:
		v, err := d.u64()
		return int64(v), err
	case tagFloat:
		v, err := d.u32()
		return math.Float32frombits(v), err
	case tagDouble:
		v, err := d.u64()
		return math.Float64frombits(v), err
	case tagByteArray:
		n, err := d.length(1)
		if err != nil {
			return nil, err
		}
		b, err := d.read(n)
		if err != nil {
			return nil, err
		}
		return bytes.Clone(b), nil
	case tagString:
		return d.str()
	case tagList:
		et, err := d.u8()
		if err != nil {
			return nil, err
		}
		if et > tagLongArray {
			return nil, fmt.Errorf("NBT: unbekannter Typ %d in Liste", et)
		}
		minSize := int64(1)
		if et == tagEnd {
			minSize = 0
		}
		n, err := d.length(minSize)
		if err != nil {
			return nil, err
		}
		if et == tagEnd && n > 0 {
			return nil, errors.New("NBT: Liste vom Typ End mit Elementen")
		}
		out := make([]any, 0, min(n, 1024))
		for range n {
			v, err := d.payload(et, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case tagCompound:
		m := map[string]any{}
		for {
			ct, err := d.u8()
			if err != nil {
				return nil, err
			}
			if ct == tagEnd {
				return m, nil
			}
			if ct > tagLongArray {
				return nil, fmt.Errorf("NBT: unbekannter Typ %d", ct)
			}
			name, err := d.str()
			if err != nil {
				return nil, err
			}
			v, err := d.payload(ct, depth+1)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
	case tagIntArray:
		n, err := d.length(4)
		if err != nil {
			return nil, err
		}
		b, err := d.read(4 * n)
		if err != nil {
			return nil, err
		}
		out := make([]int32, n)
		for i := range out {
			out[i] = int32(binary.BigEndian.Uint32(b[4*i:]))
		}
		return out, nil
	case tagLongArray:
		n, err := d.length(8)
		if err != nil {
			return nil, err
		}
		b, err := d.read(8 * n)
		if err != nil {
			return nil, err
		}
		out := make([]int64, n)
		for i := range out {
			out[i] = int64(binary.BigEndian.Uint64(b[8*i:]))
		}
		return out, nil
	}
	return nil, fmt.Errorf("NBT: unbekannter Typ %d", t)
}

// nbtGet folgt path durch verschachtelte Compounds.
func nbtGet(root map[string]any, path ...string) (any, bool) {
	var cur any = root
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}
