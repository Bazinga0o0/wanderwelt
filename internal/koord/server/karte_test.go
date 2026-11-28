package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/karte"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

// karteQuelle ist die Quelle des Dienstes in diesen Tests: Es wird nie
// gerendert, nur ausgeliefert; nur die Welten braucht Schluss.
type karteQuelle struct{ karte.Quelle }

func (karteQuelle) GroupWorlds() []store.WorldEntry {
	return []store.WorldEntry{{ID: "w1", StoreWorld: "x"}}
}

type karteEnv struct {
	*restoreEnv
	tileA, tileB []byte
}

// newKarteEnv baut einen Koordinator mit Karten-Dienst und einem fertigen
// Kartenstand der Welt w1 (zwei Kacheln: 0/0/0 und 0/-3/7).
func newKarteEnv(t *testing.T) *karteEnv {
	t.Helper()
	e := &karteEnv{restoreEnv: newRestoreEnv(t), tileA: []byte("PNG-A"), tileB: bytes.Repeat([]byte("B"), 5000)}
	dir := filepath.Join(e.dir, "karte", "w1")
	tiles := map[string]map[string]any{}
	put := func(key string, data []byte) {
		p := filepath.Join(dir, "tiles", filepath.FromSlash(key)+".png")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		tiles[key] = map[string]any{"sha256": hex.EncodeToString(sum[:]), "size": len(data)}
	}
	put("0/0/0", e.tileA)
	put("0/-3/7", e.tileB)
	m, _ := json.Marshal(map[string]any{
		"format": 1, "dimension": "overworld", "tile_size": 256, "levels": []int{0, 1, 2, 3, 4, 5, 6},
		"source": "x/3", "generated": "2026-11-27T20:00:00Z",
		"bounds": map[string]any{"0": map[string]int{"x_min": -3, "x_max": 0, "y_min": 0, "y_max": 7}},
		"tiles":  tiles,
	})
	if err := os.WriteFile(filepath.Join(dir, "karte.json"), m, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := karte.Neu(karte.Optionen{Verzeichnis: filepath.Join(e.dir, "karte"), Quelle: karteQuelle{}})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.UseKarte(d)
	e.seed(1) // legt die Welt w1 an
	return e
}

// get schickt eine Anfrage ohne Weiterleitungen und liefert Status, Kopf und Körper.
func (e *restoreEnv) get(as who, path string, hdr ...string) (int, http.Header, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.base+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	setIdentity(req, as)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func TestKarteNurMitglieder(t *testing.T) {
	e := newKarteEnv(t)
	for _, path := range []string{"/v1/karte/w1/manifest.json", "/v1/karte/w1/t/0/0/0.png"} {
		if code, _, body := e.get(stranger, path); code != http.StatusForbidden || bytes.Contains(body, e.tileA) {
			t.Errorf("Nicht-Mitglied %s: %d", path, code)
		}
		if code, _, _ := e.get(nobody, path); code != http.StatusUnauthorized {
			t.Errorf("ohne Identität %s: %d", path, code)
		}
		if code, _, _ := e.get(userB, path); code != http.StatusOK {
			t.Errorf("Mitglied %s: %d", path, code)
		}
		if code, _, _ := e.get(userA, path); code != http.StatusOK {
			t.Errorf("Admin %s: %d", path, code)
		}
	}
}

func TestKarteAuslieferung(t *testing.T) {
	e := newKarteEnv(t)
	code, h, body := e.get(userB, "/v1/karte/w1/t/0/-3/7.png")
	if code != 200 || !bytes.Equal(body, e.tileB) || h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "private, max-age=60" {
		t.Fatalf("Kachel: %d %v", code, h)
	}
	sum := sha256.Sum256(e.tileB)
	if h.Get("ETag") != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Fatalf("ETag: %s", h.Get("ETag"))
	}
	if code, _, body := e.get(userB, "/v1/karte/w1/t/0/-3/7.png", "If-None-Match", h.Get("ETag")); code != 304 || len(body) != 0 {
		t.Fatalf("If-None-Match: %d, %d Bytes", code, len(body))
	}
	if code, _, _ := e.get(userB, "/v1/karte/w1/t/0/-3/7.png", "If-None-Match", `"anders"`); code != 200 {
		t.Fatalf("anderer ETag: %d", code)
	}
	code, h, body = e.get(userB, "/v1/karte/w1/manifest.json")
	var m struct {
		Format int                       `json:"format"`
		Tiles  map[string]map[string]any `json:"tiles"`
	}
	if code != 200 || json.Unmarshal(body, &m) != nil || m.Format != 1 || len(m.Tiles) != 2 || h.Get("ETag") == "" {
		t.Fatalf("Manifest: %d %v", code, h)
	}
	if code, _, _ := e.get(userB, "/v1/karte/w1/manifest.json", "If-None-Match", h.Get("ETag")); code != 304 {
		t.Fatalf("Manifest If-None-Match: %d", code)
	}
	// Fehlendes: 404; unbekannte Welt: 404.
	for _, p := range []string{"/v1/karte/w1/t/0/1/1.png", "/v1/karte/w1/t/3/0/0.png", "/v1/karte/w2/manifest.json", "/v1/karte/w2/t/0/0/0.png"} {
		if code, _, _ := e.get(userB, p); code != http.StatusNotFound {
			t.Errorf("%s: %d, erwartet 404", p, code)
		}
	}
}

func TestKarteUngueltigeZahlenUndPfadtricks(t *testing.T) {
	e := newKarteEnv(t)
	// Eine Datei außerhalb der Kacheln, die nie ausgeliefert werden darf.
	os.WriteFile(filepath.Join(e.dir, "karte", "w1", "state.json"), []byte("GEHEIM-STATE"), 0o644)
	os.WriteFile(filepath.Join(e.dir, "lease.json"), []byte("GEHEIM-LEASE"), 0o644)
	bad := []string{
		"/v1/karte/w1/t/0/0/0.PNG", "/v1/karte/w1/t/0/0/0", "/v1/karte/w1/t/0/0/00.png", "/v1/karte/w1/t/0/-0/0.png",
		"/v1/karte/w1/t/0/+1/0.png", "/v1/karte/w1/t/0/1e2/0.png", "/v1/karte/w1/t/0/0x1/0.png", "/v1/karte/w1/t/a/0/0.png",
		"/v1/karte/w1/t/0/0/99999999999999999999.png", "/v1/karte/w1/t/0/-99999999999999999999/0.png",
		"/v1/karte/w1/t/0/9223372036854775807/0.png", "/v1/karte/w1/t/0/0/1234567890.png",
		"/v1/karte/w1/t/%2e%2e/0/0.png", "/v1/karte/w1/t/0/%2e%2e/0.png", "/v1/karte/w1/t/0/0/%2e%2e.png",
		"/v1/karte/w1/t/0/0/..%2f..%2fstate.json", "/v1/karte/w1/t/0/%2e%2e%2f%2e%2e%2fstate.json/0.png",
		"/v1/karte/w1/t/0/0/0.png%00", "/v1/karte/w1/t/0/0/%00.png", "/v1/karte/w1/t/0/%20/0.png", "/v1/karte/w1/t/0/0/..%5c..%5c0.png",
		"/v1/karte/w1/t/..%5c/0/0.png", "/v1/karte/..%2fw1/manifest.json", "/v1/karte/%2e%2e/manifest.json",
		"/v1/karte/w1/t/0/0/0.png.png", "/v1/karte/w1/t/0/0/.png", "/v1/karte/w1/t/0/0/-.png",
	}
	for _, p := range bad {
		code, _, body := e.get(userB, p)
		if code == http.StatusOK || bytes.Contains(body, []byte("GEHEIM")) || bytes.Equal(body, e.tileA) {
			t.Errorf("%s: %d – darf nichts liefern", p, code)
		}
	}
	// Die rein ungültigen Zahlen antworten 400 (nicht 404/500), kein Absturz.
	for _, p := range []string{
		"/v1/karte/w1/t/0/0/99999999999999999999.png", "/v1/karte/w1/t/a/0/0.png", "/v1/karte/w1/t/0/00/0.png",
		"/v1/karte/w1/t/0/%2e%2e/0.png", "/v1/karte/w1/t/0/9223372036854775807/0.png",
	} {
		if code, _, _ := e.get(userB, p); code != http.StatusBadRequest && code != http.StatusMovedPermanently {
			t.Errorf("%s: %d, erwartet 400 (oder Weiterleitung auf den bereinigten Pfad)", p, code)
		}
	}
	// Nach alldem funktioniert die Auslieferung noch.
	if code, _, body := e.get(userB, "/v1/karte/w1/t/0/0/0.png"); code != 200 || !bytes.Equal(body, e.tileA) {
		t.Fatalf("danach: %d", code)
	}
}

func TestKarteRatenbegrenzung(t *testing.T) {
	e := newKarteEnv(t)
	limited, ok := 0, 0
	for range 400 {
		switch code, h, _ := e.get(userB, "/v1/karte/w1/t/0/0/0.png"); code {
		case 200:
			ok++
		case http.StatusTooManyRequests:
			limited++
			if h.Get("Retry-After") == "" {
				t.Fatal("429 ohne Retry-After")
			}
		default:
			t.Fatalf("Status %d", code)
		}
	}
	if limited == 0 || ok < 100 {
		t.Fatalf("ok=%d, begrenzt=%d: großzügig, aber nicht unbegrenzt erwartet", ok, limited)
	}
	// Ein anderes Mitglied ist nicht betroffen.
	if code, _, _ := e.get(userA, "/v1/karte/w1/t/0/0/0.png"); code != 200 {
		t.Fatalf("anderes Mitglied: %d", code)
	}
}

func TestKarteInStatusWeltenUndGesundheit(t *testing.T) {
	e := newKarteEnv(t)
	var st api.Status
	if code := call(t, e.base, userB, http.MethodGet, "/v1/status", nil, &st); code != 200 {
		t.Fatalf("status: %d", code)
	}
	if len(st.Worlds) != 1 || st.Worlds[0].Karte == nil {
		t.Fatalf("Welten: %+v", st.Worlds)
	}
	k := st.Worlds[0].Karte
	if k.Version != 1 || k.Source != "x/3" || k.Tiles != 2 || len(k.Levels) != 7 || k.Bounds == nil || k.Bounds.XMin != -3 || k.Bounds.YMax != 7 || k.Generated.IsZero() {
		t.Fatalf("Karte im Status: %+v", k)
	}
	var wl api.WorldList
	if code := call(t, e.base, userB, http.MethodGet, "/v1/worlds", nil, &wl); code != 200 || wl.Worlds[0].Karte == nil {
		t.Fatalf("worlds: %d %+v", code, wl)
	}
	// Das JSON trägt den Schlüssel „karte“ mit Vertragsnamen.
	_, _, raw := e.get(userB, "/v1/status")
	if !bytes.Contains(raw, []byte(`"karte":{"version":1,"source":"x/3"`)) {
		t.Errorf("Status-JSON: %s", raw)
	}

	var h api.Health
	if code := call(t, e.base, userA, http.MethodGet, "/v1/health", nil, &h); code != 200 {
		t.Fatalf("health: %d", code)
	}
	if h.Karte == nil || h.Karte.Enabled || h.Karte.Tiles != 2 || h.Karte.SizeBytes < 5000 || h.Karte.Error != "" {
		t.Fatalf("Gesundheit Karte: %+v", h.Karte)
	}
	if code := call(t, e.base, userB, http.MethodGet, "/v1/health", nil, &h); code != http.StatusForbidden {
		t.Fatalf("health als Mitglied: %d", code)
	}
}

func TestKarteNichtEingerichtet(t *testing.T) {
	e := newRestoreEnv(t) // ohne UseKarte
	e.seed(1)
	if code, _, _ := e.get(userB, "/v1/karte/w1/manifest.json"); code != http.StatusNotFound {
		t.Fatalf("Manifest: %d", code)
	}
	if code, _, _ := e.get(userB, "/v1/karte/w1/t/0/0/0.png"); code != http.StatusNotFound {
		t.Fatalf("Kachel: %d", code)
	}
	if code, _, _ := e.get(stranger, "/v1/karte/w1/manifest.json"); code != http.StatusForbidden {
		t.Fatalf("Nicht-Mitglied: %d", code)
	}
	_, _, raw := e.get(userB, "/v1/status")
	if bytes.Contains(raw, []byte(`"karte"`)) {
		t.Errorf("ohne Karte kein Schlüssel im Status: %s", raw)
	}
	var h api.Health
	if code := call(t, e.base, userA, http.MethodGet, "/v1/health", nil, &h); code != 200 || h.Karte != nil {
		t.Fatalf("Gesundheit ohne Karte: %d %+v", code, h.Karte)
	}
	if strings.Contains(string(raw), "karte") {
		t.Errorf("Status: %s", raw)
	}
}
