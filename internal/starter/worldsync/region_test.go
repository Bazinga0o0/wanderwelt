package worldsync_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// chunk ist ein Chunk einer synthetischen Regionsdatei.
type chunk struct {
	index   int    // Platz im Kopf (0–1023)
	sector  int    // erster belegter Sektor
	count   int    // Anzahl belegter Sektoren
	payload string // Chunk-Daten
	stamp   uint32 // Zeitstempel im Kopf
}

// makeRegion baut eine Regionsdatei aus sectors Sektoren zu 4096 Bytes:
// Außerhalb des Kopfs stehen überall Fremdbytes (Reste alter Chunks), dann
// werden die Chunks eingetragen wie von Minecraft: Position im Kopf (3 Bytes
// Sektor, 1 Byte Anzahl), Zeitstempel ab Byte 4096, im Sektorbereich Länge,
// Kompressionsart 2 und Daten, der Rest des Bereichs Nullen.
func makeRegion(sectors int, chunks ...chunk) []byte {
	data := make([]byte, sectors*4096)
	for i := 8192; i < len(data); i++ {
		data[i] = byte(0xa0 + i%7)
	}
	for _, c := range chunks {
		binary.BigEndian.PutUint32(data[4*c.index:], uint32(c.sector)<<8|uint32(c.count))
		binary.BigEndian.PutUint32(data[4096+4*c.index:], c.stamp)
		area := data[c.sector*4096 : (c.sector+c.count)*4096]
		clear(area)
		binary.BigEndian.PutUint32(area, uint32(len(c.payload)+1))
		area[4] = 2
		copy(area[5:], c.payload)
	}
	return data
}

// setLocation trägt im Kopf von data für Chunk index Sektor und Anzahl ein,
// ohne Daten zu schreiben (für kaputte Köpfe).
func setLocation(data []byte, index, sector, count int) []byte {
	binary.BigEndian.PutUint32(data[4*index:], uint32(sector)<<8|uint32(count))
	return data
}

// segmentAt liefert das Segment, das bei Byte off beginnt.
func segmentAt(t *testing.T, segs []api.Segment, off int64) api.Segment {
	t.Helper()
	var pos int64
	for _, s := range segs {
		if pos == off {
			return s
		}
		pos += s.Size
	}
	t.Fatalf("kein Segment beginnt bei Byte %d (Segmente %+v)", off, segs)
	return api.Segment{}
}

// checkSegmentsOf prüft unabhängig vom Code, dass segs die Datei data lückenlos
// in Stücke mit passendem Hash zerlegt.
func checkSegmentsOf(t *testing.T, name string, data []byte, segs []api.Segment) {
	t.Helper()
	var off int64
	for i, s := range segs {
		if s.Size <= 0 || off+s.Size > int64(len(data)) {
			t.Fatalf("%s: Segment %d hat Größe %d bei Byte %d von %d", name, i, s.Size, off, len(data))
		}
		if want := shaHex(data[off : off+s.Size]); s.SHA256 != want {
			t.Fatalf("%s: Segment %d (Byte %d, %d Bytes) hat Hash %s, Inhalt %s", name, i, off, s.Size, s.SHA256, want)
		}
		off += s.Size
	}
	if off != int64(len(data)) {
		t.Fatalf("%s: Segmente ergeben %d Bytes, Datei hat %d", name, off, len(data))
	}
}

func entryOf(t *testing.T, files []api.FileEntry, path string) api.FileEntry {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("Manifest nennt %s nicht", path)
	return api.FileEntry{}
}

func blobInStore(h *harness, sha string) bool {
	_, err := os.Stat(filepath.Join(h.storeDir, "blobs", sha[:2], sha))
	return err == nil
}

func TestSplitJoinBitIdentical(t *testing.T) {
	// Im Kopf in der Reihenfolge B (Platz 0, Sektor 2), A (Platz 5, Sektor 8),
	// C (Platz 1023, Sektor 4): nicht aufsteigend. Sektor 3 und 7 sind Lücken
	// mit Fremdbytes, Sektoren 10 und 11 ein Rest am Ende.
	data := makeRegion(12,
		chunk{index: 0, sector: 2, count: 1, payload: "Chunk B", stamp: 1700000001},
		chunk{index: 5, sector: 8, count: 2, payload: strings.Repeat("Chunk A ", 600), stamp: 1700000002},
		chunk{index: 1023, sector: 4, count: 3, payload: strings.Repeat("Chunk C ", 1300), stamp: 1700000003},
	)
	orig := bytes.Clone(data)
	segs, parts, ok := worldsync.SplitRegion(data)
	if !ok {
		t.Fatal("gültige Regionsdatei nicht zerlegt")
	}
	// Kopf, B, Lücke, C, Lücke, A, Rest – in Bytes.
	bounds := []int{0, 8192, 12288, 16384, 28672, 32768, 40960, 49152}
	if len(segs) != len(bounds)-1 || len(parts) != len(bounds)-1 {
		t.Fatalf("%d Segmente, %d Teile, erwartet %d: %+v", len(segs), len(parts), len(bounds)-1, segs)
	}
	for i := range segs {
		want := orig[bounds[i]:bounds[i+1]]
		if !bytes.Equal(parts[i], want) {
			t.Errorf("Teil %d ist nicht Byte %d bis %d", i, bounds[i], bounds[i+1])
		}
		if segs[i].Size != int64(len(want)) || segs[i].SHA256 != shaHex(want) {
			t.Errorf("Segment %d = %+v, erwartet %d Bytes mit Hash %s", i, segs[i], len(want), shaHex(want))
		}
	}
	if got := worldsync.JoinSegments(parts); !bytes.Equal(got, orig) {
		t.Fatalf("JoinSegments ergibt %d Bytes, nicht das Original (%d Bytes)", len(got), len(orig))
	}
	if !bytes.Equal(data, orig) {
		t.Fatal("SplitRegion hat die Eingabe verändert")
	}

	// Dicht gepackt bis zum Dateiende, Chunk direkt hinter dem Kopf: keine
	// leeren Segmente für fehlende Lücken oder fehlenden Rest.
	packed := makeRegion(5,
		chunk{index: 7, sector: 3, count: 2, payload: "A", stamp: 1},
		chunk{index: 3, sector: 2, count: 1, payload: "B", stamp: 2},
	)
	segs, parts, ok = worldsync.SplitRegion(packed)
	if !ok {
		t.Fatal("dicht gepackte Regionsdatei nicht zerlegt")
	}
	if len(segs) != 3 || segs[0].Size != 8192 || segs[1].Size != 4096 || segs[2].Size != 8192 {
		t.Fatalf("dicht gepackt: Segmente %+v, erwartet 8192, 4096, 8192 Bytes", segs)
	}
	if !bytes.Equal(worldsync.JoinSegments(parts), packed) {
		t.Fatal("dicht gepackt: JoinSegments ergibt nicht das Original")
	}
}

