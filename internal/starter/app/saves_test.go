package app

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nbtW schreibt NBT für Tests.
type nbtW struct{ bytes.Buffer }

func (w *nbtW) tag(t byte, name string) {
	w.WriteByte(t)
	w.str(name)
}

func (w *nbtW) str(s string) {
	binary.Write(&w.Buffer, binary.BigEndian, uint16(len(s)))
	w.WriteString(s)
}

func (w *nbtW) i32(v int32) { binary.Write(&w.Buffer, binary.BigEndian, v) }
func (w *nbtW) i64(v int64) { binary.Write(&w.Buffer, binary.BigEndian, v) }

// levelDat baut ein level.dat wie Minecraft 26.2 (gekürzt); uuid nil: ohne
// singleplayer_uuid.
func levelDat(version string, snapshot bool, uuid []int32) []byte {
	var w nbtW
	w.tag(tagCompound, "")
	w.tag(tagCompound, "Data")
	w.tag(tagCompound, "Version")
	w.tag(tagString, "Name")
	w.str(version)
	w.tag(tagByte, "Snapshot")
	if snapshot {
		w.WriteByte(1)
	} else {
		w.WriteByte(0)
	}
	w.tag(tagInt, "Id")
	w.i32(4903)
	w.WriteByte(tagEnd)
	w.tag(tagString, "LevelName")
	w.str("Testwelt")
	w.tag(tagLong, "LastPlayed")
	w.i64(1795369247982)
	w.tag(tagInt, "DataVersion")
	w.i32(4903)
	w.tag(tagList, "ServerBrands")
	w.WriteByte(tagString)
	w.i32(1)
	w.str("fabric")
	if uuid != nil {
		w.tag(tagIntArray, "singleplayer_uuid")
		w.i32(int32(len(uuid)))
		for _, v := range uuid {
			w.i32(v)
		}
	}
	w.WriteByte(tagEnd) // Data
	w.WriteByte(tagEnd) // Wurzel
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(w.Bytes())
	zw.Close()
	return z.Bytes()
}

// Die UUID aus dem echten level.dat einer 26.2-Welt.
var testUUID = []int32{48982348, -107918921, -1937786693, -1056258662}

const testUUIDText = "02eb694c-f991-49b7-8c7f-b8bbc10ac59a"

func writeSave(t *testing.T, saves, folder string, dat []byte, playerFile bool) {
	t.Helper()
	dir := filepath.Join(saves, folder)
	if err := os.MkdirAll(filepath.Join(dir, "players", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "level.dat"), dat, 0o644); err != nil {
		t.Fatal(err)
	}
	if playerFile {
		if err := os.WriteFile(filepath.Join(dir, "players", "data", testUUIDText+".dat"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSavesAndChecks(t *testing.T) {
	saves := t.TempDir()
	writeSave(t, saves, "aktuell", levelDat("26.2", false, testUUID), true)
	writeSave(t, saves, "ohne-spieler", levelDat("26.2", false, testUUID), false)
	writeSave(t, saves, "kein-uuid", levelDat("26.1.1", false, nil), false)
	writeSave(t, saves, "alt", levelDat("1.21.4", false, testUUID), true)
	writeSave(t, saves, "neuer", levelDat("26.3", false, testUUID), true)
	writeSave(t, saves, "vorab", levelDat("26.2-rc1", false, testUUID), true)
	writeSave(t, saves, "snapshot", levelDat("26w14a", true, testUUID), true)
	writeSave(t, saves, "kaputt", levelDat("26.2", false, testUUID)[:40], true)
	os.MkdirAll(filepath.Join(saves, "kein-level-dat"), 0o755)

	list, err := ListSaves(saves)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SaveInfo{}
	for _, s := range list {
		got[s.Folder] = s
	}
	if len(got) != 8 {
		t.Fatalf("ListSaves: %d Welten, erwartet 8 (ohne Ordner ohne level.dat): %v", len(got), got)
	}
	if s := got["aktuell"]; s.Version != "26.2" || s.LevelName != "Testwelt" || s.PlayerUUID != testUUIDText ||
		!s.PlayerFile || s.DataVersion != 4903 || s.LastPlayed.UnixMilli() != 1795369247982 || s.Size == 0 {
		t.Errorf("aktuell: %+v", s)
	}

	tests := []struct {
		folder string
		refuse string // Teil der Ablehnung, "" = erlaubt
		warn   bool
	}{
		{"aktuell", "", false},
		{"ohne-spieler", "", true},
		{"kein-uuid", "", true},
		{"vorab", "", false},
		{"alt", "Welten vor 26.1", false},
		{"neuer", "Neuere Welten", false},
		{"snapshot", "Snapshot", false},
		{"kaputt", "nicht lesbar", false},
	}
	for _, tc := range tests {
		refuse, warn := CheckSave(got[tc.folder], "26.2")
		switch {
		case tc.refuse == "" && refuse != nil:
			t.Errorf("%s: abgelehnt: %v", tc.folder, refuse)
		case tc.refuse != "" && (refuse == nil || !strings.Contains(refuse.Error(), tc.refuse)):
			t.Errorf("%s: Ablehnung %v, erwartet %q", tc.folder, refuse, tc.refuse)
		case tc.refuse == "" && (warn != "") != tc.warn:
			t.Errorf("%s: Warnung %q, erwartet %v", tc.folder, warn, tc.warn)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"26.2", "26.2", 0},
		{"26.1", "26.2", -1},
		{"26.1.1", "26.1", 1},
		{"1.21.4", "26.1", -1},
		{"26.2-rc1", "26.2", -1},
		{"26.2-pre1", "26.1.2", 1},
		{"26.10", "26.9", 1},
	}
	for _, tc := range tests {
		got, err := compareVersions(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, %v; erwartet %d", tc.a, tc.b, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "26w14a", "26", "26.x", "26.2 beta"} {
		if _, err := compareVersions(bad, "26.2"); err == nil {
			t.Errorf("compareVersions(%q) ohne Fehler", bad)
		}
	}
}

func TestParseNBTLimits(t *testing.T) {
	// Riesige Längen dürfen nicht zu riesigen Speicheranforderungen führen.
	var w nbtW
	w.tag(tagCompound, "")
	w.tag(tagIntArray, "x")
	w.i32(0x7fffffff)
	if _, err := parseNBT(bytes.NewReader(w.Bytes())); !errors.Is(err, errNBTTooLarge) {
		t.Errorf("riesiges Int-Array: %v", err)
	}
	// Tiefe Verschachtelung von Listen.
	var d nbtW
	d.tag(tagCompound, "")
	d.tag(tagList, "l")
	for range maxNBTDepth + 10 {
		d.WriteByte(tagList)
		d.i32(1)
	}
	d.WriteByte(tagEnd)
	d.i32(0)
	if _, err := parseNBT(bytes.NewReader(d.Bytes())); err == nil || !strings.Contains(err.Error(), "tief") {
		t.Errorf("tiefe Verschachtelung: %v", err)
	}
	// Abgeschnitten.
	if _, err := parseNBTStream(bytes.NewReader(levelDat("26.2", false, testUUID)[:30])); err == nil {
		t.Error("abgeschnittene Datei ohne Fehler")
	}
}
