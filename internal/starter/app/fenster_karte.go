package app

// Reiter „Karte“ (Mitglieder): Die Instanz holt Manifest und Kacheln mit dem
// Token der Gruppe vom Koordinator und reicht sie ans Fenster durch (das
// Fenster ist ein eigener Prozess und sieht das Token nie). Endpunkte und
// Format: internal/starter/fensterapi/karte.go.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
)

// karteLogPause: Fehler beim Abruf von Kacheln stehen höchstens so oft im Log
// (ein ausgefallener Koordinator ließe sonst jede Kachel eine Zeile schreiben).
const karteLogPause = time.Minute

var karteLog struct {
	mu   sync.Mutex
	zeit time.Time
}

// karteLogFehler schreibt den Fehler eines Kachel-Abrufs ins Log, aber nicht
// öfter als alle karteLogPause. Nie das Token: logFehler nennt nur Methode,
// Status und Text bzw. Operation und Ursache.
func karteLogFehler(err error) {
	karteLog.mu.Lock()
	jetzt := time.Now()
	if jetzt.Sub(karteLog.zeit) < karteLogPause {
		karteLog.mu.Unlock()
		return
	}
	karteLog.zeit = jetzt
	karteLog.mu.Unlock()
	log.Printf("Fenster: Karte: Kachel: %s", logFehler(err))
}

// karteAnsicht beantwortet GET /v1/kartemanifest und GET /v1/kartekachel.
func (a *App) karteAnsicht(w http.ResponseWriter, r *http.Request, name string) {
	welt := r.URL.Query().Get("welt")
	if !client.KarteWeltGueltig(welt) {
		antwortFehler(w, http.StatusBadRequest, "Welche Welt? Erwartet ?welt=<Kennung>.")
		return
	}
	c := a.client()
	if c == nil {
		antwortFehler(w, http.StatusConflict, textStartetNoch)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), koordWait)
	defer cancel()
	if name == "kartemanifest" {
		a.karteManifest(ctx, w, c, welt)
		return
	}
	a.karteKachel(ctx, w, r, c, welt)
}

func (a *App) karteManifest(ctx context.Context, w http.ResponseWriter, c *client.Client, welt string) {
	data, err := retry401(ctx, func() ([]byte, error) { return c.KarteManifestRoh(ctx, welt) })
	if err != nil {
		if karteGedrosselt(w, err) {
			return
		}
		log.Printf("Fenster: Karte: Manifest: %s", logFehler(err))
		if errors.Is(err, karte.ErrNochNichtErzeugt) {
			antwortFehler(w, http.StatusNotFound, "Die Karte ist noch nicht erzeugt.")
			return
		}
		code, text := koordFehler(err)
		antwortFehler(w, code, text)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (a *App) karteKachel(ctx context.Context, w http.ResponseWriter, r *http.Request, c *client.Client, welt string) {
	q := r.URL.Query()
	k, ok := karte.ParseSchluessel(q.Get("z") + "/" + q.Get("x") + "/" + q.Get("y"))
	if !ok {
		antwortFehler(w, http.StatusBadRequest, "Welche Kachel? Erwartet ?z=&x=&y= (ganze Zahlen, Stufe 0 bis 6).")
		return
	}
	etag := strings.Trim(r.Header.Get("If-None-Match"), `"`)
	ant, err := retry401(ctx, func() (karte.Antwort, error) { return c.KarteKachel(ctx, welt, k, etag) })
	if err != nil {
		if karteGedrosselt(w, err) {
			return
		}
		if errors.Is(err, karte.ErrKachelFehlt) {
			antwortFehler(w, http.StatusNotFound, "Diese Kachel gibt es nicht.")
			return
		}
		karteLogFehler(err)
		code, text := koordFehler(err)
		antwortFehler(w, code, text)
		return
	}
	if ant.ETag != "" && karte.GueltigerHash(ant.ETag) {
		w.Header().Set("ETag", `"`+ant.ETag+`"`)
	}
	if ant.Unveraendert {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(ant.Daten)
}

// karteGedrosselt: Drosselt der Koordinator (429), gibt die Instanz das mit
// Retry-After ans Fenster weiter; das Fenster wartet und fragt noch einmal.
func karteGedrosselt(w http.ResponseWriter, err error) bool {
	g, ok := errors.AsType[*karte.Gedrosselt](err)
	if !ok {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(max(int(g.Nach.Seconds()), 1)))
	antwortFehler(w, http.StatusTooManyRequests, g.Error())
	return true
}
