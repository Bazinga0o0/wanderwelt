package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

var testHost = api.HostInfo{Email: "lukas@x.de", MinecraftName: "Lukas", MachineID: "pc-1"}

// t0 ist der Ausgangspunkt der Fake-Zeiten (UTC, Tagesbeginn).
var t0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func openStore(t *testing.T, dir string, budget int64) *Store {
	t.Helper()
	s, err := Open(dir, budget)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return s
}

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// putBlob lädt data in einem Teil hoch und liefert den Eintrag für path.
func putBlob(t *testing.T, s *Store, path string, data []byte) api.FileEntry {
	t.Helper()
	sha := shaOf(data)
	complete, err := s.PutBlobPart(sha, 0, int64(len(data)), bytes.NewReader(data))
	if err != nil || !complete {
		t.Fatalf("PutBlobPart(%s): complete=%v err=%v", path, complete, err)
	}
	return api.FileEntry{Path: path, Size: int64(len(data)), SHA256: sha}
}

// request baut einen Commit auf Generation base der gerade aktiven Welt.
func request(s *Store, base int64, newWorld bool, files ...api.FileEntry) api.CommitRequest {
	world, _, _ := s.Head()
	return api.CommitRequest{
		LeaseID:          1,
		MachineID:        testHost.MachineID,
		BaseWorldID:      world,
		BaseGeneration:   base,
		NewWorld:         newWorld,
		MinecraftVersion: "26.2",
		Files:            files,
	}
}

func commit(t *testing.T, s *Store, base int64, newWorld bool, now time.Time, files ...api.FileEntry) *api.Generation {
	t.Helper()
	g, err := s.Commit(request(s, base, newWorld, files...), testHost, now)
	if err != nil {
		t.Fatalf("Commit(base=%d, new=%v): %v", base, newWorld, err)
	}
	return g
}

func head(t *testing.T, s *Store) (string, int64) {
	t.Helper()
	id, g, err := s.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if g == nil {
		return id, 0
	}
	return id, g.Number
}

// genExists meldet, ob Generation n der Welt worldID lesbar ist.
func genExists(t *testing.T, s *Store, worldID string, n int64) bool {
	t.Helper()
	g, err := s.Generation(worldID, n)
	if errors.Is(err, ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("Generation(%s, %d): %v", worldID, n, err)
	}
	if g.Number != n || g.WorldID != worldID {
		t.Fatalf("Generation(%s, %d) liefert %s/%d", worldID, n, g.WorldID, g.Number)
	}
	return true
}

