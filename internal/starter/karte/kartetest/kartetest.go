// Package kartetest ist ein Fake-Koordinator für Tests der Karte: Er liefert
// Status, Manifest und Kacheln aus einem selbst erzeugten Kachelsatz, so wie
// der Vertrag es beschreibt (Kartenvertrag).
// Nur für Tests gedacht.
package kartetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
)

// PNG erzeugt eine 256×256-Kachel mit Palette (16 Farben); seed macht den
// Inhalt und damit den Hash verschieden.
func PNG(t testing.TB, seed int) []byte {
	t.Helper()
	pal := make(color.Palette, 16)
	for i := range pal {
		pal[i] = color.RGBA{R: uint8((i*16 + seed*29) % 256), G: uint8((i*7 + seed*13) % 256), B: uint8((255 - i*16 + seed*5) % 256), A: 255}
	}
	img := image.NewPaletted(image.Rect(0, 0, karte.KachelPx, karte.KachelPx), pal)
	for y := range karte.KachelPx {
		for x := range karte.KachelPx {
			img.SetColorIndex(x, y, uint8((x/16+y/16+seed)%16))
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Koordinator ist der Fake. Alle Felder hinter mu; Zugriff über die Methoden.
type Koordinator struct {
	Server *httptest.Server
	Token  string
	Welt   string

	mu         sync.Mutex
	kacheln    map[karte.Kachel][]byte
	generated  time.Time
	source     string
	keine      bool // noch keine Karte (404 für das Manifest, kein „karte“ im Status)
	ausfall    bool // alles antwortet 503
	manifestJ  []byte
	abrufe     map[string]int
	zugriffe   []string
	ohneToken  int           // Anfragen ohne passendes Token
	halt       chan struct{} // nicht nil: Kachel-Anfragen warten, bis es geschlossen wird
	drossel    int           // so viele Karten-Anfragen noch mit 429 beantworten
	gedrosselt int           // so viele 429 wurden gesendet
}

// Neu startet den Fake für die Welt welt; Token ist das erwartete
// Bearer-Token.
func Neu(t testing.TB, token, welt string) *Koordinator {
	t.Helper()
	k := &Koordinator{Token: token, Welt: welt, kacheln: map[karte.Kachel][]byte{}, abrufe: map[string]int{},
		generated: time.Now().UTC().Truncate(time.Second), source: "test-1"}
	k.Server = httptest.NewServer(http.HandlerFunc(k.bedienen))
	t.Cleanup(k.Server.Close)
	return k
}

// URL ist die Adresse des Fakes (für group.Group.Coordinator).
func (k *Koordinator) URL() string { return k.Server.URL }

// Setze legt die Kachel an oder ersetzt sie.
func (k *Koordinator) Setze(kachel karte.Kachel, png []byte) {
	k.mu.Lock()
	k.kacheln[kachel] = png
	k.manifestJ = nil
	k.mu.Unlock()
}

// Entferne nimmt die Kachel weg (aus dem Manifest; Abruf ergibt 404).
func (k *Koordinator) Entferne(kachel karte.Kachel) {
	k.mu.Lock()
	delete(k.kacheln, kachel)
	k.manifestJ = nil
	k.mu.Unlock()
}

// Erzeugt setzt den Zeitpunkt der Erzeugung („generated“) und die Kennung
// des Stands.
func (k *Koordinator) Erzeugt(t time.Time, source string) {
	k.mu.Lock()
	k.generated, k.source = t.UTC().Truncate(time.Second), source
	k.manifestJ = nil
	k.mu.Unlock()
}

// OhneKarte schaltet um: Der Koordinator hat (noch) keine Karte.
func (k *Koordinator) OhneKarte(keine bool) {
	k.mu.Lock()
	k.keine = keine
	k.mu.Unlock()
}

// Ausfall schaltet um: Jede Anfrage endet mit 503.
func (k *Koordinator) Ausfall(an bool) {
	k.mu.Lock()
	k.ausfall = an
	k.mu.Unlock()
}

// Drosseln lässt die nächsten n Karten-Anfragen (Manifest und Kacheln) mit
// 429 und „Retry-After: 1“ beantworten, wie die Ratenbegrenzung des
// Koordinators.
func (k *Koordinator) Drosseln(n int) {
	k.mu.Lock()
	k.drossel = n
	k.mu.Unlock()
}

// Gedrosselt ist die Zahl der gesendeten 429.
func (k *Koordinator) Gedrosselt() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gedrosselt
}

// Abrufe zählt die Anfragen je Pfad („manifest.json“, „t/0/1/2.png“, „status“).
func (k *Koordinator) Abrufe(pfad string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.abrufe[pfad]
}

// KachelAbrufe ist die Zahl aller Kachel-Anfragen.
func (k *Koordinator) KachelAbrufe() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for p, c := range k.abrufe {
		if strings.HasPrefix(p, "t/") {
			n += c
		}
	}
	return n
}

// Zugriffe sind alle angefragten Pfade samt Zeit in der Reihenfolge.
func (k *Koordinator) Zugriffe() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.zugriffe...)
}

// OhneToken ist die Zahl der Anfragen ohne passendes Token (sie bekamen 403).
func (k *Koordinator) OhneToken() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.ohneToken
}

