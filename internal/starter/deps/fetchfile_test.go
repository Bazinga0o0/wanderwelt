package deps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// countingSource zählt die Downloads; unknownSize: Größe -1 melden.
type countingSource struct {
	data        []byte
	unknownSize bool
	n           atomic.Int32
}

func (c *countingSource) Artifact(_ context.Context, name string) (io.ReadCloser, int64, error) {
	c.n.Add(1)
	size := int64(len(c.data))
	if c.unknownSize {
		size = -1
	}
	return io.NopCloser(bytes.NewReader(c.data)), size, nil
}

// FetchFile (Selbst-Update): lädt über den Koordinator nach dir/fileName,
// prüft Hash und Größe, schreibt nie mehr als erwartet und lässt bei
// Fehlern nichts liegen; eine schon passende Datei wird nicht neu geladen.
func TestFetchFile(t *testing.T) {
	exe := []byte("MZ neue Version")
	good := "sha256:" + sha256Hex(exe)
	a := api.Artifact{Name: "wanderwelt", URL: "wanderwelt:wanderwelt-0.5.0.exe", Hash: good}

	t.Run("lädt und prüft", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "update")
		src := &countingSource{data: exe}
		other := filepath.Join(dir, "andere.txt")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := FetchFile(t.Context(), a, src, dir, "wanderwelt-0.5.0.exe", int64(len(exe)), 0)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p); !bytes.Equal(b, exe) || p != filepath.Join(dir, "wanderwelt-0.5.0.exe") {
			t.Fatalf("%s: %q", p, b)
		}
		if _, err := FetchFile(t.Context(), a, src, dir, "wanderwelt-0.5.0.exe", int64(len(exe)), 0); err != nil {
			t.Fatal(err)
		}
		if n := src.n.Load(); n != 1 {
			t.Fatalf("%d Downloads, erwartet 1 (die Datei lag schon da)", n)
		}
		if _, err := os.Stat(other); err != nil {
			t.Fatalf("andere Datei weg: %v", err)
		}
	})

	for _, tc := range []struct {
		name     string
		art      api.Artifact
		src      *countingSource
		size     int64
		maxBytes int64
		wantErr  string
	}{
		{"Hash falsch", api.Artifact{Name: "wanderwelt", URL: a.URL, Hash: "sha256:" + sha256Hex([]byte("anders"))}, &countingSource{data: exe}, 0, 0, "Hash passt nicht"},
		{"Größe laut Koordinator anders", a, &countingSource{data: exe}, int64(len(exe)) + 1, 0, "erwartet"},
		{"größer als erwartet", a, &countingSource{data: exe, unknownSize: true}, int64(len(exe)) - 1, 0, "größer als"},
		{"kleiner als erwartet", a, &countingSource{data: exe, unknownSize: true}, int64(len(exe)) + 1, 0, "erwartet"},
		{"über der Grenze", a, &countingSource{data: exe, unknownSize: true}, 0, 4, "größer als 4"},
		{"ohne Koordinator", api.Artifact{Name: "wanderwelt", URL: "wanderwelt:x.exe", Hash: good}, nil, 0, 0, "Koordinator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "update")
			var src ArtifactSource
			if tc.src != nil {
				src = tc.src
			}
			_, err := FetchFile(t.Context(), tc.art, src, dir, "wanderwelt-0.5.0.exe", tc.size, tc.maxBytes)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Fehler %v, erwartet %q", err, tc.wantErr)
			}
			// Hash und Größe sind ErrMismatch, eine fehlende Quelle nicht.
			if mismatch := tc.src != nil; errors.Is(err, ErrMismatch) != mismatch {
				t.Fatalf("errors.Is(%v, ErrMismatch) = %v, erwartet %v", err, !mismatch, mismatch)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("Reste: %v", entries)
			}
		})
	}

	if _, err := FetchFile(t.Context(), a, &countingSource{data: exe}, t.TempDir(), `..\x.exe`, 0, 0); err == nil {
		t.Fatal("Dateiname mit Pfad angenommen")
	}
}