// blobOK meldet, ob der Blob da ist und genau den Inhalt seines Hashes hat.
func blobOK(t *testing.T, s *Store, f api.FileEntry) bool {
	t.Helper()
	rc, size, err := s.OpenBlob(f.SHA256)
	if errors.Is(err, ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("OpenBlob(%s): %v", f.Path, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Blob %s lesen: %v", f.Path, err)
	}
	if size != f.Size || int64(len(data)) != f.Size || shaOf(data) != f.SHA256 {
		t.Fatalf("Blob %s: Größe %d/%d, Hash passt: %v", f.Path, size, len(data), shaOf(data) == f.SHA256)
	}
	return true
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestCommitRejectsStaleBase(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	f := putBlob(t, s, "level.dat", []byte("stand"))
	for base := int64(0); base < 3; base++ {
		commit(t, s, base, false, t0.Add(time.Duration(base)*time.Minute), f)
	}
	world, h := head(t, s)
	if h != 3 {
		t.Fatalf("head = %d, want 3", h)
	}

	// 2 ist veraltet, 0 heißt "noch keine Generation" und passt nicht zu
	// einer bestehenden Welt, 4 gibt es noch nicht.
	for _, base := range []int64{2, 0, 4} {
		_, err := s.Commit(request(s, base, false, f), testHost, t0.Add(time.Hour))
		if !errors.Is(err, ErrConflict) {
			t.Errorf("Commit(base=%d) err = %v, want ErrConflict", base, err)
		}
	}
	if id, h := head(t, s); id != world || h != 3 {
		t.Fatalf("Head nach Konflikt = %s/%d, want %s/3", id, h, world)
	}
	if genExists(t, s, world, 4) {
		t.Fatal("Generation 4 trotz Konflikt geschrieben")
	}
}

func TestCommitRejectsOtherWorldBase(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	f := putBlob(t, s, "level.dat", []byte("stand"))
	try := func(worldID string, base int64) error {
		req := request(s, base, false, f)
		req.BaseWorldID = worldID
		_, err := s.Commit(req, testHost, t0.Add(time.Hour))
		return err
	}

	// Ohne jede Welt passt nur die leere Basis-Welt.
	if err := try("20260301T000000Z-deadbeef", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("Basis-Welt ohne Welt: err = %v, want ErrConflict", err)
	}
	if id, g, _ := s.Head(); id != "" || g != nil {
		t.Fatalf("Welt %q trotz Konflikt angelegt", id)
	}
	w1 := commit(t, s, 0, false, t0, f).WorldID
	if err := try("", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("leere Basis-Welt bei bestehender Welt: err = %v, want ErrConflict", err)
	}

	// Neu übernommen: W2 Generation 1. NewWorld fragt nicht nach der Basis.
	// Ein Stand auf W1 mit Basis 1 trägt dieselbe Nummer und muss trotzdem
	// abgelehnt werden.
	nreq := request(s, 0, true, f)
	nreq.BaseWorldID = "egal"
	ng, err := s.Commit(nreq, testHost, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewWorld: %v", err)
	}
	w2 := ng.WorldID
	if err := try(w1, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("Stand der alten Welt: err = %v, want ErrConflict", err)
	}
	if id, h := head(t, s); id != w2 || h != 1 || genExists(t, s, w2, 2) {
		t.Fatalf("Head = %s/%d nach Konflikt, want %s/1 ohne Generation 2", id, h, w2)
	}
	if g := commit(t, s, 1, false, t0.Add(2*time.Minute), f); g.WorldID != w2 || g.Number != 2 {
		t.Fatalf("Commit auf W2 = %s/%d, want %s/2", g.WorldID, g.Number, w2)
	}

	// Archiv W1 zurückgeholt (Head 1): Ein Stand auf W2 mit Basis 1 passt
	// nur der Nummer nach.
	if err := s.Activate(w1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := try(w2, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("Stand von W2 nach Activate(W1): err = %v, want ErrConflict", err)
	}
	if id, h := head(t, s); id != w1 || h != 1 {
		t.Fatalf("Head = %s/%d, want %s/1", id, h, w1)
	}
	if err := try(w1, 1); err != nil {
		t.Fatalf("Stand auf W1 mit Basis 1: %v", err)
	}
}

func TestCommitRejectsMissingBlob(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	known := putBlob(t, s, "level.dat", []byte("bekannt"))
	unknown := api.FileEntry{Path: "region/r.0.0.mca", Size: 5, SHA256: shaOf([]byte("fehlt"))}

	// Leerer Speicher: Der erste Stand mit fehlendem Blob legt keine Welt an.
	if _, err := s.Commit(request(s, 0, false, known, unknown), testHost, t0); !errors.Is(err, ErrMissingBlob) {
		t.Fatalf("erster Commit err = %v, want ErrMissingBlob", err)
	}
	if id, g, err := s.Head(); id != "" || g != nil || err != nil {
		t.Fatalf("Head = %q, %v, %v; want keine Welt", id, g, err)
	}
	if names := dirNames(t, filepath.Join(dir, "worlds")); len(names) != 0 {
		t.Fatalf("Weltordner trotz Ablehnung angelegt: %v", names)
	}

	g1 := commit(t, s, 0, false, t0, known)
	// Nur der fehlende Blob ist ErrMissingBlob (Starter lädt nach und
	// versucht es erneut); alles andere ist ein kaputtes Manifest.
	cases := []struct {
		name        string
		files       []api.FileEntry
		wantMissing bool
	}{
		{"unbekannter Hash", []api.FileEntry{known, unknown}, true},
		{"Größe passt nicht", []api.FileEntry{{Path: "level.dat", Size: known.Size + 1, SHA256: known.SHA256}}, false},
		{"Hash in Großbuchstaben", []api.FileEntry{{Path: "level.dat", Size: known.Size, SHA256: strings.ToUpper(known.SHA256)}}, false},
		{"Hash kein SHA-256", []api.FileEntry{{Path: "level.dat", Size: known.Size, SHA256: "abc"}}, false},
		{"kein einziger Dateieintrag", nil, false},
	}
	for _, c := range cases {
		_, err := s.Commit(request(s, 1, false, c.files...), testHost, t0.Add(time.Minute))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
		if errors.Is(err, ErrMissingBlob) != c.wantMissing {
			t.Errorf("%s: errors.Is(err, ErrMissingBlob) = %v, want %v", c.name, !c.wantMissing, c.wantMissing)
		}
	}
	if _, h := head(t, s); h != 1 {
		t.Fatalf("head = %d, want 1", h)
	}
	genDir := filepath.Join(dir, "worlds", g1.WorldID, "generations")
	if names := dirNames(t, genDir); len(names) != 1 || names[0] != "1.json" {
		t.Fatalf("Generationsordner = %v, want nur [1.json]", names)
	}
}

func TestCommitRejectsBadPaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "speicher")
	s := openStore(t, dir, 1<<30)
	f := putBlob(t, s, "level.dat", []byte("x"))
	commit(t, s, 0, false, t0, f)

	bad := []string{"../x", "/x", "C:/x", "a\\b", "", "a/../../b",
		"a//b", "region/", "./level.dat", "C:x", "a/\x00b", "a/\nb",
		// Windows: Gerätenamen (auch mit Endung, Groß/Klein egal), Punkt oder
		// Leerzeichen am Ende, verbotene Zeichen.
		"NUL", "nul.txt", "region/CON", "Aux.dat", "prn", "COM1", "com0.log",
		"LPT9", "lpt1.mca", "data/NUL .txt",
		"a.", "a ", "dir./x", "x/b ",
		"a<b", "a>b", `a"b`, "a|b", "a?b", "a*b"}
	for _, p := range bad {
		if api.ValidPath(p) {
			t.Errorf("ValidPath(%q) = true", p)
		}
		bf := f
		bf.Path = p
		if _, err := s.Commit(request(s, 1, false, bf), testHost, t0.Add(time.Minute)); !errors.Is(err, ErrInvalid) {
			t.Errorf("Commit mit Pfad %q: err = %v, want ErrInvalid", p, err)
		}
	}
	for _, p := range []string{"level.dat", "region/r.-1.0.mca", "DIM-1/region/r.0.0.mca", "a..b/.c",
		"CONFIG/x", "nullable.dat", "COM10", "LPT", "con_x.json", "a b/c d"} {
		if !api.ValidPath(p) {
			t.Errorf("ValidPath(%q) = false", p)
		}
	}

	// Doppelte Pfade, auch nur in Groß/Klein verschieden (Windows), und ein
	// Pfad, der zugleich Datei und Ordner eines anderen ist.
	at := func(p string) api.FileEntry {
		e := f
		e.Path = p
		return e
	}
	for _, files := range [][]api.FileEntry{
		{f, f},
		{at("Level.dat"), f},
		{at("a"), at("a/b")},
		{at("x/y/z"), at("X/y")},
	} {
		if _, err := s.Commit(request(s, 1, false, files...), testHost, t0.Add(time.Minute)); !errors.Is(err, ErrInvalid) {
			t.Errorf("Commit mit %s und %s: err = %v, want ErrInvalid", files[0].Path, files[1].Path, err)
		}
	}
	if _, h := head(t, s); h != 1 {
		t.Fatalf("head = %d, want 1", h)
	}
	// Gemeinsamer Ordner ohne Kollision bleibt erlaubt.
	if g := commit(t, s, 1, false, t0.Add(time.Minute), at("a/b"), at("a/c"), at("ab")); g.Number != 2 {
		t.Fatalf("Commit mit a/b, a/c, ab = Generation %d, want 2", g.Number)
	}

	// Welt-IDs und Hashes kommen aus URLs und dürfen den Speicher nicht
	// verlassen. Köder liegen dort, wohin "../" ohne Prüfung führen würde.
	if err := os.MkdirAll(filepath.Join(root, "generations"), 0o755); err != nil {
		t.Fatal(err)
	}
	bait := []byte(`{"number":1,"world_id":"../.."}`)
	if err := os.WriteFile(filepath.Join(root, "generations", "1.json"), bait, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "geheim"), []byte("geheim"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../..", "..", "", "a/b", `a\b`} {
		if _, err := s.Generation(id, 1); err == nil {
			t.Errorf("Generation(%q, 1) ohne Fehler", id)
		}
		if err := s.Activate(id); err == nil {
			t.Errorf("Activate(%q) ohne Fehler", id)
		}
	}
	if rc, _, err := s.OpenBlob("../geheim"); err == nil {
		rc.Close()
		t.Error(`OpenBlob("../geheim") ohne Fehler`)
	}
	if _, err := s.PutBlobPart("../../ausbruch", 0, 10, strings.NewReader("abc")); !errors.Is(err, ErrInvalid) {
		t.Errorf("PutBlobPart mit Pfad als Hash: err = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ausbruch.part")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Teil-Datei außerhalb des Speichers angelegt (Stat err = %v)", err)
	}
}

func TestBlobPartsAssembleAndVerify(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	data := make([]byte, 3000)
	for i := range data {
		data[i] = byte(i*7 + i/13)
	}
	sha := shaOf(data)
	parts := [][2]int64{{0, 1000}, {1000, 2000}, {2000, 3000}}

	for i, p := range parts {
		if i == 1 {
			if _, err := s.PutBlobPart(sha, 2000, 3000, bytes.NewReader(data[2000:])); err == nil {
				t.Fatal("Teil außer der Reihe angenommen")
			}
		}
		complete, err := s.PutBlobPart(sha, p[0], 3000, bytes.NewReader(data[p[0]:p[1]]))
		if err != nil {
			t.Fatalf("Teil %d: %v", i, err)
		}
		if complete != (i == len(parts)-1) {
			t.Fatalf("Teil %d: complete = %v", i, complete)
		}
	}
	rc, size, err := s.OpenBlob(sha)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || size != 3000 || !bytes.Equal(got, data) {
		t.Fatalf("Blob nicht bitgleich: size=%d len=%d err=%v", size, len(got), err)
	}
	if m := s.Missing([]string{sha}); len(m) != 0 {
		t.Fatalf("Missing nach Upload = %v", m)
	}
	if names := dirNames(t, filepath.Join(dir, "tmp")); len(names) != 0 {
		t.Fatalf("tmp nach Upload nicht leer: %v", names)
	}

	// Falscher Inhalt unter fremdem Hash: Fehler erst am Ende, dann nichts übrig.
	other := shaOf([]byte("ganz anderer Inhalt"))
	wrong := bytes.Repeat([]byte("x"), 3000)
	for i, p := range parts {
		complete, err := s.PutBlobPart(other, p[0], 3000, bytes.NewReader(wrong[p[0]:p[1]]))
		if complete {
			t.Fatalf("Teil %d mit falschem Inhalt: complete = true", i)
		}
		last := i == len(parts)-1
		if last && !errors.Is(err, ErrInvalid) {
			t.Fatalf("letzter Teil mit falschem Inhalt: err = %v, want ErrInvalid", err)
		}
		if !last && err != nil {
			t.Fatalf("Teil %d: %v", i, err)
		}
	}
	if m := s.Missing([]string{other}); len(m) != 1 || m[0] != other {
		t.Fatalf("Missing nach falschem Inhalt = %v", m)
	}
	if rc, _, err := s.OpenBlob(other); err == nil {
		rc.Close()
		t.Fatal("Blob mit falschem Inhalt lesbar")
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs", other[:2], other)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Blob mit falschem Inhalt abgelegt (Stat err = %v)", err)
	}
	if names := dirNames(t, filepath.Join(dir, "tmp")); len(names) != 0 {
		t.Fatalf("tmp nach falschem Inhalt nicht leer: %v", names)
	}
}

