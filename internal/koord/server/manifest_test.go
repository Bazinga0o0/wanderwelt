package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

// manifestEnv ist ein Koordinator mit Versions-Manifest in dir.
type manifestEnv struct {
	srv      *server.Server
	base     string
	dir      string
	manifest string
}

func newManifestEnv(t *testing.T, manifest string) *manifestEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	writeFile(t, path, []byte(manifest))
	cfg := &config.Config{
		DataDir:       dir,
		StorageBudget: 1 << 30,
		ManifestFile:  path,
		Members:       []config.Member{{Email: emailA, Minecraft: "Alice", Admin: true}},
	}
	cfg.Auth.Mode = "dev"
	lm, err := lease.New(filepath.Join(dir, "lease.json"), &fakeClock{now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, lm, st, &fakePortHolder{}, fakeUUIDs{"Alice": uuidA})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &manifestEnv{srv: srv, base: ts.URL, dir: dir, manifest: path}
}

func (e *manifestEnv) get(t *testing.T) api.Manifest {
	t.Helper()
	var m api.Manifest
	if code := call(t, e.base, userA, http.MethodGet, "/v1/manifest", nil, &m); code != http.StatusOK {
		t.Fatalf("GET /v1/manifest: %d", code)
	}
	return m
}

// starterManifest ist ein Manifest mit Minecraft-Version mc und dem Eintrag
// starter (leer: keiner).
func starterManifest(mc, starter string) string {
	return fmt.Sprintf("minecraft_version: %q\n%s", mc, starter)
}

func starterEntry(version, url, sha string, size int64) string {
	s := fmt.Sprintf("starter:\n  version: %q\n  url: %q\n  sha256: %q\n", version, url, sha)
	if size != 0 {
		s += fmt.Sprintf("  size: %d\n", size)
	}
	return s
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Den Eintrag starter liefert der Koordinator nur, wenn die Datei im
// Artefakt-Ordner liegt und Hash und Größe passen. Sonst steht ein Fehler
// im Log, und das Manifest geht ohne starter hinaus (alles andere bleibt).
func TestManifestStarterChecked(t *testing.T) {
	exe := []byte("MZ wanderwelt 0.5.0")
	sum := hexSHA256(exe)
	for _, tc := range []struct {
		name    string
		entry   string
		put     bool   // Datei ablegen
		wantErr string // Befund im Log; leer: starter wird ausgeliefert
	}{
		{"passt", starterEntry("0.5.0", "wanderwelt:wanderwelt-0.5.0.exe", sum, int64(len(exe))), true, ""},
		{"passt mit v und sha256:", starterEntry("v0.5.0", "wanderwelt:wanderwelt-0.5.0.exe", "sha256:"+strings.ToUpper(sum), 0), true, ""},
		{"Datei fehlt", starterEntry("0.5.0", "wanderwelt:wanderwelt-0.5.0.exe", sum, 0), false, "wanderwelt-0.5.0.exe"},
		{"Hash falsch", starterEntry("0.5.0", "wanderwelt:wanderwelt-0.5.0.exe", hexSHA256([]byte("anders")), 0), true, "im Manifest steht sha256"},
		{"Größe falsch", starterEntry("0.5.0", "wanderwelt:wanderwelt-0.5.0.exe", sum, int64(len(exe))+1), true, "im Manifest steht size"},
		{"externe URL", starterEntry("0.5.0", "https://example.org/wanderwelt.exe", sum, 0), true, "erwartet wanderwelt:<datei>"},
		{"Pfad im Namen", starterEntry("0.5.0", "wanderwelt:../wanderwelt-0.5.0.exe", sum, 0), true, "erwartet wanderwelt:<datei>"},
		{"keine Release-Version", starterEntry("0.0.0-SNAPSHOT-abc1234", "wanderwelt:wanderwelt-0.5.0.exe", sum, 0), true, "keine Release-Version"},
		{"Hash kein Hex", starterEntry("0.5.0", "wanderwelt:wanderwelt-0.5.0.exe", "xyz", 0), true, "64 Hex-Zeichen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			e := newManifestEnv(t, starterManifest("26.2", tc.entry))
			if tc.put {
				writeFile(t, filepath.Join(server.ArtifactDir(e.dir), "wanderwelt-0.5.0.exe"), exe)
			}
			if err := e.srv.ReloadManifest(); err != nil { // wie beim Start: Datei liegt schon
				t.Fatal(err)
			}
			m := e.get(t)
			if m.MinecraftVersion != "26.2" {
				t.Fatalf("minecraft_version %q", m.MinecraftVersion)
			}
			if tc.wantErr == "" {
				if m.Starter == nil || m.Starter.URL != "wanderwelt:wanderwelt-0.5.0.exe" {
					t.Fatalf("starter fehlt: %+v\n%s", m.Starter, logs)
				}
				if strings.Contains(logs.String(), "STARTER-UPDATE NICHT AUSLIEFERBAR") {
					t.Fatalf("Fehler im Log, obwohl alles passt:\n%s", logs)
				}
				return
			}
			if m.Starter != nil {
				t.Fatalf("starter ausgeliefert: %+v", m.Starter)
			}
			l := logs.String()
			if !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "STARTER-UPDATE NICHT AUSLIEFERBAR") || !strings.Contains(l, tc.wantErr) {
				t.Fatalf("Log ohne Fehler zu %q:\n%s", tc.wantErr, l)
			}
		})
	}

	// Auch ohne ausdrückliches Laden (erste Anfrage) wird geprüft.
	t.Run("erste Anfrage", func(t *testing.T) {
		logs := captureLog(t)
		e := newManifestEnv(t, starterManifest("26.2", starterEntry("0.5.0", "wanderwelt:fehlt.exe", sum, 0)))
		if m := e.get(t); m.Starter != nil {
			t.Fatalf("starter ausgeliefert: %+v", m.Starter)
		}
		if !strings.Contains(logs.String(), "STARTER-UPDATE NICHT AUSLIEFERBAR") {
			t.Fatalf("kein Fehler im Log:\n%s", logs)
		}
	})
}

