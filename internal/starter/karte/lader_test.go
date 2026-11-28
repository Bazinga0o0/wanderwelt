package karte

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// attrappeHoler liefert Kacheln aus einer Tabelle; blockiert (wenn gesperrt)
// bis zum Abbruch oder zur Freigabe und zählt, wie viele gleichzeitig laufen.
type attrappeHoler struct {
	mu      sync.Mutex
	daten   map[Kachel][]byte
	abrufe  map[Kachel]int
	sperre  chan struct{} // nil: nicht sperren; sonst warten, bis geschlossen
	laeuft  int
	maxLauf int
	abbruch map[Kachel]bool // Abruf wurde per Kontext abgebrochen
	fehler  error
	etagMit []string
}

func neuerHoler() *attrappeHoler {
	return &attrappeHoler{daten: map[Kachel][]byte{}, abrufe: map[Kachel]int{}, abbruch: map[Kachel]bool{}}
}

func (h *attrappeHoler) Kachel(ctx context.Context, k Kachel, etag string) (Antwort, error) {
	h.mu.Lock()
	h.abrufe[k]++
	h.laeuft++
	h.maxLauf = max(h.maxLauf, h.laeuft)
	h.etagMit = append(h.etagMit, etag)
	sperre, fehler, d := h.sperre, h.fehler, h.daten[k]
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.laeuft--; h.mu.Unlock() }()
	if sperre != nil {
		select {
		case <-sperre:
		case <-ctx.Done():
			h.mu.Lock()
			h.abbruch[k] = true
			h.mu.Unlock()
			return Antwort{}, ctx.Err()
		}
	}
	if fehler != nil {
		return Antwort{}, fehler
	}
	if d == nil {
		return Antwort{}, ErrKachelFehlt
	}
	return Antwort{Daten: d}, nil
}

func (h *attrappeHoler) abgerufen(k Kachel) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.abrufe[k]
}

func (h *attrappeHoler) gesamt() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.abrufe {
		n += c
	}
	return n
}

// satz erzeugt n×n Kacheln der Stufe 0 (Spalten und Zeilen ab 0) im Holer.
func satz(t *testing.T, h *attrappeHoler, n int) (*Manifest, map[string][]byte) {
	t.Helper()
	roh := map[string][]byte{}
	for y := range n {
		for x := range n {
			k := Kachel{0, x, y}
			d := testPNG(t, y*n+x)
			h.daten[k] = d
			roh[k.String()] = d
		}
	}
	return testManifest(t, time.Unix(1_700_000_000, 0), roh), roh
}

func neuerTestLader(ctx context.Context, h Holer, p *Platte) *Lader[*image.RGBA] {
	return NeuerLader(ctx, h, p, func(i *image.RGBA) *image.RGBA { return i }, nil)
}

func TestLaderLaedtSichtbareUndHaeltGrenzen(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 12) // 144 Kacheln
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m)

	// Nur drei Kacheln sichtbar: nur diese werden abgerufen.
	sichtbar := []Kachel{{0, 0, 0}, {0, 1, 0}, {0, 2, 0}}
	l.Anfordern(sichtbar)
	bis(t, "drei Kacheln geladen", func() bool {
		for _, k := range sichtbar {
			if _, z := l.Bild(k); z != Fertig {
				return false
			}
		}
		return true
	})
	if n := h.gesamt(); n != 3 {
		t.Errorf("%d Abrufe für drei sichtbare Kacheln", n)
	}
	if b, z := l.Bild(Kachel{0, 5, 5}); z != Laedt || b != nil {
		t.Errorf("nicht angeforderte Kachel: %v, %v", b, z)
	}
	if _, z := l.Bild(Kachel{0, 40, 40}); z != Fehlt {
		t.Errorf("Kachel außerhalb des Manifests: %v", z)
	}
	// Erneutes Anfordern holt nichts noch einmal.
	l.Anfordern(sichtbar)
	time.Sleep(20 * time.Millisecond)
	if n := h.gesamt(); n != 3 {
		t.Errorf("%d Abrufe nach erneutem Anfordern", n)
	}
}

