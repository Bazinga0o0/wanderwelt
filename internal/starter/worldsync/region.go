package worldsync

// Speicher pro Chunk: Minecraft schreibt eine Regionsdatei (*.mca, bis zu
// 1024 Chunks) ganz neu, sobald sich ein Chunk ändert. Zerlegt in Segmente –
// Kopf, je Chunk sein belegter Sektorbereich, die Lücken dazwischen und ein
// Rest am Ende – ist jedes Segment ein eigener Blob. Ein unveränderter Chunk
// ergibt so denselben Blob, auch wenn Minecraft ihn in der Datei verschiebt.
//
// Format: Sektoren zu 4096 Bytes. Der Kopf (2 Sektoren) enthält 1024
// Positionen zu 4 Bytes (big-endian: 3 Bytes erster Sektor, 1 Byte Anzahl
// Sektoren; 0 heißt kein Chunk) und danach 1024 Zeitstempel.

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

const (
	sectorSize    = 4096
	headerSectors = 2
	headerSize    = headerSectors * sectorSize
	regionChunks  = 1024
	// maxSplitSize: Größere Regionsdateien werden nicht zerlegt; zum
	// Zerlegen liegt die Datei ganz im Speicher.
	maxSplitSize = 256 << 20
)

// Segment ist ein Stück einer Datei, das als eigener Blob abgelegt wird.
type Segment = api.Segment

// isRegion meldet, ob rel (Pfad mit "/") eine Regionsdatei ist.
func isRegion(rel string) bool {
	return strings.HasSuffix(rel, ".mca")
}

// SplitRegion zerlegt die Regionsdatei data in Segmente, in Dateireihenfolge:
// den Kopf (8192 Bytes), dann abwechselnd Lücken und belegte
// Chunk-Sektorbereiche (erster Sektor·4096 bis (erster Sektor+Anzahl)·4096)
// und am Ende gegebenenfalls einen Rest. Leere Lücken gibt es nicht. parts
// sind die Bytes der Segmente (Teile von data, nicht kopiert); segs ihre
// Größen und SHA-256.
//
// ok ist false, wenn data keine lesbare Regionsdatei ist: Größe kein
// Vielfaches von 4096 oder kleiner als der Kopf, eine Position zeigt in den
// Kopf oder über das Dateiende hinaus, belegt 0 Sektoren oder überlappt eine
// andere. Der Aufrufer nimmt dann die ganze Datei als einen Blob.
func SplitRegion(data []byte) (segs []Segment, parts [][]byte, ok bool) {
	if len(data) < headerSize || len(data)%sectorSize != 0 {
		return nil, nil, false
	}
	sectors := len(data) / sectorSize

	type span struct{ from, to int } // Sektoren, to ausschließlich
	used := make([]span, 0, regionChunks)
	for i := range regionChunks {
		loc := binary.BigEndian.Uint32(data[4*i:])
		if loc == 0 {
			continue
		}
		from, count := int(loc>>8), int(loc&0xff)
		// Ein Bereich im Kopf fällt unten als Überlappung mit dem Kopf auf.
		if count == 0 || from+count > sectors {
			return nil, nil, false
		}
		used = append(used, span{from, from + count})
	}
	slices.SortFunc(used, func(a, b span) int { return cmp.Compare(a.from, b.from) })

	// Schnitte in Sektoren: Kopf, dann je Chunk Lücke davor und Bereich.
	cuts := []int{0, headerSectors}
	pos := headerSectors
	for _, u := range used {
		if u.from < pos {
			return nil, nil, false // überlappt den Kopf oder den vorigen Bereich
		}
		if u.from > pos {
			cuts = append(cuts, u.from)
		}
		cuts = append(cuts, u.to)
		pos = u.to
	}
	if pos < sectors {
		cuts = append(cuts, sectors)
	}

	segs = make([]Segment, 0, len(cuts)-1)
	parts = make([][]byte, 0, len(cuts)-1)
	for i := 1; i < len(cuts); i++ {
		part := data[cuts[i-1]*sectorSize : cuts[i]*sectorSize : cuts[i]*sectorSize]
		sum := sha256.Sum256(part)
		segs = append(segs, Segment{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(part))})
		parts = append(parts, part)
	}
	return segs, parts, true
}

// JoinSegments setzt die Segmente parts wieder zur Datei zusammen; mit den
// Teilen aus SplitRegion ist das Ergebnis bitgleich zum Original.
func JoinSegments(parts [][]byte) []byte {
	return bytes.Join(parts, nil)
}