// Neuladen (SIGHUP → ReloadManifest): Erst danach gilt eine geänderte
// Datei; eine kaputte ändert nichts (das bisherige Manifest gilt weiter),
// ein nicht auslieferbarer starter fällt weg.
func TestManifestReloadOnSighup(t *testing.T) {
	logs := captureLog(t)
	e := newManifestEnv(t, starterManifest("26.2", ""))
	if m := e.get(t); m.MinecraftVersion != "26.2" || m.Starter != nil {
		t.Fatalf("Start: %+v", m)
	}

	exe := []byte("MZ wanderwelt 0.5.0")
	writeFile(t, filepath.Join(server.ArtifactDir(e.dir), "wanderwelt-0.5.0.exe"), exe)
	writeFile(t, e.manifest, []byte(starterManifest("26.3",
		starterEntry("0.5.0", "wanderwelt:wanderwelt-0.5.0.exe", hexSHA256(exe), int64(len(exe))))))
	if m := e.get(t); m.MinecraftVersion != "26.2" || m.Starter != nil {
		t.Fatalf("vor dem Neuladen schon geändert: %+v", m)
	}
	if err := e.srv.ReloadManifest(); err != nil {
		t.Fatal(err)
	}
	m := e.get(t)
	want := api.StarterRelease{Version: "0.5.0", URL: "wanderwelt:wanderwelt-0.5.0.exe", SHA256: hexSHA256(exe), Size: int64(len(exe))}
	if m.MinecraftVersion != "26.3" || m.Starter == nil || *m.Starter != want {
		t.Fatalf("nach dem Neuladen: %+v %+v", m, m.Starter)
	}

	// Kaputte Datei: Fehler, das bisherige gilt weiter.
	for _, bad := range []string{"minecraft_version: [kaputt\n", "unbekannt: 1\nminecraft_version: \"26.4\"\n", ""} {
		writeFile(t, e.manifest, []byte(bad))
		if err := e.srv.ReloadManifest(); err == nil {
			t.Fatalf("%q: kein Fehler", bad)
		}
		if m := e.get(t); m.MinecraftVersion != "26.3" || m.Starter == nil || *m.Starter != want {
			t.Fatalf("%q: Manifest geändert: %+v", bad, m)
		}
	}
	if !strings.Contains(logs.String(), "VERSIONS-MANIFEST NICHT NEU GELADEN") {
		t.Fatalf("kein Fehler im Log:\n%s", logs)
	}

	// Neue Version, deren Datei fehlt: ohne starter, der Rest gilt.
	writeFile(t, e.manifest, []byte(starterManifest("26.3",
		starterEntry("0.6.0", "wanderwelt:wanderwelt-0.6.0.exe", hexSHA256([]byte("0.6.0")), 0))))
	if err := e.srv.ReloadManifest(); err != nil {
		t.Fatal(err)
	}
	if m := e.get(t); m.MinecraftVersion != "26.3" || m.Starter != nil {
		t.Fatalf("fehlende Datei: %+v %+v", m, m.Starter)
	}
}