func TestLaderMaxAchtGleichzeitig(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 6) // 36 Kacheln
	h.sperre = make(chan struct{})
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m)
	var alle []Kachel
	for y := range 6 {
		for x := range 6 {
			alle = append(alle, Kachel{0, x, y})
		}
	}
	l.Anfordern(alle)
	bis(t, "acht Abrufe laufen", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.laeuft == 8 })
	time.Sleep(50 * time.Millisecond) // wäre mehr möglich, wäre es jetzt passiert
	h.mu.Lock()
	if h.maxLauf != 8 {
		t.Errorf("%d Abrufe gleichzeitig, erlaubt genau 8", h.maxLauf)
	}
	h.mu.Unlock()
	close(h.sperre)
	bis(t, "alle geladen", func() bool {
		for _, k := range alle {
			if _, z := l.Bild(k); z != Fertig {
				return false
			}
		}
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.maxLauf > 8 {
		t.Errorf("%d Abrufe gleichzeitig, erlaubt 8", h.maxLauf)
	}
}

func TestLaderBrichtUnsichtbareAb(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 6)
	h.sperre = make(chan struct{})
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m)
	erste := []Kachel{{0, 0, 0}, {0, 1, 0}, {0, 2, 0}}
	l.Anfordern(erste)
	bis(t, "drei Abrufe laufen", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.laeuft == 3 })
	// Die Karte wurde verschoben: andere Kacheln sind sichtbar.
	l.Anfordern([]Kachel{{0, 4, 4}})
	bis(t, "die ersten drei sind abgebrochen", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.abbruch[erste[0]] && h.abbruch[erste[1]] && h.abbruch[erste[2]]
	})
	for _, k := range erste {
		if _, z := l.Bild(k); z == Fertig || z == Fehler {
			t.Errorf("abgebrochene Kachel %v hat Zustand %v (kein Fehler, kein Bild)", k, z)
		}
	}
	if n, _ := l.Fehler(); n != 0 {
		t.Errorf("Abbruch zählt als Fehler (%d)", n)
	}
	close(h.sperre)
	bis(t, "neue Kachel geladen", func() bool { _, z := l.Bild(Kachel{0, 4, 4}); return z == Fertig })
}

func TestLaderStoppWartetAufGoroutinen(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 4)
	h.sperre = make(chan struct{}) // nie freigegeben
	l := neuerTestLader(t.Context(), h, nil)
	l.Setzen(m)
	var alle []Kachel
	for y := range 4 {
		for x := range 4 {
			alle = append(alle, Kachel{0, x, y})
		}
	}
	l.Anfordern(alle)
	bis(t, "Abrufe laufen", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.laeuft > 0 })
	fertig := make(chan struct{})
	go func() { l.Stopp(); close(fertig) }()
	select {
	case <-fertig:
	case <-time.After(5 * time.Second):
		t.Fatal("Stopp kehrt nicht zurück")
	}
	h.mu.Lock()
	if h.laeuft != 0 {
		t.Errorf("%d Abrufe laufen nach Stopp noch", h.laeuft)
	}
	h.mu.Unlock()
	// Nach Stopp lädt nichts mehr.
	vor := h.gesamt()
	l.Anfordern(alle)
	time.Sleep(20 * time.Millisecond)
	if h.gesamt() != vor {
		t.Error("Lader holt nach Stopp weiter")
	}
}

func TestLaderEndeDesKontexts(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 3)
	h.sperre = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	l := neuerTestLader(ctx, h, nil)
	l.Setzen(m)
	l.Anfordern([]Kachel{{0, 0, 0}, {0, 1, 0}})
	bis(t, "Abrufe laufen", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.laeuft == 2 })
	cancel()
	bis(t, "Abrufe enden mit dem Kontext", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.laeuft == 0 })
	l.Stopp()
}

// Die Antwort passt nicht zum Hash des Manifests: weder Bild noch Platte.
func TestLaderHashPruefung(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 2)
	// Der Koordinator liefert für 0/0/0 etwas anderes als im Manifest steht.
	h.daten[Kachel{0, 0, 0}] = testPNG(t, 99)
	dir := t.TempDir()
	p, _ := NeuePlatte(dir, 0)
	l := neuerTestLader(t.Context(), h, p)
	defer l.Stopp()
	l.Setzen(m)
	l.Anfordern([]Kachel{{0, 0, 0}, {0, 1, 0}})
	bis(t, "Fehler und Bild", func() bool {
		_, z0 := l.Bild(Kachel{0, 0, 0})
		_, z1 := l.Bild(Kachel{0, 1, 0})
		return z0 == Fehler && z1 == Fertig
	})
	if n, text := l.Fehler(); n != 1 || text == "" {
		t.Errorf("Fehler = %d, %q", n, text)
	}
	// Auf der Platte liegt nur die gute Kachel.
	if got := len(mustReadDir(t, dir)); got != 1 {
		t.Errorf("%d Dateien im Zwischenspeicher, want 1", got)
	}
	if _, ok := p.Lesen(HashVon(h.daten[Kachel{0, 0, 0}])); ok {
		t.Error("die falsche Kachel steht im Zwischenspeicher")
	}
}

