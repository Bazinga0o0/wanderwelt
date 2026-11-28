package karte

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Dateien eines Kartenstands unter data_dir/karte/<welt>/.
const (
	manifestName = "karte.json"
	stateName    = "state.json"
	tilesName    = "tiles"

	// ManifestFormat ist das Manifest-format, das dieser Koordinator kennt.
	ManifestFormat = 1
	// MaxLevel ist die höchste Stufe (Stufen 0–6).
	MaxLevel = 6
	// maxManifest begrenzt karte.json beim Lesen.
	maxManifest = 64 << 20
	// maxTiles begrenzt die Zahl der Kacheln eines Manifests.
	maxTiles = 1 << 20
)

// ErrZuGross: Die Karte hat mehr als max_bytes.
var ErrZuGross = errors.New("Karte größer als karte.max_bytes")

// tileEntry ist ein Eintrag von tiles im Manifest.
type tileEntry struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// manifestFile ist karte.json (Vertrag „Manifest“); unbekannte Felder
// werden ignoriert.
type manifestFile struct {
	Format    int                        `json:"format"`
	Dimension string                     `json:"dimension"`
	TileSize  int                        `json:"tile_size"`
	Levels    []int                      `json:"levels"`
	Source    string                     `json:"source"`
	Generated time.Time                  `json:"generated"`
	Bounds    map[string]api.KarteBounds `json:"bounds"`
	Tiles     map[string]tileEntry       `json:"tiles"`
}

// state ist state.json im Kartenordner: woraus der Stand gerendert ist.
// Er liegt im Ordner, damit er mit ihm zusammen ausgetauscht wird.
type state struct {
	Format     int               `json:"format"`
	Source     string            `json:"source"` // „<Weltkennung>/<Generation>“
	StoreWorld string            `json:"store_world"`
	Generation int64             `json:"generation"`
	Regions    map[string]string `json:"regions"` // „X Z“ → Hash der Regionsdatei
	LastRun    time.Time         `json:"last_run"`
	DurationMs int64             `json:"duration_ms"`
}

// stand ist ein geladener Kartenstand: das Manifest steht einmal im Speicher
// (kein Neu-Parsen je Anfrage).
type stand struct {
	dir      string
	manifest []byte
	etag     string // das Manifest als Antwort: "<sha256 der Bytes>" (mit Anführungszeichen)
	tiles    map[string]tileEntry
	info     api.KarteInfo
	bytes    int64 // Summe der Kacheln und des Manifests
	st       state
}

// tileKey schreibt die Kachel z/x/y kanonisch.
func tileKey(z, x, y int) string {
	return strconv.Itoa(z) + "/" + strconv.Itoa(x) + "/" + strconv.Itoa(y)
}

// parseTileKey liest „z/x/y“ streng (kanonisch, z in 0–MaxLevel).
func parseTileKey(k string) (z, x, y int, ok bool) {
	parts := strings.Split(k, "/")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var oks [3]bool
	z, oks[0] = parseCoord(parts[0])
	x, oks[1] = parseCoord(parts[1])
	y, oks[2] = parseCoord(parts[2])
	if !oks[0] || !oks[1] || !oks[2] || z < 0 || z > MaxLevel {
		return 0, 0, 0, false
	}
	return z, x, y, true
}