func TestNewWorldArchivesOld(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	a := putBlob(t, s, "level.dat", []byte("alte Welt 1"))
	b := putBlob(t, s, "level.dat", []byte("alte Welt 2"))
	commit(t, s, 0, false, t0, a)
	old := commit(t, s, 1, false, t0.Add(time.Minute), b)

	n := putBlob(t, s, "level.dat", []byte("neue Welt"))
	// BaseGeneration zählt bei NewWorld nicht.
	ng := commit(t, s, 7, true, t0.Add(time.Hour), n)
	if ng.WorldID == "" || ng.WorldID == old.WorldID || ng.Number != 1 || ng.Parent != 0 {
		t.Fatalf("neue Welt: %s/%d Parent %d (alte Welt %s)", ng.WorldID, ng.Number, ng.Parent, old.WorldID)
	}
	if ng.WorldID < old.WorldID {
		t.Errorf("Welt-IDs nicht nach Zeit sortierbar: %s < %s", ng.WorldID, old.WorldID)
	}
	if id, h := head(t, s); id != ng.WorldID || h != 1 {
		t.Fatalf("Head = %s/%d, want %s/1", id, h, ng.WorldID)
	}

	arch, err := s.Generation(old.WorldID, 2)
	if err != nil {
		t.Fatalf("Archiv-Head lesen: %v", err)
	}
	if len(arch.Files) != 1 || arch.Files[0] != b || arch.WorldID != old.WorldID {
		t.Fatalf("Archiv-Head = %+v", arch)
	}

	if g := commit(t, s, 1, false, t0.Add(2*time.Hour), n); g.WorldID != ng.WorldID || g.Number != 2 {
		t.Fatalf("Folge-Commit = %s/%d, want %s/2", g.WorldID, g.Number, ng.WorldID)
	}

	// Archiv zurückholen und darauf weiterspielen.
	if err := s.Activate(old.WorldID); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if id, h := head(t, s); id != old.WorldID || h != 2 {
		t.Fatalf("Head nach Activate = %s/%d, want %s/2", id, h, old.WorldID)
	}
	if g := commit(t, s, 2, false, t0.Add(3*time.Hour), a); g.WorldID != old.WorldID || g.Number != 3 {
		t.Fatalf("Commit nach Activate = %s/%d, want %s/3", g.WorldID, g.Number, old.WorldID)
	}
	if err := s.Activate("20990101T000000Z-deadbeef"); err == nil {
		t.Fatal("Activate einer unbekannten Welt ohne Fehler")
	}
}