func TestLaderPlatteSpartAbruf(t *testing.T) {
	h := neuerHoler()
	m, roh := satz(t, h, 2)
	p, _ := NeuePlatte(t.TempDir(), 0)
	for _, d := range roh {
		p.Schreiben(HashVon(d), d)
	}
	l := neuerTestLader(t.Context(), h, p)
	defer l.Stopp()
	l.Setzen(m)
	k := Kachel{0, 1, 1}
	l.Anfordern([]Kachel{k})
	bis(t, "von der Platte geladen", func() bool { _, z := l.Bild(k); return z == Fertig })
	if n := h.gesamt(); n != 0 {
		t.Errorf("%d Abrufe trotz Platte", n)
	}
}

func TestLaderKaputtePlatteWirdNeuGeholt(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 2)
	dir := t.TempDir()
	p, _ := NeuePlatte(dir, 0)
	k := Kachel{0, 1, 0}
	hash, _ := m.Hash(k)
	// Datei mit dem richtigen Namen, aber falschem Inhalt.
	if err := writeFile(dir, hash+".png", []byte("kein png")); err != nil {
		t.Fatal(err)
	}
	l := neuerTestLader(t.Context(), h, p)
	defer l.Stopp()
	l.Setzen(m)
	l.Anfordern([]Kachel{k})
	bis(t, "neu geholt", func() bool { _, z := l.Bild(k); return z == Fertig })
	if n := h.abgerufen(k); n != 1 {
		t.Errorf("%d Abrufe, want 1", n)
	}
	if d, ok := p.Lesen(hash); !ok || HashVon(d) != hash {
		t.Error("die Platte hat die gute Kachel nicht")
	}
}

func TestLaderKeinPNG(t *testing.T) {
	h := neuerHoler()
	kaputt := []byte("das ist kein png")
	h.daten[Kachel{0, 0, 0}] = kaputt
	m := testManifest(t, time.Unix(1, 0), map[string][]byte{"0/0/0": kaputt}) // Hash stimmt
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m)
	l.Anfordern([]Kachel{{0, 0, 0}})
	bis(t, "Fehler", func() bool { _, z := l.Bild(Kachel{0, 0, 0}); return z == Fehler })
}

func TestDekodiereKachelGrenzen(t *testing.T) {
	if _, err := DekodiereKachel(testPNG(t, 1)); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string][]byte{
		"leer":           nil,
		"Müll":           []byte("xxxx"),
		"klein":          pngGroesse(t, 16, 16),
		"groß":           pngGroesse(t, 512, 512),
		"breit":          pngGroesse(t, 4096, 1),
		"zu viele Bytes": append(testPNG(t, 1), make([]byte, MaxKachelBytes)...),
	} {
		if _, err := DekodiereKachel(d); err == nil {
			t.Errorf("%s wurde angenommen", name)
		}
	}
}

func pngGroesse(t *testing.T, b, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, b, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLaderSpeicherGrenze64(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 10) // 100 Kacheln
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m)
	var alle []Kachel
	for y := range 10 {
		for x := range 10 {
			alle = append(alle, Kachel{0, x, y})
		}
	}
	// In Gruppen anfordern, wie beim Verschieben.
	for i := 0; i < len(alle); i += 20 {
		gruppe := alle[i : i+20]
		l.Anfordern(gruppe)
		bis(t, "Gruppe geladen", func() bool {
			for _, k := range gruppe {
				if _, z := l.Bild(k); z != Fertig {
					return false
				}
			}
			return true
		})
		if n := l.ImSpeicher(); n > SpeicherKacheln {
			t.Fatalf("%d Bilder im Speicher, erlaubt %d", n, SpeicherKacheln)
		}
	}
	if n := l.ImSpeicher(); n != SpeicherKacheln {
		t.Errorf("%d Bilder im Speicher, want %d", n, SpeicherKacheln)
	}
	// Die ältesten sind raus (ohne Platte wären sie neu zu holen), die jüngsten da.
	if _, z := l.Bild(alle[0]); z == Fertig {
		t.Error("älteste Kachel noch im Speicher")
	}
	if _, z := l.Bild(alle[99]); z != Fertig {
		t.Error("jüngste Kachel nicht im Speicher")
	}
}

