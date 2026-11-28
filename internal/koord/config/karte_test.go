package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ladeKarte(t *testing.T, karte string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k.yaml")
	y := baseConfig + "auth: {mode: tunneld}\nmembers: [{email: a@x.de, minecraft: A}]\n" + karte
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestKarteStandardAus(t *testing.T) {
	for name, y := range map[string]string{"ohne Abschnitt": "", "leer": "karte: {}\n", "null": "karte:\n"} {
		c, err := ladeKarte(t, y)
		if err != nil || c.Karte.An() {
			t.Errorf("%s: %+v %v (Standard: aus)", name, c.Karte, err)
		}
	}
}

func TestKarteWerteUndStandardwerte(t *testing.T) {
	c, err := ladeKarte(t, "karte:\n  binary: /opt/wanderwelt/karte\n")
	if err != nil {
		t.Fatal(err)
	}
	k := c.Karte
	if !k.An() || k.Binary != "/opt/wanderwelt/karte" || k.Colors != "" || k.Nice != 10 || k.Threads != 1 ||
		k.MinInterval != 30*time.Minute || k.MaxBytes != 1<<30 {
		t.Fatalf("Standardwerte: %+v", k)
	}
	c, err = ladeKarte(t, `karte:
  binary: /opt/wanderwelt/karte
  colors: /opt/wanderwelt/farben.json
  nice: 0
  threads: 2
  min_interval: 5m
  max_bytes: 1073741824
`)
	if err != nil {
		t.Fatal(err)
	}
	k = c.Karte
	if k.Colors != "/opt/wanderwelt/farben.json" || k.Nice != 0 || k.Threads != 2 || k.MinInterval != 5*time.Minute || k.MaxBytes != 1<<30 {
		t.Fatalf("Werte: %+v", k)
	}
	if c, err = ladeKarte(t, "karte: {binary: /x/karte, max_bytes: 512MiB}\n"); err != nil || c.Karte.MaxBytes != 512<<20 {
		t.Fatalf("max_bytes mit Einheit: %+v %v", c.Karte, err)
	}
}

func TestKartePruefung(t *testing.T) {
	bad := map[string]string{
		"kein binary":           "karte: {nice: 5}\n",
		"binary relativ":        "karte: {binary: karte}\n",
		"binary relativ mit ./": "karte: {binary: ./karte}\n",
		"binary mit ..":         "karte: {binary: /opt/../etc/karte}\n",
		"colors relativ":        "karte: {binary: /x/karte, colors: farben.json}\n",
		"nice negativ":          "karte: {binary: /x/karte, nice: -1}\n",
		"nice zu groß":          "karte: {binary: /x/karte, nice: 20}\n",
		"threads 0":             "karte: {binary: /x/karte, threads: 0}\n",
		"threads zu viele":      "karte: {binary: /x/karte, threads: 5}\n",
		"min_interval zu kurz":  "karte: {binary: /x/karte, min_interval: 30s}\n",
		"min_interval zu lang":  "karte: {binary: /x/karte, min_interval: 25h}\n",
		"min_interval Unsinn":   "karte: {binary: /x/karte, min_interval: bald}\n",
		"max_bytes 0":           "karte: {binary: /x/karte, max_bytes: 0}\n",
		"max_bytes zu klein":    "karte: {binary: /x/karte, max_bytes: 1000}\n",
		"max_bytes zu groß":     "karte: {binary: /x/karte, max_bytes: 2TiB}\n",
		"max_bytes Unsinn":      "karte: {binary: /x/karte, max_bytes: viel}\n",
		"unbekannter Schlüssel": "karte: {binary: /x/karte, farbe: rot}\n",
		"Steuerzeichen im Pfad": "karte: {binary: \"/x/ka\\nrte\"}\n",
	}
	for name, y := range bad {
		if _, err := ladeKarte(t, y); err == nil {
			t.Errorf("%s: sollte abgelehnt werden", name)
		} else if strings.Contains(name, "binary") || strings.Contains(name, "nice") || strings.Contains(name, "threads") {
			if !strings.Contains(err.Error(), "karte") {
				t.Errorf("%s: Fehler nennt den Schlüssel nicht: %v", name, err)
			}
		}
	}
}
