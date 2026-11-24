package server_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// get schickt GET path als as und liefert Status, Körper und Antwort.
func get(t *testing.T, base string, as who, path string, header ...string) (int, []byte, *http.Response) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	setIdentity(req, as)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, resp
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// I3: GET /v1/artifacts/{name} liefert Dateien aus DataDir/artifacts – nur
// an Mitglieder, nur einfache Namen, nie etwas außerhalb, kein Listing.
func TestArtifactEndpoint(t *testing.T) {
	srv, _, _, dir := newServerIn(t, t.TempDir(), "dev", fakeUUIDs{})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	content := []byte("tunnel-client für Windows")
	writeFile(t, filepath.Join(server.ArtifactDir(dir), "tunnel.exe"), content)
	writeFile(t, filepath.Join(dir, "geheim.txt"), []byte("GEHEIM"))
	if err := os.MkdirAll(filepath.Join(server.ArtifactDir(dir), "ordner"), 0o755); err != nil {
		t.Fatal(err)
	}

	code, body, resp := get(t, ts.URL, userB, "/v1/artifacts/tunnel.exe")
	if code != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("Mitglied: %d %q", code, body)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(content)) {
		t.Fatalf("Content-Length %q, erwartet %d", cl, len(content))
	}
	if code, body, _ := get(t, ts.URL, userB, "/v1/artifacts/tunnel.exe", "Range", "bytes=0-5"); code != http.StatusPartialContent || string(body) != "tunnel" {
		t.Fatalf("Range: %d %q", code, body)
	}
	if code, _, _ := get(t, ts.URL, stranger, "/v1/artifacts/tunnel.exe"); code != http.StatusForbidden {
		t.Fatalf("Nicht-Mitglied: %d, erwartet 403", code)
	}
	for _, p := range []string{
		"/v1/artifacts/..%2Fgeheim.txt",
		"/v1/artifacts/../geheim.txt",
		"/v1/artifacts/..",
		"/v1/artifacts/%2E%2E",
		"/v1/artifacts/a%5Cb",
		"/v1/artifacts/ordner",
		"/v1/artifacts/",
		"/v1/artifacts/fehlt.exe",
	} {
		code, body, _ := get(t, ts.URL, userA, p)
		if code == http.StatusOK || bytes.Contains(body, []byte("GEHEIM")) {
			t.Errorf("%s: %d %q, erwartet Ablehnung", p, code, body)
		}
	}
	if code, _, _ := get(t, ts.URL, userA, "/v1/artifacts/..%2Fgeheim.txt"); code != http.StatusBadRequest {
		t.Errorf("..%%2Fgeheim.txt: %d, erwartet 400", code)
	}

	// Über den Client: Inhalt und Größe.
	c := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: emailB})
	rc, size, err := c.Artifact(t.Context(), "tunnel.exe")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if size != int64(len(content)) || !bytes.Equal(got, content) {
		t.Fatalf("Client: %d Bytes %q", size, got)
	}
	if _, _, err := c.Artifact(t.Context(), "fehlt.exe"); err == nil {
		t.Fatal("fehlendes Artefakt ohne Fehler")
	}
}

func sha256Of(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func sha512Of(b []byte) string {
	h := sha512.Sum512(b)
	return "sha512:" + hex.EncodeToString(h[:])
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// I3: Der Starter lädt Einträge wanderwelt:<name> über den angemeldeten
// Client vom Koordinator und prüft den Hash wie bei jeder anderen Quelle.
func TestEnsureLoadsArtifactsFromCoordinator(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	srv, _, _, dir := newServerIn(t, t.TempDir(), "dev", fakeUUIDs{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	java := "java"
	if runtime.GOOS == "windows" {
		java = "java.exe"
	}
	jre := zipOf(t, map[string]string{"jdk-25-jre/bin/" + java: "JAVA", "jdk-25-jre/release": "25"})
	fabric := []byte("FABRIC-JAR")
	tunnel := []byte("TUNNEL-EXE")
	mod := []byte("LITHIUM")
	ad := server.ArtifactDir(dir)
	writeFile(t, filepath.Join(ad, "jre.zip"), jre)
	writeFile(t, filepath.Join(ad, "fabric-server-launch.jar"), fabric)
	writeFile(t, filepath.Join(ad, "tunnel.exe"), tunnel)
	writeFile(t, filepath.Join(ad, "lithium.jar"), mod)

	m := &api.Manifest{
		MinecraftVersion: "26.2",
		Java:             api.Artifact{Name: "temurin-jre", URL: "wanderwelt:jre.zip", Hash: sha256Of(jre)},
		FabricServer:     api.Artifact{Name: "fabric-server-launch", URL: "wanderwelt:fabric-server-launch.jar", Hash: sha256Of(fabric)},
		Tunnel:           api.Artifact{Name: "tunnel", URL: "wanderwelt:tunnel.exe", Hash: sha256Of(tunnel)},
		Mods:             []api.Artifact{{Name: "lithium", URL: "wanderwelt:lithium.jar", Hash: sha512Of(mod)}},
	}
	if probs := server.CheckArtifacts(dir, m); len(probs) != 0 {
		t.Fatalf("CheckArtifacts: %q", probs)
	}
	c := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: emailB})
	p, err := deps.Ensure(t.Context(), m, api.Profile{}, c, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for path, want := range map[string]string{p.Java: "JAVA", p.FabricServerJar: "FABRIC-JAR", p.Tunnel: "TUNNEL-EXE", p.Mods[0]: "LITHIUM"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; erwartet %q", path, got, err, want)
		}
	}
	if filepath.Base(p.FabricServerJar) != "fabric-server-launch.jar" {
		t.Errorf("Fabric-Jar heißt %s", p.FabricServerJar)
	}

	// Falscher Hash im Manifest: abgelehnt, nichts installiert.
	bad := *m
	bad.Tunnel.Name, bad.Tunnel.Hash = "tunnel2", sha256Of([]byte("anderer tunnel"))
	if _, err := deps.Ensure(t.Context(), &bad, api.Profile{}, c, nil); err == nil || !strings.Contains(err.Error(), "Hash passt nicht") {
		t.Fatalf("Ensure mit falschem Hash: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(group.Home(), "deps", "tunnel2")); len(entries) != 0 {
		t.Fatalf("nach Hash-Fehler liegt etwas in deps/tunnel2: %v", entries)
	}
	if probs := server.CheckArtifacts(dir, &bad); len(probs) != 1 || !strings.Contains(probs[0], "tunnel2") {
		t.Fatalf("CheckArtifacts mit falschem Hash: %q", probs)
	}
	// Fehlt die Datei auf dem Koordinator, scheitert Ensure mit 404.
	missing := *m
	missing.Tunnel = api.Artifact{Name: "tunnel3", URL: "wanderwelt:fehlt.exe", Hash: sha256Of(tunnel)}
	if _, err := deps.Ensure(t.Context(), &missing, api.Profile{}, c, nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("Ensure mit fehlendem Artefakt: %v", err)
	}
	if probs := server.CheckArtifacts(dir, &missing); len(probs) != 1 {
		t.Fatalf("CheckArtifacts mit fehlender Datei: %q", probs)
	}
}