// Neues Manifest mit geänderten Hashes: Nur die geänderten Kacheln werden neu
// geholt; bis dahin zeigt der Lader die alte Fassung („veraltet“).
func TestLaderNeuesManifestLaedtNurGeaenderte(t *testing.T) {
	h := neuerHoler()
	m1, roh := satz(t, h, 3)
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m1)
	var alle []Kachel
	for y := range 3 {
		for x := range 3 {
			alle = append(alle, Kachel{0, x, y})
		}
	}
	l.Anfordern(alle)
	bis(t, "alle geladen", func() bool {
		for _, k := range alle {
			if _, z := l.Bild(k); z != Fertig {
				return false
			}
		}
		return true
	})
	vor := h.gesamt()

	// Die Mitte (1/1) ändert sich, alles andere bleibt.
	neu := testPNG(t, 500)
	h.mu.Lock()
	h.daten[Kachel{0, 1, 1}] = neu
	h.mu.Unlock()
	roh2 := map[string][]byte{}
	for k, d := range roh {
		roh2[k] = d
	}
	roh2["0/1/1"] = neu
	m2 := testManifest(t, time.Unix(1_700_000_100, 0), roh2)
	if m1.Gleich(m2) {
		t.Fatal("Manifeste sollten verschieden sein")
	}
	h.mu.Lock()
	h.sperre = make(chan struct{}) // die neue Kachel hängt, solange wir prüfen
	sperre := h.sperre
	h.mu.Unlock()
	l.Setzen(m2)
	l.Anfordern(alle)
	bis(t, "Kachel veraltet und lädt", func() bool { _, z := l.Bild(Kachel{0, 1, 1}); return z == Veraltet })
	if b, _ := l.Bild(Kachel{0, 1, 1}); b == nil {
		t.Error("veraltete Kachel liefert kein Bild")
	}
	if _, z := l.Bild(Kachel{0, 0, 0}); z != Fertig {
		t.Errorf("unveränderte Kachel: %v", z)
	}
	close(sperre)
	bis(t, "neue Fassung da", func() bool { _, z := l.Bild(Kachel{0, 1, 1}); return z == Fertig })
	if n := h.gesamt() - vor; n != 1 {
		t.Errorf("%d Abrufe nach Manifest-Wechsel, want 1", n)
	}
}

func TestLaderFehlerPauseUndNeuLaden(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 2)
	h.fehler = errors.New("Koordinator weg")
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	var jetzt atomic.Int64
	jetzt.Store(1_000_000)
	l.jetzt = func() time.Time { return time.Unix(jetzt.Load(), 0) }
	l.Setzen(m)
	k := Kachel{0, 0, 0}
	l.Anfordern([]Kachel{k})
	bis(t, "Fehler", func() bool { _, z := l.Bild(k); return z == Fehler })
	if n, text := l.Fehler(); n != 1 || text != "Koordinator weg" {
		t.Errorf("Fehler = %d, %q", n, text)
	}
	// Innerhalb der Pause kein neuer Versuch.
	l.Anfordern([]Kachel{k})
	time.Sleep(20 * time.Millisecond)
	if n := h.abgerufen(k); n != 1 {
		t.Errorf("%d Abrufe innerhalb der Pause", n)
	}
	// „Neu laden“ vergisst den Fehler; der Koordinator ist wieder da.
	h.mu.Lock()
	h.fehler = nil
	h.mu.Unlock()
	l.FehlerVergessen()
	l.Anfordern([]Kachel{k})
	bis(t, "nach Neu laden geladen", func() bool { _, z := l.Bild(k); return z == Fertig })
	// Nach Ablauf der Pause versucht der Lader von allein neu.
	h2 := neuerHoler()
	m2, _ := satz(t, h2, 2)
	h2.fehler = errors.New("weg")
	l2 := neuerTestLader(t.Context(), h2, nil)
	defer l2.Stopp()
	l2.jetzt = l.jetzt
	l2.Setzen(m2)
	l2.Anfordern([]Kachel{k})
	bis(t, "Fehler 2", func() bool { _, z := l2.Bild(k); return z == Fehler })
	jetzt.Add(int64(fehlerPause/time.Second) + 1)
	h2.mu.Lock()
	h2.fehler = nil
	h2.mu.Unlock()
	l2.Anfordern([]Kachel{k})
	bis(t, "nach der Pause neu versucht", func() bool { _, z := l2.Bild(k); return z == Fertig })
}

