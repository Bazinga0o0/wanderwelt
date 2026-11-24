package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// minImportVersion: Welten davor lehnt der Import ab (Spec 6.2.4: erst seit
// 26.1 liegen die Daten des Einzelspieler-Hosts unter players/data/).
const minImportVersion = "26.1"

// SaveInfo beschreibt einen Minecraft-Speicherstand (Ordner unter saves).
type SaveInfo struct {
	Dir         string // absoluter Pfad des Weltordners
	Folder      string // Name des Ordners
	LevelName   string // Data.LevelName
	Version     string // Data.Version.Name, etwa "26.2"
	Snapshot    bool   // Data.Version.Snapshot
	DataVersion int32  // Data.DataVersion
	LastPlayed  time.Time
	Size        int64
	// PlayerUUID ist Data.singleplayer_uuid ("" wenn es keinen gibt);
	// PlayerFile, ob players/data/<uuid>.dat existiert.
	PlayerUUID string
	PlayerFile bool
	// Err: level.dat fehlt oder ist nicht lesbar.
	Err error
}

// Label ist die Zeile in der Auswahlliste.
func (s SaveInfo) Label() string {
	version := s.Version
	if s.Err != nil {
		version = "unlesbar"
	} else if version == "" {
		version = "Version unbekannt"
	}
	name := s.Folder
	if s.LevelName != "" && s.LevelName != s.Folder {
		name = fmt.Sprintf("%s (%s)", s.Folder, s.LevelName)
	}
	played := "nie gespielt"
	if !s.LastPlayed.IsZero() {
		played = "zuletzt gespielt " + s.LastPlayed.Local().Format("02.01.2006 15:04")
	}
	return fmt.Sprintf("%s · %s · %s · %s", name, version, humanSize(s.Size), played)
}

// ListSaves liest alle Weltordner in savesDir (Ordner mit level.dat),
// zuletzt gespielte zuerst. Ein fehlender saves-Ordner ist ein Fehler, der
// fs.ErrNotExist enthält.
func ListSaves(savesDir string) ([]SaveInfo, error) {
	entries, err := os.ReadDir(savesDir)
	if err != nil {
		return nil, err
	}
	var out []SaveInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(savesDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "level.dat")); err != nil {
			continue
		}
		out = append(out, ReadSave(dir))
	}
	slices.SortStableFunc(out, func(a, b SaveInfo) int {
		if c := b.LastPlayed.Compare(a.LastPlayed); c != 0 {
			return c
		}
		return strings.Compare(a.Folder, b.Folder)
	})
	return out, nil
}

// ReadSave liest level.dat, die Größe und die Spielerdatei eines
// Weltordners. Fehler beim Lesen von level.dat stehen in Err.
func ReadSave(dir string) SaveInfo {
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	s := SaveInfo{Dir: dir, Folder: filepath.Base(dir)}
	s.Size = dirSize(dir)
	info, statErr := os.Stat(filepath.Join(dir, "level.dat"))
	if statErr == nil {
		s.LastPlayed = info.ModTime()
	}
	root, err := readNBTFile(filepath.Join(dir, "level.dat"))
	if err != nil {
		s.Err = err
		return s
	}
	applyLevelDat(&s, root)
	if s.PlayerUUID != "" {
		p := filepath.Join(dir, "players", "data", s.PlayerUUID+".dat")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			s.PlayerFile = true
		}
	}
	return s
}

// applyLevelDat übernimmt die Felder aus der Wurzel von level.dat.
func applyLevelDat(s *SaveInfo, root map[string]any) {
	if v, ok := nbtGet(root, "Data", "Version", "Name"); ok {
		s.Version, _ = v.(string)
	}
	if v, ok := nbtGet(root, "Data", "Version", "Snapshot"); ok {
		if b, ok := v.(int8); ok && b != 0 {
			s.Snapshot = true
		}
	}
	if v, ok := nbtGet(root, "Data", "DataVersion"); ok {
		s.DataVersion, _ = v.(int32)
	}
	if v, ok := nbtGet(root, "Data", "LevelName"); ok {
		s.LevelName, _ = v.(string)
	}
	if v, ok := nbtGet(root, "Data", "LastPlayed"); ok {
		if ms, ok := v.(int64); ok && ms > 0 {
			s.LastPlayed = time.UnixMilli(ms)
		}
	}
	if v, ok := nbtGet(root, "Data", "singleplayer_uuid"); ok {
		s.PlayerUUID = uuidFromNBT(v)
	}
}