// tilePath ist der Pfad der Kachel im Ordner dir; entsteht nur aus Zahlen.
func tilePath(dir string, z, x, y int) string {
	return filepath.Join(dir, tilesName, strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+".png")
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// parseManifest liest und prüft karte.json.
func parseManifest(data []byte) (*manifestFile, error) {
	var m manifestFile
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("karte.json: %w", err)
	}
	if m.Format != ManifestFormat {
		return nil, fmt.Errorf("karte.json: format %d unbekannt (erwartet %d)", m.Format, ManifestFormat)
	}
	if m.Dimension != "overworld" {
		return nil, fmt.Errorf("karte.json: dimension %q (erwartet overworld)", m.Dimension)
	}
	if len(m.Tiles) > maxTiles {
		return nil, fmt.Errorf("karte.json: %d Kacheln sind zu viele", len(m.Tiles))
	}
	for _, l := range m.Levels {
		if l < 0 || l > MaxLevel {
			return nil, fmt.Errorf("karte.json: Stufe %d außerhalb von 0–%d", l, MaxLevel)
		}
	}
	for k, t := range m.Tiles {
		z, x, y, ok := parseTileKey(k)
		if !ok || tileKey(z, x, y) != k {
			return nil, fmt.Errorf("karte.json: ungültiger Kachelschlüssel %q", k)
		}
		if !validHash(t.SHA256) || t.Size < 0 {
			return nil, fmt.Errorf("karte.json: ungültiger Eintrag für Kachel %s", k)
		}
	}
	return &m, nil
}

// loadStand liest den Kartenstand in dir (Manifest und state.json). Mit
// check prüft es zusätzlich, dass jede Kachel des Manifests als Datei mit der
// angegebenen Größe vorhanden ist.
func loadStand(dir string, check bool) (*stand, error) {
	f, err := os.Open(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	f.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifest {
		return nil, fmt.Errorf("karte.json ist größer als %d Bytes", maxManifest)
	}
	m, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	s := &stand{dir: dir, manifest: bytes.Clone(data), tiles: m.Tiles, bytes: int64(len(data))}
	sum := sha256.Sum256(data)
	s.etag = `"` + hex.EncodeToString(sum[:]) + `"`
	for k, t := range m.Tiles {
		if t.Size > 1<<40-s.bytes {
			return nil, fmt.Errorf("karte.json: Größen unplausibel")
		}
		s.bytes += t.Size
		if check {
			z, x, y, _ := parseTileKey(k)
			fi, err := os.Lstat(tilePath(dir, z, x, y))
			if err != nil {
				return nil, fmt.Errorf("Kachel %s fehlt: %w", k, err)
			}
			if !fi.Mode().IsRegular() || fi.Size() != t.Size {
				return nil, fmt.Errorf("Kachel %s hat nicht die Größe aus dem Manifest", k)
			}
		}
	}
	s.info = api.KarteInfo{Version: m.Format, Source: m.Source, Generated: m.Generated, Tiles: len(m.Tiles),
		Levels: append([]int{}, m.Levels...)}
	if b, ok := m.Bounds["0"]; ok {
		s.info.Bounds = &b
	}
	if data, err := os.ReadFile(filepath.Join(dir, stateName)); err == nil {
		if err := json.Unmarshal(data, &s.st); err != nil {
			s.st = state{} // kaputt: wie ohne; der nächste Lauf rendert alles
		}
	}
	return s, nil
}

// regionHashes liest die Regionen aus dem Zustand; nil, wenn es keinen
// brauchbaren gibt (dann rendert der nächste Lauf alles).
func (s *state) regionHashes() map[Region]string {
	if s == nil || s.Format != 1 || s.Regions == nil {
		return nil
	}
	out := make(map[Region]string, len(s.Regions))
	for k, h := range s.Regions {
		r, ok := parseStateKey(k)
		if !ok || !validHash(h) {
			return nil
		}
		out[r] = h
	}
	return out
}

// writeJSONFile schreibt v als JSON nach path (neu angelegt, 0600).
func writeJSONFile(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// copyTree kopiert den Kartenordner src nach dst (nur Ordner und reguläre
// Dateien; Verknüpfungen werden nicht verfolgt und nicht kopiert; state.json
// bleibt zurück).
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == stateName {
			continue
		}
		from, to := filepath.Join(src, name), filepath.Join(dst, name)
		switch {
		case e.IsDir():
			if err := copyTree(from, to); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := copyFile(from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// renameRetry benennt um und versucht es bei einem Fehler noch einige Male
// (unter Windows hindern offene Dateien das Umbenennen von Ordnern).
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}