// Manifest liefert das aktuelle karte.json (so, wie das Rust-Tool es schreibt).
func (k *Koordinator) Manifest(t testing.TB) []byte {
	t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.manifestLocked(t)
}

func (k *Koordinator) manifestLocked(t testing.TB) []byte {
	if k.manifestJ != nil {
		return k.manifestJ
	}
	tiles := map[string]karte.Eintrag{}
	bounds := map[string]map[string]int{}
	for kachel, d := range k.kacheln {
		tiles[kachel.String()] = karte.Eintrag{SHA256: karte.HashVon(d), Size: int64(len(d))}
		b := bounds[strconv.Itoa(kachel.Z)]
		if b == nil {
			bounds[strconv.Itoa(kachel.Z)] = map[string]int{"x_min": kachel.X, "x_max": kachel.X, "y_min": kachel.Y, "y_max": kachel.Y}
			continue
		}
		b["x_min"], b["x_max"] = min(b["x_min"], kachel.X), max(b["x_max"], kachel.X)
		b["y_min"], b["y_max"] = min(b["y_min"], kachel.Y), max(b["y_max"], kachel.Y)
	}
	j, err := json.Marshal(map[string]any{
		"format": 1, "dimension": "overworld", "tile_size": 256, "levels": []int{0, 1, 2, 3, 4, 5, 6},
		"source": k.source, "generated": k.generated, "bounds": bounds, "tiles": tiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	k.manifestJ = j
	return j
}

// Status ist, was der Koordinator unter GET /v1/status liefert (nur die
// Weltliste; mit „karte“, sobald es eine Karte gibt).
func (k *Koordinator) status() api.Status {
	w := api.GroupWorld{ID: k.Welt, Name: "Testwelt", HeadGeneration: 1}
	if !k.keine && len(k.kacheln) > 0 {
		w.Karte = &api.KarteInfo{Version: 1, Source: k.source, Generated: k.generated, Tiles: len(k.kacheln), Levels: []int{0, 1, 2, 3, 4, 5, 6}}
	}
	return api.Status{Worlds: []api.GroupWorld{w}, RunningWorld: k.Welt}
}

// Anhalten lässt Kachel-Anfragen warten (bis zur Freigabe oder zum Abbruch der
// Anfrage); die Funktion gibt sie frei. Für Zustände wie „Kacheln laden noch“.
func (k *Koordinator) Anhalten() (freigeben func()) {
	k.mu.Lock()
	ch := make(chan struct{})
	k.halt = ch
	k.mu.Unlock()
	var einmal sync.Once
	return func() {
		einmal.Do(func() {
			k.mu.Lock()
			k.halt = nil
			k.mu.Unlock()
			close(ch)
		})
	}
}

// bedienen ist der Handler: Status, Manifest, Kacheln nach dem Vertrag.
func (k *Koordinator) bedienen(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	halt := k.halt
	k.mu.Unlock()
	if halt != nil && strings.Contains(r.URL.Path, "/t/") {
		select {
		case <-halt:
		case <-r.Context().Done():
			return
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	fehler := func(code int, text string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": text})
	}
	k.zugriffe = append(k.zugriffe, r.URL.Path)
	if r.Header.Get("Authorization") == "" {
		k.ohneToken++
		fehler(http.StatusUnauthorized, "keine Identität")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+k.Token {
		k.ohneToken++
		fehler(http.StatusForbidden, "kein Mitglied")
		return
	}
	if k.ausfall {
		fehler(http.StatusServiceUnavailable, "Koordinator überlastet")
		return
	}
	pfad := strings.TrimPrefix(r.URL.Path, "/v1/")
	if k.drossel > 0 && strings.HasPrefix(pfad, "karte/") {
		k.drossel--
		k.gedrosselt++
		w.Header().Set("Retry-After", "1")
		fehler(http.StatusTooManyRequests, "zu viele Anfragen")
		return
	}
	switch {
	case r.Method == http.MethodGet && pfad == "status":
		k.abrufe["status"]++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(k.status())
		return
	case r.Method == http.MethodGet && pfad == "karte/"+k.Welt+"/manifest.json":
		k.abrufe["manifest.json"]++
		if k.keine || len(k.kacheln) == 0 {
			fehler(http.StatusNotFound, "keine Karte")
			return
		}
		j := k.manifestLocked(nil)
		etag := `"` + karte.HashVon(j) + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(j)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(pfad, "karte/"+k.Welt+"/t/") && strings.HasSuffix(pfad, ".png"):
		teile := strings.Split(strings.TrimSuffix(strings.TrimPrefix(pfad, "karte/"+k.Welt+"/t/"), ".png"), "/")
		key := strings.Join(teile, "/")
		kachel, ok := karte.ParseSchluessel(key)
		if !ok {
			fehler(http.StatusBadRequest, "ungültige Kachel")
			return
		}
		k.abrufe["t/"+key+".png"]++
		d, da := k.kacheln[kachel]
		if !da || k.keine {
			fehler(http.StatusNotFound, "keine Kachel")
			return
		}
		etag := `"` + karte.HashVon(d) + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=60")
		if m := r.Header.Get("If-None-Match"); m == etag || m == strings.Trim(etag, `"`) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(d)
		return
	}
	fehler(http.StatusNotFound, fmt.Sprintf("unbekannter Endpunkt %s %s", r.Method, r.URL.Path))
}
