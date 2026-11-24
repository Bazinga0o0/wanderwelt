package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"slices"
	"strings"
	"testing"
)

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// recipeBytes baut ein Rezept von Hand: Magie, Anzahl, Einträge.
func recipeBytes(magic string, count uint32, entries ...[]byte) []byte {
	b := []byte(magic)
	b = binary.BigEndian.AppendUint32(b, count)
	for _, e := range entries {
		b = append(b, e...)
	}
	return b
}

func entry(sha string, size uint64) []byte {
	raw, _ := hex.DecodeString(sha)
	return binary.BigEndian.AppendUint64(raw, size)
}

func TestRecipeRoundTrip(t *testing.T) {
	segs := []Segment{
		{SHA256: hashOf("Kopf"), Size: 8192},
		{SHA256: hashOf("Chunk"), Size: 4096},
		{SHA256: hashOf("Kopf"), Size: 8192}, // doppelt ist erlaubt
		{SHA256: hashOf("groß"), Size: 1 << 40},
	}
	data, err := EncodeRecipe(segs)
	if err != nil {
		t.Fatalf("EncodeRecipe: %v", err)
	}
	// "WWR1" + uint32 BE Anzahl + je 32 Byte Hash und uint64 BE Größe.
	want := recipeBytes("WWR1", 4,
		entry(segs[0].SHA256, 8192), entry(segs[1].SHA256, 4096), entry(segs[2].SHA256, 8192), entry(segs[3].SHA256, 1<<40))
	if !bytes.Equal(data, want) || len(data) != 8+4*40 {
		t.Fatalf("Kodierung %x, erwartet %x", data, want)
	}
	if again, _ := EncodeRecipe(slices.Clone(segs)); !bytes.Equal(again, data) {
		t.Fatal("gleiche Liste ergibt andere Bytes")
	}
	if n, err := RecipeLen(data[:8]); err != nil || n != len(data) {
		t.Fatalf("RecipeLen = %d, %v; erwartet %d", n, err, len(data))
	}
	got, err := ParseRecipe(data)
	if err != nil || !slices.Equal(got, segs) {
		t.Fatalf("ParseRecipe = %+v, %v", got, err)
	}
	var walked []Segment
	if err := WalkRecipe(bytes.NewReader(data), func(s Segment) error {
		walked = append(walked, s)
		return nil
	}); err != nil || !slices.Equal(walked, segs) {
		t.Fatalf("WalkRecipe = %+v, %v", walked, err)
	}
	if total, ok := RecipeSize(segs); !ok || total != 2*8192+4096+1<<40 {
		t.Fatalf("RecipeSize = %d, %v", total, ok)
	}

	// Höchstens 4096 Einträge: genau 4096 geht.
	many := make([]Segment, 4096)
	for i := range many {
		many[i] = Segment{SHA256: hashOf("x"), Size: 1}
	}
	if data, err := EncodeRecipe(many); err != nil || len(data) != 8+4096*40 {
		t.Fatalf("4096 Einträge: %d Bytes, %v", len(data), err)
	}
	if MaxRecipeSize != 163848 {
		t.Fatalf("MaxRecipeSize = %d, erwartet 163848 (8 + 4096·40)", MaxRecipeSize)
	}
}

func TestRecipeRejectsInvalid(t *testing.T) {
	a, b := hashOf("a"), hashOf("b")
	cases := []struct {
		name string
		data []byte
	}{
		{"leer", nil},
		{"nur halber Kopf", []byte("WWR1\x00\x00")},
		{"falsche Magie", recipeBytes("WWR2", 1, entry(a, 1))},
		{"Magie klein", recipeBytes("wwr1", 1, entry(a, 1))},
		{"Anzahl 0", recipeBytes("WWR1", 0)},
		{"Anzahl 4097", recipeBytes("WWR1", 4097, slices.Repeat([][]byte{entry(a, 1)}, 4097)...)},
		{"Anzahl größer als Einträge", recipeBytes("WWR1", 3, entry(a, 1), entry(b, 2))},
		{"Anzahl kleiner als Einträge", recipeBytes("WWR1", 1, entry(a, 1), entry(b, 2))},
		{"Eintrag abgeschnitten", recipeBytes("WWR1", 1, entry(a, 1)[:39])},
		{"Größe negativ", recipeBytes("WWR1", 1, entry(a, 1<<63))},
		{"Größe uint64 max", recipeBytes("WWR1", 2, entry(a, 1), entry(b, math.MaxUint64))},
	}
	for _, c := range cases {
		if segs, err := ParseRecipe(c.data); err == nil {
			t.Errorf("ParseRecipe %s: angenommen: %+v", c.name, segs)
		}
		if err := WalkRecipe(bytes.NewReader(c.data), func(Segment) error { return nil }); err == nil {
			t.Errorf("WalkRecipe %s: angenommen", c.name)
		}
	}
	for _, h := range []string{"WWR2\x00\x00\x00\x01", "WWR1\x00\x00\x00\x00", "WWR1\x00\x00\x10\x01"} {
		if n, err := RecipeLen([]byte(h)); err == nil {
			t.Errorf("RecipeLen(%q) = %d ohne Fehler", h, n)
		}
	}

	// Kodieren lehnt ab, was Parsen ablehnen würde.
	for name, segs := range map[string][]Segment{
		"keine Segmente": nil,
		"4097 Segmente":  slices.Repeat([]Segment{{SHA256: a, Size: 1}}, 4097),
		"Hash groß":      {{SHA256: strings.ToUpper(a), Size: 1}},
		"Hash zu kurz":   {{SHA256: a[:62], Size: 1}},
		"negative Größe": {{SHA256: a, Size: -1}},
	} {
		if data, err := EncodeRecipe(segs); err == nil {
			t.Errorf("EncodeRecipe %s: %x ohne Fehler", name, data)
		}
	}

	// Überlaufsichere Summe.
	if total, ok := RecipeSize([]Segment{{SHA256: a, Size: math.MaxInt64}, {SHA256: b, Size: 1}}); ok {
		t.Errorf("RecipeSize mit Überlauf = %d, ok", total)
	}
	if total, ok := RecipeSize([]Segment{{SHA256: a, Size: math.MaxInt64 - 1}, {SHA256: b, Size: 1}}); !ok || total != math.MaxInt64 {
		t.Errorf("RecipeSize bis genau MaxInt64 = %d, %v", total, ok)
	}
}