func TestPruneKeepsLast24AndDailies(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	f := putBlob(t, s, "level.dat", []byte("stand"))

	// 200 Stände über 40 Tage: alle 4,8 Stunden einer, 5 pro Tag.
	step := 40 * 24 * time.Hour / 200
	var world string
	created := map[int64]time.Time{}
	for i := int64(1); i <= 200; i++ {
		at := t0.Add(time.Duration(i-1) * step)
		g := commit(t, s, i-1, false, at, f)
		world = g.WorldID
		created[i] = at
	}
	now := created[200].Add(time.Minute)
	if err := s.Prune(now); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	want := map[int64]bool{}
	for n := int64(177); n <= 200; n++ {
		want[n] = true
	}
	lastOfDay := map[string]int64{}
	for n, at := range created {
		day := at.Format("2006-01-02")
		if n > lastOfDay[day] {
			lastOfDay[day] = n
		}
	}
	for d := 0; d < 30; d++ {
		day := now.AddDate(0, 0, -d).Format("2006-01-02")
		if n, ok := lastOfDay[day]; ok {
			want[n] = true
		}
	}
	if len(want) != 49 {
		t.Fatalf("Testannahme: %d erwartete Stände, want 49", len(want))
	}
	for n := int64(1); n <= 200; n++ {
		if got := genExists(t, s, world, n); got != want[n] {
			t.Errorf("Generation %d: vorhanden = %v, want %v", n, got, want[n])
		}
	}
	if id, h := head(t, s); id != world || h != 200 {
		t.Fatalf("Head nach Prune = %s/%d", id, h)
	}
}

func TestPruneBudgetDropsOldestDailiesFirst(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 30_000)
	blob := func(label string) []byte {
		data := make([]byte, 1000)
		copy(data, label)
		return data
	}

	// Archiv: 3 Stände an den Tagen 0–2, je ein eigener Blob von 1000 Bytes.
	var archive string
	for k := int64(0); k < 3; k++ {
		f := putBlob(t, s, "level.dat", blob(fmt.Sprintf("archiv %d", k)))
		archive = commit(t, s, k, false, t0.Add(time.Duration(k)*24*time.Hour+time.Hour), f).WorldID
	}
	// Aktive Welt: Stand i am Tag i+2, je ein eigener Blob.
	var active string
	for i := int64(1); i <= 40; i++ {
		f := putBlob(t, s, "level.dat", blob(fmt.Sprintf("aktiv %d", i)))
		active = commit(t, s, i-1, i == 1, t0.Add(time.Duration(i+2)*24*time.Hour+time.Hour), f).WorldID
	}
	now := t0.Add(42*24*time.Hour + 2*time.Hour)

	check := func(phase string, activeFrom int64, archiveFrom int64, wantBytes int64) {
		t.Helper()
		for n := int64(1); n <= 40; n++ {
			if got := genExists(t, s, active, n); got != (n >= activeFrom) {
				t.Errorf("%s: aktive Generation %d vorhanden = %v", phase, n, got)
			}
		}
		for n := int64(1); n <= 3; n++ {
			if got := genExists(t, s, archive, n); got != (n >= archiveFrom) {
				t.Errorf("%s: Archiv-Generation %d vorhanden = %v", phase, n, got)
			}
		}
		var total int64
		filepath.WalkDir(filepath.Join(dir, "blobs"), func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				info, _ := d.Info()
				total += info.Size()
			}
			return nil
		})
		if total != wantBytes {
			t.Errorf("%s: Blobs belegen %d Bytes, want %d", phase, total, wantBytes)
		}
	}

	// Ohne Budget blieben aktiv 11–40 (Tage 13–42) und das ganze Archiv:
	// 33 000 Bytes. Für 30 000 fallen die drei ältesten Tagesstände.
	if err := s.Prune(now); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	check("Budget 30000", 14, 1, 30_000)

	// Winziges Budget: alle Tagesstände weg, dann das Archiv bis auf seinen
	// letzten Stand; die letzten 24 bleiben, auch über Budget.
	s = openStore(t, dir, 1000)
	if err := s.Prune(now); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	check("Budget 1000", 17, 3, 25_000)
	if id, h := head(t, s); id != active || h != 40 {
		t.Fatalf("Head nach Prune = %s/%d", id, h)
	}
}