func TestLaderMeldetAenderungen(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 2)
	var n atomic.Int32
	l := NeuerLader(t.Context(), h, nil, func(i *image.RGBA) *image.RGBA { return i }, func() { n.Add(1) })
	defer l.Stopp()
	l.Setzen(m)
	l.Anfordern([]Kachel{{0, 0, 0}})
	bis(t, "Meldung nach dem Laden", func() bool { _, z := l.Bild(Kachel{0, 0, 0}); return z == Fertig && n.Load() >= 2 })
}

func TestLaderKachelFehltImHolerIstFehler(t *testing.T) {
	h := neuerHoler()
	m, _ := satz(t, h, 2)
	delete(h.daten, Kachel{0, 1, 1}) // das Manifest nennt sie, der Koordinator hat sie nicht
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m)
	l.Anfordern([]Kachel{{0, 1, 1}})
	bis(t, "Fehler", func() bool { _, z := l.Bild(Kachel{0, 1, 1}); return z == Fehler })
}

// Drosselt der Koordinator (429), wartet der Lader und fragt noch einmal;
// das ist kein Fehler.
func TestLaderWiederholtBeiDrosselung(t *testing.T) {
	alt := MaxDrosselWarte
	MaxDrosselWarte = 5 * time.Millisecond
	t.Cleanup(func() { MaxDrosselWarte = alt })
	h := &drosselHoler{attrappeHoler: neuerHoler(), drossel: 2}
	m, _ := satz(t, h.attrappeHoler, 2)
	l := neuerTestLader(t.Context(), h, nil)
	defer l.Stopp()
	l.Setzen(m)
	k := Kachel{0, 1, 0}
	l.Anfordern([]Kachel{k})
	bis(t, "nach Drosselung geladen", func() bool { _, z := l.Bild(k); return z == Fertig })
	if n := h.abgerufen(k); n != 3 {
		t.Errorf("%d Abrufe, want 3 (zwei gedrosselt, einer erfolgreich)", n)
	}
	if n, _ := l.Fehler(); n != 0 {
		t.Errorf("Drosselung zählt als Fehler (%d)", n)
	}
	// Dauerhaft gedrosselt: nach MaxDrosselVersuche ein Fehler.
	h2 := &drosselHoler{attrappeHoler: neuerHoler(), drossel: 1000}
	m2, _ := satz(t, h2.attrappeHoler, 2)
	l2 := neuerTestLader(t.Context(), h2, nil)
	defer l2.Stopp()
	l2.Setzen(m2)
	l2.Anfordern([]Kachel{k})
	bis(t, "Fehler nach zu vielen Versuchen", func() bool { _, z := l2.Bild(k); return z == Fehler })
	if n := h2.abgerufen(k); n != MaxDrosselVersuche {
		t.Errorf("%d Versuche, want %d", n, MaxDrosselVersuche)
	}
}

// drosselHoler antwortet die ersten drossel Male mit 429.
type drosselHoler struct {
	*attrappeHoler
	mu2     sync.Mutex
	drossel int
}

func (h *drosselHoler) Kachel(ctx context.Context, k Kachel, etag string) (Antwort, error) {
	h.mu2.Lock()
	if h.drossel > 0 {
		h.drossel--
		h.mu2.Unlock()
		h.attrappeHoler.mu.Lock()
		h.abrufe[k]++
		h.attrappeHoler.mu.Unlock()
		return Antwort{}, &Gedrosselt{Nach: time.Second}
	}
	h.mu2.Unlock()
	return h.attrappeHoler.Kachel(ctx, k, etag)
}
