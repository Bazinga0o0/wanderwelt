package client

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte/kartetest"
)

const karteToken = "geheim-token-1234567890"

func karteClient(t *testing.T) (*Client, *kartetest.Koordinator) {
	t.Helper()
	k := kartetest.Neu(t, karteToken, "w1")
	k.Setze(karte.Kachel{Z: 0, X: -2, Y: 1}, kartetest.PNG(t, 1))
	k.Setze(karte.Kachel{Z: 0, X: 0, Y: 0}, kartetest.PNG(t, 2))
	return New(&group.Group{Coordinator: k.URL(), Token: karteToken}), k
}

func TestKarteManifestUndKachel(t *testing.T) {
	c, k := karteClient(t)
	m, err := c.KarteManifest(t.Context(), "w1")
	if err != nil || m.Anzahl() != 2 {
		t.Fatalf("KarteManifest = %+v, %v", m, err)
	}
	hash, ok := m.Hash(karte.Kachel{Z: 0, X: -2, Y: 1})
	if !ok {
		t.Fatal("Kachel -2/1 fehlt im Manifest")
	}
	a, err := c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: -2, Y: 1}, "")
	if err != nil || a.Unveraendert || karte.HashVon(a.Daten) != hash || a.ETag != hash {
		t.Fatalf("KarteKachel = %d Bytes, ETag %q, unverändert %v, %v (Hash %s)", len(a.Daten), a.ETag, a.Unveraendert, err, hash)
	}
	if _, err := karte.DekodiereKachel(a.Daten); err != nil {
		t.Errorf("die Kachel ist kein gültiges Bild: %v", err)
	}
	if k.OhneToken() != 0 {
		t.Error("Anfragen ohne Token")
	}
}

func TestKarteKachelETag304(t *testing.T) {
	c, k := karteClient(t)
	kachel := karte.Kachel{Z: 0, X: 0, Y: 0}
	a, err := c.KarteKachel(t.Context(), "w1", kachel, "")
	if err != nil {
		t.Fatal(err)
	}
	// Mit dem ETag der vorhandenen Fassung: 304, keine Daten.
	b, err := c.KarteKachel(t.Context(), "w1", kachel, a.ETag)
	if err != nil || !b.Unveraendert || len(b.Daten) != 0 || b.ETag != a.ETag {
		t.Fatalf("mit ETag: %+v, %v", b, err)
	}
	// Auch in Anführungszeichen (so steht er im Kopf) funktioniert es.
	if b, err = c.KarteKachel(t.Context(), "w1", kachel, `"`+a.ETag+`"`); err != nil || !b.Unveraendert {
		t.Fatalf("mit ETag in Anführungszeichen: %+v, %v", b, err)
	}
	// Die Kachel hat sich geändert: der alte ETag liefert die neue Fassung.
	k.Setze(kachel, kartetest.PNG(t, 77))
	d, err := c.KarteKachel(t.Context(), "w1", kachel, a.ETag)
	if err != nil || d.Unveraendert || len(d.Daten) == 0 || d.ETag == a.ETag {
		t.Fatalf("nach Änderung: %+v, %v", d, err)
	}
	// Ein Müll-ETag wird gar nicht erst geschickt (kein Kopf-Einschleusen).
	var kopf string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kopf = r.Header.Get("If-None-Match")
		w.Write(kartetest.PNG(t, 3))
	}))
	defer ts.Close()
	c2 := New(&group.Group{Coordinator: ts.URL, Token: karteToken})
	if _, err := c2.KarteKachel(t.Context(), "w1", kachel, "x\r\nX-Evil: 1"); err != nil || kopf != "" {
		t.Errorf("Müll-ETag: Kopf %q, %v", kopf, err)
	}
}