func TestUnchangedChunkSameBlobAfterMove(t *testing.T) {
	a1 := chunk{index: 0, sector: 5, count: 1, payload: "A alt", stamp: 100}
	b := chunk{index: 1, sector: 2, count: 2, payload: strings.Repeat("unveränderter Chunk B ", 250), stamp: 101}
	c := chunk{index: 2, sector: 4, count: 1, payload: "C bleibt", stamp: 102}
	v1 := makeRegion(8, a1, b, c)

	// Minecraft hat A neu geschrieben (größer, also an einen neuen Platz) und
	// B in neue Sektoren gelegt; der Kopf ändert sich, B selbst nicht.
	a2 := chunk{index: 0, sector: 2, count: 2, payload: strings.Repeat("A neu ", 1000), stamp: 200}
	b.sector = 6
	v2 := makeRegion(9, a2, b, c)

	segs1, _, ok1 := worldsync.SplitRegion(v1)
	segs2, _, ok2 := worldsync.SplitRegion(v2)
	if !ok1 || !ok2 {
		t.Fatalf("gültige Regionsdateien nicht zerlegt: %v, %v", ok1, ok2)
	}
	checkSegmentsOf(t, "vorher", v1, segs1)
	checkSegmentsOf(t, "nachher", v2, segs2)

	// B lag in Sektor 2–3 und liegt jetzt in 6–7, C bleibt in Sektor 4.
	wantB := shaHex(v1[2*4096 : 4*4096])
	if shaHex(v2[6*4096:8*4096]) != wantB {
		t.Fatal("Testaufbau: B ist nach dem Verschieben nicht bytegleich")
	}
	if before, after := segmentAt(t, segs1, 2*4096), segmentAt(t, segs2, 6*4096); before.SHA256 != wantB || after.SHA256 != wantB || after.Size != 8192 {
		t.Errorf("Segment von B: vorher %+v, nachher %+v, erwartet beide %s mit 8192 Bytes", before, after, wantB)
	}
	if before, after := segmentAt(t, segs1, 4*4096), segmentAt(t, segs2, 4*4096); before != after {
		t.Errorf("Segment von C: vorher %+v, nachher %+v", before, after)
	}
	if segmentAt(t, segs1, 0) == segmentAt(t, segs2, 0) {
		t.Error("Kopf-Segment unverändert, obwohl Positionen und Zeitstempel sich geändert haben")
	}
}

// recipeOf liest das Rezept sha aus dem Speicher des Koordinators.
func recipeOf(t *testing.T, h *harness, sha string) []api.Segment {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.storeDir, "blobs", sha[:2], sha))
	if err != nil {
		t.Fatalf("Rezept %s nicht beim Koordinator: %v", sha, err)
	}
	segs, err := api.ParseRecipe(data)
	if err != nil {
		t.Fatalf("Rezept %s: %v", sha, err)
	}
	return segs
}

func localRecipe(sha string) string { return filepath.Join(group.Home(), "recipes", sha) }

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func hashesOf(segs []api.Segment) []string {
	var out []string
	for _, s := range segs {
		out = append(out, s.SHA256)
	}
	return out
}

func sorted(s []string) []string { return slices.Sorted(slices.Values(s)) }