// uuidFromNBT macht aus einer UUID im NBT (Int-Array mit 4 Werten oder
// Text) die Form mit Bindestrichen, klein geschrieben.
func uuidFromNBT(v any) string {
	switch u := v.(type) {
	case []int32:
		if len(u) != 4 {
			return ""
		}
		var b [16]byte
		for i, x := range u {
			b[4*i] = byte(uint32(x) >> 24)
			b[4*i+1] = byte(uint32(x) >> 16)
			b[4*i+2] = byte(uint32(x) >> 8)
			b[4*i+3] = byte(uint32(x))
		}
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	case string:
		s := strings.ToLower(strings.TrimSpace(u))
		if uuidRE.MatchString(s) {
			return s
		}
	}
	return ""
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CheckSave prüft einen Speicherstand für „Welt übernehmen“ gegen die
// Manifest-Version (Spec 6.2.4). refuse: geht nicht; warn: geht nur nach
// Bestätigung.
func CheckSave(s SaveInfo, manifestVersion string) (refuse error, warn string) {
	if s.Err != nil {
		return fmt.Errorf("level.dat von „%s“ ist nicht lesbar (%w).", s.Folder, s.Err), ""
	}
	if s.Snapshot {
		return fmt.Errorf("„%s“ ist eine Snapshot-Welt (%s); übernommen werden nur Welten aus Vollversionen.", s.Folder, s.Version), ""
	}
	c, err := compareVersions(s.Version, minImportVersion)
	if err != nil {
		return fmt.Errorf("„%s“: Minecraft-Version %q nicht erkannt.", s.Folder, s.Version), ""
	}
	if c < 0 {
		return fmt.Errorf("„%s“ ist mit Minecraft %s gespeichert. Welten vor %s werden nicht übernommen – öffne sie erst einmal in Minecraft %s oder neuer und speichere sie.", s.Folder, s.Version, minImportVersion, manifestVersion), ""
	}
	c, err = compareVersions(s.Version, manifestVersion)
	if err != nil {
		return fmt.Errorf("Manifest-Version %q nicht erkannt.", manifestVersion), ""
	}
	if c > 0 {
		return fmt.Errorf("„%s“ ist mit Minecraft %s gespeichert, die Gruppe spielt %s. Neuere Welten werden nicht übernommen.", s.Folder, s.Version, manifestVersion), ""
	}
	switch {
	case s.PlayerUUID == "":
		warn = "In level.dat steht kein Einzelspieler (singleplayer_uuid). Inventar und Position des bisherigen Einzelspielers gehen dann verloren."
	case !s.PlayerFile:
		warn = fmt.Sprintf("Die Spielerdatei des Einzelspielers (players/data/%s.dat) fehlt. Inventar und Position des bisherigen Einzelspielers gehen dann verloren.", s.PlayerUUID)
	}
	return nil, warn
}

// compareVersions vergleicht zwei Minecraft-Versionen (api.CompareVersions).
func compareVersions(a, b string) (int, error) { return api.CompareVersions(a, b) }

// dirSize summiert die Dateigrößen unter dir (Fehler zählen nicht).
func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// worldOpen meldet, ob Minecraft die Welt gerade geöffnet hat: Das Spiel
// sperrt session.lock, solange die Welt offen ist; Lesen scheitert dann.
func worldOpen(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "session.lock"))
	if err != nil {
		return false
	}
	defer f.Close()
	var b [1]byte
	_, err = f.Read(b[:])
	return err != nil && !errors.Is(err, io.EOF)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f MB", float64(n)/(1<<20)), ".", ",", 1)
	default:
		return strings.Replace(fmt.Sprintf("%.1f KB", float64(n)/(1<<10)), ".", ",", 1)
	}
}
