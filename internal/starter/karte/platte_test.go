package karte

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func inhalt(n int, fuell byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = fuell + byte(i%7)
	}
	return b
}

func TestPlatteSchreibenLesen(t *testing.T) {
	p, err := NeuePlatte(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	d := testPNG(t, 1)
	h := HashVon(d)
	if _, ok := p.Lesen(h); ok {
		t.Fatal("Lesen vor Schreiben")
	}
	if err := p.Schreiben(h, d); err != nil {
		t.Fatal(err)
	}
	got, ok := p.Lesen(h)
	if !ok || string(got) != string(d) {
		t.Fatal("Lesen nach Schreiben liefert nicht den Inhalt")
	}
	if p.Groesse() != int64(len(d)) {
		t.Errorf("Groesse = %d, want %d", p.Groesse(), len(d))
	}
	// Neu geöffnet: Datei und Größe sind da.
	p2, err := NeuePlatte(p.dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p2.Lesen(h); !ok || p2.Groesse() != int64(len(d)) {
		t.Error("neu geöffneter Zwischenspeicher kennt die Datei nicht")
	}
}

func TestPlatteSchreibenPrueftHash(t *testing.T) {
	dir := t.TempDir()
	p, _ := NeuePlatte(dir, 0)
	d := testPNG(t, 2)
	falsch := HashVon(testPNG(t, 3))
	if err := p.Schreiben(falsch, d); err == nil {
		t.Fatal("Inhalt mit falschem Hash wurde angenommen")
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("Datei trotz Fehler angelegt: %v", es)
	}
}

func TestPlatteLesenVerwirftKaputteDatei(t *testing.T) {
	dir := t.TempDir()
	p, _ := NeuePlatte(dir, 0)
	d := testPNG(t, 4)
	h := HashVon(d)
	if err := p.Schreiben(h, d); err != nil {
		t.Fatal(err)
	}
	// Ein Byte kippen: Der Inhalt passt nicht mehr zum Hash.
	pfad := filepath.Join(dir, h+".png")
	kaputt := append([]byte(nil), d...)
	kaputt[len(kaputt)/2] ^= 0xff
	if err := os.WriteFile(pfad, kaputt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Lesen(h); ok {
		t.Fatal("kaputte Datei wurde geliefert")
	}
	if _, err := os.Stat(pfad); !os.IsNotExist(err) {
		t.Error("kaputte Datei blieb liegen")
	}
	if p.Groesse() != 0 {
		t.Errorf("Groesse nach Verwerfen = %d", p.Groesse())
	}
	// Abgeschnittene Datei (halb geschrieben) ebenso.
	if err := os.WriteFile(pfad, d[:len(d)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Lesen(h); ok {
		t.Fatal("abgeschnittene Datei wurde geliefert")
	}
}

func TestPlatteGrenzeAeltesteZuerst(t *testing.T) {
	dir := t.TempDir()
	p, _ := NeuePlatte(dir, 1000)
	jetzt := time.Unix(1_000_000, 0)
	p.jetzt = func() time.Time { jetzt = jetzt.Add(time.Second); return jetzt }
	var hashes []string
	for i := range 20 { // 20 × 100 Bytes = 2000 > 1000
		d := inhalt(100, byte(i))
		h := HashVon(d)
		hashes = append(hashes, h)
		if err := p.Schreiben(h, d); err != nil {
			t.Fatal(err)
		}
		if i == 4 {
			p.Lesen(hashes[0]) // die erste wird benutzt und gilt wieder als jung
		}
		if g := p.Groesse(); g > 1000 {
			t.Fatalf("nach %d Dateien: %d Bytes über der Grenze 1000", i+1, g)
		}
	}
	// Auf der Platte stimmt es auch.
	var summe int64
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		fi, _ := e.Info()
		summe += fi.Size()
	}
	if summe > 1000 || summe != p.Groesse() {
		t.Errorf("Platte hat %d Bytes, Index %d", summe, p.Groesse())
	}
	// Die jüngste ist noch da, eine der ältesten nicht.
	if _, ok := p.Lesen(hashes[19]); !ok {
		t.Error("jüngste Datei fehlt")
	}
	if _, ok := p.Lesen(hashes[1]); ok {
		t.Error("alte Datei ist noch da")
	}
}

func TestPlatteRaeumtBeimOeffnenAuf(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		d := inhalt(100, byte(i))
		os.WriteFile(filepath.Join(dir, HashVon(d)+".png"), d, 0o600)
	}
	os.WriteFile(filepath.Join(dir, HashVon([]byte("x"))+".abc123.tmp"), []byte("halb"), 0o600)
	os.WriteFile(filepath.Join(dir, "fremd.txt"), []byte("lass liegen"), 0o600)
	p, err := NeuePlatte(dir, 500)
	if err != nil {
		t.Fatal(err)
	}
	if p.Groesse() > 500 {
		t.Errorf("Groesse = %d", p.Groesse())
	}
	for _, e := range mustReadDir(t, dir) {
		if strings.HasSuffix(e, ".tmp") {
			t.Errorf("Rest %s nicht entfernt", e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "fremd.txt")); err != nil {
		t.Error("fremde Datei wurde angefasst")
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	return n
}

// Kein Pfadtrick: Hash und Namen werden nie zu einem Pfad außerhalb.
func TestPlatteKeinPfadtrick(t *testing.T) {
	basis := t.TempDir()
	dir := filepath.Join(basis, "cache")
	p, err := NeuePlatte(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"../evil", `..\evil`, "../../" + strings.Repeat("a", 64), "a/b", "", "evil.png", strings.Repeat("a", 63) + "/"} {
		if err := p.Schreiben(h, []byte("x")); err == nil {
			t.Errorf("Schreiben(%q) war erfolgreich", h)
		}
		if _, ok := p.Lesen(h); ok {
			t.Errorf("Lesen(%q) war erfolgreich", h)
		}
		p.Verwerfen(h) // darf nichts außerhalb löschen
	}
	if es := mustReadDir(t, basis); len(es) != 1 || es[0] != "cache" {
		t.Errorf("Eintrag außerhalb des Zwischenspeichers: %v", es)
	}
	if es := mustReadDir(t, dir); len(es) != 0 {
		t.Errorf("Dateien im Zwischenspeicher: %v", es)
	}
}

func TestSichererNameUndOrdner(t *testing.T) {
	basis := filepath.Join(t.TempDir(), "home")
	for _, c := range []struct{ in, want string }{
		{"w1", "w1"},
		{"wanderwelt.example.de", "wanderwelt.example.de"},
		{"..", "_"},
		{".", "_"},
		{"../..", "_.._"},
		{"", "_"},
		{"a/b", "a_b"},
		{`a\b`, "a_b"},
		{"C:", "C_"},
		{"nul", "_nul"},
		{"CON.txt", "_CON.txt"},
		{"com1", "_com1"},
		{"ende.", "ende"},
		{"Wélt", "W_lt"},
	} {
		got := SichererName(c.in)
		if got != c.want {
			t.Errorf("SichererName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if n := SichererName(strings.Repeat("x", 500)); len(n) > 80 {
		t.Errorf("Name nicht gekürzt: %d", len(n))
	}
	// Welche Namen auch kommen: Der Ordner liegt immer unter basis/karte-cache/<g>/<w>.
	for _, g := range []string{"../../x", `..\..\x`, "/abs", "C:\\Windows", "a/../../b", "..", ""} {
		for _, w := range []string{"../w1", "w1/../../..", "..", `..\..`, "NUL"} {
			dir := OrdnerFuer(basis, g, w)
			rel, err := filepath.Rel(filepath.Join(basis, "karte-cache"), dir)
			if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) || strings.Count(filepath.ToSlash(rel), "/") != 1 {
				t.Errorf("OrdnerFuer(%q, %q) = %q (rel %q)", g, w, dir, rel)
			}
		}
	}
}
