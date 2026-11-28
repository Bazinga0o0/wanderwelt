package karte

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"sync"
	"time"
)

const (
	// MaxParallel: so viele Kacheln werden höchstens gleichzeitig abgerufen.
	MaxParallel = 8
	// SpeicherKacheln: so viele Bilder hält der Arbeitsspeicher-Zwischenspeicher.
	SpeicherKacheln = 64
	// fehlerPause: So lange wird eine gescheiterte Kachel nicht neu versucht
	// (bis „Neu laden“).
	fehlerPause = 10 * time.Second
)

// Antwort auf den Abruf einer Kachel. Unveraendert: Der Koordinator hat auf
// If-None-Match mit 304 geantwortet (Daten dann leer).
type Antwort struct {
	Daten        []byte
	ETag         string
	Unveraendert bool
}

// Holer ruft eine Kachel beim Koordinator ab (etag: Hash der Fassung, die der
// Aufrufer schon hat, "" ohne). Er muss auf ctx achten.
type Holer interface {
	Kachel(ctx context.Context, k Kachel, etag string) (Antwort, error)
}

// DekodiereKachel liest eine PNG-Kachel: höchstens 256×256 Pixel (vorab aus
// dem Kopf geprüft, damit kein Riesenbild entpackt wird), Ergebnis immer
// *image.RGBA in genau dieser Größe.
func DekodiereKachel(data []byte) (*image.RGBA, error) {
	if len(data) == 0 || len(data) > MaxKachelBytes {
		return nil, errors.New("Kachel hat eine unzulässige Größe")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("Kachel ist kein PNG: %w", err)
	}
	if cfg.Width != KachelPx || cfg.Height != KachelPx {
		return nil, fmt.Errorf("Kachel ist %d×%d statt %d×%d Pixel", cfg.Width, cfg.Height, KachelPx, KachelPx)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("Kachel nicht lesbar: %w", err)
	}
	if rgba, ok := img.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba, nil
	}
	out := image.NewRGBA(image.Rect(0, 0, KachelPx, KachelPx))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out, nil
}

// Zustand einer Kachel im Lader.
type Zustand int

const (
	// Fehlt: nicht im Manifest (ungenerierte Fläche) – nichts zu laden.
	Fehlt Zustand = iota
	// Laedt: Das Bild ist noch nicht da (Platzhalter zeigen).
	Laedt
	// Veraltet: Es gibt eine ältere Fassung (wird ersetzt); sie wird gezeigt.
	Veraltet
	// Fertig: Das Bild ist aktuell.
	Fertig
	// Fehler: Das Laden ist gescheitert (kurz nicht neu versucht).
	Fehler
)

type bildEintrag[B any] struct {
	hash string
	bild B
}

type lauf struct {
	hash   string
	cancel context.CancelFunc
}

type fehlerEintrag struct {
	text string
	bis  time.Time
}

// Lader holt die sichtbaren Kacheln: erst aus dem Arbeitsspeicher, dann von
// der Platte (Hash geprüft), zuletzt vom Koordinator (höchstens MaxParallel
// gleichzeitig; Abrufe nicht mehr sichtbarer Kacheln werden abgebrochen),
// prüft jede Antwort gegen den Hash des Manifests, legt sie auf der Platte
// ab und hält höchstens SpeicherKacheln Bilder im Speicher. B ist der
// Bildtyp der Oberfläche (etwa eine Gio-Textur); wandeln macht ihn aus dem
// dekodierten Bild.
type Lader[B any] struct {
	holer     Holer
	platte    *Platte // darf nil sein
	wandeln   func(*image.RGBA) B
	geaendert func() // darf nil sein; läuft aus Goroutinen des Laders
	jetzt     func() time.Time

	ctx      context.Context
	stopp    context.CancelFunc
	wg       sync.WaitGroup
	sem      chan struct{}
	gestoppt bool

	mu      sync.Mutex
	m       *Manifest
	bilder  *lru[Kachel, bildEintrag[B]]
	laufend map[Kachel]*lauf
	fehler  map[Kachel]fehlerEintrag
}

// NeuerLader baut einen Lader; ctx beendet ihn (alle Abrufe brechen ab);
// zusätzlich Stopp.
func NeuerLader[B any](ctx context.Context, h Holer, p *Platte, wandeln func(*image.RGBA) B, geaendert func()) *Lader[B] {
	c, stopp := context.WithCancel(ctx)
	return &Lader[B]{
		holer: h, platte: p, wandeln: wandeln, geaendert: geaendert, jetzt: time.Now,
		ctx: c, stopp: stopp,
		sem:     make(chan struct{}, MaxParallel),
		bilder:  neuerLRU[Kachel, bildEintrag[B]](SpeicherKacheln),
		laufend: map[Kachel]*lauf{},
		fehler:  map[Kachel]fehlerEintrag{},
	}
}

// Stopp bricht alle Abrufe ab und wartet, bis alle Goroutinen des Laders
// beendet sind. Danach lädt der Lader nichts mehr.
func (l *Lader[B]) Stopp() {
	l.mu.Lock()
	l.gestoppt = true
	l.mu.Unlock()
	l.stopp()
	l.wg.Wait()
}

