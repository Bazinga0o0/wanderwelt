package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
)

// Karte (Mitglieder): Manifest und Kacheln der Oberwelt vom Koordinator
// (GET /v1/karte/{welt}/manifest.json und /v1/karte/{welt}/t/{z}/{x}/{y}.png;
// Kartenvertrag: Kachelraster, Manifest, Endpunkte).

const (
	// karteManifestTimeout und karteKachelTimeout begrenzen je einen Abruf.
	karteManifestTimeout = 60 * time.Second
	karteKachelTimeout   = 30 * time.Second
)

// karteWeltRE: so sehen Weltkennungen aus (w1, w2, …); nichts, was einen
// Pfad verändern könnte.
var karteWeltRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// KarteWeltGueltig: ist welt als Weltkennung für die Karte zulässig?
func KarteWeltGueltig(welt string) bool { return karteWeltRE.MatchString(welt) }

func karteWelt(welt string) (string, error) {
	if !KarteWeltGueltig(welt) {
		return "", fmt.Errorf("Karte: ungültige Weltkennung %q", shorten(welt, 40))
	}
	return url.PathEscape(welt), nil
}

// KarteManifestRoh holt karte.json der Welt unverändert (höchstens
// karte.MaxManifestBytes). 404: karte.ErrNochNichtErzeugt (der
// *StatusError bleibt erreichbar); 403: kein Mitglied.
func (c *Client) KarteManifestRoh(ctx context.Context, welt string) ([]byte, error) {
	w, err := karteWelt(welt)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, karteManifestTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/karte/"+w+"/manifest.json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Koordinator: %w", err)
	}
	defer drainClose(resp)
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &karte.Gedrosselt{Nach: karte.DrosselWartezeit(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := statusError(req, resp)
		if se.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w", karte.ErrNochNichtErzeugt, se)
		}
		return nil, se
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, karte.MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("Koordinator: Karte: Antwort nicht lesbar: %w", err)
	}
	if len(data) > karte.MaxManifestBytes {
		return nil, fmt.Errorf("%w: Datei zu groß", karte.ErrManifestUngueltig)
	}
	return data, nil
}

// KarteManifest holt und prüft das Manifest der Welt (siehe
// karte.LeseManifest; ein kaputtes ist karte.ErrManifestUngueltig).
func (c *Client) KarteManifest(ctx context.Context, welt string) (*karte.Manifest, error) {
	data, err := c.KarteManifestRoh(ctx, welt)
	if err != nil {
		return nil, err
	}
	return karte.LeseManifest(data)
}

// KarteKachel holt die Kachel k der Welt. etag ist der ETag (Hash) einer
// schon vorhandenen Fassung ("" ohne): Hat der Koordinator sie noch, ist
// Antwort.Unveraendert gesetzt (304) und Daten ist leer. 404: karte.ErrKachelFehlt.
// Antwortet der Koordinator mit 429, ist der Fehler *karte.Gedrosselt (mit
// der Wartezeit aus Retry-After; karte.MitDrossel wiederholt). Höchstens karte.MaxKachelBytes.
func (c *Client) KarteKachel(ctx context.Context, welt string, k karte.Kachel, etag string) (karte.Antwort, error) {
	w, err := karteWelt(welt)
	if err != nil {
		return karte.Antwort{}, err
	}
	if k.Z < 0 || k.Z > karte.MaxStufe {
		return karte.Antwort{}, fmt.Errorf("Karte: ungültige Stufe %d", k.Z)
	}
	ctx, cancel := context.WithTimeout(ctx, karteKachelTimeout)
	defer cancel()
	path := "/v1/karte/" + w + "/t/" + strconv.Itoa(k.Z) + "/" + strconv.Itoa(k.X) + "/" + strconv.Itoa(k.Y) + ".png"
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return karte.Antwort{}, err
	}
	if etag = strings.Trim(etag, `"`); karte.GueltigerHash(etag) {
		req.Header.Set("If-None-Match", `"`+etag+`"`)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return karte.Antwort{}, fmt.Errorf("Koordinator: %w", err)
	}
	defer drainClose(resp)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return karte.Antwort{}, &karte.Gedrosselt{Nach: karte.DrosselWartezeit(resp.Header.Get("Retry-After"))}
	case resp.StatusCode == http.StatusNotModified:
		return karte.Antwort{ETag: etagWert(resp), Unveraendert: true}, nil
	case resp.StatusCode == http.StatusNotFound:
		se := statusError(req, resp)
		return karte.Antwort{}, fmt.Errorf("%w: %w", karte.ErrKachelFehlt, se)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return karte.Antwort{}, statusError(req, resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, karte.MaxKachelBytes+1))
	if err != nil {
		return karte.Antwort{}, fmt.Errorf("Koordinator: Kachel: Antwort nicht lesbar: %w", err)
	}
	if len(data) > karte.MaxKachelBytes {
		return karte.Antwort{}, errors.New("Koordinator: Kachel ist zu groß")
	}
	return karte.Antwort{Daten: data, ETag: etagWert(resp)}, nil
}

// etagWert: der ETag der Antwort ohne Anführungszeichen und „W/“.
func etagWert(resp *http.Response) string {
	e := strings.TrimPrefix(strings.TrimSpace(resp.Header.Get("ETag")), "W/")
	return strings.Trim(e, `"`)
}
