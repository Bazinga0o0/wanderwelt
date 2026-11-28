package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Standardwerte und Grenzen des Abschnitts karte.
const (
	KarteStandardNice        = 10
	KarteStandardThreads     = 1
	KarteStandardMinInterval = 30 * time.Minute
	KarteStandardMaxBytes    = int64(1) << 30 // 1 GiB

	karteMaxNice        = 19
	karteMaxThreads     = 4
	karteMinMinInterval = time.Minute
	karteMaxMinInterval = 24 * time.Hour
	karteMinMaxBytes    = int64(1) << 20 // 1 MiB
	karteMaxMaxBytes    = int64(1) << 40 // 1 TiB
)

// Karte ist die Konfiguration der Karten-Erzeugung (Abschnitt karte). Ohne
// Binary ist die Erzeugung aus (Standard); schon erzeugte Karten werden
// trotzdem ausgeliefert.
type Karte struct {
	// Binary: absoluter Pfad des Programms „karte“ (Pflicht, wenn der
	// Abschnitt vorkommt). Colors: absoluter Pfad einer Farbtabelle (JSON),
	// leer = die eingebaute. Nice: Priorität des Programms (0–19, nur Linux).
	// Threads: Zahl der Threads des Programms (1–4). MinInterval: Mindestabstand
	// zwischen zwei Läufen derselben Welt (1m–24h). MaxBytes: höchste Größe der
	// Karte einer Welt in Bytes.
	Binary      string        `yaml:"-"`
	Colors      string        `yaml:"-"`
	Nice        int           `yaml:"-"`
	Threads     int           `yaml:"-"`
	MinInterval time.Duration `yaml:"-"`
	MaxBytes    int64         `yaml:"-"`
}

// An meldet, ob die Erzeugung eingeschaltet ist.
func (k Karte) An() bool { return k.Binary != "" }

// rawKarte ist die Datei-Form: fehlende Werte bleiben unterscheidbar von 0.
type rawKarte struct {
	Binary      string `yaml:"binary"`
	Colors      string `yaml:"colors"`
	Nice        *int   `yaml:"nice"`
	Threads     *int   `yaml:"threads"`
	MinInterval string `yaml:"min_interval"`
	// MaxBytes: Größe wie storage_budget ("1GiB", "512MiB" oder reine Bytes).
	MaxBytes string `yaml:"max_bytes"`
}

// parseKarte prüft den Abschnitt und setzt Standardwerte. Ohne Abschnitt (nil)
// oder mit leerem Abschnitt ist die Karte aus. Fehler nennen den Schlüssel.
func parseKarte(r *rawKarte) (Karte, error) {
	if r == nil {
		return Karte{}, nil
	}
	k := Karte{Nice: KarteStandardNice, Threads: KarteStandardThreads,
		MinInterval: KarteStandardMinInterval, MaxBytes: KarteStandardMaxBytes}
	r.Binary, r.Colors = strings.TrimSpace(r.Binary), strings.TrimSpace(r.Colors)
	if *r == (rawKarte{}) {
		return Karte{}, nil
	}
	if r.Binary == "" {
		return Karte{}, fmt.Errorf("karte.binary ist Pflicht (absoluter Pfad des Programms „karte“), sonst den ganzen Abschnitt weglassen")
	}
	if err := checkAbsPath(r.Binary); err != nil {
		return Karte{}, fmt.Errorf("karte.binary: %w", err)
	}
	k.Binary = r.Binary
	if r.Colors != "" {
		if err := checkAbsPath(r.Colors); err != nil {
			return Karte{}, fmt.Errorf("karte.colors: %w", err)
		}
		k.Colors = r.Colors
	}
	if r.Nice != nil {
		if *r.Nice < 0 || *r.Nice > karteMaxNice {
			return Karte{}, fmt.Errorf("karte.nice: %d liegt außerhalb von 0–%d", *r.Nice, karteMaxNice)
		}
		k.Nice = *r.Nice
	}
	if r.Threads != nil {
		if *r.Threads < 1 || *r.Threads > karteMaxThreads {
			return Karte{}, fmt.Errorf("karte.threads: %d liegt außerhalb von 1–%d", *r.Threads, karteMaxThreads)
		}
		k.Threads = *r.Threads
	}
	if s := strings.TrimSpace(r.MinInterval); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return Karte{}, fmt.Errorf("karte.min_interval: ungültige Dauer %q (erlaubt: Go-Dauer wie 30m)", s)
		}
		if d < karteMinMinInterval || d > karteMaxMinInterval {
			return Karte{}, fmt.Errorf("karte.min_interval: %q liegt außerhalb von 1m–24h", s)
		}
		k.MinInterval = d
	}
	if s := strings.TrimSpace(r.MaxBytes); s != "" {
		n, err := parseSize(s)
		if err != nil {
			return Karte{}, fmt.Errorf("karte.max_bytes: %w", err)
		}
		if n < karteMinMaxBytes || n > karteMaxMaxBytes {
			return Karte{}, fmt.Errorf("karte.max_bytes: %q liegt außerhalb von 1MiB–1TiB", s)
		}
		k.MaxBytes = n
	}
	return k, nil
}

// checkAbsPath: absoluter Pfad (auch mit „/“ am Anfang, wenn die Datei für
// Linux gedacht ist), ohne Steuerzeichen, ohne „..“-Teile.
func checkAbsPath(p string) error {
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("Steuerzeichen im Pfad")
		}
	}
	if !filepath.IsAbs(p) && !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%q ist kein absoluter Pfad", p)
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == 0x2f || r == 0x5c }) {
		if part == ".." {
			return fmt.Errorf("%q enthält „..“", p)
		}
	}
	return nil
}
