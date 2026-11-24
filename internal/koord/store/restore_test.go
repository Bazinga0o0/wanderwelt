package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

var testAdmin = api.HostInfo{Email: "admin@x.de", MinecraftName: "Chefin", MachineID: "pc-admin"}

// commitPlayers ist commit mit Spielerliste und Minecraft-Version.
func commitPlayers(t *testing.T, s *Store, base int64, newWorld bool, now time.Time, version string, players []string, files ...api.FileEntry) *api.Generation {
	t.Helper()
	req := request(s, base, newWorld, files...)
	req.Players = players
	req.MinecraftVersion = version
	g, err := s.Commit(req, testHost, now)
	if err != nil {
		t.Fatalf("Commit(base=%d, new=%v): %v", base, newWorld, err)
	}
	return g
}

func sizeOf(files ...api.FileEntry) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

func refString(r *api.GenRef) string {
	if r == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s/%d", r.WorldID, r.Number)
}

func metaString(m GenMeta) string {
	return fmt.Sprintf("%s/%d created=%s host=%s players=%q size=%d mc=%s restored=%s",
		m.WorldID, m.Number, m.Created.UTC().Format(time.RFC3339), m.HostEmail, m.Players, m.Size,
		m.MinecraftVersion, refString(m.RestoredFrom))
}

func worldsString(ws []WorldMeta) string {
	var b strings.Builder
	for _, w := range ws {
		fmt.Fprintf(&b, "Welt %s aktiv=%v\n", w.WorldID, w.Active)
		for _, g := range w.Gens {
			fmt.Fprintf(&b, "  %s\n", metaString(g))
		}
	}
	return b.String()
}

func worlds(t *testing.T, s *Store) []WorldMeta {
	t.Helper()
	ws, err := s.Worlds()
	if err != nil {
		t.Fatalf("Worlds: %v", err)
	}
	return ws
}

// snapshot hält current.json und alle Dateien unter worlds/ fest (Pfad →
// Inhalt), um "nichts verändert" zu prüfen.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		if rel != "current.json" && !strings.HasPrefix(rel, "worlds") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return out
}

func sameFiles(a, b []api.FileEntry) bool { return slices.Equal(a, b) }