// Während ein Neuladen das neue Manifest prüft (Artefakte hashen), liefert
// /v1/manifest sofort den bisherigen Stand, statt auf die Prüfung zu warten;
// danach gilt der neue.
func TestManifestReloadDoesNotBlockRequests(t *testing.T) {
	e := newManifestEnv(t, starterManifest("26.2", ""))
	var armed atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e.srv.SetManifestCheckHook(func() {
		if armed.Load() {
			entered <- struct{}{}
			<-release
		}
	})
	if m := e.get(t); m.MinecraftVersion != "26.2" {
		t.Fatalf("Start: %+v", m)
	}

	writeFile(t, e.manifest, []byte(starterManifest("26.3", "")))
	armed.Store(true)
	errc := make(chan error, 1)
	go func() { errc <- e.srv.ReloadManifest() }()
	released := false
	defer func() {
		if !released {
			close(release)
			<-errc
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Neuladen prüft nicht")
	}

	// Die Prüfung hängt: Anfragen bekommen trotzdem sofort den alten Stand.
	req, err := http.NewRequest(http.MethodGet, e.base+"/v1/manifest", nil)
	if err != nil {
		t.Fatal(err)
	}
	setIdentity(req, userA)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("/v1/manifest wartet auf das Neuladen: %v", err)
	}
	var m api.Manifest
	err = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || m.MinecraftVersion != "26.2" {
		t.Fatalf("während des Neuladens: %d %+v %v", resp.StatusCode, m, err)
	}

	close(release)
	released = true
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if m := e.get(t); m.MinecraftVersion != "26.3" {
		t.Fatalf("nach dem Neuladen: %+v", m)
	}
}

// Ungültige configs beim Neuladen: Fehler, das bisherige Manifest gilt
// weiter; gültige werden ausgeliefert.
func TestManifestConfigsReload(t *testing.T) {
	e := newManifestEnv(t, starterManifest("26.2", ""))
	h := "sha256:" + strings.Repeat("0", 64)

	writeFile(t, e.manifest, []byte(starterManifest("26.3",
		"configs:\n  - {path: config/a.json, url: \"https://example.org/a.json\", hash: \""+h+"\"}\n")))
	if err := e.srv.ReloadManifest(); err != nil {
		t.Fatal(err)
	}
	m := e.get(t)
	if m.MinecraftVersion != "26.3" || len(m.Configs) != 1 || m.Configs[0].Path != "config/a.json" {
		t.Fatalf("configs nicht ausgeliefert: %+v", m)
	}

	writeFile(t, e.manifest, []byte(starterManifest("26.4",
		"configs:\n  - {path: config/../server.properties, url: \"https://example.org/a\", hash: \""+h+"\"}\n")))
	if err := e.srv.ReloadManifest(); err == nil || !strings.Contains(err.Error(), "config/") {
		t.Fatalf("ungültiger Pfad: %v", err)
	}
	if m := e.get(t); m.MinecraftVersion != "26.3" || len(m.Configs) != 1 {
		t.Fatalf("Manifest hat sich trotz Fehler geändert: %+v", m)
	}
}
