package deps

import (
	"os"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Konfigdateien: gute werden geladen, schlechte (Hash, Pfad, fehlende Datei)
// nur übersprungen – die Kette bricht nicht ab.
func TestEnsureConfigsSkipsBroken(t *testing.T) {
	good := []byte(`{"a":1}`)
	src := fakeSource{"good.json": good, "bad.json": []byte("anders")}
	gh := "sha256:" + sha256Hex(good)
	cfgs := []api.ConfigFile{
		{Path: "config/good.json", URL: "wanderwelt:good.json", Hash: gh},
		{Path: "config/bad.json", URL: "wanderwelt:bad.json", Hash: "sha256:" + sha256Hex([]byte("erwartet"))},
		{Path: "config/fehlt.json", URL: "wanderwelt:fehlt.json", Hash: gh},
		{Path: "config/../evil.json", URL: "wanderwelt:good.json", Hash: gh, Mode: "default"},
		{Path: "config/d.json", URL: "wanderwelt:good.json", Hash: gh, Mode: "default"},
	}
	f := &fetcher{base: t.TempDir(), progress: func(string, int64, int64) {}}
	got := ensureConfigs(t.Context(), cfgs, src, f)
	if len(got) != 2 || got[0].Path != "config/good.json" || got[1].Path != "config/d.json" {
		t.Fatalf("Ergebnis: %+v", got)
	}
	if got[0].Mode != "enforce" || got[1].Mode != "default" {
		t.Errorf("Modi: %+v", got)
	}
	if b, err := os.ReadFile(got[0].Src); err != nil || string(b) != string(good) {
		t.Errorf("Cache-Datei: %q %v", b, err)
	}
	if !FileHashMatches(got[0].Src, got[0].Hash) || FileHashMatches(got[0].Src, "sha256:"+sha256Hex([]byte("x"))) || FileHashMatches(got[0].Src, "kaputt") {
		t.Error("FileHashMatches")
	}
}

func TestEnsureConfigsSizeLimit(t *testing.T) {
	big := make([]byte, maxConfigBytes+1)
	f := &fetcher{base: t.TempDir(), progress: func(string, int64, int64) {}}
	got := ensureConfigs(t.Context(), []api.ConfigFile{
		{Path: "config/big.json", URL: "wanderwelt:big.json", Hash: "sha256:" + sha256Hex(big)},
	}, fakeSource{"big.json": big}, f)
	if len(got) != 0 {
		t.Fatalf("zu große Datei geladen: %+v", got)
	}
}