func TestKarte404UndFehler(t *testing.T) {
	c, k := karteClient(t)
	// Fehlende Kachel: ErrKachelFehlt (und der StatusError bleibt erreichbar).
	_, err := c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: 5, Y: 5}, "")
	var se *StatusError
	if !errors.Is(err, karte.ErrKachelFehlt) || !errors.As(err, &se) || se.StatusCode != 404 {
		t.Errorf("fehlende Kachel: %v", err)
	}
	// Noch keine Karte: ErrNochNichtErzeugt.
	k.OhneKarte(true)
	if _, err := c.KarteManifest(t.Context(), "w1"); !errors.Is(err, karte.ErrNochNichtErzeugt) {
		t.Errorf("ohne Karte: %v", err)
	}
	k.OhneKarte(false)
	// Koordinator überlastet: StatusError 503, kein Sondertyp.
	k.Ausfall(true)
	_, err = c.KarteManifest(t.Context(), "w1")
	if !errors.As(err, &se) || se.StatusCode != 503 || errors.Is(err, karte.ErrNochNichtErzeugt) {
		t.Errorf("Ausfall: %v", err)
	}
	if _, err = c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: 0, Y: 0}, ""); !errors.As(err, &se) || se.StatusCode != 503 {
		t.Errorf("Ausfall Kachel: %v", err)
	}
	k.Ausfall(false)
	// Falsches Token: 403.
	fremd := New(&group.Group{Coordinator: k.URL(), Token: "falsch"})
	if _, err = fremd.KarteManifest(t.Context(), "w1"); !errors.As(err, &se) || se.StatusCode != 403 {
		t.Errorf("falsches Token: %v", err)
	}
	// Nicht erreichbar.
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	weg := New(&group.Group{Coordinator: url, Token: karteToken})
	if _, err = weg.KarteManifest(t.Context(), "w1"); err == nil {
		t.Error("kein Fehler bei nicht erreichbarem Koordinator")
	}
}

func TestKarteManifestKaputt(t *testing.T) {
	for name, body := range map[string]string{
		"kein JSON": "hallo",
		"Format 9":  `{"format":9,"tile_size":256,"tiles":{"0/0/0":{"sha256":"` + strings.Repeat("a", 64) + `","size":1}}}`,
		"leer":      `{"format":1,"tile_size":256,"tiles":{}}`,
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		c := New(&group.Group{Coordinator: ts.URL, Token: karteToken})
		if _, err := c.KarteManifest(t.Context(), "w1"); !errors.Is(err, karte.ErrManifestUngueltig) {
			t.Errorf("%s: %v", name, err)
		}
		ts.Close()
	}
}

func TestKarteGroessenGrenzen(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), karte.MaxKachelBytes+10))
	}))
	defer ts.Close()
	c := New(&group.Group{Coordinator: ts.URL, Token: karteToken})
	if _, err := c.KarteKachel(t.Context(), "w1", karte.Kachel{}, ""); err == nil {
		t.Error("zu große Kachel wurde angenommen")
	}
}

func TestKarteWeltUndKachelPruefung(t *testing.T) {
	var pfade []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pfade = append(pfade, r.URL.RequestURI())
		w.WriteHeader(404)
	}))
	defer ts.Close()
	c := New(&group.Group{Coordinator: ts.URL, Token: karteToken})
	for _, welt := range []string{"", "../w1", "w1/../../x", "w1?x=1", "w 1", `w1\x`, strings.Repeat("a", 65), "-w1", "w1%2f.."} {
		if _, err := c.KarteManifestRoh(t.Context(), welt); err == nil {
			t.Errorf("Welt %q angenommen", welt)
		}
		if _, err := c.KarteKachel(t.Context(), welt, karte.Kachel{}, ""); err == nil {
			t.Errorf("Welt %q angenommen (Kachel)", welt)
		}
	}
	if _, err := c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 7}, ""); err == nil {
		t.Error("Stufe 7 angenommen")
	}
	if len(pfade) != 0 {
		t.Errorf("ungültige Anfragen gingen raus: %v", pfade)
	}
	// Eine gültige Anfrage landet auf genau dem Pfad des Vertrags (negative Zahlen inklusive).
	c.KarteKachel(t.Context(), "w2", karte.Kachel{Z: 3, X: -4, Y: -5}, "")
	c.KarteManifestRoh(t.Context(), "w2")
	if len(pfade) != 2 || pfade[0] != "/v1/karte/w2/t/3/-4/-5.png" || pfade[1] != "/v1/karte/w2/manifest.json" {
		t.Errorf("Pfade = %v", pfade)
	}
}

