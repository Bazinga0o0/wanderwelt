package deps

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func TestSafeRel(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string // "" mit ok: Wurzeleintrag
		ok   bool
	}{
		{"bin/java.exe", "bin/java.exe", true},
		{"jdk/./bin//java.exe", "jdk/bin/java.exe", true},
		{"a/b/../c", "a/c", true},
		{"./", "", true},
		{"", "", false},
		{"../evil", "", false},
		{"a/../../evil", "", false},
		{"..", "", false},
		{"/etc/passwd", "", false},
		{`C:\Windows\evil.dll`, "", false},
		{"C:/Windows/evil.dll", "", false},
		{`bin\java.exe`, "", false},
		{"a\x00b", "", false},
		{"datei:strom", "", false},
	} {
		got, err := safeRel(tc.name)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("safeRel(%q) = %q, %v; erwartet %q, ok=%v", tc.name, got, err, tc.want, tc.ok)
		}
	}
}

type zipEntry struct {
	name string
	data string
	mode fs.FileMode // 0: normale Datei
}

func writeZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, e.data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// unzip lehnt Zip-Slip, absolute Pfade, Laufwerke und Links ab und schreibt
// dabei nichts außerhalb des Ziels.
func TestUnzipRejectsUnsafeEntries(t *testing.T) {
	ok := writeZip(t, []zipEntry{{name: "jdk/"}, {name: "jdk/bin/java.exe", data: "JAVA"}, {name: "jdk/release", data: "25"}})
	dest := filepath.Join(t.TempDir(), "ziel")
	if err := unzip(ok, dest); err != nil {
		t.Fatalf("gültiges Archiv: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "jdk", "bin", "java.exe")); err != nil || string(b) != "JAVA" {
		t.Fatalf("entpackt: %q, %v", b, err)
	}

	for _, tc := range []struct {
		what  string
		entry zipEntry
	}{
		{"Zip-Slip", zipEntry{name: "../draussen.txt", data: "x"}},
		{"Zip-Slip tief", zipEntry{name: "jdk/../../draussen.txt", data: "x"}},
		{"absoluter Pfad", zipEntry{name: "/draussen.txt", data: "x"}},
		{"Laufwerk", zipEntry{name: "C:/draussen.txt", data: "x"}},
		{"Backslash", zipEntry{name: `..\draussen.txt`, data: "x"}},
		{"symbolischer Link", zipEntry{name: "jdk/link", data: "../../draussen.txt", mode: fs.ModeSymlink | 0o777}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "ziel")
			z := writeZip(t, []zipEntry{{name: "jdk/ok.txt", data: "ok"}, tc.entry})
			if err := unzip(z, dest); err == nil {
				t.Fatalf("%s (%q) nicht abgelehnt", tc.what, tc.entry.name)
			}
			if _, err := os.Lstat(filepath.Join(base, "draussen.txt")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("%s: außerhalb des Ziels geschrieben (%v)", tc.what, err)
			}
		})
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeSource liefert Artefakte für wanderwelt:<name> aus einer Map.
type fakeSource map[string][]byte

func (f fakeSource) Artifact(_ context.Context, name string) (io.ReadCloser, int64, error) {
	b, ok := f[name]
	if !ok {
		return nil, 0, errors.New("404 nicht gefunden")
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

// Hashprüfung: Ein falscher oder ungültiger Hash wird abgelehnt und nichts
// installiert – für file- und wanderwelt-URLs gleich.
func TestEnsureFileRejectsHash(t *testing.T) {
	data := []byte("TUNNEL")
	local := filepath.Join(t.TempDir(), "tunnel.exe")
	if err := os.WriteFile(local, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fileURL := "file:///" + filepath.ToSlash(local)
	good, wrong := "sha256:"+sha256Hex(data), "sha256:"+sha256Hex([]byte("anders"))
	src := fakeSource{"tunnel.exe": data}

	for _, tc := range []struct {
		what    string
		url     string
		hash    string
		src     ArtifactSource
		wantErr string // "" = Erfolg
	}{
		{"file, Hash passt", fileURL, good, nil, ""},
		{"wanderwelt, Hash passt", "wanderwelt:tunnel.exe", good, src, ""},
		{"file, falscher Hash", fileURL, wrong, nil, "Hash passt nicht"},
		{"wanderwelt, falscher Hash", "wanderwelt:tunnel.exe", wrong, src, "Hash passt nicht"},
		{"Hash leer", fileURL, "", nil, "erwartet"},
		{"Hash ohne Verfahren", fileURL, sha256Hex(data), nil, "erwartet"},
		{"Hash md5", fileURL, "md5:" + sha256Hex(data)[:32], nil, "unbekanntes Verfahren"},
		{"Hash zu kurz", fileURL, "sha256:abcd", nil, "Hex-Zeichen"},
		{"wanderwelt ohne Koordinator", "wanderwelt:tunnel.exe", good, nil, "Koordinator"},
		{"wanderwelt mit Pfad", "wanderwelt:../tunnel.exe", good, src, "einfachem Dateinamen"},
		{"wanderwelt fehlt", "wanderwelt:fehlt.exe", good, src, "404"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			f := &fetcher{base: t.TempDir(), progress: func(string, int64, int64) {}, src: tc.src}
			p, err := f.ensureFile(t.Context(), api.Artifact{Name: "tunnel", URL: tc.url, Hash: tc.hash}, "tunnel.exe")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Fehler: %v", err)
				}
				if b, err := os.ReadFile(p); err != nil || !bytes.Equal(b, data) {
					t.Fatalf("installiert: %q, %v", b, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Fehler %v, erwartet %q", err, tc.wantErr)
			}
			if _, err := os.Stat(filepath.Join(f.base, "tunnel", "tunnel.exe")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("trotz Fehler installiert (%v)", err)
			}
			if entries, _ := os.ReadDir(filepath.Join(f.base, "tunnel")); len(entries) != 0 {
				t.Fatalf("Reste im Artefakt-Ordner: %v", entries)
			}
		})
	}
}

// Name aus der URL: bei wanderwelt:<name> der Name (Zip-Erkennung, Jar-Name).
func TestURLBase(t *testing.T) {
	for _, tc := range []struct{ url, base string }{
		{"wanderwelt:tunnel-1.2-windows-amd64.zip", "tunnel-1.2-windows-amd64.zip"},
		{"WANDERWELT:fabric.jar", "fabric.jar"},
		{"https://example.org/a/b/lithium.jar", "lithium.jar"},
		{"https://meta.fabricmc.net/v2/versions/loader/26.2/0.20.1/1.1.2/server/jar", "jar"},
	} {
		if got := urlBase(tc.url); got != tc.base {
			t.Errorf("urlBase(%q) = %q, erwartet %q", tc.url, got, tc.base)
		}
	}
	if !isZip(api.Artifact{URL: "wanderwelt:tunnel.zip"}) || isZip(api.Artifact{URL: "wanderwelt:tunnel.exe"}) {
		t.Error("isZip erkennt wanderwelt-URLs nicht")
	}
	if got := jarName(api.Artifact{Name: "fabric-server-launch", URL: "wanderwelt:fabric-server-launch.jar"}); got != "fabric-server-launch.jar" {
		t.Errorf("jarName = %q", got)
	}
}
