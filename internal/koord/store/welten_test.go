package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// commitTo reicht einen Stand für die Welt world ein, aufbauend auf ihrem
// Head (neueWelt: Weltkennung ersetzen).
func commitTo(t *testing.T, s *Store, world string, neueWelt bool, now time.Time, files ...api.FileEntry) *api.Generation {
	t.Helper()
	id, n := s.HeadRefOf(world)
	g, err := s.CommitTo(world, api.CommitRequest{
		LeaseID: 1, MachineID: testHost.MachineID, BaseWorldID: id, BaseGeneration: n,
		NewWorld: neueWelt, MinecraftVersion: "26.2", Files: files,
	}, testHost, now)
	if err != nil {
		t.Fatalf("CommitTo(%q, neu=%v): %v", world, neueWelt, err)
	}
	return g
}

// schreibeV1 baut von Hand die Struktur eines Speichers im Format 1 auf, wie
// ihn der bisherige Koordinator angelegt hat: format.json Version 1,
// current.json {world_id, head}, Generationsdateien, Blobs, ein Archiv.
// Liefert die Weltkennung der aktiven Welt, die des Archivs und den Pfad
// aller Dateien mit ihrem Hash (für den Vergleich vorher/nachher).
func schreibeV1(t *testing.T, dir string) (aktiv, archiv string) {
	t.Helper()
	aktiv, archiv = "20260101T000000Z-aaaa1111", "20251201T000000Z-bbbb2222"
	put := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	blob := func(data []byte) api.FileEntry {
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		put(filepath.Join(dir, "blobs", sha[:2], sha), data)
		return api.FileEntry{Path: "level.dat", Size: int64(len(data)), SHA256: sha}
	}
	gen := func(world string, n int64, f api.FileEntry) {
		g := api.Generation{Number: n, Parent: n - 1, WorldID: world, Created: t0.Add(time.Duration(n) * time.Hour),
			HostEmail: "a@x.de", MachineID: "pc-1", MinecraftVersion: "26.2", Files: []api.FileEntry{f}}
		data, _ := json.Marshal(g)
		put(filepath.Join(dir, "worlds", world, "generations", jsonName(n)), data)
	}
	f1, f2, f3 := blob([]byte("Stand eins")), blob([]byte("Stand zwei")), blob([]byte("Archiv"))
	gen(aktiv, 1, f1)
	gen(aktiv, 2, f2)
	gen(archiv, 1, f3)
	put(filepath.Join(dir, "current.json"), []byte(`{"world_id":"`+aktiv+`","head":2}`))
	put(FormatPath(dir), []byte(`{"version":1}`+"\n"))
	return aktiv, archiv
}

func jsonName(n int64) string { return strconv.FormatInt(n, 10) + ".json" }

