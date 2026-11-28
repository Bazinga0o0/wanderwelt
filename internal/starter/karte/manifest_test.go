package karte

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var hash64 = strings.Repeat("ab", 32)

func rohJSON(t *testing.T, aender func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"format": 1, "dimension": "overworld", "tile_size": 256, "levels": []int{0, 1},
		"source": "s", "generated": "2026-11-27T20:00:00Z",
		"bounds":  map[string]any{"0": map[string]int{"x_min": -9, "x_max": 8, "y_min": -4, "y_max": 12}},
		"tiles":   map[string]any{"0/-9/3": map[string]any{"sha256": hash64, "size": 10}},
		"zukunft": "unbekanntes Feld",
	}
	if aender != nil {
		aender(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLeseManifest(t *testing.T) {
	m, err := LeseManifest(rohJSON(t, func(m map[string]any) {
		m["tiles"] = map[string]any{
			"0/-9/3":  map[string]any{"sha256": hash64, "size": 10},
			"0/8/-4":  map[string]any{"sha256": hash64, "size": 10},
			"1/-5/12": map[string]any{"sha256": hash64, "size": 10},
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if m.Anzahl() != 3 || m.Source != "s" || !m.Generated.Equal(time.Date(2026, 11, 27, 20, 0, 0, 0, time.UTC)) {
		t.Errorf("Manifest = %+v", m)
	}
	if h, ok := m.Hash(Kachel{0, -9, 3}); !ok || h != hash64 {
		t.Errorf("Hash = %q, %v", h, ok)
	}
	if _, ok := m.Hash(Kachel{0, 0, 0}); ok {
		t.Error("Kachel 0/0/0 darf nicht vorhanden sein")
	}
	// Grenzen aus den Kacheln: Stufe 0 reicht von x −9…8, y −4…3.
	g := m.Grenzen(0)
	if g == nil || *g != (Bereich{XMin: -9, XMax: 8, YMin: -4, YMax: 3}) {
		t.Errorf("Grenzen(0) = %+v", g)
	}
	if m.Grenzen(2) != nil || m.Grenzen(-1) != nil || m.Grenzen(9) != nil {
		t.Error("Grenzen außerhalb müssen nil sein")
	}
	// Mitte von Stufe 0: x (−9+8+1)/2 = 0 Kacheln → 0 Blöcke; y (−4+3+1)/2 = 0.
	bx, bz, ok := m.Mitte()
	if !ok || bx != 0 || bz != 0 {
		t.Errorf("Mitte = %v, %v, %v", bx, bz, ok)
	}
	x0, z0, x1, z1, _ := m.Ausdehnung()
	if x0 != -9*256 || z0 != -4*256 || x1 != 9*256 || z1 != 4*256 {
		t.Errorf("Ausdehnung = %v %v %v %v", x0, z0, x1, z1)
	}
}

func TestMitteNegativ(t *testing.T) {
	m, err := LeseManifest(rohJSON(t, func(m map[string]any) {
		m["tiles"] = map[string]any{
			"0/-3/-2": map[string]any{"sha256": hash64, "size": 1},
			"0/-2/-1": map[string]any{"sha256": hash64, "size": 1},
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	bx, bz, _ := m.Mitte()
	// Kacheln x −3…−2 → Blöcke −768…−256 → Mitte −512; y −2…−1 → −512…0 → −256.
	if bx != -512 || bz != -256 {
		t.Errorf("Mitte = %v, %v", bx, bz)
	}
}

func TestManifestUngueltig(t *testing.T) {
	kachel := func(k string, e map[string]any) func(m map[string]any) {
		return func(m map[string]any) { m["tiles"] = map[string]any{k: e} }
	}
	gut := map[string]any{"sha256": hash64, "size": 10}
	for name, aender := range map[string]func(m map[string]any){
		"Format 2":                  func(m map[string]any) { m["format"] = 2 },
		"Format fehlt":              func(m map[string]any) { delete(m, "format") },
		"Kachelgröße":               func(m map[string]any) { m["tile_size"] = 512 },
		"Stufe 7":                   func(m map[string]any) { m["levels"] = []int{0, 7} },
		"Stufe negativ":             func(m map[string]any) { m["levels"] = []int{-1} },
		"keine Kacheln":             func(m map[string]any) { m["tiles"] = map[string]any{} },
		"Stufe nicht genannt":       kachel("2/0/0", gut),
		"Schlüssel zu kurz":         kachel("0/0", gut),
		"Schlüssel zu lang":         kachel("0/0/0/0", gut),
		"führende Null":             kachel("0/01/2", gut),
		"Plus":                      kachel("0/+1/2", gut),
		"Minus null":                kachel("0/-0/2", gut),
		"Pfadtrick":                 kachel("../../x/y", gut),
		"Schlüssel mit Leerzeichen": kachel("0/ 1/2", gut),
		"Stufe 7 im Schlüssel":      kachel("7/0/0", gut),
		"Index riesig":              kachel("0/99999999/0", gut),
		"Hash zu kurz":              kachel("0/0/0", map[string]any{"sha256": "abc", "size": 1}),
		"Hash groß geschrieben":     kachel("0/0/0", map[string]any{"sha256": strings.ToUpper(hash64), "size": 1}),
		"Hash mit Pfad":             kachel("0/0/0", map[string]any{"sha256": "../" + hash64[:61], "size": 1}),
		"Hash leer":                 kachel("0/0/0", map[string]any{"size": 1}),
		"Größe negativ":             kachel("0/0/0", map[string]any{"sha256": hash64, "size": -1}),
		"Größe riesig":              kachel("0/0/0", map[string]any{"sha256": hash64, "size": MaxKachelBytes + 1}),
		"Zeit kaputt":               func(m map[string]any) { m["generated"] = "gestern" },
	} {
		_, err := LeseManifest(rohJSON(t, aender))
		if !errors.Is(err, ErrManifestUngueltig) {
			t.Errorf("%s: err = %v, want ErrManifestUngueltig", name, err)
		}
	}
	if _, err := LeseManifest([]byte("kein json")); !errors.Is(err, ErrManifestUngueltig) {
		t.Errorf("kein JSON: %v", err)
	}
	if _, err := LeseManifest(nil); !errors.Is(err, ErrManifestUngueltig) {
		t.Errorf("leer: %v", err)
	}
}

func TestManifestGleichUndAlter(t *testing.T) {
	a := testManifest(t, time.Unix(1000, 0), map[string][]byte{"0/0/0": {1}, "0/1/0": {2}})
	b := testManifest(t, time.Unix(1000, 0), map[string][]byte{"0/0/0": {1}, "0/1/0": {2}})
	c := testManifest(t, time.Unix(1000, 0), map[string][]byte{"0/0/0": {1}, "0/1/0": {3}})
	d := testManifest(t, time.Unix(2000, 0), map[string][]byte{"0/0/0": {1}, "0/1/0": {2}})
	if !a.Gleich(b) || a.Gleich(c) || a.Gleich(d) || a.Gleich(nil) {
		t.Error("Gleich vergleicht falsch")
	}
	var n *Manifest
	if !n.Gleich(nil) {
		t.Error("nil == nil")
	}
	if alt, ok := a.Alter(time.Unix(1000+7*60, 0)); !ok || alt != 7*time.Minute {
		t.Errorf("Alter = %v, %v", alt, ok)
	}
	if alt, _ := a.Alter(time.Unix(0, 0)); alt != 0 {
		t.Errorf("Alter vor der Erzeugung = %v", alt)
	}
}

func TestAlterText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "gerade eben", 59 * time.Second: "gerade eben", time.Minute: "vor 1 Min.", 7*time.Minute + 30*time.Second: "vor 7 Min.",
		59 * time.Minute: "vor 59 Min.", 60 * time.Minute: "vor 1 Std.", 5 * time.Hour: "vor 5 Std.", 72 * time.Hour: "vor 3 Tagen",
	} {
		if got := AlterText(d); got != want {
			t.Errorf("AlterText(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestGueltigerHash(t *testing.T) {
	for h, want := range map[string]bool{
		hash64: true, strings.Repeat("0", 64): true, strings.Repeat("g", 64): false, strings.ToUpper(hash64): false,
		"": false, hash64 + "a": false, hash64[:63]: false, "../" + hash64[:61]: false, hash64[:63] + "\x00": false,
	} {
		if got := GueltigerHash(h); got != want {
			t.Errorf("GueltigerHash(%q) = %v", h, got)
		}
	}
}