func TestChangedChunkOnlyNewSegments(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), t.TempDir()

	// Kopf, A, B (2 Sektoren), Lücke, C, Rest: sechs Segmente.
	bOld := chunk{index: 1, sector: 3, count: 2, payload: strings.Repeat("B alt ", 1000), stamp: 1000}
	region := makeRegion(10,
		chunk{index: 0, sector: 2, count: 1, payload: "A", stamp: 999},
		bOld,
		chunk{index: 32, sector: 6, count: 1, payload: "C", stamp: 998},
	)
	nether := makeRegion(4, chunk{index: 9, sector: 3, count: 1, payload: "Nether", stamp: 5})
	world := map[string]string{
		"level.dat":               strings.Repeat("L", 3000),
		"region/r.0.0.mca":        string(region),
		"DIM-1/region/r.-1.0.mca": string(nether),
		"poi/r.0.0.mca":           "", // leere Regionsdatei: ganze Datei
		"data/raids.dat":          "raids",
	}
	world1 := maps.Clone(world)
	writeTree(t, worldDir, world)
	gen1 := h.snapshot(t, worldDir, stagingDir, hc, nil)
	_, batched := h.take()

	r1 := entryOf(t, gen1.Files, "region/r.0.0.mca")
	if r1.Size != int64(len(region)) || r1.SHA256 != shaHex(region) || r1.Recipe == "" {
		t.Fatalf("Eintrag der Regionsdatei: %+v; erwartet %d Bytes, Hash %s, mit Rezept", r1, len(region), shaHex(region))
	}
	segs1 := recipeOf(t, h, r1.Recipe)
	if len(segs1) != 6 {
		t.Fatalf("Rezept der Regionsdatei hat %d Segmente, erwartet 6: %+v", len(segs1), segs1)
	}
	checkSegmentsOf(t, "region/r.0.0.mca", region, segs1)
	n1 := entryOf(t, gen1.Files, "DIM-1/region/r.-1.0.mca")
	segsN := recipeOf(t, h, n1.Recipe)
	if len(segsN) != 3 {
		t.Fatalf("Nether-Rezept hat %d Segmente, erwartet 3", len(segsN))
	}
	checkSegmentsOf(t, "DIM-1/region/r.-1.0.mca", nether, segsN)
	for _, p := range []string{"poi/r.0.0.mca", "level.dat", "data/raids.dat"} {
		if f := entryOf(t, gen1.Files, p); f.Recipe != "" || f.SHA256 != shaHex([]byte(world[p])) {
			t.Errorf("%s: %+v, erwartet ganze Datei mit Hash %s", p, f, shaHex([]byte(world[p])))
		}
	}
	// Für zerlegte Dateien geht nie ein Blob der ganzen Datei hoch: Segmente
	// und Rezepte gebündelt, ganze Dateien einzeln (3 PUTs).
	if want := slices.Concat(hashesOf(segs1), hashesOf(segsN), []string{r1.Recipe, n1.Recipe}); !slices.Equal(sorted(batched), sorted(want)) {
		t.Errorf("gebündelt hochgeladen %d Blobs, erwartet die %d Segmente und Rezepte", len(batched), len(want))
	}
	if n := h.blobPuts.Load(); n != 3 {
		t.Errorf("%d einzelne PUTs, erwartet 3 (ganze Dateien)", n)
	}
	for _, f := range []api.FileEntry{r1, n1} {
		if blobInStore(h, f.SHA256) {
			t.Errorf("%s: Blob der ganzen Datei hochgeladen", f.Path)
		}
	}

	// Chunk B wird an seinem Platz neu geschrieben (gleiche Größe), sein
	// Zeitstempel im Kopf ändert sich.
	bNew := bOld
	bNew.payload, bNew.stamp = strings.Repeat("B neu ", 1000), 2000
	changed := makeRegion(10,
		chunk{index: 0, sector: 2, count: 1, payload: "A", stamp: 999},
		bNew,
		chunk{index: 32, sector: 6, count: 1, payload: "C", stamp: 998},
	)
	path := filepath.Join(worldDir, "region", "r.0.0.mca")
	old, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Time{}, old.ModTime().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	world["region/r.0.0.mca"] = string(changed)

	h.blobPuts.Store(0)
	gen2 := h.snapshot(t, worldDir, stagingDir, hc, gen1)
	_, batched = h.take()
	r2 := entryOf(t, gen2.Files, "region/r.0.0.mca")
	segs2 := recipeOf(t, h, r2.Recipe)
	checkSegmentsOf(t, "region/r.0.0.mca geändert", changed, segs2)
	if len(segs2) != 6 {
		t.Fatalf("geänderte Regionsdatei hat %d Segmente, erwartet 6", len(segs2))
	}
	for i := range segs2 {
		if differs := segs2[i] != segs1[i]; differs != (i == 0 || i == 2) {
			t.Errorf("Segment %d geändert = %v (nur Kopf und B dürfen sich ändern)", i, differs)
		}
	}
	// Zweiter Upload: genau Kopf und Chunk B, danach das neue Rezept.
	if want := []string{segs2[0].SHA256, segs2[2].SHA256, r2.Recipe}; !slices.Equal(batched, want) || h.blobPuts.Load() != 0 {
		t.Fatalf("zweiter Upload: gebündelt %d Blobs, %d PUTs; erwartet Kopf, B, Rezept und keinen PUT", len(batched), h.blobPuts.Load())
	}
	if blobInStore(h, r2.SHA256) {
		t.Error("geänderte Regionsdatei als ganzer Blob hochgeladen")
	}
	// Lokal liegen nur Rezepte, die der Cache noch nennt.
	if exists(localRecipe(r1.Recipe)) || !exists(localRecipe(r2.Recipe)) || !exists(localRecipe(n1.Recipe)) {
		t.Errorf("lokale Rezepte: alt %v, neu %v, Nether %v; erwartet nur neu und Nether",
			exists(localRecipe(r1.Recipe)), exists(localRecipe(r2.Recipe)), exists(localRecipe(n1.Recipe)))
	}
	// Ein Download (hier Generation 1) mit demselben Cache lässt Rezepte
	// liegen, die der Cache für die Bereitstellung nennt.
	if err := worldsync.Download(ctx, h.c, gen1, t.TempDir(), hc, nil); err != nil {
		t.Fatalf("Download Generation 1 beim Host: %v", err)
	}
	if !exists(localRecipe(r2.Recipe)) || !exists(localRecipe(r1.Recipe)) {
		t.Errorf("nach Download beim Host: Rezept der Bereitstellung %v, der Generation %v; erwartet beide",
			exists(localRecipe(r2.Recipe)), exists(localRecipe(r1.Recipe)))
	}

	// Der Hash-Cache merkt sich das Rezept (auch über Speichern und Laden):
	// Bei gleicher Größe und Änderungszeit wird nicht neu zerlegt. Beweis:
	// Inhalt der Bereitstellung heimlich ändern, Zeit zurücksetzen – mit
	// Cache bleibt das alte Rezept, ohne Cache kommt das des Inhalts.
	staged := filepath.Join(stagingDir, "region", "r.0.0.mca")
	info, err := os.Lstat(staged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, region, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staged, time.Time{}, info.ModTime()); err != nil {
		t.Fatal(err)
	}
	stagedEntry := func(hc *worldsync.HashCache) api.FileEntry {
		t.Helper()
		files, err := worldsync.BuildManifest(stagingDir, hc)
		if err != nil {
			t.Fatal(err)
		}
		return entryOf(t, files, "region/r.0.0.mca")
	}
	if f := stagedEntry(worldsync.LoadHashCache()); f != r2 {
		t.Errorf("mit Cache: %+v, erwartet das gemerkte %+v", f, r2)
	}
	if f := stagedEntry(nil); f != r1 {
		t.Errorf("ohne Cache: %+v, erwartet das des Inhalts %+v", f, r1)
	}
	// Fehlt das Rezept lokal, gilt der Eintrag nicht: neu zerlegen.
	if err := os.Remove(localRecipe(r2.Recipe)); err != nil {
		t.Fatal(err)
	}
	if f := stagedEntry(worldsync.LoadHashCache()); f != r1 || !exists(localRecipe(r1.Recipe)) {
		t.Errorf("ohne lokales Rezept: %+v, erwartet neu zerlegt %+v", f, r1)
	}
	// Cache-Datei auf Platte ändern (Zahlen exakt, mtime_ns passt nicht in
	// float64): unverändert zurückgeschrieben gilt der Eintrag weiter, mit
	// kaputtem Rezept-Hash wird er beim Laden verworfen.
	cachePath := filepath.Join(group.Home(), "hashcache.json")
	rewrite := func(change func(entry map[string]any)) {
		t.Helper()
		raw, err := os.ReadFile(cachePath)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		entry, ok := doc["entries"].(map[string]any)[staged].(map[string]any)
		if !ok {
			t.Fatalf("Hash-Cache hat keinen Eintrag für %s", staged)
		}
		change(entry)
		if raw, err = json.Marshal(doc); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cachePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rewrite(func(entry map[string]any) { entry["sha256"] = r2.SHA256; entry["recipe"] = r2.Recipe })
	if err := os.WriteFile(localRecipe(r2.Recipe), mustEncode(t, segs2), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := stagedEntry(worldsync.LoadHashCache()); f != r2 {
		t.Fatalf("Testaufbau: zurückgeschriebener Cache gilt nicht: %+v", f)
	}
	// Ein Rezept-„Hash“, der aus der Rezeptablage hinauszeigt, gilt nicht.
	rewrite(func(entry map[string]any) { entry["recipe"] = "../hashcache.json" })
	if f := stagedEntry(worldsync.LoadHashCache()); f != r1 {
		t.Errorf("mit beschädigtem Cache: %+v, erwartet neu zerlegt %+v", f, r1)
	}

	// Ein neuer Starter (leerer Cache, keine Rezepte) lädt Generation 1: die
	// Rezepte in einer Anfrage, je Regionsdatei eine, bis zu vier Dateien
	// gleichzeitig.
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	hc2 := worldsync.LoadHashCache()
	target := t.TempDir()
	h.take()
	h.blobGets.Store(0)
	h.fetchReqs.Store(0)
	h.dlDelay.Store(int64(30 * time.Millisecond))
	if err := worldsync.Download(ctx, h.c, gen1, target, hc2, nil); err != nil {
		t.Fatalf("Download Generation 1: %v", err)
	}
	h.dlDelay.Store(0)
	if got := readTree(t, target); !maps.Equal(got, world1) {
		t.Fatalf("Welt nach Download weicht ab: %q", slices.Sorted(maps.Keys(got)))
	}
	fetched, _ := h.take()
	if want := slices.Concat([]string{r1.Recipe, n1.Recipe}, hashesOf(segs1), hashesOf(segsN)); !slices.Equal(sorted(fetched), sorted(want)) ||
		h.fetchReqs.Load() != 3 || h.blobGets.Load() != 3 {
		t.Errorf("erster Download: %d Hashes in %d Fetch-Anfragen, %d GETs; erwartet %d in 3, 3 GETs",
			len(fetched), h.fetchReqs.Load(), h.blobGets.Load(), len(want))
	}
	if m := h.dlMaxFlight.Load(); m < 2 || m > 4 {
		t.Errorf("höchstens %d Lade-Anfragen gleichzeitig, erwartet 2 bis 4", m)
	}

	// Wechsel auf Generation 2: Die vorhandene Datei liefert alle Segmente
	// außer Kopf und B; nur diese und das neue Rezept kommen vom Koordinator.
	h.blobGets.Store(0)
	h.fetchReqs.Store(0)
	if err := worldsync.Download(ctx, h.c, gen2, target, hc2, nil); err != nil {
		t.Fatalf("Download Generation 2: %v", err)
	}
	if got := readTree(t, target); !maps.Equal(got, world) {
		t.Fatalf("Welt nach Download weicht ab: %q", slices.Sorted(maps.Keys(got)))
	}
	// Lokal bleiben nur Rezepte, die diese Generation oder der Cache nennt
	// (das Rezept von Generation 1 ist weg).
	if names := dirEntries(t, filepath.Join(group.Home(), "recipes")); !slices.Equal(names, sorted([]string{r2.Recipe, n1.Recipe})) {
		t.Errorf("lokale Rezepte nach Download: %q, erwartet nur die von Generation 2", names)
	}
	fetched, _ = h.take()
	if want := []string{r2.Recipe, segs2[0].SHA256, segs2[2].SHA256}; !slices.Equal(sorted(fetched), sorted(want)) ||
		h.fetchReqs.Load() != 2 || h.blobGets.Load() != 0 {
		t.Errorf("zweiter Download: %d Hashes in %d Fetch-Anfragen, %d GETs; erwartet Rezept, Kopf, B in 2 Anfragen, kein GET",
			len(fetched), h.fetchReqs.Load(), h.blobGets.Load())
	}

	// Download hinterlegt Hash und Rezept, StageCached gibt die Einträge an
	// die Bereitstellung weiter (Beweis wie oben: heimlich geänderter
	// Inhalt).
	staging3 := t.TempDir()
	if err := worldsync.StageCached(target, staging3, hc2); err != nil {
		t.Fatalf("StageCached: %v", err)
	}
	staged3 := filepath.Join(staging3, "region", "r.0.0.mca")
	if info, err = os.Lstat(staged3); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged3, region, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staged3, time.Time{}, info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if files, err := worldsync.BuildManifest(staging3, hc2); err != nil || entryOf(t, files, "region/r.0.0.mca") != r2 {
		t.Errorf("Manifest nach StageCached: %+v, %v; erwartet den Eintrag aus dem Download %+v", entryOf(t, files, "region/r.0.0.mca"), err, r2)
	}
	if again, err := worldsync.BuildManifest(target, hc2); err != nil || entryOf(t, again, "region/r.0.0.mca") != r2 {
		t.Fatalf("Manifest des Weltordners nach Download: %v", err)
	}

	// Ein Starter ohne Cache findet die Welt schon aktuell vor: nichts wird
	// geholt, und auch für die vorhandenen Dateien kennt der Cache danach das
	// Rezept.
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	hc3 := worldsync.LoadHashCache()
	h.take()
	h.blobGets.Store(0)
	if err := worldsync.Download(ctx, h.c, gen2, target, hc3, nil); err != nil {
		t.Fatalf("Download in aktuelle Welt: %v", err)
	}
	if fetched, _ := h.take(); len(fetched) != 0 || h.blobGets.Load() != 0 {
		t.Errorf("Download in aktuelle Welt holte %d Hashes und %d Blobs", len(fetched), h.blobGets.Load())
	}
	staging4 := t.TempDir()
	if err := worldsync.StageCached(target, staging4, hc3); err != nil {
		t.Fatal(err)
	}
	staged4 := filepath.Join(staging4, "region", "r.0.0.mca")
	if info, err = os.Lstat(staged4); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged4, region, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staged4, time.Time{}, info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if files, err := worldsync.BuildManifest(staging4, hc3); err != nil || entryOf(t, files, "region/r.0.0.mca") != r2 {
		t.Errorf("Manifest nach Download in aktuelle Welt: %v; erwartet den Eintrag %+v aus dem Cache", err, r2)
	}
}

func mustEncode(t *testing.T, segs []api.Segment) []byte {
	t.Helper()
	data, err := api.EncodeRecipe(segs)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Nach einem 412 prüft UploadAndCommit die Segmente aller Rezepte, auch
// wenn das Rezept selbst beim Koordinator liegt.
func TestCommitRetryReuploadsRecipeSegments(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	path := filepath.Join(worldDir, "region", "r.0.0.mca")
	v1 := makeRegion(6, chunk{index: 0, sector: 2, count: 1, payload: "A1"}, chunk{index: 1, sector: 3, count: 2, payload: "B"})
	v2 := makeRegion(6, chunk{index: 0, sector: 2, count: 1, payload: "A2"}, chunk{index: 1, sector: 3, count: 2, payload: "B"})
	writeTree(t, worldDir, map[string]string{"level.dat": "L", "region/r.0.0.mca": string(v1)})
	base, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	setRegion := func(data []byte, shift time.Duration) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Time{}, base.ModTime().Add(shift)); err != nil {
			t.Fatal(err)
		}
	}
	gen1 := h.snapshot(t, worldDir, stagingDir, hc, nil)
	r1 := entryOf(t, gen1.Files, "region/r.0.0.mca")
	a1 := recipeOf(t, h, r1.Recipe)[1] // Segment von Chunk A1
	setRegion(v2, 2*time.Second)
	gen2 := h.snapshot(t, worldDir, stagingDir, hc, gen1)

	// Stand 3 ist wieder Stand 1: Sein Rezept liegt beim Koordinator, steht
	// aber nicht im Head; ein Segment davon ist inzwischen weg.
	setRegion(v1, 4*time.Second)
	if err := os.Remove(filepath.Join(h.storeDir, "blobs", a1.SHA256[:2], a1.SHA256)); err != nil {
		t.Fatal(err)
	}
	if err := worldsync.Stage(worldDir, stagingDir); err != nil {
		t.Fatal(err)
	}
	files, err := worldsync.BuildManifest(stagingDir, hc)
	if err != nil {
		t.Fatal(err)
	}
	if entryOf(t, files, "region/r.0.0.mca") != r1 {
		t.Fatal("Testaufbau: Stand 3 hat nicht das Rezept von Stand 1")
	}
	h.commits.Store(0)
	h.take()
	gen3, err := worldsync.UploadAndCommit(ctx, h.c, stagingDir, h.request(files, gen2))
	if err != nil || gen3.Number != 3 {
		t.Fatalf("UploadAndCommit: %+v, %v", gen3, err)
	}
	if _, batched := h.take(); h.commits.Load() != 2 || !slices.Equal(batched, []string{a1.SHA256}) {
		t.Fatalf("%d Commits, nachgeladen %d Blobs; erwartet 2 Commits und genau das fehlende Segment", h.commits.Load(), len(batched))
	}
	target := t.TempDir()
	if err := worldsync.Download(ctx, h.c, gen3, target, worldsync.LoadHashCache(), nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := readTree(t, target); got["region/r.0.0.mca"] != string(v1) {
		t.Fatal("Regionsdatei nach Download weicht ab")
	}
	// Das Rezept lag lokal und wurde nicht geholt.
	if fetched, _ := h.take(); !slices.Equal(sorted(fetched), sorted(hashesOf(recipeOf(t, h, r1.Recipe)))) {
		t.Errorf("Download holte %d Hashes (Rezept dabei: %v), erwartet genau die 4 Segmente", len(fetched), slices.Contains(fetched, r1.Recipe))
	}

	// Ist die lokale Kopie beschädigt, holt Download das Rezept vom
	// Koordinator und ersetzt die Kopie; auch ohne Cache bleibt das Rezept
	// der geladenen Generation liegen.
	if err := os.WriteFile(localRecipe(r1.Recipe), []byte("kaputt"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ohne Cache räumt Download keine lokalen Rezepte auf: Er weiß nicht,
	// welche eine Bereitstellung noch braucht.
	foreign := localRecipe(strings.Repeat("ab", 32))
	if err := os.WriteFile(foreign, []byte("Rezept einer Bereitstellung"), 0o644); err != nil {
		t.Fatal(err)
	}
	target2 := t.TempDir()
	if err := worldsync.Download(ctx, h.c, gen3, target2, nil, nil); err != nil {
		t.Fatalf("Download mit beschädigtem lokalem Rezept: %v", err)
	}
	if !exists(foreign) {
		t.Error("Download ohne Cache hat ein lokales Rezept aufgeräumt")
	}
	if got := readTree(t, target2); got["region/r.0.0.mca"] != string(v1) {
		t.Fatal("Regionsdatei nach Download mit beschädigtem Rezept weicht ab")
	}
	if fetched, _ := h.take(); !slices.Contains(fetched, r1.Recipe) {
		t.Error("beschädigtes lokales Rezept nicht vom Koordinator geholt")
	}
	if shaHex(mustRead(t, localRecipe(r1.Recipe))) != r1.Recipe {
		t.Error("lokale Kopie des Rezepts nach dem Download noch beschädigt")
	}
}

// Größenprobe: 200 Regionsdateien zu je 1024 Chunks. Das Manifest bleibt
// klein, jede Missing-Anfrage nennt höchstens 10 000 Hashes.
func TestManifestSizeProbe(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	chunks := make([]chunk, 1024)
	for i := range chunks {
		chunks[i] = chunk{index: i, sector: 2 + i, count: 1, payload: fmt.Sprintf("Chunk %04d", i), stamp: uint32(i)}
	}
	region := makeRegion(2+1024, chunks...)
	src := filepath.Join(t.TempDir(), "r.mca")
	if err := os.WriteFile(src, region, 0o644); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staging, "region"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Harte Links: 200 Dateien, aber nur einmal 4 MiB auf der Platte.
	for i := range 200 {
		if err := os.Link(src, filepath.Join(staging, "region", fmt.Sprintf("r.%d.0.mca", i))); err != nil {
			t.Skipf("keine harten Links: %v", err)
		}
	}
	files, err := worldsync.BuildManifest(staging, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 200 || files[0].Recipe == "" {
		t.Fatalf("Manifest: %d Einträge, erster %+v", len(files), files[0])
	}
	recipe, err := os.ReadFile(localRecipe(files[0].Recipe))
	if err != nil {
		t.Fatal(err)
	}
	segs, err := api.ParseRecipe(recipe)
	if err != nil {
		t.Fatal(err)
	}
	inline, _ := json.Marshal(segs) // so groß wäre die Segmentliste im Manifest
	t.Logf("Größenprobe: 200 Regionsdateien × 1024 Chunks = %d Segmente je Datei; Manifest %d Bytes (%d je Eintrag); Rezept %d Bytes; Segmentlisten im Manifest wären %d Bytes",
		len(segs), len(manifest), len(manifest)/len(files), len(recipe), 200*len(inline))
	if len(manifest) >= 1_000_000 {
		t.Errorf("Manifest %d Bytes, erwartet unter 1 MB", len(manifest))
	}
	if len(segs) != 1025 {
		t.Errorf("Rezept mit %d Segmenten, erwartet 1025 (Kopf und 1024 Chunks)", len(segs))
	}

	// Missing in Häppchen zu höchstens 10 000 Hashes.
	fake := make([]string, 25_000)
	for i := range fake {
		fake[i] = shaHex([]byte(fmt.Sprint("fehlt ", i)))
	}
	if m, err := h.c.Missing(ctx, fake); err != nil || len(m) != 25_000 {
		t.Fatalf("Missing: %d, %v", len(m), err)
	}
	if n, m := h.missingReqs.Load(), h.missingMax.Load(); n != 3 || m != 10_000 {
		t.Errorf("Missing in %d Anfragen, größte %d Hashes; erwartet 3 zu höchstens 10 000", n, m)
	}

	// Upload der Probe: eine Missing-Anfrage für Dateien und Rezepte (ein
	// gemeinsames Rezept), eine für seine 1025 Segmente; ein Paket Segmente,
	// eines mit dem Rezept.
	h.missingReqs.Store(0)
	h.missingMax.Store(0)
	if err := worldsync.Upload(ctx, h.c, staging, files); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if n, m, b := h.missingReqs.Load(), h.missingMax.Load(), h.batchReqs.Load(); n != 2 || m != 1025 || b != 2 {
		t.Errorf("Upload: %d Missing-Anfragen (größte %d), %d Pakete; erwartet 2 (1025), 2", n, m, b)
	}
	t.Logf("Größenprobe Upload: %d Missing-Anfragen, größte %d Hashes, %d Batch-Anfragen", h.missingReqs.Load(), h.missingMax.Load(), h.batchReqs.Load())
}

// Absturzreste (.wanderwelt-*.tmp) in der Welt werden nie bereitgestellt
// oder ins Manifest aufgenommen.
func TestStageSkipsCrashLeftovers(t *testing.T) {
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	keep := map[string]string{"level.dat": "L", "data/keep.tmp": "fremd, bleibt", ".wanderwelt-notiz.txt": "kein .tmp"}
	world := maps.Clone(keep)
	world["region/.wanderwelt-123.tmp"] = "Rest einer Kopie"
	world[".wanderwelt-download.456.tmp"] = "Rest der Markierung"
	world["data/.wanderwelt-x.tmp"] = "Rest"
	writeTree(t, worldDir, world)
	if err := worldsync.Stage(worldDir, stagingDir); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if got := readTree(t, stagingDir); !maps.Equal(got, keep) {
		t.Errorf("bereitgestellt: %q", slices.Sorted(maps.Keys(got)))
	}
	files, err := worldsync.BuildManifest(worldDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, slices.Sorted(maps.Keys(keep))) {
		t.Errorf("Manifest der Welt nennt %q", paths)
	}
}

func TestInvalidRegionFallsBackToWholeFile(t *testing.T) {
	header := func() []byte { return make([]byte, 8192) }
	cases := []struct {
		name string
		data []byte
	}{
		{"Größe 5000", make([]byte, 5000)},
		{"Größe 0", nil},
		{"nur 4096 Bytes", make([]byte, 4096)},
		{"kein Vielfaches von 4096", append(makeRegion(3, chunk{index: 0, sector: 2, count: 1, payload: "x"}), 0)},
		{"Sektor außerhalb", setLocation(append(header(), make([]byte, 2*4096)...), 0, 3, 2)},
		{"Sektor weit außerhalb", setLocation(append(header(), make([]byte, 4096)...), 0, 0xffffff, 255)},
		{"zeigt in den Kopf", setLocation(append(header(), make([]byte, 4096)...), 0, 1, 1)},
		{"zeigt auf Sektor 0", setLocation(append(header(), make([]byte, 4096)...), 0, 0, 1)},
		{"überlappend", setLocation(setLocation(append(header(), make([]byte, 3*4096)...), 0, 2, 2), 1, 3, 1)},
		{"doppelt belegt", setLocation(setLocation(append(header(), make([]byte, 2*4096)...), 0, 2, 1), 1, 2, 1)},
		{"Anzahl 0", setLocation(append(header(), make([]byte, 4096)...), 0, 2, 0)},
	}
	for _, c := range cases {
		if segs, parts, ok := worldsync.SplitRegion(c.data); ok {
			t.Errorf("%s: zerlegt in %d Segmente (%d Teile)", c.name, len(segs), len(parts))
		}
	}

	// Im Manifest bleiben solche Dateien ganze Blobs, ebenso eine gültige
	// Regionsdatei, die nicht auf .mca endet.
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	odd := make([]byte, 5000)
	for i := range odd {
		odd[i] = byte(i * 7)
	}
	world := map[string]string{
		"level.dat":          "level",
		"region/r.0.0.mca":   string(odd),
		"region/r.0.1.mca":   string(cases[4].data), // Sektor außerhalb
		"region/r.1.0.mca":   string(cases[8].data), // überlappend
		"region/r.1.1.mca":   string(cases[6].data), // zeigt in den Kopf
		"entities/r.0.0.mca": "",
		"data/region.bin":    string(makeRegion(4, chunk{index: 0, sector: 2, count: 1, payload: "kein mca"})),
	}
	writeTree(t, worldDir, world)
	if err := worldsync.Stage(worldDir, stagingDir); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	files, err := worldsync.BuildManifest(stagingDir, hc)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(files) != len(world) {
		t.Fatalf("Manifest hat %d Einträge, erwartet %d", len(files), len(world))
	}
	for _, f := range files {
		content := world[f.Path]
		if f.Recipe != "" || f.Size != int64(len(content)) || f.SHA256 != shaHex([]byte(content)) {
			t.Errorf("%s: %+v; erwartet ganze Datei", f.Path, f)
		}
	}
	gen, err := worldsync.UploadAndCommit(ctx, h.c, stagingDir, h.request(files, nil))
	if err != nil {
		t.Fatalf("UploadAndCommit: %v", err)
	}
	for _, f := range files {
		if !blobInStore(h, f.SHA256) {
			t.Errorf("%s: ganzer Blob fehlt im Speicher", f.Path)
		}
	}
	target := t.TempDir()
	if err := worldsync.Download(ctx, h.c, gen, target, hc, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := readTree(t, target); !maps.Equal(got, world) {
		t.Fatalf("Welt nach Download weicht ab: %q", slices.Sorted(maps.Keys(got)))
	}
}

// Härtung aus dem Review von Task 6: Ein abgebrochener Download hinterlässt
// eine Markierung; solange sie da ist, stellt Stage den Ordner nicht bereit.
func TestInterruptedDownloadBlocksStage(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	world := sampleWorld()
	world["region/r.5.5.mca"] = string(makeRegion(6, chunk{index: 4, sector: 3, count: 2, payload: "Chunk"}))
	writeTree(t, worldDir, world)
	gen := h.snapshot(t, worldDir, stagingDir, hc, nil)

	base := t.TempDir()
	target := filepath.Join(base, "welt")
	marker := filepath.Join(target, ".wanderwelt-download")
	if worldsync.DownloadIncomplete(target) {
		t.Fatal("DownloadIncomplete ohne Download")
	}
	// Lässt sich die Markierung nicht prüfen, gilt der Ordner als
	// unvollständig; Stage meldet dann den Prüffehler, nicht „Download nicht
	// abgeschlossen“.
	if !worldsync.DownloadIncomplete("ungültig\x00pfad") {
		t.Error("DownloadIncomplete bei nicht prüfbarem Pfad false")
	}
	unreadable := filepath.Join(t.TempDir(), "a*b") // Windows: ungültiger Name
	if runtime.GOOS != "windows" {
		unreadable = filepath.Join(t.TempDir(), "datei") // Datei statt Ordner
		if err := os.WriteFile(unreadable, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := worldsync.Stage(unreadable, filepath.Join(t.TempDir(), "s")); err == nil || errors.Is(err, worldsync.ErrDownloadIncomplete) {
		t.Errorf("Stage bei nicht prüfbarer Markierung: %v", err)
	}
	if !worldsync.DownloadIncomplete(unreadable) {
		t.Error("DownloadIncomplete bei nicht prüfbarer Markierung false")
	}

	// Ein Manifest, das die Markierung selbst nennt, wird vor jeder Änderung
	// abgelehnt.
	for _, p := range []string{".wanderwelt-download", ".WANDERWELT-DOWNLOAD", ".wanderwelt-download/x.dat"} {
		bad := *gen
		bad.Files = append(slices.Clone(gen.Files), api.FileEntry{Path: p, Size: 5, SHA256: shaHex([]byte("raids"))})
		if err := worldsync.Download(ctx, h.c, &bad, target, hc, nil); err == nil {
			t.Errorf("Download mit Pfad %q ohne Fehler", p)
		}
		if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Pfad %q: Weltordner angelegt (%v): %q", p, err, slices.Sorted(maps.Keys(readTree(t, base))))
		}
	}

	// Lässt sich die Markierung nicht anlegen (an ihrer Stelle liegt ein
	// Ordner), scheitert Download, ohne etwas anzufassen.
	writeTree(t, target, map[string]string{"extra.txt": "bleibt", ".wanderwelt-download/x": "Ordner"})
	if err := worldsync.Download(ctx, h.c, gen, target, hc, nil); err == nil {
		t.Fatal("Download ohne Markierung ohne Fehler")
	}
	want := map[string]string{"welt/extra.txt": "bleibt", "welt/.wanderwelt-download/x": "Ordner"}
	if got := readTree(t, base); !maps.Equal(got, want) {
		t.Fatalf("Download ohne Markierung hat den Ordner verändert: %q", got)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}

	// Der letzte Blob fehlt auf dem Koordinator: Der Download bricht ab,
	// nachdem er schon Dateien geschrieben hat. Schon bevor die erste Datei
	// geladen wird, liegt die Markierung.
	broken := *gen
	broken.Files = append(slices.Clone(gen.Files), api.FileEntry{Path: "zzz/fehlt.dat", Size: 6, SHA256: shaHex([]byte("fehlt!"))})
	markedEarly := false
	first := true
	progress := func(done, total int64) {
		if first {
			first = false
			_, err := os.Lstat(marker)
			markedEarly = err == nil
		}
	}
	if err := worldsync.Download(ctx, h.c, &broken, target, hc, progress); err == nil {
		t.Fatal("Download mit fehlendem Blob ohne Fehler")
	}
	if !markedEarly {
		t.Error("Markierung lag beim Start des Ladens noch nicht")
	}
	if got := readTree(t, target); len(got) < 2 {
		t.Fatalf("Testaufbau: Download hat vor dem Abbruch nichts geschrieben: %q", slices.Sorted(maps.Keys(got)))
	}
	if !worldsync.DownloadIncomplete(target) {
		t.Fatal("DownloadIncomplete nach abgebrochenem Download false")
	}
	staging2 := filepath.Join(t.TempDir(), "staging")
	if err := worldsync.Stage(target, staging2); !errors.Is(err, worldsync.ErrDownloadIncomplete) {
		t.Fatalf("Stage eines halb heruntergeladenen Weltordners: %v, want ErrDownloadIncomplete", err)
	}
	if _, err := os.Lstat(staging2); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stage hat trotz Ablehnung bereitgestellt: %v", err)
	}

	// Ein vollständiger Download entfernt die Markierung, danach geht Stage.
	if err := worldsync.Download(ctx, h.c, gen, target, hc, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if worldsync.DownloadIncomplete(target) {
		t.Fatal("Markierung nach vollständigem Download noch da")
	}
	if got := readTree(t, target); !maps.Equal(got, withoutLock(world)) {
		t.Fatalf("Welt nach Download weicht ab: %q", slices.Sorted(maps.Keys(got)))
	}
	if err := worldsync.Stage(target, staging2); err != nil {
		t.Fatalf("Stage nach vollständigem Download: %v", err)
	}
}

// Härtung aus dem Review von Task 6: Speichert der Bereitstellungsordner die
// Änderungszeit gröber als die Quelle, scheitert Stage laut, statt dem
// Hash-Cache einen unzuverlässigen Schlüssel zu geben.
func TestStageRejectsCoarseMtime(t *testing.T) {
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	writeTree(t, worldDir, map[string]string{"level.dat": "level", "region/r.0.0.mca": "region"})
	// Auf 100 ns genau (NTFS), aber auf keiner gröberen Stufe gerundet.
	mtime := time.Date(2026, 11, 22, 12, 0, 1, 234_567_800, time.UTC)
	for _, p := range []string{"level.dat", "region/r.0.0.mca"} {
		if err := os.Chtimes(filepath.Join(worldDir, filepath.FromSlash(p)), time.Time{}, mtime); err != nil {
			t.Fatal(err)
		}
	}

	// Wie FAT (2 s), ext3/HFS+ (1 s) und exFAT (10 ms).
	for _, step := range []time.Duration{2 * time.Second, time.Second, 10 * time.Millisecond} {
		restore := worldsync.SetChtimes(func(name string, atime, mtime time.Time) error {
			return os.Chtimes(name, atime, mtime.Truncate(step))
		})
		err := worldsync.Stage(worldDir, stagingDir)
		restore()
		if err == nil {
			t.Fatalf("Stage auf Dateisystem mit Zeiten in Schritten von %v ohne Fehler", step)
		}
		if got := readTree(t, stagingDir); len(got) != 0 {
			t.Fatalf("Schritt %v: trotz Fehler bereitgestellt: %q", step, slices.Sorted(maps.Keys(got)))
		}
	}

	// Mit genauen Zeiten geht es, und die Bereitstellung trägt genau die Zeit
	// der Quelle.
	if err := worldsync.Stage(worldDir, stagingDir); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	info, err := os.Lstat(filepath.Join(stagingDir, "level.dat"))
	if err != nil || !info.ModTime().Equal(mtime) {
		t.Fatalf("bereitgestellte Änderungszeit %v (%v), erwartet %v", info.ModTime(), err, mtime)
	}
}

// Ein Segment über der Grenze einer gebündelten Anfrage (32 MiB − 40 B, hier
// über die Test-Naht 3 Sektoren) lässt sich nicht bündeln: Die Datei bleibt
// ein ganzer Blob. Kommt trotzdem ein Rezept mit so einem Segment (anderer
// Starter, alter Cache), nehmen Upload und Download dafür den Einzelweg.
func TestOversizedSegmentFallsBackToWholeFile(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	// Die echte Grenze: 32 MiB je Anfrage abzüglich 40 Byte Rahmenkopf.
	if n := worldsync.MaxSegment(); n != 32<<20-40 {
		t.Fatalf("Segmentgrenze %d, erwartet 32 MiB − 40 B", n)
	}
	restore := worldsync.SetMaxSegment(3 * 4096)
	defer restore()

	// Ein kleiner Chunk, dahinter ein Rest von 5 Sektoren (20 480 Bytes).
	big := makeRegion(8, chunk{index: 0, sector: 2, count: 1, payload: "klein"})
	small := makeRegion(5, chunk{index: 0, sector: 2, count: 1, payload: "a"}, chunk{index: 1, sector: 3, count: 2, payload: "b"})
	world := map[string]string{"level.dat": "L", "region/r.0.0.mca": string(big), "region/r.1.0.mca": string(small)}
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	writeTree(t, worldDir, world)
	h.take()
	gen1 := h.snapshot(t, worldDir, stagingDir, worldsync.LoadHashCache(), nil)
	whole := entryOf(t, gen1.Files, "region/r.0.0.mca")
	if whole.Recipe != "" || whole.SHA256 != shaHex(big) || !blobInStore(h, whole.SHA256) {
		t.Fatalf("Regionsdatei mit zu großem Rest: %+v; erwartet ganzer Blob", whole)
	}
	split := entryOf(t, gen1.Files, "region/r.1.0.mca")
	if split.Recipe == "" {
		t.Fatalf("kleine Regionsdatei ohne Rezept: %+v", split)
	}
	if _, batched := h.take(); slices.Contains(batched, whole.SHA256) || h.blobPuts.Load() != 2 {
		t.Errorf("Upload: ganzer Blob gebündelt = %v, %d PUTs; erwartet 2 PUTs (level.dat und die große Regionsdatei)",
			slices.Contains(batched, whole.SHA256), h.blobPuts.Load())
	}

	t.Setenv("WANDERWELT_HOME", t.TempDir())
	target := t.TempDir()
	h.blobGets.Store(0)
	if err := worldsync.Download(ctx, h.c, gen1, target, worldsync.LoadHashCache(), nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := readTree(t, target); !maps.Equal(got, world) {
		t.Fatalf("Welt nach Download weicht ab: %q", slices.Sorted(maps.Keys(got)))
	}
	if fetched, _ := h.take(); slices.Contains(fetched, whole.SHA256) || h.blobGets.Load() != 2 {
		t.Errorf("Download: ganzer Blob gebündelt geholt = %v, %d GETs; erwartet 2 GETs", slices.Contains(fetched, whole.SHA256), h.blobGets.Load())
	}

	// Rezept mit zu großem Segment: mit der vollen Grenze zerlegt, dann mit
	// der kleinen hoch- und heruntergeladen.
	restore()
	dir2 := t.TempDir()
	writeTree(t, dir2, world)
	files, err := worldsync.BuildManifest(dir2, nil)
	if err != nil {
		t.Fatal(err)
	}
	withRecipe := entryOf(t, files, "region/r.0.0.mca")
	if withRecipe.Recipe == "" {
		t.Fatal("Testaufbau: mit voller Grenze kein Rezept")
	}
	restore = worldsync.SetMaxSegment(3 * 4096)
	segs, err := api.ParseRecipe(mustRead(t, localRecipe(withRecipe.Recipe)))
	if err != nil || len(segs) != 3 || segs[2].Size != 5*4096 {
		t.Fatalf("Testaufbau: Rezept %+v, %v; erwartet Kopf, Chunk, Rest von 20 480 Bytes", segs, err)
	}
	h.blobPuts.Store(0)
	h.take()
	gen2, err := worldsync.UploadAndCommit(ctx, h.c, dir2, h.request(files, gen1))
	if err != nil {
		t.Fatalf("UploadAndCommit mit großem Segment: %v", err)
	}
	if _, batched := h.take(); slices.Contains(batched, segs[2].SHA256) || h.blobPuts.Load() != 1 ||
		!slices.Equal(sorted(batched), sorted([]string{segs[0].SHA256, segs[1].SHA256, withRecipe.Recipe})) {
		t.Errorf("Upload: %d PUTs, gebündelt %d Blobs; erwartet das große Segment einzeln, Kopf, Chunk und Rezept gebündelt", h.blobPuts.Load(), len(batched))
	}
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	target2 := t.TempDir()
	h.blobGets.Store(0)
	if err := worldsync.Download(ctx, h.c, gen2, target2, worldsync.LoadHashCache(), nil); err != nil {
		t.Fatalf("Download mit großem Segment: %v", err)
	}
	if got := readTree(t, target2); !maps.Equal(got, world) {
		t.Fatalf("Welt nach Download weicht ab: %q", slices.Sorted(maps.Keys(got)))
	}
	if fetched, _ := h.take(); slices.Contains(fetched, segs[2].SHA256) || h.blobGets.Load() != 2 {
		t.Errorf("Download: großes Segment gebündelt = %v, %d GETs; erwartet es einzeln (und level.dat)", slices.Contains(fetched, segs[2].SHA256), h.blobGets.Load())
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Eine beschädigte lokale Kopie eines Rezepts heilt sich selbst: Ein
// Cache-Treffer prüft das Rezept (nicht nur, ob es da ist) und zerlegt sonst
// neu; kann Upload es nicht laden, löscht er die Kopie und scheitert klar,
// und der nächste Stand geht wieder durch.
func TestDamagedLocalRecipeHeals(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	staging := t.TempDir()
	region := makeRegion(4, chunk{index: 0, sector: 2, count: 1, payload: "A"}, chunk{index: 1, sector: 3, count: 1, payload: "B"})
	writeTree(t, staging, map[string]string{"level.dat": "L", "region/r.0.0.mca": string(region)})
	files, err := worldsync.BuildManifest(staging, hc)
	if err != nil {
		t.Fatal(err)
	}
	r := entryOf(t, files, "region/r.0.0.mca")
	if r.Recipe == "" {
		t.Fatal("Testaufbau: Regionsdatei ohne Rezept")
	}
	good := mustRead(t, localRecipe(r.Recipe))
	damage := func() {
		t.Helper()
		if err := os.WriteFile(localRecipe(r.Recipe), []byte("kaputt"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Cache-Treffer mit beschädigtem Rezept: neu zerlegt, Kopie ersetzt.
	damage()
	if files, err = worldsync.BuildManifest(staging, hc); err != nil || entryOf(t, files, "region/r.0.0.mca") != r {
		t.Fatalf("BuildManifest mit beschädigtem Rezept: %v", err)
	}
	if !bytes.Equal(mustRead(t, localRecipe(r.Recipe)), good) {
		t.Fatal("Cache-Treffer trotz beschädigtem lokalem Rezept: Kopie nicht ersetzt")
	}

	// Beschädigt nach dem Manifest: Upload löscht die Kopie und scheitert.
	damage()
	err = worldsync.Upload(ctx, h.c, staging, files)
	if !errors.Is(err, worldsync.ErrLocalRecipe) {
		t.Fatalf("Upload mit beschädigtem Rezept = %v, erwartet ErrLocalRecipe", err)
	}
	if exists(localRecipe(r.Recipe)) {
		t.Fatal("beschädigtes lokales Rezept nach dem Upload noch da")
	}
	// Der nächste Stand zerlegt neu und geht durch.
	if files, err = worldsync.BuildManifest(staging, hc); err != nil || entryOf(t, files, "region/r.0.0.mca") != r {
		t.Fatalf("BuildManifest nach dem Löschen: %v", err)
	}
	if _, err := worldsync.UploadAndCommit(ctx, h.c, staging, h.request(files, nil)); err != nil {
		t.Fatalf("nächster Stand: %v", err)
	}
}
