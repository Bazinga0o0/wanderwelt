package app

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte/kartetest"
)

const kartenToken = "karten-token-0123456789"

// Manifest und Kacheln gehen vom Koordinator durch die Steuerung ans Fenster:
// mit ETag/304, Fehlern als deutscher Text und ohne dass das Token das
// Fenster oder das Log erreicht.
func TestFensterKarte(t *testing.T) {
	probeAnzeige(t)
	var logBuf bytes.Buffer
	altOut, altFlags := log.Writer(), log.Flags()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(altOut); log.SetFlags(altFlags) })

	k := kartetest.Neu(t, kartenToken, "w1")
	kachel := karte.Kachel{Z: 0, X: -3, Y: 2}
	k.Setze(kachel, kartetest.PNG(t, 5))
	k.Setze(karte.Kachel{Z: 1, X: -2, Y: 1}, kartetest.PNG(t, 6))
	ft := startFenster(t, client.New(&group.Group{Coordinator: k.URL(), Token: kartenToken}))

	m, err := ft.fc.KarteManifest(t.Context(), "w1")
	if err != nil || m.Anzahl() != 2 {
		t.Fatalf("KarteManifest = %+v, %v", m, err)
	}
	hash, _ := m.Hash(kachel)
	a, err := ft.fc.KarteKachel(t.Context(), "w1", kachel, "")
	if err != nil || karte.HashVon(a.Daten) != hash || a.ETag != hash {
		t.Fatalf("KarteKachel = %d Bytes, ETag %q, %v", len(a.Daten), a.ETag, err)
	}
	// ETag → 304 bis ans Fenster.
	b, err := ft.fc.KarteKachel(t.Context(), "w1", kachel, hash)
	if err != nil || !b.Unveraendert || len(b.Daten) != 0 {
		t.Fatalf("mit ETag: %+v, %v", b, err)
	}
	// Fehlende Kachel und fehlende Karte haben ihre eigenen Fehler samt Text.
	_, err = ft.fc.KarteKachel(t.Context(), "w1", karte.Kachel{Z: 0, X: 40, Y: 40}, "")
	if !errors.Is(err, karte.ErrKachelFehlt) || !strings.Contains(err.Error(), "gibt es nicht") {
		t.Errorf("fehlende Kachel: %v", err)
	}
	k.OhneKarte(true)
	_, err = ft.fc.KarteManifest(t.Context(), "w1")
	if !errors.Is(err, karte.ErrNochNichtErzeugt) || !strings.Contains(err.Error(), "noch nicht erzeugt") {
		t.Errorf("ohne Karte: %v", err)
	}
	k.OhneKarte(false)
	// Drosselung (429) geht als Wartezeit bis ans Fenster, kein Fehlertext.
	k.Drosseln(2)
	_, err = ft.fc.KarteKachel(t.Context(), "w1", kachel, "")
	var g *karte.Gedrosselt
	if !errors.As(err, &g) || g.Nach != time.Second {
		t.Errorf("Kachel gedrosselt: %v", err)
	}
	if _, err = ft.fc.KarteManifest(t.Context(), "w1"); !errors.Is(err, karte.ErrGedrosselt) {
		t.Errorf("Manifest gedrosselt: %v", err)
	}
	if _, err = ft.fc.KarteManifest(t.Context(), "w1"); err != nil {
		t.Errorf("nach der Drosselung: %v", err)
	}
	// Koordinator weg: Text für den Nutzer, kein 404-Sonderfall.
	k.Ausfall(true)
	_, err = ft.fc.KarteManifest(t.Context(), "w1")
	if err == nil || errors.Is(err, karte.ErrNochNichtErzeugt) {
		t.Errorf("Ausfall: %v", err)
	}
	k.Ausfall(false)

	// Ungültige Eingaben werden schon an der Steuerung abgewiesen; der
	// Koordinator sieht nichts davon.
	vor := len(k.Zugriffe())
	for _, pfad := range []string{
		"/v1/kartemanifest", "/v1/kartemanifest?welt=../w1", "/v1/kartemanifest?welt=w1/../x",
		"/v1/kartekachel?welt=w1", "/v1/kartekachel?welt=w1&z=0&x=1", "/v1/kartekachel?welt=w1&z=7&x=0&y=0",
		"/v1/kartekachel?welt=w1&z=0&x=01&y=0", "/v1/kartekachel?welt=w1&z=0&x=%2B1&y=0", "/v1/kartekachel?welt=w1&z=0&x=../1&y=0",
		"/v1/kartekachel?welt=w1&z=0&x=1&y=0/0", "/v1/kartekachel?welt=w1&z=0&x=99999999999&y=0", "/v1/kartekachel?welt=..&z=0&x=0&y=0",
	} {
		code, _, body := ft.roh(t, http.MethodGet, pfad, ft.host, "Bearer "+ft.token, "")
		if code != http.StatusBadRequest {
			t.Errorf("GET %s = %d %s, want 400", pfad, code, body)
		}
	}
	if n := len(k.Zugriffe()) - vor; n != 0 {
		t.Errorf("%d ungültige Anfragen erreichten den Koordinator", n)
	}
	// Ohne Token der Steuerung: 403 (wie alle Endpunkte).
	if code, _, _ := ft.roh(t, http.MethodGet, "/v1/kartemanifest?welt=w1", ft.host, "", ""); code != http.StatusForbidden {
		t.Errorf("ohne Token: %d", code)
	}
	// Das Token des Koordinators steht weder im Log noch in einer Antwort.
	if strings.Contains(logBuf.String(), kartenToken) || strings.Contains(logBuf.String(), ft.token) {
		t.Errorf("Token im Log: %s", logBuf.String())
	}
	if k.OhneToken() != 0 {
		t.Errorf("%d Anfragen an den Koordinator ohne Token", k.OhneToken())
	}
}

func TestFensterKarteOhneClient(t *testing.T) {
	probeAnzeige(t)
	ft := startFenster(t, nil)
	if _, err := ft.fc.KarteManifest(t.Context(), "w1"); err == nil || !strings.Contains(err.Error(), "startet noch") {
		t.Errorf("ohne Koordinator-Client: %v", err)
	}
}