func TestWorldsMeta(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	if ws := worlds(t, s); len(ws) != 0 {
		t.Fatalf("Worlds ohne Welt = %v", ws)
	}

	// Welt A: zwei Stände, danach "Welt übernehmen" → Welt B (aktiv).
	a1 := putBlob(t, s, "level.dat", []byte("welt A eins"))
	ar := putBlob(t, s, "region/r.0.0.mca", []byte("region der welt A"))
	gA1 := commitPlayers(t, s, 0, false, t0, "26.1", []string{"Lukas", "Max"}, a1, ar)
	a2 := putBlob(t, s, "level.dat", []byte("welt A zwei"))
	gA2 := commitPlayers(t, s, 1, false, t0.Add(time.Hour), "26.1", nil, a2, ar)

	b1 := putBlob(t, s, "level.dat", []byte("welt B, der erste Stand"))
	gB1 := commitPlayers(t, s, 2, true, t0.Add(2*time.Hour), "26.2", []string{"Max"}, b1)
	b2 := putBlob(t, s, "level.dat", []byte("welt B zwei"))
	gB2 := commitPlayers(t, s, 1, false, t0.Add(3*time.Hour), "26.2", []string{"Lukas"}, b2)
	gB3, err := s.Restore(api.GenRef{WorldID: gB1.WorldID, Number: 1}, testAdmin, t0.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	A, B := gA1.WorldID, gB1.WorldID
	if gA2.WorldID != A || gB2.WorldID != B || gB3.WorldID != B || A == B {
		t.Fatalf("Testannahme: Welten %s/%s, %s/%s/%s", gA1.WorldID, gA2.WorldID, gB1.WorldID, gB2.WorldID, gB3.WorldID)
	}

	want := []WorldMeta{
		{WorldID: B, Active: true, Gens: []GenMeta{
			{WorldID: B, Number: 3, Created: t0.Add(4 * time.Hour), HostEmail: testAdmin.Email,
				Size: sizeOf(b1), MinecraftVersion: "26.2", RestoredFrom: &api.GenRef{WorldID: B, Number: 1}},
			{WorldID: B, Number: 2, Created: t0.Add(3 * time.Hour), HostEmail: testHost.Email,
				Players: []string{"Lukas"}, Size: sizeOf(b2), MinecraftVersion: "26.2"},
			{WorldID: B, Number: 1, Created: t0.Add(2 * time.Hour), HostEmail: testHost.Email,
				Players: []string{"Max"}, Size: sizeOf(b1), MinecraftVersion: "26.2"},
		}},
		{WorldID: A, Active: false, Gens: []GenMeta{
			{WorldID: A, Number: 2, Created: t0.Add(time.Hour), HostEmail: testHost.Email,
				Size: sizeOf(a2, ar), MinecraftVersion: "26.1"},
			{WorldID: A, Number: 1, Created: t0, HostEmail: testHost.Email,
				Players: []string{"Lukas", "Max"}, Size: sizeOf(a1, ar), MinecraftVersion: "26.1"},
		}},
	}
	wantStr := worldsString(want)
	if got := worldsString(worlds(t, s)); got != wantStr {
		t.Fatalf("Worlds =\n%s\nwant\n%s", got, wantStr)
	}

	// Aus den Dateien gelesen (neu geöffnet) dasselbe.
	s = openStore(t, dir, 1<<30)
	if got := worldsString(worlds(t, s)); got != wantStr {
		t.Fatalf("Worlds nach Neuöffnen =\n%s\nwant\n%s", got, wantStr)
	}

	// Einmal gelesene Metadaten bleiben im Speicher: Worlds liest die
	// Generationsdateien nicht bei jeder Abfrage neu.
	if err := os.WriteFile(s.genPath(A, 1), []byte("kaputt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := worldsString(worlds(t, s)); got != wantStr {
		t.Fatalf("Worlds mit Metadaten im Speicher =\n%s\nwant\n%s", got, wantStr)
	}

	// Die gelieferten Listen sind Kopien.
	ws := worlds(t, s)
	ws[0].Gens[1].Players[0] = "Mallory"
	ws[0].Gens[0].RestoredFrom.Number = 99
	if got := worldsString(worlds(t, s)); got != wantStr {
		t.Fatalf("Worlds nach Ändern der Rückgabe =\n%s\nwant\n%s", got, wantStr)
	}
}

func TestRestoreActiveWorld(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	shared := putBlob(t, s, "region/r.0.0.mca", []byte("gemeinsame Region"))
	files := map[int64][]api.FileEntry{}
	var world string
	for i := int64(1); i <= 7; i++ {
		f := putBlob(t, s, "level.dat", []byte(fmt.Sprintf("stand %d", i)))
		version := "26.2"
		if i == 3 {
			version = "26.1"
		}
		g := commitPlayers(t, s, i-1, false, t0.Add(time.Duration(i)*time.Hour), version, []string{"Lukas"}, f, shared)
		world, files[i] = g.WorldID, g.Files
	}

	now := t0.Add(10 * time.Hour)
	g, err := s.Restore(api.GenRef{WorldID: world, Number: 3}, testAdmin, now)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	check := func(phase string, g *api.Generation) {
		t.Helper()
		if g.WorldID != world || g.Number != 8 || g.Parent != 7 {
			t.Fatalf("%s: %s/%d Parent %d, want %s/8 Parent 7", phase, g.WorldID, g.Number, g.Parent, world)
		}
		if !sameFiles(g.Files, files[3]) {
			t.Fatalf("%s: Files = %v, want %v", phase, g.Files, files[3])
		}
		if g.RestoredFrom == nil || *g.RestoredFrom != (api.GenRef{WorldID: world, Number: 3}) {
			t.Fatalf("%s: RestoredFrom = %s, want %s/3", phase, refString(g.RestoredFrom), world)
		}
		if g.HostEmail != testAdmin.Email || g.MachineID != testAdmin.MachineID || !g.Created.Equal(now) {
			t.Fatalf("%s: Host %s/%s, Created %s", phase, g.HostEmail, g.MachineID, g.Created)
		}
		if g.MinecraftVersion != "26.1" {
			t.Fatalf("%s: MinecraftVersion = %q, want die des Stands 3 (26.1)", phase, g.MinecraftVersion)
		}
		if len(g.Players) != 0 {
			t.Fatalf("%s: Players = %q, beim Zurückholen war niemand online", phase, g.Players)
		}
	}
	check("Rückgabe", g)
	if id, h := head(t, s); id != world || h != 8 {
		t.Fatalf("Head = %s/%d, want %s/8", id, h, world)
	}
	_, hg, _ := s.Head()
	check("Head", hg)
	for n := int64(1); n <= 7; n++ {
		if !genExists(t, s, world, n) {
			t.Fatalf("Generation %d fehlt nach Restore", n)
		}
	}

	// Dauerhaft: nach Neuöffnen derselbe Head samt Vermerk.
	s = openStore(t, dir, 1<<30)
	_, hg, _ = s.Head()
	if hg == nil {
		t.Fatal("kein Head nach Neuöffnen")
	}
	check("Head nach Neuöffnen", hg)

	// Weiterspielen baut auf dem zurückgeholten Stand auf.
	f := putBlob(t, s, "level.dat", []byte("weiter"))
	if g := commit(t, s, 8, false, now.Add(time.Hour), f, shared); g.Number != 9 || g.Parent != 8 || g.RestoredFrom != nil {
		t.Fatalf("Commit nach Restore = %d Parent %d RestoredFrom %s", g.Number, g.Parent, refString(g.RestoredFrom))
	}
}

func TestRestoreArchivedWorld(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	bFiles := map[int64][]api.FileEntry{}
	var B string
	for i := int64(1); i <= 3; i++ {
		f := putBlob(t, s, "level.dat", []byte(fmt.Sprintf("archiv B %d", i)))
		g := commit(t, s, i-1, false, t0.Add(time.Duration(i)*time.Hour), f)
		B, bFiles[i] = g.WorldID, g.Files
	}
	a1 := putBlob(t, s, "level.dat", []byte("aktiv A 1"))
	A := commit(t, s, 3, true, t0.Add(5*time.Hour), a1).WorldID
	a2 := putBlob(t, s, "level.dat", []byte("aktiv A 2"))
	commit(t, s, 1, false, t0.Add(6*time.Hour), a2)
	if A == B {
		t.Fatal("Testannahme: zwei Welten")
	}

	now := t0.Add(7 * time.Hour)
	g, err := s.Restore(api.GenRef{WorldID: B, Number: 2}, testAdmin, now)
	if err != nil {
		t.Fatalf("Restore aus dem Archiv: %v", err)
	}
	if g.WorldID != B || g.Number != 4 || g.Parent != 3 {
		t.Fatalf("Restore = %s/%d Parent %d, want %s/4 Parent 3", g.WorldID, g.Number, g.Parent, B)
	}
	if !sameFiles(g.Files, bFiles[2]) {
		t.Fatalf("Files = %v, want %v", g.Files, bFiles[2])
	}
	if g.RestoredFrom == nil || *g.RestoredFrom != (api.GenRef{WorldID: B, Number: 2}) {
		t.Fatalf("RestoredFrom = %s", refString(g.RestoredFrom))
	}
	if id, n := s.HeadRef(); id != B || n != 4 {
		t.Fatalf("HeadRef = %s/%d, want %s/4", id, n, B)
	}

	ws := worlds(t, s)
	if len(ws) != 2 || ws[0].WorldID != B || !ws[0].Active || ws[1].WorldID != A || ws[1].Active {
		t.Fatalf("Worlds nach Restore =\n%s", worldsString(ws))
	}
	if len(ws[1].Gens) != 2 || len(ws[0].Gens) != 4 {
		t.Fatalf("Worlds nach Restore =\n%s", worldsString(ws))
	}
	for n := int64(1); n <= 2; n++ {
		if !genExists(t, s, A, n) {
			t.Fatalf("Welt A Generation %d fehlt", n)
		}
	}

	// Dauerhaft, und die bisher aktive Welt ist Archiv: Commits auf ihr
	// sind Konflikte, auf dem neuen Head nicht.
	s = openStore(t, dir, 1<<30)
	if id, h := head(t, s); id != B || h != 4 {
		t.Fatalf("Head nach Neuöffnen = %s/%d, want %s/4", id, h, B)
	}
	stale := api.CommitRequest{BaseWorldID: A, BaseGeneration: 2, MinecraftVersion: "26.2", Files: []api.FileEntry{a2}}
	if _, err := s.Commit(stale, testHost, now.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("Commit auf der archivierten Welt: %v, want ErrConflict", err)
	}
	if g := commit(t, s, 4, false, now.Add(2*time.Minute), a2); g.WorldID != B || g.Number != 5 {
		t.Fatalf("Commit nach Restore = %s/%d, want %s/5", g.WorldID, g.Number, B)
	}

	// Und zurück: ein Stand der jetzt archivierten Welt A.
	g, err = s.Restore(api.GenRef{WorldID: A, Number: 1}, testAdmin, now.Add(time.Hour))
	if err != nil || g.WorldID != A || g.Number != 3 || g.Parent != 2 || !sameFiles(g.Files, []api.FileEntry{a1}) {
		t.Fatalf("Restore zurück nach A = %+v, %v", g, err)
	}

	// Archive: das zuletzt bespielte zuerst, nicht das zuletzt angelegte.
	// B ist älter als A, wird aber danach noch einmal bespielt.
	if _, err := s.Restore(api.GenRef{WorldID: B, Number: 5}, testAdmin, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("Restore B/5: %v", err)
	}
	c1 := putBlob(t, s, "level.dat", []byte("welt C"))
	C := commit(t, s, 6, true, now.Add(3*time.Hour), c1).WorldID
	ws = worlds(t, s)
	if len(ws) != 3 || ws[0].WorldID != C || ws[1].WorldID != B || ws[2].WorldID != A {
		t.Fatalf("Reihenfolge der Welten (C aktiv, dann B, dann A) =\n%s", worldsString(ws))
	}
	// Die aktive Welt steht vorn, auch wenn Archive jüngere Stände haben
	// (Activate legt keinen Stand an).
	if err := s.Activate(A); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	ws = worlds(t, s)
	if len(ws) != 3 || ws[0].WorldID != A || !ws[0].Active || ws[1].WorldID != C || ws[2].WorldID != B || ws[1].Active || ws[2].Active {
		t.Fatalf("Reihenfolge der Welten (A aktiv, dann C, dann B) =\n%s", worldsString(ws))
	}
}

func TestRestoreUnknown(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	var archive string
	for i := int64(1); i <= 2; i++ {
		f := putBlob(t, s, "level.dat", []byte(fmt.Sprintf("archiv %d", i)))
		archive = commit(t, s, i-1, false, t0.Add(time.Duration(i)*time.Hour), f).WorldID
	}
	var active string
	for i := int64(1); i <= 3; i++ {
		f := putBlob(t, s, "level.dat", []byte(fmt.Sprintf("aktiv %d", i)))
		active = commit(t, s, i-1, i == 1, t0.Add(time.Duration(i+2)*time.Hour), f).WorldID
	}
	before := snapshot(t, dir)
	wsBefore := worldsString(worlds(t, s))

	for _, tc := range []struct {
		ref  api.GenRef
		want error
	}{
		{api.GenRef{WorldID: active, Number: 4}, ErrNotFound},
		{api.GenRef{WorldID: active, Number: 99}, ErrNotFound},
		{api.GenRef{WorldID: archive, Number: 3}, ErrNotFound},
		{api.GenRef{WorldID: "20990101T000000Z-deadbeef", Number: 1}, ErrNotFound},
		{api.GenRef{WorldID: active, Number: 0}, ErrInvalid},
		{api.GenRef{WorldID: "../" + active, Number: 1}, ErrInvalid},
		{api.GenRef{WorldID: "", Number: 1}, ErrInvalid},
	} {
		g, err := s.Restore(tc.ref, testAdmin, t0.Add(24*time.Hour))
		if !errors.Is(err, tc.want) || g != nil {
			t.Errorf("Restore(%s/%d) = %v, %v; want %v", tc.ref.WorldID, tc.ref.Number, g, err, tc.want)
		}
	}
	if id, h := head(t, s); id != active || h != 3 {
		t.Fatalf("Head = %s/%d, want %s/3", id, h, active)
	}
	after := snapshot(t, dir)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("Speicher verändert:\nvorher %v\nnachher %v", before, after)
	}
	if got := worldsString(worlds(t, s)); got != wsBefore {
		t.Fatalf("Worlds verändert:\n%s\nvorher\n%s", got, wsBefore)
	}
}

func TestPruneKeepsHourly(t *testing.T) {
	if KeepHours != 48 || KeepLast != 24 || KeepDays != 30 {
		t.Fatalf("Aufbewahrung %d/%d/%d, want 24/48/30", KeepLast, KeepHours, KeepDays)
	}
	s := openStore(t, t.TempDir(), 1<<30)
	f := putBlob(t, s, "level.dat", []byte("stand"))

	// 180 Stände über 60 Stunden: alle 20 Minuten einer, 3 je Stunde.
	const step = 20 * time.Minute
	var world string
	created := map[int64]time.Time{}
	for i := int64(1); i <= 180; i++ {
		at := t0.Add(time.Duration(i-1) * step)
		world = commit(t, s, i-1, false, at, f).WorldID
		created[i] = at
	}
	now := created[180].Add(time.Minute)
	// Vor dem Aufräumen einmal abfragen, damit die Metadaten im Speicher sind.
	if ws := worlds(t, s); len(ws) != 1 || len(ws[0].Gens) != 180 {
		t.Fatalf("Worlds vor Prune: %d Welten", len(ws))
	}
	if err := s.Prune(now); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	want := map[int64]bool{}
	for n := int64(180 - KeepLast + 1); n <= 180; n++ {
		want[n] = true
	}
	lastOf := func(layout string) map[string]int64 {
		m := map[string]int64{}
		for n, at := range created {
			if k := at.Format(layout); n > m[k] {
				m[k] = n
			}
		}
		return m
	}
	lastOfHour, lastOfDay := lastOf("2006-01-02T15"), lastOf("2006-01-02")
	for h := 0; h < 48; h++ {
		if n, ok := lastOfHour[now.Add(-time.Duration(h)*time.Hour).Format("2006-01-02T15")]; ok {
			want[n] = true
		}
	}
	for d := 0; d < KeepDays; d++ {
		if n, ok := lastOfDay[now.AddDate(0, 0, -d).Format("2006-01-02")]; ok {
			want[n] = true
		}
	}
	// Letzte 24 = 8 Stunden; dazu 40 weitere Stunden; die Tagesstände
	// fallen auf Stundenstände. Die Stunden 0–11 des ersten Tags fallen weg.
	if len(want) != 64 {
		t.Fatalf("Testannahme: %d erwartete Stände, want 64", len(want))
	}
	for n := int64(1); n <= 180; n++ {
		if got := genExists(t, s, world, n); got != want[n] {
			t.Errorf("Generation %d (%s): vorhanden = %v, want %v", n, created[n].Format("01-02 15:04"), got, want[n])
		}
	}
	ws := worlds(t, s)
	if len(ws) != 1 || len(ws[0].Gens) != len(want) {
		t.Fatalf("Worlds nach Prune: %d Stände, want %d", len(ws[0].Gens), len(want))
	}
	for _, g := range ws[0].Gens {
		if !want[g.Number] {
			t.Errorf("Worlds nennt weggeräumte Generation %d", g.Number)
		}
	}
	// Prune entfernt die Angaben weggeräumter Generationen auch im Speicher.
	if len(s.meta) != len(want) {
		t.Errorf("%d Angaben im Speicher nach Prune, want %d", len(s.meta), len(want))
	}
}
