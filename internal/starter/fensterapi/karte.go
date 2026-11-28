package fensterapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
)

// Reiter „Karte“ (Mitglieder): Die Instanz holt Manifest und Kacheln mit dem
// Token der Gruppe vom Koordinator und reicht sie durch; das Fenster sieht
// das Token nie.
//
//	GET /v1/kartemanifest?welt=<id>                → karte.json (JSON, unverändert)
//	GET /v1/kartekachel?welt=<id>&z=&x=&y=          → PNG; ETag = sha256 der Kachel,
//	                                                  If-None-Match → 304
//
// 429 mit Retry-After heißt: Der Koordinator drosselt (karte.Gedrosselt).
// Fehler wie sonst als {"fehler": …}; 404 mit diesem Körper heißt: Die Karte
// ist noch nicht erzeugt bzw. die Kachel gibt es nicht.

// KarteManifestRoh holt karte.json der Welt (nur Mitglieder).
func (c *Client) KarteManifestRoh(ctx context.Context, welt string) ([]byte, error) {
	resp, err := c.doKarte(ctx, "/v1/kartemanifest?welt="+url.QueryEscape(welt), "", karte.ErrNochNichtErzeugt)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, karte.MaxManifestBytes+1))
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	if len(data) > karte.MaxManifestBytes {
		return nil, fmt.Errorf("%w: Datei zu groß", karte.ErrManifestUngueltig)
	}
	return data, nil
}

// KarteManifest holt und prüft das Manifest der Welt.
func (c *Client) KarteManifest(ctx context.Context, welt string) (*karte.Manifest, error) {
	data, err := c.KarteManifestRoh(ctx, welt)
	if err != nil {
		return nil, err
	}
	return karte.LeseManifest(data)
}

// KarteKachel holt die Kachel k der Welt (etag: Hash einer vorhandenen
// Fassung, "" ohne; bei 304 ist Antwort.Unveraendert gesetzt).
func (c *Client) KarteKachel(ctx context.Context, welt string, k karte.Kachel, etag string) (karte.Antwort, error) {
	q := url.Values{"welt": {welt}, "z": {strconv.Itoa(k.Z)}, "x": {strconv.Itoa(k.X)}, "y": {strconv.Itoa(k.Y)}}
	resp, err := c.doKarte(ctx, "/v1/kartekachel?"+q.Encode(), etag, karte.ErrKachelFehlt)
	if err != nil {
		return karte.Antwort{}, err
	}
	defer resp.Body.Close()
	e := strings.Trim(strings.TrimPrefix(resp.Header.Get("ETag"), "W/"), `"`)
	if resp.StatusCode == http.StatusNotModified {
		return karte.Antwort{ETag: e, Unveraendert: true}, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, karte.MaxKachelBytes+1))
	if err != nil {
		return karte.Antwort{}, c.transportError(ctx, err)
	}
	if len(data) > karte.MaxKachelBytes {
		return karte.Antwort{}, fmt.Errorf("Steuerung: Kachel ist zu groß")
	}
	return karte.Antwort{Daten: data, ETag: e}, nil
}

// doKarte ist do für die Karte: 200 und 304 gehen durch; ein 404 mit
// {"fehler": …} ist fehlt (mit dem Text der Instanz), jede andere Antwort
// ein Fehler wie bei do. etag (nur Hexziffern) geht als If-None-Match mit.
func (c *Client) doKarte(ctx context.Context, path, etag string, fehlt error) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if etag = strings.Trim(etag, `"`); karte.GueltigerHash(etag) {
		req.Header.Set("If-None-Match", `"`+etag+`"`)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
		return resp, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &karte.Gedrosselt{Nach: karte.DrosselWartezeit(resp.Header.Get("Retry-After"))}
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if resp.StatusCode == http.StatusNotFound {
		var e struct {
			Fehler string `json:"fehler"`
		}
		if json.Unmarshal(data, &e) == nil && e.Fehler != "" {
			return nil, &gemeldet{art: fehlt, text: e.Fehler}
		}
	}
	return nil, responseError(resp.StatusCode, data)
}

// gemeldet ist ein Fehler mit dem Text der Instanz, der zugleich seine Art
// (etwa karte.ErrKachelFehlt) für errors.Is trägt.
type gemeldet struct {
	art  error
	text string
}

func (e *gemeldet) Error() string { return e.text }
func (e *gemeldet) Unwrap() error { return e.art }
