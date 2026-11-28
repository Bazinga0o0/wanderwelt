package karte

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Cache-Control der Antworten: Kacheln wie im Vertrag; das Manifest wird
// immer neu angefragt (bedingt, 304 ist billig), damit der Starter neue
// Stände sieht.
const (
	cacheKachel   = "private, max-age=60"
	cacheManifest = "private, no-cache"
)

func fehler(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// etagPasst wertet If-None-Match aus (Liste, „*“, schwache Marken).
func etagPasst(header []string, etag string) bool {
	for _, h := range header {
		for _, part := range strings.Split(h, ",") {
			part = strings.TrimSpace(part)
			if part == "*" || strings.TrimPrefix(part, "W/") == etag {
				return true
			}
		}
	}
	return false
}

// Manifest antwortet auf GET /v1/karte/{welt}/manifest.json aus dem
// Speicher (einmal je Stand geparst und gehalten). welt ist die Kennung der
// Welt der Gruppe; der Aufrufer hat die Mitgliedschaft schon geprüft.
func (d *Dienst) Manifest(w http.ResponseWriter, r *http.Request, welt string) {
	if !validWelt(welt) {
		fehler(w, http.StatusBadRequest, "ungültige Welt")
		return
	}
	s := d.stand(welt)
	if s == nil {
		fehler(w, http.StatusNotFound, "für diese Welt gibt es noch keine Karte")
		return
	}
	h := w.Header()
	h.Set("ETag", s.etag)
	h.Set("Cache-Control", cacheManifest)
	if etagPasst(r.Header.Values("If-None-Match"), s.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(s.manifest)))
	w.Write(s.manifest)
}

// Kachel antwortet auf GET /v1/karte/{welt}/t/{z}/{x}/{y}.png. z, x und y
// sind die Rohwerte der Anfrage (y mit „.png“): Sie werden streng als Zahlen
// gelesen (kein Vorzeichen außer „-“, keine führenden Nullen, höchstens 9
// Ziffern); der Dateipfad entsteht nur aus diesen Zahlen und nur für Kacheln,
// die das Manifest nennt. Die Datei wird gestreamt.
func (d *Dienst) Kachel(w http.ResponseWriter, r *http.Request, welt, zs, xs, ys string) {
	if !validWelt(welt) {
		fehler(w, http.StatusBadRequest, "ungültige Welt")
		return
	}
	ys, ok := strings.CutSuffix(ys, ".png")
	if !ok {
		fehler(w, http.StatusBadRequest, "ungültige Kachel (erwartet {y}.png)")
		return
	}
	z, ok1 := parseCoord(zs)
	x, ok2 := parseCoord(xs)
	y, ok3 := parseCoord(ys)
	if !ok1 || !ok2 || !ok3 {
		fehler(w, http.StatusBadRequest, "ungültige Kachel-Koordinaten")
		return
	}

	// Nachschlagen und Öffnen unter der Lesesperre: Der Austausch ersetzt
	// den ganzen Ordner; eine geöffnete Datei bleibt lesbar.
	d.mu.RLock()
	s := d.staende[welt]
	var (
		t     tileEntry
		found bool
		f     *os.File
		oerr  error
	)
	if s != nil {
		t, found = s.tiles[tileKey(z, x, y)]
		if found {
			f, oerr = os.Open(tilePath(s.dir, z, x, y))
		}
	}
	d.mu.RUnlock()
	switch {
	case s == nil:
		fehler(w, http.StatusNotFound, "für diese Welt gibt es noch keine Karte")
		return
	case !found:
		fehler(w, http.StatusNotFound, "diese Kachel gibt es nicht")
		return
	case oerr != nil:
		fehler(w, http.StatusNotFound, "diese Kachel gibt es nicht")
		return
	}
	defer f.Close()

	etag := `"` + t.SHA256 + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", cacheKachel)
	if etagPasst(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		fehler(w, http.StatusNotFound, "diese Kachel gibt es nicht")
		return
	}
	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	if r.Method == http.MethodHead {
		return
	}
	io.Copy(w, f)
}
