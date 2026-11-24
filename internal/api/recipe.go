package api

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
)

// Ein Rezept listet die Segmente einer zerlegten Datei (FileEntry.Recipe)
// und liegt selbst als Blob vor. Format, kanonisch: "WWR1", uint32
// big-endian Anzahl, dann je Segment 32 Byte SHA-256 (roh) und uint64
// big-endian Größe. Dieselbe Liste ergibt dieselben Bytes und damit
// denselben Blob, so teilen sich Generationen unveränderte Rezepte.
const (
	RecipeMagic      = "WWR1"
	MaxRecipeEntries = 4096
	recipeHeaderSize = 8
	recipeEntrySize  = 32 + 8
	// MaxRecipeSize ist die Größe eines Rezepts mit MaxRecipeEntries Einträgen.
	MaxRecipeSize = recipeHeaderSize + MaxRecipeEntries*recipeEntrySize
)

// Grenzen der gebündelten Übertragung (POST /v1/blobs/fetch und
// /v1/blobs/batch), gemeinsam für Koordinator und Starter.
const (
	// FetchMaxBlobs: so viele Hashes darf eine Fetch-Anfrage nennen.
	FetchMaxBlobs = 4096
	// BatchMaxFrames: so viele Rahmen darf eine Batch-Anfrage enthalten.
	BatchMaxFrames = 4096
	// TransferMaxBytes: so viele Bytes liefert eine Fetch-Antwort bzw.
	// umfasst eine Batch-Anfrage (samt Rahmenköpfen) höchstens.
	TransferMaxBytes = 32 << 20
	// BatchFrameHeader: Kopf eines Batch-Rahmens (32 Byte SHA-256 roh,
	// uint64 big-endian Größe), danach die Daten.
	BatchFrameHeader = 32 + 8
)

var errNoRecipe = errors.New("kein Rezept")

// EncodeRecipe kodiert segs. Fehler: keine oder mehr als MaxRecipeEntries
// Segmente, ein Hash, der kein SHA-256 in Kleinbuchstaben ist, eine negative
// Größe.
func EncodeRecipe(segs []Segment) ([]byte, error) {
	if len(segs) == 0 || len(segs) > MaxRecipeEntries {
		return nil, fmt.Errorf("Rezept mit %d Segmenten (erlaubt 1–%d)", len(segs), MaxRecipeEntries)
	}
	out := make([]byte, 0, recipeHeaderSize+len(segs)*recipeEntrySize)
	out = append(out, RecipeMagic...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(segs)))
	for _, s := range segs {
		if !validHash(s.SHA256) {
			return nil, fmt.Errorf("Rezept: ungültiger Hash %q", s.SHA256)
		}
		if s.Size < 0 {
			return nil, fmt.Errorf("Rezept: negative Größe %d", s.Size)
		}
		out, _ = hex.AppendDecode(out, []byte(s.SHA256))
		out = binary.BigEndian.AppendUint64(out, uint64(s.Size))
	}
	return out, nil
}

// RecipeLen liefert aus den ersten 8 Bytes eines Rezepts (Magie und Anzahl)
// seine Gesamtlänge in Bytes.
func RecipeLen(header []byte) (int, error) {
	if len(header) < recipeHeaderSize || string(header[:4]) != RecipeMagic {
		return 0, fmt.Errorf("%w: Kopf %q", errNoRecipe, header)
	}
	n := binary.BigEndian.Uint32(header[4:8])
	if n == 0 || n > MaxRecipeEntries {
		return 0, fmt.Errorf("Rezept mit %d Segmenten (erlaubt 1–%d)", n, MaxRecipeEntries)
	}
	return recipeHeaderSize + int(n)*recipeEntrySize, nil
}

// ParseRecipe liest ein ganzes Rezept. Abgeschnittene Einträge, Daten nach
// dem letzten Eintrag und Größen über math.MaxInt64 sind Fehler.
func ParseRecipe(data []byte) ([]Segment, error) {
	n, err := RecipeLen(data)
	if err != nil {
		return nil, err
	}
	if len(data) != n {
		return nil, fmt.Errorf("Rezept hat %d Bytes, laut Anzahl %d", len(data), n)
	}
	segs := make([]Segment, 0, (n-recipeHeaderSize)/recipeEntrySize)
	err = WalkRecipe(bytes.NewReader(data), func(s Segment) error {
		segs = append(segs, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return segs, nil
}

// WalkRecipe liest ein Rezept aus r Eintrag für Eintrag, ohne es ganz zu
// laden, und ruft visit für jedes Segment; Fehler wie bei ParseRecipe.
// Bricht visit mit einem Fehler ab, ist das das Ergebnis.
func WalkRecipe(r io.Reader, visit func(Segment) error) error {
	br := bufio.NewReader(r)
	var head [recipeHeaderSize]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return fmt.Errorf("%w: Kopf nicht lesbar: %v", errNoRecipe, err)
	}
	n, err := RecipeLen(head[:])
	if err != nil {
		return err
	}
	var e [recipeEntrySize]byte
	for range (n - recipeHeaderSize) / recipeEntrySize {
		if _, err := io.ReadFull(br, e[:]); err != nil {
			return fmt.Errorf("Rezept abgeschnitten: %v", err)
		}
		size := binary.BigEndian.Uint64(e[32:])
		if size > math.MaxInt64 {
			return fmt.Errorf("Rezept: Größe %d zu groß (negativ als int64)", size)
		}
		if err := visit(Segment{SHA256: hex.EncodeToString(e[:32]), Size: int64(size)}); err != nil {
			return err
		}
	}
	if _, err := br.ReadByte(); err != io.EOF {
		if err == nil {
			return errors.New("Rezept: Daten nach dem letzten Eintrag")
		}
		return err
	}
	return nil
}

// RecipeSize ist die Summe der Segmentgrößen; ok ist false bei einer
// negativen Größe oder wenn die Summe über math.MaxInt64 hinausginge.
func RecipeSize(segs []Segment) (total int64, ok bool) {
	for _, s := range segs {
		if s.Size < 0 || s.Size > math.MaxInt64-total {
			return 0, false
		}
		total += s.Size
	}
	return total, true
}

// validHash: genau 64 Hex-Zeichen in Kleinbuchstaben.
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