// Setzen ersetzt das Manifest. Bilder bleiben im Speicher (als „veraltet“,
// wenn der Hash nicht mehr stimmt, bis die neue Fassung da ist); Abrufe mit
// altem Hash werden abgebrochen, frühere Fehler vergessen.
func (l *Lader[B]) Setzen(m *Manifest) {
	l.mu.Lock()
	l.m = m
	for k, r := range l.laufend {
		if h, ok := m.Hash(k); !ok || h != r.hash {
			r.cancel()
			delete(l.laufend, k)
		}
	}
	clear(l.fehler)
	l.mu.Unlock()
	l.melde()
}

// FehlerVergessen: Gescheiterte Kacheln dürfen sofort neu versucht werden
// („Neu laden“).
func (l *Lader[B]) FehlerVergessen() {
	l.mu.Lock()
	clear(l.fehler)
	l.mu.Unlock()
}

func (l *Lader[B]) melde() {
	if l.geaendert != nil {
		l.geaendert()
	}
}

// Bild liefert das Bild der Kachel und ihren Zustand. Bei Laedt ist das Bild
// der Nullwert.
func (l *Lader[B]) Bild(k Kachel) (B, Zustand) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var null B
	if l.m == nil {
		return null, Fehlt
	}
	want, ok := l.m.Hash(k)
	if !ok {
		return null, Fehlt
	}
	if e, ok := l.bilder.holen(k); ok {
		if e.hash == want {
			return e.bild, Fertig
		}
		return e.bild, Veraltet
	}
	if _, ok := l.fehler[k]; ok {
		return null, Fehler
	}
	return null, Laedt
}

// Anfordern sagt dem Lader, welche Kacheln gerade sichtbar sind: Fehlende
// werden geholt, Abrufe anderer Kacheln abgebrochen.
func (l *Lader[B]) Anfordern(sichtbar []Kachel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gestoppt || l.m == nil {
		return
	}
	jetzt := l.jetzt()
	im := make(map[Kachel]bool, len(sichtbar))
	for _, k := range sichtbar {
		im[k] = true
	}
	for k, r := range l.laufend {
		if !im[k] {
			r.cancel()
			delete(l.laufend, k)
		}
	}
	for _, k := range sichtbar {
		want, ok := l.m.Hash(k)
		if !ok {
			continue
		}
		if e, ok := l.bilder.holen(k); ok && e.hash == want {
			continue
		}
		if r, ok := l.laufend[k]; ok {
			if r.hash == want {
				continue
			}
			r.cancel() // Abruf einer überholten Fassung
		}
		if f, ok := l.fehler[k]; ok {
			if jetzt.Before(f.bis) {
				continue
			}
			delete(l.fehler, k)
		}
		ctx, cancel := context.WithCancel(l.ctx)
		r := &lauf{hash: want, cancel: cancel}
		l.laufend[k] = r
		l.wg.Add(1)
		go l.laden(ctx, k, r)
	}
}

// Ausstehend ist die Zahl der Kacheln, die gerade geladen werden.
func (l *Lader[B]) Ausstehend() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.laufend)
}

// Fehler liefert die Zahl der gescheiterten Kacheln und den letzten Fehlertext.
func (l *Lader[B]) Fehler() (anzahl int, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.fehler {
		anzahl++
		text = f.text
	}
	return anzahl, text
}

// ImSpeicher ist die Zahl der Bilder im Arbeitsspeicher.
func (l *Lader[B]) ImSpeicher() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bilder.laenge()
}

func (l *Lader[B]) laden(ctx context.Context, k Kachel, r *lauf) {
	defer l.wg.Done()
	defer r.cancel()
	bild, err := l.hole(ctx, k, r.hash)

	l.mu.Lock()
	if l.laufend[k] == r {
		delete(l.laufend, k)
	}
	if ctx.Err() != nil {
		// Abgebrochen (nicht mehr sichtbar, neues Manifest, Ende): kein Fehler.
		l.mu.Unlock()
		return
	}
	if err != nil {
		l.fehler[k] = fehlerEintrag{text: err.Error(), bis: l.jetzt().Add(fehlerPause)}
	} else if want, ok := l.m.Hash(k); ok && want == r.hash {
		l.bilder.setzen(k, bildEintrag[B]{hash: r.hash, bild: bild})
	}
	l.mu.Unlock()
	l.melde()
}

// hole beschafft und dekodiert die Kachel mit dem Hash want.
func (l *Lader[B]) hole(ctx context.Context, k Kachel, want string) (B, error) {
	var null B
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-ctx.Done():
		return null, ctx.Err()
	}
	var data []byte
	if l.platte != nil {
		data, _ = l.platte.Lesen(want)
	}
	if data != nil {
		if img, err := DekodiereKachel(data); err == nil {
			return l.wandeln(img), nil
		}
		l.platte.Verwerfen(want) // Hash stimmt, Inhalt aber kein gültiges Bild
		data = nil
	}
	a, err := MitDrossel(ctx, func() (Antwort, error) { return l.holer.Kachel(ctx, k, "") })
	if err != nil {
		return null, err
	}
	if a.Unveraendert {
		return null, errors.New("der Koordinator meldet „unverändert“, obwohl keine Fassung vorliegt")
	}
	if HashVon(a.Daten) != want {
		return null, errors.New("die Kachel passt nicht zum Manifest (Karte wurde gerade erneuert?)")
	}
	img, err := DekodiereKachel(a.Daten)
	if err != nil {
		return null, err
	}
	if l.platte != nil {
		l.platte.Schreiben(want, a.Daten) // ein Fehler hier kostet nur den Zwischenspeicher
	}
	return l.wandeln(img), nil
}