// hashes liefert Pfad → Hash aller Dateien unter dir/sub.
func hashes(t *testing.T, dir string, subs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, sub := range subs {
		root := filepath.Join(dir, sub)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			rel, _ := filepath.Rel(dir, p)
			out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Die Migration auf Format 2 macht die bisherige Welt zu Welt w1 und rührt
// weder Weltkennung noch Stände noch Blobs an; die alte current.json bleibt
// daneben liegen.
func TestMigrationWeltliste(t *testing.T) {
	dir := t.TempDir()
	aktiv, archiv := schreibeV1(t, dir)
	alteCurrent, _ := os.ReadFile(filepath.Join(dir, "current.json"))
	vorher := hashes(t, dir, "worlds", "blobs")

	s := openStore(t, dir, 1<<30)

	if v, found, err := ReadFormat(dir); err != nil || !found || v != 2 {
		t.Fatalf("Format nach Migration: %d %v %v", v, found, err)
	}
	if nachher := hashes(t, dir, "worlds", "blobs"); !mapsEqual(vorher, nachher) {
		t.Fatal("die Migration hat Generationen oder Blobs verändert")
	}
	if bak, err := os.ReadFile(filepath.Join(dir, currentBackup)); err != nil || string(bak) != string(alteCurrent) {
		t.Fatalf("Kopie der alten current.json: %q %v", bak, err)
	}
	ws := s.GroupWorlds()
	if len(ws) != 1 || ws[0].ID != "w1" || ws[0].Name != "Welt" || ws[0].StoreWorld != aktiv || ws[0].Head != 2 ||
		ws[0].Gamemode != "" || ws[0].Difficulty != "" || !slices.Equal(ws[0].Previous, []string{archiv}) {
		t.Fatalf("Weltliste: %+v", ws)
	}
	// Die alten Wege liefern unverändert die bisherige Welt.
	if id, n := head(t, s); id != aktiv || n != 2 {
		t.Fatalf("Head = %s/%d, want %s/2", id, n, aktiv)
	}
	wl, err := s.Worlds()
	if err != nil || len(wl) != 2 || wl[0].WorldID != aktiv || !wl[0].Active || wl[1].WorldID != archiv || wl[1].Active || wl[1].GroupWorld != "w1" {
		t.Fatalf("Worlds: %+v %v", wl, err)
	}
	// Weiterspielen baut auf Generation 2 der alten Weltkennung auf.
	f := putBlob(t, s, "level.dat", []byte("Stand drei"))
	if g := commit(t, s, 2, false, t0.Add(5*time.Hour), f); g.WorldID != aktiv || g.Number != 3 {
		t.Fatalf("Commit nach Migration: %s/%d", g.WorldID, g.Number)
	}

	// Zweites Öffnen: nichts ändert sich mehr, die Kopie bleibt die erste.
	s2 := openStore(t, dir, 1<<30)
	if ws2 := s2.GroupWorlds(); len(ws2) != 1 || ws2[0].Head != 3 || ws2[0].StoreWorld != aktiv {
		t.Fatalf("Weltliste nach zweitem Open: %+v", ws2)
	}
	if bak, _ := os.ReadFile(filepath.Join(dir, currentBackup)); string(bak) != string(alteCurrent) {
		t.Fatal("die Kopie der alten current.json wurde überschrieben")
	}
}

// Bricht die Migration nach dem Schreiben der neuen current.json ab (format.json
// steht noch auf 1), erkennt der nächste Start die Weltliste und legt sie nicht
// noch einmal an.
func TestMigrationWiederholbar(t *testing.T) {
	dir := t.TempDir()
	aktiv, _ := schreibeV1(t, dir)
	if err := migrateWorldList(dir); err != nil {
		t.Fatal(err)
	}
	neu, _ := os.ReadFile(filepath.Join(dir, "current.json"))
	if err := migrateWorldList(dir); err != nil { // wie ein zweiter Start bei format.json == 1
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "current.json")); string(again) != string(neu) {
		t.Fatal("zweite Migration hat current.json verändert")
	}
	s := openStore(t, dir, 1<<30) // format.json 1 → Migration läuft ein drittes Mal
	if ws := s.GroupWorlds(); len(ws) != 1 || ws[0].StoreWorld != aktiv {
		t.Fatalf("%+v", ws)
	}
}

func TestMigrationKaputteCurrentLaesstAlles(t *testing.T) {
	dir := t.TempDir()
	schreibeV1(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte(`{"world_id":"../x","head":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, 1<<30); err == nil {
		t.Fatal("kaputte current.json angenommen")
	}
	if v, _, _ := ReadFormat(dir); v != 1 {
		t.Fatalf("Format nach gescheiterter Migration: %d, erwartet 1", v)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "current.json")); !strings.Contains(string(data), "../x") {
		t.Fatalf("current.json verändert: %s", data)
	}
}

// Archive ohne current.json (selten) gehören nach der Migration zu w1.
func TestMigrationNurArchive(t *testing.T) {
	dir := t.TempDir()
	_, archiv := schreibeV1(t, dir)
	os.Remove(filepath.Join(dir, "current.json"))
	os.RemoveAll(filepath.Join(dir, "worlds", "20260101T000000Z-aaaa1111"))
	s := openStore(t, dir, 1<<30)
	ws := s.GroupWorlds()
	if len(ws) != 1 || !ws[0].Empty() || !slices.Equal(ws[0].Previous, []string{archiv}) {
		t.Fatalf("%+v", ws)
	}
	if _, err := s.Restore(api.GenRef{WorldID: archiv, Number: 1}, testHost, t0); err != nil {
		t.Fatalf("Archiv zurückholen: %v", err)
	}
	if id, n := head(t, s); id != archiv || n != 2 {
		t.Fatalf("Head %s/%d", id, n)
	}
}

// Mit mehreren Welten hat jede ihren eigenen Stand und Verlauf; ein Commit
// in die eine rührt die andere nie an.
func TestWeltenGetrennt(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	a := putBlob(t, s, "level.dat", []byte("survival 1"))
	g1 := commit(t, s, 0, false, t0, a) // legt w1 an
	if ws := s.GroupWorlds(); len(ws) != 1 || ws[0].ID != "w1" || ws[0].Name != "Welt" {
		t.Fatalf("erste Welt: %+v", ws)
	}
	cw, err := s.AddGroupWorld("Creative", "creative", "peaceful", t0)
	if err != nil || cw.ID != "w2" || !cw.Empty() {
		t.Fatalf("AddGroupWorld: %+v %v", cw, err)
	}
	if id, n := s.HeadRefOf("w2"); id != "" || n != 0 {
		t.Fatalf("leere Welt hat Head %s/%d", id, n)
	}
	// Erster Stand der leeren Welt: legt ihre eigene Weltkennung an.
	b := putBlob(t, s, "level.dat", []byte("creative 1"))
	c1 := commitTo(t, s, "w2", false, t0.Add(time.Minute), b)
	if c1.WorldID == g1.WorldID || c1.Number != 1 || c1.Parent != 0 {
		t.Fatalf("erster Stand der zweiten Welt: %s/%d (erste Welt %s)", c1.WorldID, c1.Number, g1.WorldID)
	}
	// Die erste Welt ist unverändert.
	if id, n := s.HeadRefOf("w1"); id != g1.WorldID || n != 1 {
		t.Fatalf("w1 nach Commit in w2: %s/%d", id, n)
	}
	// Weiterspielen in w1 und w2, getrennte Nummern.
	a2 := putBlob(t, s, "level.dat", []byte("survival 2"))
	commitTo(t, s, "w1", false, t0.Add(2*time.Minute), a2)
	commitTo(t, s, "w1", false, t0.Add(3*time.Minute), a2)
	if _, n := s.HeadRefOf("w1"); n != 3 {
		t.Fatalf("w1 Head %d", n)
	}
	if _, n := s.HeadRefOf("w2"); n != 1 {
		t.Fatalf("w2 Head %d", n)
	}
	// Die Basis einer anderen Welt gilt nicht.
	_, err = s.CommitTo("w2", api.CommitRequest{BaseWorldID: g1.WorldID, BaseGeneration: 3, MinecraftVersion: "26.2", Files: []api.FileEntry{b}}, testHost, t0)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Basis der anderen Welt angenommen: %v", err)
	}
	// Eine Welt, die es nicht gibt.
	if _, err := s.CommitTo("w9", api.CommitRequest{MinecraftVersion: "26.2", Files: []api.FileEntry{b}}, testHost, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Commit in unbekannte Welt: %v", err)
	}
	// Alte Wege (ohne Welt) gelten für die erste.
	if id, g, _ := s.Head(); id != g1.WorldID || g.Number != 3 {
		t.Fatalf("Head() = %s/%d", id, g.Number)
	}
	// „Welt übernehmen“ in w2 ersetzt nur w2; w2 behält die alte Weltkennung als Archiv.
	n := putBlob(t, s, "level.dat", []byte("creative neu"))
	nc := commitTo(t, s, "w2", true, t0.Add(time.Hour), n)
	if nc.WorldID == c1.WorldID || nc.Number != 1 {
		t.Fatalf("Import: %s/%d", nc.WorldID, nc.Number)
	}
	if id, n := s.HeadRefOf("w1"); id != g1.WorldID || n != 3 {
		t.Fatalf("w1 nach Import in w2: %s/%d", id, n)
	}
	w2, _ := s.GroupWorld("w2")
	if w2.StoreWorld != nc.WorldID || !slices.Equal(w2.Previous, []string{c1.WorldID}) {
		t.Fatalf("w2 nach Import: %+v", w2)
	}
	// Alles übersteht ein Neuöffnen.
	s2 := openStore(t, dir, 1<<30)
	if id, n := s2.HeadRefOf("w2"); id != nc.WorldID || n != 1 {
		t.Fatalf("w2 nach Open: %s/%d", id, n)
	}
	if got := s2.GroupWorlds(); len(got) != 2 || got[1].Name != "Creative" || got[1].Gamemode != "creative" || got[1].Difficulty != "peaceful" {
		t.Fatalf("Liste nach Open: %+v", got)
	}
	// Die Stände-Liste zeigt jede Weltkennung mit ihrer Welt.
	wl, err := s2.Worlds()
	if err != nil || len(wl) != 3 {
		t.Fatalf("Worlds: %+v %v", wl, err)
	}
	for _, w := range wl {
		want := map[string]string{g1.WorldID: "w1", c1.WorldID: "w2", nc.WorldID: "w2"}[w.WorldID]
		if w.GroupWorld != want {
			t.Errorf("Worlds: %s gehört zu %q, erwartet %q", w.WorldID, w.GroupWorld, want)
		}
	}
	if !wl[0].Active || !wl[1].Active || wl[2].Active || wl[2].WorldID != c1.WorldID {
		t.Errorf("aktiv/Archiv falsch: %+v", wl)
	}
}

// Zurückholen aus einem Archiv bindet die Welt wieder an diese Weltkennung –
// nur die zugehörige Welt ändert sich.
func TestWeltenRestoreArchiv(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	a := putBlob(t, s, "level.dat", []byte("a"))
	g1 := commit(t, s, 0, false, t0, a)
	s.AddGroupWorld("Creative", "", "", t0)
	b := putBlob(t, s, "level.dat", []byte("b"))
	c1 := commitTo(t, s, "w2", false, t0, b)
	n := putBlob(t, s, "level.dat", []byte("c"))
	commitTo(t, s, "w2", true, t0.Add(time.Hour), n) // c1 wird Archiv von w2

	g, err := s.Restore(api.GenRef{WorldID: c1.WorldID, Number: 1}, testHost, t0.Add(2*time.Hour))
	if err != nil || g.WorldID != c1.WorldID || g.Number != 2 {
		t.Fatalf("Restore: %+v %v", g, err)
	}
	if w2, _ := s.GroupWorld("w2"); w2.StoreWorld != c1.WorldID || w2.Head != 2 {
		t.Fatalf("w2: %+v", w2)
	}
	if id, n := s.HeadRefOf("w1"); id != g1.WorldID || n != 1 {
		t.Fatalf("w1 hat sich geändert: %s/%d", id, n)
	}
}

func TestWeltenVerwalten(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	if _, err := s.AddGroupWorld("Survival", "survival", "hard", t0); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ name, gm, diff string }{
		{"", "", ""}, {"   ", "", ""}, {strings.Repeat("x", 41), "", ""}, {"a\x00b", "", ""}, {"a\nb", "", ""},
		{"ok", "hardcore", ""}, {"ok", "", "extreme"}, {"\xff", "", ""},
	} {
		if _, err := s.AddGroupWorld(bad.name, bad.gm, bad.diff, t0); !errors.Is(err, ErrInvalid) {
			t.Errorf("AddGroupWorld(%q,%q,%q): %v, erwartet ErrInvalid", bad.name, bad.gm, bad.diff, err)
		}
	}
	if _, err := s.AddGroupWorld("survival", "", "", t0); !errors.Is(err, ErrConflict) {
		t.Errorf("doppelter Name (andere Schreibweise): %v", err)
	}
	cw, err := s.AddGroupWorld("  Creative  ", "creative", "", t0)
	if err != nil || cw.Name != "Creative" {
		t.Fatalf("%+v %v", cw, err)
	}
	name := "Bauen"
	up, err := s.UpdateGroupWorld(cw.ID, WorldPatch{Name: &name})
	if err != nil || up.Name != "Bauen" || up.Gamemode != "creative" {
		t.Fatalf("Umbenennen: %+v %v", up, err)
	}
	dup := "SURVIVAL"
	if _, err := s.UpdateGroupWorld(cw.ID, WorldPatch{Name: &dup}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Umbenennen auf vorhandenen Namen: %v", err)
	}
	gm := "unsinn"
	if _, err := s.UpdateGroupWorld(cw.ID, WorldPatch{Gamemode: &gm}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ungültiger Spielmodus: %v", err)
	}
	if w, _ := s.GroupWorld(cw.ID); w.Name != "Bauen" || w.Gamemode != "creative" {
		t.Fatalf("fehlgeschlagene Änderung hat etwas verändert: %+v", w)
	}
	if _, err := s.UpdateGroupWorld("w77", WorldPatch{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbekannte Welt: %v", err)
	}

	// Löschen: Vermerk; die Kennung wird nie wieder vergeben; die letzte bleibt.
	if err := s.DeleteGroupWorld(cw.ID, t0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GroupWorld(cw.ID); ok {
		t.Fatal("gelöschte Welt noch in der Liste")
	}
	if err := s.DeleteGroupWorld(cw.ID, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("zweites Löschen: %v", err)
	}
	if err := s.DeleteGroupWorld("w1", t0); !errors.Is(err, ErrLastWorld) {
		t.Fatalf("letzte Welt gelöscht: %v", err)
	}
	nw, err := s.AddGroupWorld("Neu", "", "", t0)
	if err != nil || nw.ID == cw.ID {
		t.Fatalf("Kennung wiederverwendet: %+v %v", nw, err)
	}
	s2 := openStore(t, dir, 1<<30)
	if got := s2.GroupWorlds(); len(got) != 2 || got[0].Name != "Survival" || got[1].Name != "Neu" {
		t.Fatalf("Liste nach Open: %+v", got)
	}
}

// Eine gelöschte Welt lässt sich nicht still wieder einführen; ihre Stände
// bleiben liegen und fallen unter Platzdruck als Erste.
func TestWeltLoeschenStaendeBleibenPruneZuerst(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	a := putBlob(t, s, "level.dat", []byte(strings.Repeat("a", 1000)))
	g1 := commit(t, s, 0, false, t0, a)
	s.AddGroupWorld("Weg", "", "", t0)
	b := putBlob(t, s, "level.dat", []byte(strings.Repeat("b", 1000)))
	w2 := commitTo(t, s, "w2", false, t0, b)
	if err := s.DeleteGroupWorld("w2", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(api.GenRef{WorldID: w2.WorldID, Number: 1}, testHost, t0); !errors.Is(err, ErrDeleted) || !errors.Is(err, ErrConflict) {
		t.Fatalf("Restore aus gelöschter Welt: %v", err)
	}
	if err := s.Activate(w2.WorldID); !errors.Is(err, ErrDeleted) {
		t.Fatalf("Activate: %v", err)
	}
	if wl, _ := s.Worlds(); len(wl) != 1 || wl[0].WorldID != g1.WorldID {
		t.Fatalf("Worlds zeigt die gelöschte Welt: %+v", wl)
	}
	// Ohne Platzdruck bleibt alles.
	if err := s.Prune(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !genExists(t, s, w2.WorldID, 1) || !blobOK(t, s, b) {
		t.Fatal("Prune ohne Platzdruck hat die gelöschte Welt weggeräumt")
	}
	// Mit Platzdruck (Budget nur für eine Datei) fällt sie als Erste, die aktive Welt bleibt.
	if err := s.SetBudget(1500); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if genExists(t, s, w2.WorldID, 1) || blobOK(t, s, b) {
		t.Fatal("die gelöschte Welt wurde unter Platzdruck nicht zuerst aufgeräumt")
	}
	if !genExists(t, s, g1.WorldID, 1) || !blobOK(t, s, a) {
		t.Fatal("die Welt w1 hat gelitten")
	}
}

// Prune behandelt jede Welt der Liste wie eine aktive Welt: alle behalten
// ihre letzten Stände, auch die, die nicht die erste ist.
func TestPruneAlleWeltenAktiv(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	a := putBlob(t, s, "level.dat", []byte("a"))
	commit(t, s, 0, false, t0, a)
	s.AddGroupWorld("Zweite", "", "", t0)
	var w2 string
	for i := range KeepLast + 5 {
		f := putBlob(t, s, "level.dat", []byte{byte(i), 'x'})
		g := commitTo(t, s, "w2", false, t0.Add(time.Duration(i)*time.Second), f)
		w2 = g.WorldID
	}
	if err := s.Prune(t0.Add(30 * 24 * time.Hour * 3)); err != nil {
		t.Fatal(err)
	}
	// Die letzten KeepLast Stände der zweiten Welt bleiben, ältere (ohne Tages-/Stundenstand) fallen.
	for n := int64(5 + 1); n <= int64(KeepLast+5); n++ {
		if !genExists(t, s, w2, n) {
			t.Fatalf("Stand %d der zweiten Welt weggeräumt", n)
		}
	}
	if _, n := s.HeadRefOf("w2"); n != int64(KeepLast+5) {
		t.Fatalf("Head w2 %d", n)
	}
}

// Beschädigte Weltliste startet nicht (und ändert nichts).
func TestOpenKaputteWeltliste(t *testing.T) {
	for name, cur := range map[string]string{
		"doppelte Kennung":     `{"next":3,"worlds":[{"id":"w1","name":"a"},{"id":"w1","name":"b"}]}`,
		"Zähler zu klein":      `{"next":1,"worlds":[{"id":"w1","name":"a"}]}`,
		"Weltkennung ungültig": `{"next":2,"worlds":[{"id":"w1","name":"a","store_world":"../x","head":1}]}`,
		"Head ohne Welt":       `{"next":2,"worlds":[{"id":"w1","name":"a","head":4}]}`,
		"Kennung ungültig":     `{"next":2,"worlds":[{"id":"../w","name":"a"}]}`,
		"Spielmodus":           `{"next":2,"worlds":[{"id":"w1","name":"a","gamemode":"kreativ"}]}`,
		"Welt zweimal belegt":  `{"next":3,"worlds":[{"id":"w1","name":"a","store_world":"x","head":1},{"id":"w2","name":"b","previous":["x"]}]}`,
		"kein JSON":            `kaputt`,
	} {
		dir := t.TempDir()
		openStore(t, dir, 1<<30)
		if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte(cur), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, 1<<30); err == nil {
			t.Errorf("%s: Open ohne Fehler", name)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