func TestGCKeepsReferencedBlobs(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	shared := putBlob(t, s, "region/r.0.0.mca", []byte("gemeinsame Region"))

	// Archiv-Welt mit zwei Ständen.
	a1 := putBlob(t, s, "level.dat", []byte("archiv 1"))
	commit(t, s, 0, false, t0, a1, shared)
	a2 := putBlob(t, s, "level.dat", []byte("archiv 2"))
	archive := commit(t, s, 1, false, t0.Add(time.Minute), a2, shared).WorldID

	// Aktive Welt: 26 Stände an einem Tag; 1 und 2 fallen aus den letzten
	// 24 und sind nicht der letzte Stand des Tages.
	var active string
	var files []api.FileEntry
	var orphan api.FileEntry
	for i := 1; i <= 26; i++ {
		if i == 26 {
			orphan = putBlob(t, s, "nie/referenziert", []byte("verwaister Blob"))
		}
		f := putBlob(t, s, "level.dat", []byte(fmt.Sprintf("aktiv %d", i)))
		files = append(files, f)
		active = commit(t, s, int64(i-1), i == 1, t0.Add(24*time.Hour+time.Duration(i)*time.Minute), f, shared).WorldID
	}
	// Ein Upload für den nächsten Stand läuft gerade (noch kein Commit). Er
	// enthält wieder den Inhalt von Stand 1; Missing hat dem Starter gesagt,
	// dass der Blob schon da ist.
	inflight := putBlob(t, s, "level.dat", []byte("Upload läuft noch"))
	if m := s.Missing([]string{files[0].SHA256}); len(m) != 0 {
		t.Fatalf("Missing = %v, want leer", m)
	}

	if err := s.Prune(t0.Add(25 * time.Hour)); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	kept := 0
	for _, w := range []struct {
		id string
		n  int64
	}{{archive, 2}, {active, 26}} {
		for n := int64(1); n <= w.n; n++ {
			g, err := s.Generation(w.id, n)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				t.Fatalf("Generation(%s, %d): %v", w.id, n, err)
			}
			kept++
			for _, f := range g.Files {
				if !blobOK(t, s, f) {
					t.Errorf("Blob %s von %s/%d fehlt nach Prune", f.Path, w.id, n)
				}
			}
		}
	}
	if kept != 2+24 {
		t.Fatalf("%d Generationen behalten, want 26 (2 Archiv + 24 aktiv)", kept)
	}
	for _, f := range []api.FileEntry{files[1], orphan} {
		if blobOK(t, s, f) {
			t.Errorf("nicht referenzierter Blob %q noch da", f.Path)
		}
	}
	if !blobOK(t, s, inflight) || !blobOK(t, s, files[0]) {
		t.Error("Blob eines laufenden Uploads gelöscht")
	}
}

func TestSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	f := putBlob(t, s, "level.dat", []byte("stand"))
	commit(t, s, 0, false, t0, f)
	commit(t, s, 1, false, t0.Add(time.Minute), f)
	id1, g1, err := s.Head()
	if err != nil || g1 == nil {
		t.Fatalf("Head: %v, %v", g1, err)
	}

	// Absturz zwischen Generationsdatei und current.json: Datei 3 liegt
	// schon da, der Head ist noch 2.
	genDir := filepath.Join(dir, "worlds", id1, "generations")
	orphan := *g1
	orphan.Number, orphan.Parent = 3, 2
	data, _ := json.Marshal(orphan)
	if err := os.WriteFile(filepath.Join(genDir, "3.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if genExists(t, s, id1, 3) {
		t.Fatal("halbe Generation 3 sichtbar")
	}

	s2 := openStore(t, dir, 1<<30)
	if _, err := os.Stat(filepath.Join(genDir, "3.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("halbe Generation 3 nach Open nicht weggeräumt (Stat err = %v)", err)
	}
	id2, g2, err := s2.Head()
	if err != nil || g2 == nil {
		t.Fatalf("Head nach Open: %v, %v", g2, err)
	}
	j1, _ := json.Marshal(g1)
	j2, _ := json.Marshal(g2)
	if id1 != id2 || !bytes.Equal(j1, j2) {
		t.Fatalf("Head nach Open verschieden:\n%s %s\n%s %s", id1, j1, id2, j2)
	}
	if genExists(t, s2, id2, 3) {
		t.Fatal("halbe Generation 3 nach Open sichtbar")
	}
	if g := commit(t, s2, 2, false, t0.Add(2*time.Minute), f); g.Number != 3 || g.Parent != 2 {
		t.Fatalf("Commit nach Open = %d (Parent %d), want 3 (Parent 2)", g.Number, g.Parent)
	}
	if !blobOK(t, s2, f) {
		t.Fatal("Blob nach Open weg")
	}
}

// putRecipe lädt die Segmente parts einzeln hoch, dazu ihr Rezept, und
// liefert den Eintrag der zerlegten Datei path (Hash und Größe der ganzen
// Datei, deren Blob es nicht gibt) und die Segmente.
func putRecipe(t *testing.T, s *Store, path string, parts ...[]byte) (api.FileEntry, []api.Segment) {
	t.Helper()
	var segs []api.Segment
	var whole []byte
	for _, p := range parts {
		b := putBlob(t, s, path, p)
		segs = append(segs, api.Segment{SHA256: b.SHA256, Size: b.Size})
		whole = append(whole, p...)
	}
	rec := putRecipeOf(t, s, segs)
	return api.FileEntry{Path: path, Size: int64(len(whole)), SHA256: shaOf(whole), Recipe: rec}, segs
}

// putRecipeOf lädt das Rezept segs hoch und liefert seinen Hash.
func putRecipeOf(t *testing.T, s *Store, segs []api.Segment) string {
	t.Helper()
	data, err := api.EncodeRecipe(segs)
	if err != nil {
		t.Fatal(err)
	}
	return putBlob(t, s, "rezept", data).SHA256
}

// rawRecipe baut ein Rezept von Hand (auch ungültige).
func rawRecipe(magic string, count uint32, segs ...api.Segment) []byte {
	b := binary.BigEndian.AppendUint32([]byte(magic), count)
	for _, seg := range segs {
		raw, _ := hex.DecodeString(seg.SHA256)
		b = append(b, raw...)
		b = binary.BigEndian.AppendUint64(b, uint64(seg.Size))
	}
	return b
}

func TestCommitRejectsMissingSegmentBlob(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	level := putBlob(t, s, "level.dat", []byte("level"))
	split, segs := putRecipe(t, s, "region/r.0.0.mca",
		[]byte("Kopf der Regionsdatei"), []byte("Chunk 0,0"), []byte("Chunk 0,1 – länger als die anderen"))
	if blobOK(t, s, api.FileEntry{Path: split.Path, Size: split.Size, SHA256: split.SHA256}) {
		t.Fatal("Testaufbau: Blob der ganzen Datei existiert")
	}

	// Gewollter Pfad: angenommen ohne Blob der ganzen Datei; das Rezept steht
	// in der Generation, auch nach neuem Öffnen.
	g1 := commit(t, s, 0, false, t0, level, split)
	if !slices.Contains(g1.Files, split) {
		t.Fatalf("Generation 1 ohne den Eintrag %+v: %+v", split, g1.Files)
	}
	s = openStore(t, dir, 1<<30)
	if g, err := s.Generation(g1.WorldID, 1); err != nil || !slices.Contains(g.Files, split) {
		t.Fatalf("nach Open: %+v, %v", g, err)
	}

	sa, sb, sc := segs[0], segs[1], segs[2]
	total := split.Size
	missing := api.Segment{SHA256: shaOf([]byte("fehlt")), Size: 5}
	resized := func(seg api.Segment, size int64) api.Segment {
		seg.Size = size
		return seg
	}
	// with: Eintrag mit Größe size und dem (hochgeladenen) Rezept rec.
	with := func(size int64, rec []byte) []api.FileEntry {
		f := split
		f.Size, f.Recipe = size, putBlob(t, s, "rezept", rec).SHA256
		return []api.FileEntry{level, f}
	}
	enc := func(segs ...api.Segment) []byte {
		data, err := api.EncodeRecipe(segs)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	sameRecipe := func(size int64) []api.FileEntry {
		f := split
		f.Size = size
		return []api.FileEntry{level, f}
	}
	noRecipeBlob, badRecipeHash, badWhole := split, split, split
	noRecipeBlob.Recipe = shaOf([]byte("nie hochgeladen"))
	badRecipeHash.Recipe = strings.ToUpper(split.Recipe)
	badWhole.SHA256 = "abc"
	tooBig := append(rawRecipe("WWR1", 4096, slices.Repeat([]api.Segment{sa}, 4096)...), 0)
	cases := []struct {
		name        string
		files       []api.FileEntry
		wantMissing bool
	}{
		{"Rezept-Blob fehlt", []api.FileEntry{level, noRecipeBlob}, true},
		{"Segment eines neuen Rezepts fehlt", with(total+5, enc(sa, sb, sc, missing)), true},
		// Das Rezept des Heads mit anderer Größe wird trotzdem geprüft.
		{"Summe kleiner als Größe", sameRecipe(total + 1), false},
		{"Summe größer als Größe", sameRecipe(total - 1), false},
		{"Segment ausgelassen", with(total, enc(sa, sc)), false},
		{"Segmentgröße passt nicht zum Blob", with(total, enc(resized(sa, sa.Size+1), resized(sb, sb.Size-1), sc)), false},
		{"falsche Magie", with(total, rawRecipe("WWR2", 3, sa, sb, sc)), false},
		{"Anzahl größer als Einträge", with(total, rawRecipe("WWR1", 4, sa, sb, sc)), false},
		{"Anzahl kleiner als Einträge", with(total, rawRecipe("WWR1", 2, sa, sb, sc)), false},
		{"negative Größe", with(total, rawRecipe("WWR1", 4, sa, sb, sc, resized(missing, -1))), false},
		// Läuft genau auf die Größe um; die Blobs fehlen, der Fehler ist aber
		// das Rezept, nicht der fehlende Blob.
		{"Summe läuft um", with(total, enc(resized(missing, math.MaxInt64), resized(missing, math.MaxInt64), resized(sa, total+2))), false},
		{"Rezept größer als 160 KiB", with(total, tooBig), false},
		{"Rezept-Hash in Großbuchstaben", []api.FileEntry{level, badRecipeHash}, false},
		{"Hash der ganzen Datei ungültig", []api.FileEntry{level, badWhole}, false},
	}
	for _, c := range cases {
		_, err := s.Commit(request(s, 1, false, c.files...), testHost, t0.Add(time.Minute))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
		if errors.Is(err, ErrMissingBlob) != c.wantMissing {
			t.Errorf("%s: errors.Is(err, ErrMissingBlob) = %v, want %v", c.name, !c.wantMissing, c.wantMissing)
		}
	}
	if _, h := head(t, s); h != 1 {
		t.Fatalf("head = %d, want 1", h)
	}
	genDir := filepath.Join(dir, "worlds", g1.WorldID, "generations")
	if names := dirNames(t, genDir); len(names) != 1 || names[0] != "1.json" {
		t.Fatalf("Generationsordner = %v, want nur [1.json]", names)
	}

	// Ein Rezept, das schon im Head steht, wird nicht erneut gelesen (Prune
	// hält seine Segmente, solange der Head steht). Beleg: Ein Segment fehlt
	// auf der Platte, der unveränderte Stand geht trotzdem durch.
	if err := os.Remove(s.blobPath(sb.SHA256)); err != nil {
		t.Fatal(err)
	}
	if g, err := s.Commit(request(s, 1, false, level, split), testHost, t0.Add(2*time.Minute)); err != nil || g.Number != 2 {
		t.Fatalf("Commit mit unverändertem Rezept: %+v, %v", g, err)
	}
	// Ein neues Rezept mit diesem Segment wird dagegen geprüft.
	extra := api.Segment{SHA256: putBlob(t, s, "x", []byte("x")).SHA256, Size: 1}
	if _, err := s.Commit(request(s, 2, false, with(total+1, enc(sa, sb, sc, extra))...),
		testHost, t0.Add(3*time.Minute)); !errors.Is(err, ErrMissingBlob) {
		t.Fatalf("neues Rezept mit fehlendem Segment: err = %v, want ErrMissingBlob", err)
	}
}

func TestGCKeepsSegmentBlobs(t *testing.T) {
	dir := t.TempDir()
	// Jeder Stand: eine Datei aus dem gemeinsamen Segment (1000 Bytes) und
	// einem eigenen (10 Bytes), dazu ihr Rezept (8 + 2·40 = 88 Bytes).
	// Stände 2–26 belegen 1000 + 25·10 + 25·88 = 3450 Bytes; als ganze
	// Dateien gezählt wären es 25·1010 = 25 250.
	s := openStore(t, dir, 4000)
	shared := bytes.Repeat([]byte("S"), 1000)
	own := func(i int) []byte { return []byte(fmt.Sprintf("Chunk %04d", i)) }
	type stand struct {
		f    api.FileEntry
		segs []api.Segment
	}
	var stands []stand
	var orphan api.FileEntry
	var world string
	// Stand 1 und 2 am Tag 0 (2 ist dessen letzter Stand), 3–26 am Tag 1.
	for i := 1; i <= 26; i++ {
		if i == 26 {
			orphan = putBlob(t, s, "nie/referenziert", []byte("verwaist"))
		}
		day := time.Duration(0)
		if i >= 3 {
			day = 24 * time.Hour
		}
		f, segs := putRecipe(t, s, "region/r.0.0.mca", shared, own(i))
		stands = append(stands, stand{f, segs})
		world = commit(t, s, int64(i-1), false, t0.Add(day+time.Duration(i)*time.Minute), f).WorldID
	}
	now := t0.Add(25 * time.Hour)
	blobBytes := func() int64 {
		var total int64
		filepath.WalkDir(filepath.Join(dir, "blobs"), func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				info, _ := d.Info()
				total += info.Size()
			}
			return nil
		})
		return total
	}
	recipeBlob := func(i int) api.FileEntry {
		data, _ := api.EncodeRecipe(stands[i-1].segs)
		return api.FileEntry{Path: "rezept", Size: int64(len(data)), SHA256: stands[i-1].f.Recipe}
	}

	// Ein unlesbares Rezept eines behaltenen Stands: Prune bricht ab, ohne
	// etwas zu löschen.
	rec5 := s.blobPath(stands[4].f.Recipe)
	saved, err := os.ReadFile(rec5)
	if err != nil {
		t.Fatal(err)
	}
	before := blobBytes()
	for _, broken := range [][]byte{[]byte("kaputt"), nil} {
		if broken == nil {
			err = os.Remove(rec5)
		} else {
			err = os.WriteFile(rec5, broken, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Prune(now); err == nil {
			t.Fatalf("Prune mit unlesbarem Rezept (%q) ohne Fehler", broken)
		}
		if !genExists(t, s, world, 1) || !blobOK(t, s, orphan) {
			t.Fatal("Prune hat trotz unlesbarem Rezept gelöscht")
		}
	}
	if err := os.WriteFile(rec5, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := blobBytes(); got != before {
		t.Fatalf("Blobs nach abgebrochenem Prune %d Bytes, vorher %d", got, before)
	}

	if err := s.Prune(now); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if genExists(t, s, world, 1) {
		t.Error("Stand 1 nach Prune noch da (weder unter den letzten 24 noch Tagesstand)")
	}
	for n := int64(2); n <= 26; n++ {
		if !genExists(t, s, world, n) {
			t.Errorf("Stand %d nach Prune weg", n)
		}
	}
	for i, st := range stands {
		for j, seg := range st.segs {
			want := i > 0 || j == 0 // nur das eigene Segment von Stand 1 ist frei
			if got := blobOK(t, s, segBlob(st.f.Path, seg)); got != want {
				t.Errorf("Stand %d, Segment %d: Blob vorhanden = %v, want %v", i+1, j, got, want)
			}
		}
		if got := blobOK(t, s, recipeBlob(i+1)); got != (i > 0) {
			t.Errorf("Stand %d: Rezept vorhanden = %v", i+1, got)
		}
	}
	if blobOK(t, s, orphan) {
		t.Error("nicht referenzierter Blob noch da")
	}
	if got := blobBytes(); got != 3450 {
		t.Errorf("Blobs belegen %d Bytes, want 3450", got)
	}

	// Budget 3400: Auch die Rezept-Blobs zählen, also fällt Tagesstand 2
	// (danach 3450 − 10 − 88 = 3352 Bytes).
	s = openStore(t, dir, 3400)
	if err := s.Prune(now); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if genExists(t, s, world, 2) {
		t.Error("Tagesstand 2 trotz Budget 3400 noch da")
	}
	if got := blobBytes(); got != 3352 {
		t.Errorf("Blobs belegen %d Bytes, want 3352", got)
	}
}

// segBlob ist der Segment-Blob seg als Eintrag für blobOK.
func segBlob(path string, seg api.Segment) api.FileEntry {
	return api.FileEntry{Path: path, Size: seg.Size, SHA256: seg.SHA256}
}

// frame baut einen Batch-Rahmen: Hash sha (roh), Größe, Daten.
func frame(sha string, data []byte) []byte {
	raw, _ := hex.DecodeString(sha)
	b := binary.BigEndian.AppendUint64(raw, uint64(len(data)))
	return append(b, data...)
}

func TestPutBlobsFrames(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	a, b, c := []byte("erster"), []byte("zweiter Blob"), []byte("dritter")
	existing := putBlob(t, s, "schon da", []byte("schon da"))

	// Gewollter Pfad: zwei neue, ein vorhandener, ein leerer Blob.
	body := slices.Concat(frame(shaOf(a), a), frame(existing.SHA256, []byte("schon da")), frame(shaOf(b), b), frame(shaOf(nil), nil))
	n, err := s.PutBlobs(bytes.NewReader(body), api.BatchMaxFrames)
	if err != nil || n != 4 {
		t.Fatalf("PutBlobs = %d, %v; want 4, nil", n, err)
	}
	for _, d := range [][]byte{a, b, nil, []byte("schon da")} {
		if !blobOK(t, s, api.FileEntry{Path: string(d), Size: int64(len(d)), SHA256: shaOf(d)}) {
			t.Errorf("Blob %q fehlt", d)
		}
	}
	if m := s.Missing([]string{shaOf(a), shaOf(b), shaOf(c)}); !slices.Equal(m, []string{shaOf(c)}) {
		t.Errorf("Missing = %v, want nur c", m)
	}

	// Falscher Hash im zweiten Rahmen: Fehler, dieser Blob entsteht nicht;
	// der Rahmen davor bleibt (jeder ist für sich geprüft).
	d, e := []byte("vierter"), []byte("fünfter")
	cases := []struct {
		name string
		body []byte
		want []byte // Blob, den es danach geben muss
	}{
		{"falscher Hash", slices.Concat(frame(shaOf(d), d), frame(shaOf(e), []byte("nicht e"))), d},
		{"falscher Hash bei vorhandenem Blob", frame(existing.SHA256, []byte("anders!!")), nil},
		{"Daten abgeschnitten", frame(shaOf(e), e)[:40+3], nil},
		{"Kopf abgeschnitten", frame(shaOf(e), e)[:20], nil},
		{"Größe negativ", append(frame(shaOf(e), nil)[:32], 0x80, 0, 0, 0, 0, 0, 0, 5), nil},
	}
	for _, cs := range cases {
		if _, err := s.PutBlobs(bytes.NewReader(cs.body), api.BatchMaxFrames); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", cs.name, err)
		}
		if blobOK(t, s, api.FileEntry{Path: "e", Size: int64(len(e)), SHA256: shaOf(e)}) {
			t.Fatalf("%s: Blob e angelegt", cs.name)
		}
		if cs.want != nil && !blobOK(t, s, api.FileEntry{Path: "d", Size: int64(len(cs.want)), SHA256: shaOf(cs.want)}) {
			t.Errorf("%s: gültiger Rahmen davor nicht abgelegt", cs.name)
		}
	}
	if names := dirNames(t, filepath.Join(s.dir, "tmp")); len(names) != 0 {
		t.Errorf("Reste in tmp: %v", names)
	}

	// Größen und fehlende Hashes für /v1/blobs/fetch.
	sizes, miss, err := s.BlobSizes([]string{shaOf(b), shaOf(c), shaOf(a), shaOf(c)})
	if err != nil || !slices.Equal(sizes, []int64{int64(len(b)), -1, int64(len(a)), -1}) || !slices.Equal(miss, []string{shaOf(c)}) {
		t.Errorf("BlobSizes = %v, %v, %v", sizes, miss, err)
	}
	if _, _, err := s.BlobSizes([]string{"abc"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("BlobSizes mit ungültigem Hash: %v", err)
	}
}

func TestHeadRef(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	if id, n := s.HeadRef(); id != "" || n != 0 {
		t.Fatalf("HeadRef ohne Welt = %q, %d", id, n)
	}
	f := putBlob(t, s, "level.dat", []byte("stand"))
	commit(t, s, 0, false, t0, f)
	g := commit(t, s, 1, false, t0.Add(time.Minute), f)
	if id, n := s.HeadRef(); id != g.WorldID || n != 2 {
		t.Fatalf("HeadRef = %q, %d; want %q, 2", id, n, g.WorldID)
	}
}