// Das Token steht nie im Log, in keiner Fehlermeldung und nie in der URL.
func TestKarteTokenNichtImLog(t *testing.T) {
	var logBuf bytes.Buffer
	alt, altFlags := log.Writer(), log.Flags()
	log.SetOutput(&logBuf)
	defer log.SetOutput(alt)
	defer log.SetFlags(altFlags)

	c, k := karteClient(t)
	var alle []string
	merke := func(err error) {
		if err != nil {
			alle = append(alle, err.Error())
		}
	}
	_, err := c.KarteManifest(t.Context(), "w1")
	merke(err)
	_, err = c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: 0, Y: 0}, "")
	merke(err)
	_, err = c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: 9, Y: 9}, "") // 404
	merke(err)
	k.Ausfall(true)
	_, err = c.KarteManifest(t.Context(), "w1") // 503
	merke(err)
	k.Ausfall(false)
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	_, err = c.KarteKachel(ctx, "w1", karte.Kachel{}, "") // Zeitüberschreitung
	merke(err)
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	_, err = New(&group.Group{Coordinator: url, Token: karteToken}).KarteManifest(t.Context(), "w1") // nicht erreichbar
	merke(err)
	if len(alle) < 4 {
		t.Fatalf("zu wenige Fehlerfälle erzeugt: %v", alle)
	}
	for _, s := range append(alle, logBuf.String()) {
		if strings.Contains(s, karteToken) {
			t.Errorf("Token in Text oder Log: %q", s)
		}
	}
	for _, p := range k.Zugriffe() {
		if strings.Contains(p, karteToken) {
			t.Errorf("Token in der URL: %q", p)
		}
	}
}

// Der Koordinator drosselt mit 429 und Retry-After: kein gewöhnlicher Fehler,
// sondern *karte.Gedrosselt mit der Wartezeit; MitDrossel wiederholt.
func TestKarte429Drosselung(t *testing.T) {
	c, k := karteClient(t)
	k.Drosseln(2)
	_, err := c.KarteManifestRoh(t.Context(), "w1")
	var g *karte.Gedrosselt
	if !errors.As(err, &g) || g.Nach != time.Second || !errors.Is(err, karte.ErrGedrosselt) {
		t.Fatalf("Manifest: %v", err)
	}
	_, err = c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: 0, Y: 0}, "")
	if !errors.As(err, &g) || g.Nach != time.Second {
		t.Fatalf("Kachel: %v", err)
	}
	// Danach geht es wieder.
	alt := karte.MaxDrosselWarte
	karte.MaxDrosselWarte = 5 * time.Millisecond
	defer func() { karte.MaxDrosselWarte = alt }()
	k.Drosseln(3)
	a, err := karte.MitDrossel(t.Context(), func() (karte.Antwort, error) {
		return c.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: 0, Y: 0}, "")
	})
	if err != nil || len(a.Daten) == 0 || k.Gedrosselt() != 5 {
		t.Errorf("nach Drosselung: %d Bytes, %v, %d×429", len(a.Daten), err, k.Gedrosselt())
	}
}

// Ohne Identität antwortet der Koordinator mit 401: ein StatusError, kein Sonderfall.
func TestKarteOhneIdentitaet401(t *testing.T) {
	_, k := karteClient(t)
	c := New(&group.Group{Coordinator: k.URL()}) // kein Token
	var se *StatusError
	if _, err := c.KarteManifest(t.Context(), "w1"); !errors.As(err, &se) || se.StatusCode != 401 {
		t.Errorf("ohne Token: %v", err)
	}
}
