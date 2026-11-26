package api

import (
	"fmt"
	"strings"
)

// Modi von ConfigFile.Mode.
const (
	// ConfigEnforce: Der Starter schreibt die Datei bei jedem Start des
	// Servers neu, wenn sie fehlt oder ihr Hash abweicht (Standard).
	ConfigEnforce = "enforce"
	// ConfigDefault: Der Starter legt die Datei nur an, wenn sie fehlt; der
	// Host darf sie lokal ändern.
	ConfigDefault = "default"
)

// Grenzen für das Manifest-Feld configs.
const (
	MaxConfigs       = 64
	maxConfigPathLen = 200
	maxConfigSegLen  = 100
)

// configExts sind die erlaubten Dateiendungen (klein geschrieben).
var configExts = []string{".json", ".toml", ".yaml", ".yml", ".properties", ".txt", ".cfg", ".conf"}

// ConfigFile ist eine Konfigdatei, die auf allen Hosts gleich sein soll.
// Path: relativ zum Server-Ordner, mit "/" getrennt, nur unterhalb von
// "config/". URL: "wanderwelt:<datei>" oder https. Hash: "sha256:<hex>" oder
// "sha512:<hex>" (wie bei den Mods). Mode: "enforce" (Standard) oder
// "default".
type ConfigFile struct {
	Path string `json:"path" yaml:"path"`
	URL  string `json:"url" yaml:"url"`
	Hash string `json:"hash" yaml:"hash"`
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// EffectiveMode liefert den Modus, leer als "enforce".
func (c ConfigFile) EffectiveMode() string {
	if c.Mode == "" {
		return ConfigEnforce
	}
	return c.Mode
}

// ValidateConfigPath prüft einen Konfig-Pfad streng: relativ, nur unterhalb
// von config/, kein "..", keine Laufwerksbuchstaben, UNC-Pfade oder
// Alternate Data Streams, nur bekannte Endungen, begrenzte Länge.
func ValidateConfigPath(p string) error {
	if p == "" {
		return fmt.Errorf("Pfad fehlt")
	}
	if len(p) > maxConfigPathLen {
		return fmt.Errorf("Pfad %q ist länger als %d Zeichen", p, maxConfigPathLen)
	}
	if strings.ContainsAny(p, `\:`) {
		return fmt.Errorf("Pfad %q: Backslash, Laufwerksangabe und Datenströme (\\ und :) sind nicht erlaubt; Trenner ist /", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("Pfad %q ist absolut; erwartet wird ein Pfad relativ zum Server-Ordner", p)
	}
	segs := strings.Split(p, "/")
	if len(segs) < 2 || segs[0] != "config" {
		return fmt.Errorf("Pfad %q liegt nicht unterhalb von config/", p)
	}
	for _, s := range segs {
		if err := validConfigSegment(s); err != nil {
			return fmt.Errorf("Pfad %q: %w", p, err)
		}
	}
	name := strings.ToLower(segs[len(segs)-1])
	for _, e := range configExts {
		if strings.HasSuffix(name, e) && len(name) > len(e) {
			return nil
		}
	}
	return fmt.Errorf("Pfad %q: Dateiendung nicht erlaubt (erlaubt: json, toml, yaml, yml, properties, txt, cfg, conf)", p)
}

func validConfigSegment(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("leerer Pfadteil (// oder / am Ende)")
	case s == "." || s == "..":
		return fmt.Errorf("%q ist nicht erlaubt", s)
	case len(s) > maxConfigSegLen:
		return fmt.Errorf("ein Pfadteil ist länger als %d Zeichen", maxConfigSegLen)
	case strings.HasSuffix(s, ".") || strings.HasSuffix(s, " "):
		return fmt.Errorf("Pfadteil %q endet auf Punkt oder Leerzeichen", s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`*?"<>|`, r) {
			return fmt.Errorf("Pfadteil %q enthält ein unerlaubtes Zeichen", s)
		}
	}
	// Windows-Gerätenamen (auch mit Endung, etwa nul.txt).
	base, _, _ := strings.Cut(strings.ToUpper(s), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("Pfadteil %q ist ein reservierter Windows-Gerätename", s)
	}
	return nil
}

// ValidateConfigs prüft den Abschnitt configs des Manifests. Die Meldung
// nennt den ersten Fehler mit der laufenden Nummer des Eintrags.
func ValidateConfigs(cfgs []ConfigFile) error {
	if len(cfgs) > MaxConfigs {
		return fmt.Errorf("configs: höchstens %d Einträge erlaubt, im Manifest stehen %d", MaxConfigs, len(cfgs))
	}
	seen := make(map[string]bool, len(cfgs))
	for i, c := range cfgs {
		fail := func(err error) error { return fmt.Errorf("configs[%d] (%s): %w", i+1, c.Path, err) }
		if err := ValidateConfigPath(c.Path); err != nil {
			return fail(err)
		}
		key := strings.ToLower(c.Path)
		if seen[key] {
			return fail(fmt.Errorf("Pfad steht doppelt im Manifest"))
		}
		seen[key] = true
		switch c.Mode {
		case "", ConfigEnforce, ConfigDefault:
		default:
			return fail(fmt.Errorf("mode %q unbekannt (erlaubt: enforce, default)", c.Mode))
		}
		if err := validConfigHash(c.Hash); err != nil {
			return fail(err)
		}
		if err := validConfigURL(c.URL); err != nil {
			return fail(err)
		}
	}
	return nil
}

func validConfigHash(s string) error {
	algo, hx, ok := strings.Cut(strings.TrimSpace(s), ":")
	n := 0
	switch strings.ToLower(algo) {
	case "sha256":
		n = 64
	case "sha512":
		n = 128
	}
	if !ok || n == 0 || len(hx) != n {
		return fmt.Errorf("hash %q: erwartet \"sha256:<64 Hex-Zeichen>\" oder \"sha512:<128 Hex-Zeichen>\"", s)
	}
	for _, r := range hx {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f' || 'A' <= r && r <= 'F') {
			return fmt.Errorf("hash %q: kein Hex", s)
		}
	}
	return nil
}

func validConfigURL(u string) error {
	if rest, ok := strings.CutPrefix(strings.ToLower(u), "https://"); ok {
		if rest == "" || strings.HasPrefix(rest, "/") {
			return fmt.Errorf("url %q: Host fehlt", u)
		}
		return nil
	}
	scheme, name, ok := strings.Cut(u, ":")
	if ok && strings.EqualFold(scheme, "wanderwelt") && ValidArtifactFileName(name) {
		return nil
	}
	return fmt.Errorf("url %q: erwartet https://… oder wanderwelt:<datei> (Datei nur aus Buchstaben, Ziffern, . _ -)", u)
}

// ValidArtifactFileName: 1–128 Zeichen aus Buchstaben, Ziffern, ".", "_"
// und "-", nicht "." oder "..".
func ValidArtifactFileName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 128 {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
