package worldsync_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

const (
	adminEmail = "admin@example.org"
	machineID  = "pc-test"
)

type noUUIDs struct{}

func (noUUIDs) UUID(context.Context, string) (string, error) {
	return "", errors.New("im Test nicht gebraucht")
}

// harness ist ein echter Koordinator (Dev-Modus, ein Admin) hinter
// httptest, mit Zählern für Blob-Anfragen und einem Zuschlag zum
// Einreichen.
type harness struct {
	c        *client.Client
	storeDir string
	lease    int64

	blobPuts  atomic.Int64 // PUT /v1/blobs/… (jeder Blob hier ein Teil)
	blobGets  atomic.Int64 // GET /v1/blobs/…
	commits   atomic.Int64 // POST /v1/generations
	interrupt atomic.Bool  // ab dem zweiten Blob-PUT Verbindung abbrechen (interruptAfterFirstPut)
	putDelay  atomic.Int64 // Nanosekunden, die jeder Blob-PUT im Handler wartet
	inflight  atomic.Int64 // gerade laufende Blob-PUTs
	maxFlight atomic.Int64 // höchstens gleichzeitig gelaufene Blob-PUTs

	fetchReqs    atomic.Int64 // POST /v1/blobs/fetch
	batchReqs    atomic.Int64 // POST /v1/blobs/batch
	missingReqs  atomic.Int64 // POST /v1/blobs/missing
	missingMax   atomic.Int64 // meiste Hashes in einer Missing-Anfrage
	dlDelay      atomic.Int64 // Nanosekunden, die jeder Blob-GET und -Fetch wartet
	dlFlight     atomic.Int64 // gerade laufende Blob-GETs und -Fetches
	dlMaxFlight  atomic.Int64 // höchstens gleichzeitig gelaufene
	mu           sync.Mutex
	fetched      []string // per Fetch angefragte Hashes
	batched      []string // per Batch hochgeladene Hashes
	beforeCommit func()   // vor jedem POST /v1/generations, bis zurückgesetzt
	// firstPut ist zu, sobald der erste Blob-PUT nach interruptAfterFirstPut
	// fertig ist.
	firstPut chan struct{}
}

// takeFetched liefert die seit dem letzten Aufruf per Fetch angefragten und
// per Batch hochgeladenen Hashes.
func (h *harness) take() (fetched, batched []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fetched, batched = h.fetched, h.batched
	h.fetched, h.batched = nil, nil
	return fetched, batched
}

// inflight zählt eine laufende Lade-Anfrage, wartet dlDelay und meldet das
// Ende.
func (h *harness) download() func() {
	n := h.dlFlight.Add(1)
	for m := h.dlMaxFlight.Load(); n > m && !h.dlMaxFlight.CompareAndSwap(m, n); m = h.dlMaxFlight.Load() {
	}
	time.Sleep(time.Duration(h.dlDelay.Load()))
	return func() { h.dlFlight.Add(-1) }
}

// readBody liest den Körper von r und legt ihn zurück.
func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:       dir,
		StorageBudget: 1 << 30,
		Members:       []config.Member{{Email: adminEmail, Minecraft: "Admin", Admin: true}},
	}
	cfg.Auth.Mode = "dev"
	lm, err := lease.New(filepath.Join(dir, "lease.json"), server.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{storeDir: filepath.Join(dir, "store")}
	st, err := store.Open(h.storeDir, cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, lm, st, portholder.Noop(), noUUIDs{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob := strings.HasPrefix(r.URL.Path, "/v1/blobs/") && r.URL.Path != "/v1/blobs/missing"
		switch {
		case blob && r.Method == http.MethodPut:
			if n := h.blobPuts.Add(1); h.interrupt.Load() {
				h.mu.Lock()
				first := h.firstPut
				h.mu.Unlock()
				if n == 1 {
					// Nach ServeHTTP: Der Blob ist abgelegt, sein Platz im
					// Speicher wieder frei.
					defer close(first)
				} else {
					// Erst abbrechen, wenn der erste Blob fertig ist: Sonst
					// sieht der Starter den Abbruch, während der Koordinator
					// den ersten noch annimmt, und sein nächster Versuch trifft
					// auf einen belegten Blob (409) – je nach Zeitablauf.
					<-first
					panic(http.ErrAbortHandler) // Verbindung weg, keine Antwort
				}
			}
			n := h.inflight.Add(1)
			defer h.inflight.Add(-1)
			for m := h.maxFlight.Load(); n > m && !h.maxFlight.CompareAndSwap(m, n); m = h.maxFlight.Load() {
			}
			time.Sleep(time.Duration(h.putDelay.Load()))
		case blob && r.Method == http.MethodGet:
			h.blobGets.Add(1)
			defer h.download()()
		case r.Method == http.MethodPost && r.URL.Path == "/v1/blobs/fetch":
			h.fetchReqs.Add(1)
			var in struct{ Hashes []string }
			json.Unmarshal(readBody(r), &in)
			h.mu.Lock()
			h.fetched = append(h.fetched, in.Hashes...)
			h.mu.Unlock()
			defer h.download()()
		case r.Method == http.MethodPost && r.URL.Path == "/v1/blobs/batch":
			h.batchReqs.Add(1)
			body := readBody(r)
			var hashes []string
			for len(body) >= 40 {
				hashes = append(hashes, hex.EncodeToString(body[:32]))
				body = body[min(len(body), 40+int(binary.BigEndian.Uint64(body[32:40]))):]
			}
			h.mu.Lock()
			h.batched = append(h.batched, hashes...)
			h.mu.Unlock()
		case r.Method == http.MethodPost && r.URL.Path == "/v1/blobs/missing":
			h.missingReqs.Add(1)
			var in struct{ Hashes []string }
			json.Unmarshal(readBody(r), &in)
			for n, m := int64(len(in.Hashes)), h.missingMax.Load(); n > m && !h.missingMax.CompareAndSwap(m, n); m = h.missingMax.Load() {
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/generations":
			h.commits.Add(1)
			h.mu.Lock()
			f := h.beforeCommit
			h.mu.Unlock()
			if f != nil {
				f()
			}
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	h.c = client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: adminEmail})

	// Zuschlag ohne Sammelfenster (wie beim Welt-Import): Es gibt noch keine
	// Welt, die Basis ist also leer.
	resp, err := h.c.Play(t.Context(), api.PlayRequest{MachineID: machineID, UploadOnly: true})
	if err != nil || resp.Role != api.RoleHost || resp.LeaseID == 0 {
		t.Fatalf("Zuschlag: %+v, %v", resp, err)
	}
	h.lease = resp.LeaseID
	return h
}

// interruptAfterFirstPut zählt die Blob-PUTs von vorn; ab dem zweiten
// bricht der Koordinator die Verbindung ab, aber erst, wenn der erste fertig
// ist (bis interrupt wieder aus ist).
func (h *harness) interruptAfterFirstPut() {
	h.mu.Lock()
	h.firstPut = make(chan struct{})
	h.mu.Unlock()
	h.blobPuts.Store(0)
	h.interrupt.Store(true)
}

// onCommit lässt f vor jedem Commit laufen (nil: nichts mehr).
func (h *harness) onCommit(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeCommit = f
}

func (h *harness) request(files []api.FileEntry, base *api.Generation) api.CommitRequest {
	req := api.CommitRequest{
		LeaseID:          h.lease,
		MachineID:        machineID,
		MinecraftVersion: "26.2",
		Files:            files,
	}
	if base != nil {
		req.BaseWorldID, req.BaseGeneration = base.WorldID, base.Number
	}
	return req
}

// snapshot: Stage, Manifest, Upload und Commit wie beim Zwischenstand.
func (h *harness) snapshot(t *testing.T, worldDir, stagingDir string, hc *worldsync.HashCache, base *api.Generation) *api.Generation {
	t.Helper()
	ctx := t.Context()
	if err := worldsync.Stage(worldDir, stagingDir); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	files, err := worldsync.BuildManifest(stagingDir, hc)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if err := worldsync.Upload(ctx, h.c, stagingDir, files); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	gen, err := worldsync.Commit(ctx, h.c, h.request(files, base))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return gen
}

// writeTree legt files (Pfad mit "/" → Inhalt) unter dir an.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree liefert alle regulären Dateien unter dir (Pfad mit "/" →
// Inhalt). Alles andere als Ordner und reguläre Dateien ist ein Fehler.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			t.Errorf("%s ist weder Ordner noch Datei (%v)", path, d.Type())
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sampleWorld: Unterordner, Umlaut im Namen, leere Datei, zwei gleiche
// Inhalte und session.lock (wird nie übertragen).
func sampleWorld() map[string]string {
	return map[string]string{
		"level.dat":                 strings.Repeat("L", 3000),
		"session.lock":              "☃",
		"region/r.0.0.mca":          strings.Repeat("region-0-0 ", 400),
		"region/r.0.1.mca":          strings.Repeat("region-0-1 ", 300),
		"DIM-1/region/r.-1.0.mca":   strings.Repeat("nether ", 500),
		"data/Grüße_äöüß.dat":       "Umlaute im Namen",
		"data/leer.dat":             "",
		"players/data/abc.dat":      "spieler",
		"players/data/abc_old.dat":  "spieler", // gleicher Inhalt → ein Blob
		"players/data-alt.json":     "[]",      // vor "players/data/…" sortiert, beim Durchlaufen danach
		"datapacks/x/pack.mcmeta":   `{"pack":{}}`,
		"DIM1/data/raids_end.dat":   "end",
		"stats/abc.json":            "{}",
		"advancements/abc.json":     "[]",
		"region/r.-1.-1.mca":        strings.Repeat("\x00\x01\x02", 700),
		"entities/r.0.0.mca":        strings.Repeat("e", 1024),
		"poi/r.0.0.mca":             strings.Repeat("p", 512),
		"DIM-1/entities/r.0.0.mca":  strings.Repeat("n", 100),
		"DIM1/region/r.0.0.mca":     strings.Repeat("end-region ", 64),
		"icon.png":                  "\x89PNG\r\n\x1a\n",
		"level.dat_old":             strings.Repeat("O", 2999),
		"data/minecraft/scores.dat": "s",
	}
}

func withoutLock(files map[string]string) map[string]string {
	out := maps.Clone(files)
	delete(out, "session.lock")
	return out
}

func TestRoundTripBitIdentical(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	// Ein kaputter Hash-Cache wird verworfen, nicht zum Fehler.
	cachePath := filepath.Join(group.Home(), "hashcache.json")
	if err := os.WriteFile(cachePath, []byte(`{"version":1,"entries":{"`), 0o644); err != nil {
		t.Fatal(err)
	}
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), filepath.Join(t.TempDir(), "staging")
	world := sampleWorld()
	writeTree(t, worldDir, world)

	// Ein Bereitstellungsordner in der Welt würde sich selbst mitkopieren.
	if err := worldsync.Stage(worldDir, filepath.Join(worldDir, "staging")); err == nil {
		t.Error("Stage in einen Ordner innerhalb der Welt ohne Fehler")
	}
	if err := worldsync.Stage(worldDir, stagingDir); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stagingDir, "session.lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("session.lock bereitgestellt: %v", err)
	}
	files, err := worldsync.BuildManifest(stagingDir, hc)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	// Die Bereitstellung ist ein treues Abbild: dasselbe Manifest wie die
	// Welt selbst (BuildManifest lässt session.lock auch dort weg).
	if direct, err := worldsync.BuildManifest(worldDir, nil); err != nil || !slices.Equal(direct, files) {
		t.Errorf("Manifest der Welt %+v (%v) ≠ Manifest der Bereitstellung %+v", direct, err, files)
	}
	if info, err := os.Stat(cachePath); err != nil || info.Size() < 100 {
		t.Errorf("Hash-Cache nicht gespeichert: %v, %v", info, err)
	}
	want := withoutLock(world)
	if len(files) != len(want) {
		t.Fatalf("Manifest hat %d Dateien, erwartet %d: %+v", len(files), len(want), files)
	}
	for i, f := range files {
		content, ok := want[f.Path]
		if !ok {
			t.Fatalf("Manifest nennt %q", f.Path)
		}
		if f.Size != int64(len(content)) || f.SHA256 != shaHex([]byte(content)) {
			t.Errorf("Manifest %q: %d %s", f.Path, f.Size, f.SHA256)
		}
		if i > 0 && files[i-1].Path >= f.Path {
			t.Errorf("Manifest nicht nach Pfad sortiert: %q vor %q", files[i-1].Path, f.Path)
		}
	}

	// Jeder Blob-PUT dauert etwas, damit sich gleichzeitige überlappen.
	h.putDelay.Store(int64(30 * time.Millisecond))
	if err := worldsync.Upload(ctx, h.c, stagingDir, files); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	h.putDelay.Store(0)
	blobs := map[string]bool{}
	for _, c := range want {
		blobs[shaHex([]byte(c))] = true
	}
	if n := h.blobPuts.Load(); n != int64(len(blobs)) {
		t.Errorf("%d Blob-PUTs für %d verschiedene Inhalte", n, len(blobs))
	}
	if m := h.maxFlight.Load(); m < 2 || m > 4 {
		t.Errorf("höchstens %d Blobs gleichzeitig hochgeladen, erwartet 2 bis 4", m)
	}
	gen, err := worldsync.Commit(ctx, h.c, h.request(files, nil))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if gen == nil || gen.WorldID == "" || gen.Number != 1 {
		t.Fatalf("Generation: %+v", gen)
	}
	head, err := h.c.Head(ctx)
	if err != nil || head == nil || head.WorldID != gen.WorldID || head.Number != gen.Number {
		t.Fatalf("Head: %+v, %v", head, err)
	}

	// Neuer Host: leerer Ordner, leerer Hash-Cache.
	target := filepath.Join(t.TempDir(), "welt")
	var lastDone, lastTotal int64
	progress := func(done, total int64) { lastDone, lastTotal = done, total }
	if err := worldsync.Download(ctx, h.c, head, target, worldsync.LoadHashCache(), progress); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := readTree(t, target); !maps.Equal(got, want) {
		t.Fatalf("Welt nach Download weicht ab:\n got %q\nwant %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	var total int64
	for _, c := range want {
		total += int64(len(c))
	}
	if lastDone != total || lastTotal != total {
		t.Errorf("Fortschritt am Ende %d/%d, erwartet %d/%d", lastDone, lastTotal, total, total)
	}

	// Ein zweiter Download in denselben Ordner lädt nichts mehr.
	gets := h.blobGets.Load()
	if err := worldsync.Download(ctx, h.c, head, target, hc, nil); err != nil {
		t.Fatalf("zweiter Download: %v", err)
	}
	if n := h.blobGets.Load() - gets; n != 0 {
		t.Errorf("zweiter Download holte %d Blobs, erwartet 0", n)
	}
	if got := readTree(t, target); !maps.Equal(got, want) {
		t.Fatal("Welt nach zweitem Download weicht ab")
	}
}

func TestSecondUploadOnlySendsChanged(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	world := sampleWorld()
	writeTree(t, worldDir, world)
	gen1 := h.snapshot(t, worldDir, stagingDir, hc, nil)

	// Wie der Server: Regionsdatei bleibt offen und wird mit gleicher Größe
	// überschrieben. Unter Windows zeigt der Ordnereintrag danach noch die
	// alte Änderungszeit; erkannt wird die Änderung nur über Lstat.
	path := filepath.Join(worldDir, "region", "r.0.0.mca")
	changed := strings.Repeat("REGION-0-0 ", 400)
	time.Sleep(20 * time.Millisecond) // neue Änderungszeit auch bei grober Uhr
	open, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if _, err := open.WriteAt([]byte(changed), 0); err != nil {
		t.Fatal(err)
	}

	// Bereitgestellte Dateien vorher; SameFile lädt die Datei-ID sofort, sonst
	// erst beim Vergleich (dann schon von der neuen Datei).
	stagedBefore := map[string]os.FileInfo{}
	for _, p := range []string{"region/r.0.0.mca", "level.dat", "DIM-1/region/r.-1.0.mca"} {
		info, err := os.Stat(filepath.Join(stagingDir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		os.SameFile(info, info)
		stagedBefore[p] = info
	}

	h.blobPuts.Store(0)
	gen2 := h.snapshot(t, worldDir, stagingDir, hc, gen1)
	if err := open.Close(); err != nil {
		t.Fatal(err)
	}
	if n := h.blobPuts.Load(); n != 1 {
		t.Fatalf("zweiter Upload schickte %d Blobs, erwartet genau 1", n)
	}
	// Stage hat nur die geänderte Datei neu kopiert.
	for p, before := range stagedBefore {
		after, err := os.Stat(filepath.Join(stagingDir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if copied := !os.SameFile(before, after); copied != (p == "region/r.0.0.mca") {
			t.Errorf("%s: neu kopiert = %v", p, copied)
		}
	}
	if gen2.WorldID != gen1.WorldID || gen2.Number != 2 {
		t.Fatalf("Generation 2: %+v", gen2)
	}
	for _, f := range gen2.Files {
		if f.Path == "region/r.0.0.mca" && f.SHA256 != shaHex([]byte(changed)) {
			t.Fatalf("Manifest hat für die geänderte Datei den alten Hash %s", f.SHA256)
		}
	}

	// Nichts geändert: kein Blob.
	h.blobPuts.Store(0)
	gen3 := h.snapshot(t, worldDir, stagingDir, hc, gen2)
	if n := h.blobPuts.Load(); n != 0 {
		t.Fatalf("Upload ohne Änderung schickte %d Blobs", n)
	}

	// Andere Größe, aber die alte Änderungszeit: zählt auch als geändert
	// (Schlüssel ist Pfad, Größe und Änderungszeit).
	path = filepath.Join(worldDir, "region", "r.0.1.mca")
	old, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	grown := strings.Repeat("region-0-1 ", 301)
	if err := os.WriteFile(path, []byte(grown), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	h.blobPuts.Store(0)
	h.snapshot(t, worldDir, stagingDir, hc, gen3)
	if n := h.blobPuts.Load(); n != 1 {
		t.Fatalf("Upload nach Größenänderung schickte %d Blobs, erwartet genau 1", n)
	}

	// Der Stand kommt beim nächsten Host so an.
	world["region/r.0.0.mca"] = changed
	world["region/r.0.1.mca"] = grown
	target := t.TempDir()
	head, err := h.c.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := worldsync.Download(ctx, h.c, head, target, hc, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := readTree(t, target); !maps.Equal(got, withoutLock(world)) {
		t.Fatal("Welt nach Download weicht ab")
	}
}

func TestInterruptedUploadLeavesHeadUnchanged(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	world := sampleWorld()
	writeTree(t, worldDir, world)
	gen1 := h.snapshot(t, worldDir, stagingDir, hc, nil)

	// Drei geänderte Dateien → drei fehlende Blobs; eine Datei gelöscht.
	world["level.dat"] = strings.Repeat("M", 3100)
	world["region/r.0.1.mca"] = strings.Repeat("neu ", 300)
	world["data/neu.dat"] = "ganz neu"
	writeTree(t, worldDir, world)
	delete(world, "level.dat_old")
	if err := os.Remove(filepath.Join(worldDir, "level.dat_old")); err != nil {
		t.Fatal(err)
	}
	if err := worldsync.Stage(worldDir, stagingDir); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	files, err := worldsync.BuildManifest(stagingDir, hc)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	headUnchanged := func(when string) {
		t.Helper()
		head, err := h.c.Head(ctx)
		if err != nil || head == nil || head.WorldID != gen1.WorldID || head.Number != gen1.Number ||
			!slices.Equal(head.Files, gen1.Files) {
			t.Fatalf("Head %s: %+v, %v", when, head, err)
		}
	}

	// Der Koordinator bricht nach dem ersten Blob ab: Fehler, und es wird
	// gar nicht erst eingereicht.
	h.commits.Store(0)
	h.interruptAfterFirstPut()
	if _, err := worldsync.UploadAndCommit(ctx, h.c, stagingDir, h.request(files, gen1)); err == nil {
		t.Fatal("Upload trotz Abbruch erfolgreich")
	}
	if n := h.blobPuts.Load(); n < 2 {
		t.Fatalf("nur %d Blob-PUTs; der Abbruch wurde nicht erreicht", n)
	}
	if n := h.commits.Load(); n != 0 {
		t.Fatalf("nach abgebrochenem Upload %d-mal eingereicht", n)
	}
	headUnchanged("nach abgebrochenem Upload")
	h.interrupt.Store(false)

	// Falsche Basis (Generation 1 übersprungen): 409 kommt als
	// client.ErrLeaseLost an, der Head bleibt.
	if _, err := worldsync.UploadAndCommit(ctx, h.c, stagingDir, h.request(files, nil)); !errors.Is(err, client.ErrLeaseLost) {
		t.Fatalf("Commit mit veralteter Basis: %v", err)
	}
	headUnchanged("nach Commit mit veralteter Basis")

	// Der Koordinator verliert vor jedem Commit einen Blob (wie nach einem
	// Aufräumlauf): 412, einmal nachladen und neu einreichen, dann aufgeben.
	lost := shaHex([]byte(world["data/neu.dat"]))
	lose := func() {
		if err := os.Remove(filepath.Join(h.storeDir, "blobs", lost[:2], lost)); err != nil {
			t.Errorf("Blob entfernen: %v", err)
		}
	}
	h.commits.Store(0)
	h.onCommit(lose)
	_, err = worldsync.UploadAndCommit(ctx, h.c, stagingDir, h.request(files, gen1))
	if !errors.Is(err, worldsync.ErrMissingBlobs) {
		t.Fatalf("Blob fehlt bei jedem Commit: %v", err)
	}
	if n := h.commits.Load(); n != 2 {
		t.Fatalf("%d Commits, erwartet genau 2 (einmal neu eingereicht)", n)
	}
	headUnchanged("nach zweimal fehlendem Blob")

	// Zweiter Versuch vollständig; der Blob fehlt nur beim ersten Commit.
	var once sync.Once
	h.onCommit(func() { once.Do(lose) })
	gen2, err := worldsync.UploadAndCommit(ctx, h.c, stagingDir, h.request(files, gen1))
	h.onCommit(nil)
	if err != nil {
		t.Fatalf("zweiter Versuch: %v", err)
	}
	if gen2.WorldID != gen1.WorldID || gen2.Number != 2 {
		t.Fatalf("Generation nach zweitem Versuch: %+v", gen2)
	}
	if head, err := h.c.Head(ctx); err != nil || head.Number != 2 || !slices.Equal(head.Files, files) {
		t.Fatalf("Head nach zweitem Versuch: %+v, %v", head, err)
	}
	target := t.TempDir()
	if err := worldsync.Download(ctx, h.c, gen2, target, hc, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := readTree(t, target); !maps.Equal(got, withoutLock(world)) {
		t.Fatal("Welt nach Download weicht ab")
	}
}

// makeDirLink legt link als Verweis auf den Ordner target an: Symlink, unter
// Windows ohne Recht dazu eine Junction.
func makeDirLink(t *testing.T, link, target string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if jerr == nil {
			return
		}
		t.Skipf("weder Symlink (%v) noch Junction (%v: %s)", err, jerr, out)
	}
	t.Skipf("kein Symlink: %v", err)
}

func TestDownloadRefusesEscapingPaths(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()

	good, evil := []byte("gute Daten"), []byte("böse Daten")
	for _, b := range [][]byte{good, evil} {
		if err := h.c.PutBlob(ctx, shaHex(b), bytes.NewReader(b), int64(len(b))); err != nil {
			t.Fatal(err)
		}
	}
	goodEntry := api.FileEntry{Path: "level.dat", Size: int64(len(good)), SHA256: shaHex(good)}
	evilEntry := func(p string) api.FileEntry {
		return api.FileEntry{Path: p, Size: int64(len(evil)), SHA256: shaHex(evil)}
	}

	t.Run("Pfade", func(t *testing.T) {
		base := t.TempDir()
		worldDir := filepath.Join(base, "welt")
		abs := filepath.ToSlash(filepath.Join(base, "evil"))
		for _, p := range []string{
			"../evil", "a/../../evil", "..", "../welt2/evil", `..\evil`, `a\..\..\evil`,
			"/evil", abs, "C:/evil", "C:evil", "//server/share/evil", "a//b", "./evil", "",
			"evil.", "nul", "con.txt", "a/aux/b", "evil:stream", "region/../../evil",
		} {
			// Direkt gebaut, am Koordinator vorbei: Der würde so etwas nie annehmen.
			gen := &api.Generation{Number: 1, WorldID: "w", Files: []api.FileEntry{goodEntry, evilEntry(p)}}
			if err := worldsync.Download(ctx, h.c, gen, worldDir, hc, nil); err == nil {
				t.Errorf("Download mit Pfad %q ohne Fehler", p)
			}
			// Nichts geschrieben, auch nicht die gültige Datei.
			if got := readTree(t, base); len(got) != 0 {
				t.Fatalf("Pfad %q: Dateien angelegt: %q", p, slices.Sorted(maps.Keys(got)))
			}
		}
		// Doppelte Pfade (auch nur in Groß/Klein), Datei zugleich Ordner und
		// eine Größe, die nicht zum Blob passt (knapp darunter, knapp darüber).
		shorter, longer := goodEntry, goodEntry
		shorter.Size--
		longer.Size++
		// Zerlegte Datei aus den Segmenten good und evil; die Rezepte liegen
		// nur auf dem Koordinator.
		joined := append(slices.Clone(good), evil...)
		segGood := api.Segment{SHA256: shaHex(good), Size: int64(len(good))}
		segEvil := api.Segment{SHA256: shaHex(evil), Size: int64(len(evil))}
		recipe := func(segs ...api.Segment) string {
			data, err := api.EncodeRecipe(segs)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.c.PutBlob(ctx, shaHex(data), bytes.NewReader(data), int64(len(data))); err != nil {
				t.Fatal(err)
			}
			return shaHex(data)
		}
		split := func(size int64, rec string) api.FileEntry {
			return api.FileEntry{Path: "region/r.0.0.mca", Size: size, SHA256: shaHex(joined), Recipe: rec}
		}
		okRec := recipe(segGood, segEvil)
		// Abgelehnt, bevor irgendetwas angefasst wird.
		for _, files := range [][]api.FileEntry{
			{goodEntry, evilEntry("level.dat")},
			{goodEntry, evilEntry("LEVEL.DAT")},
			{goodEntry, evilEntry("level.dat/x")},
			{split(int64(len(joined)), strings.ToUpper(okRec))},
			{split(int64(len(joined)), "abc")},
		} {
			gen := &api.Generation{Number: 1, WorldID: "w", Files: files}
			if err := worldsync.Download(ctx, h.c, gen, worldDir, hc, nil); err == nil {
				t.Errorf("Download mit %+v ohne Fehler", files)
			}
			if got := readTree(t, base); len(got) != 0 {
				t.Fatalf("%+v: Dateien angelegt: %q", files, slices.Sorted(maps.Keys(got)))
			}
		}
		// Scheitert erst beim Laden: Die Datei entsteht nicht, aber die
		// Markierung des abgebrochenen Downloads bleibt.
		missingSeg := api.Segment{SHA256: shaHex([]byte("fehlt")), Size: 5}
		for _, files := range [][]api.FileEntry{
			{shorter},
			{longer},
			{split(int64(len(joined)), shaHex([]byte("kein Rezept da")))},       // Rezept fehlt
			{split(int64(len(joined)), goodEntry.SHA256)},                       // Blob ist kein Rezept
			{split(int64(len(joined))+1, okRec)},                                // Summe ≠ Größe
			{split(int64(len(joined)), recipe(segEvil, segGood))},               // vertauscht: Gesamt-Hash
			{split(int64(len(joined))+5, recipe(segGood, segEvil, missingSeg))}, // Segment fehlt
		} {
			gen := &api.Generation{Number: 1, WorldID: "w", Files: files}
			if err := worldsync.Download(ctx, h.c, gen, worldDir, hc, nil); err == nil {
				t.Errorf("Download mit %+v ohne Fehler", files)
			}
			got := readTree(t, base)
			delete(got, "welt/.wanderwelt-download")
			if len(got) != 0 {
				t.Fatalf("%+v: Dateien angelegt: %q", files, slices.Sorted(maps.Keys(got)))
			}
			if !worldsync.DownloadIncomplete(worldDir) {
				t.Errorf("%+v: keine Markierung nach abgebrochenem Download", files)
			}
		}
		// Gegenprobe: In der richtigen Reihenfolge entsteht die Datei.
		if err := worldsync.Download(ctx, h.c, &api.Generation{Number: 1, WorldID: "w",
			Files: []api.FileEntry{split(int64(len(joined)), okRec)}}, worldDir, hc, nil); err != nil {
			t.Fatalf("Download der zerlegten Datei: %v", err)
		}
		if got := readTree(t, base); !maps.Equal(got, map[string]string{"welt/region/r.0.0.mca": string(joined)}) {
			t.Fatalf("nach Download der zerlegten Datei: %q", got)
		}
		if err := os.RemoveAll(worldDir); err != nil {
			t.Fatal(err)
		}
		// Ein leeres Manifest würde die Welt leeren.
		if err := worldsync.Download(ctx, h.c, &api.Generation{Number: 1, WorldID: "w"}, worldDir, hc, nil); err == nil {
			t.Error("Download eines leeren Manifests ohne Fehler")
		}
	})

	t.Run("Verweise", func(t *testing.T) {
		base := t.TempDir()
		worldDir := filepath.Join(base, "welt")
		outside := filepath.Join(base, "draussen")
		victim := filepath.Join(base, "opfer")
		for _, d := range []string{worldDir, outside, victim} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(victim, "bleibt.txt"), []byte("bleibt"), 0o644); err != nil {
			t.Fatal(err)
		}
		// level.dat ist ein harter Link auf eine Datei draußen.
		outsideFile := filepath.Join(base, "draussen.dat")
		if err := os.WriteFile(outsideFile, []byte("draußen"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(outsideFile, filepath.Join(worldDir, "level.dat")); err != nil {
			t.Fatal(err)
		}
		// data zeigt nach draußen und steht im Manifest; fremd zeigt auf einen
		// fremden Ordner und steht nicht drin, alt/verweis ebenso, in einem
		// echten Ordner, der als Ganzes weg muss.
		makeDirLink(t, filepath.Join(worldDir, "data"), outside)
		makeDirLink(t, filepath.Join(worldDir, "fremd"), victim)
		if err := os.MkdirAll(filepath.Join(worldDir, "alt"), 0o755); err != nil {
			t.Fatal(err)
		}
		makeDirLink(t, filepath.Join(worldDir, "alt", "verweis"), victim)

		gen := &api.Generation{Number: 1, WorldID: "w", Files: []api.FileEntry{goodEntry, evilEntry("data/evil.dat")}}
		if err := worldsync.Download(ctx, h.c, gen, worldDir, hc, nil); err != nil {
			t.Fatalf("Download: %v", err)
		}
		if got := readTree(t, outside); len(got) != 0 {
			t.Errorf("draußen angelegt: %q", slices.Sorted(maps.Keys(got)))
		}
		if b, err := os.ReadFile(outsideFile); err != nil || string(b) != "draußen" {
			t.Errorf("Datei draußen über harten Link verändert: %q, %v", b, err)
		}
		if b, err := os.ReadFile(filepath.Join(victim, "bleibt.txt")); err != nil || string(b) != "bleibt" {
			t.Errorf("fremder Ordner beim Aufräumen angefasst: %q, %v", b, err)
		}
		if info, err := os.Lstat(filepath.Join(worldDir, "data")); err != nil || !info.IsDir() || info.Mode()&fs.ModeType != fs.ModeDir {
			t.Errorf("data ist kein echter Ordner: %v, %v", info, err)
		}
		want := map[string]string{"level.dat": string(good), "data/evil.dat": string(evil)}
		if got := readTree(t, worldDir); !maps.Equal(got, want) {
			t.Errorf("Welt: %q", got)
		}
	})
}

func TestDownloadRemovesExtraFiles(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	hc := worldsync.LoadHashCache()
	worldDir, stagingDir := t.TempDir(), t.TempDir()
	world := sampleWorld()
	writeTree(t, worldDir, world)
	gen := h.snapshot(t, worldDir, stagingDir, hc, nil)

	// Ein lokaler Stand mit Resten: Zusatzdateien, ein alter Ordner, eine
	// Datei, wo ein Ordner hingehört, ein veralteter Inhalt, eine
	// liegengebliebene Temp-Datei und die Sperrdatei des Servers.
	target := t.TempDir()
	writeTree(t, target, map[string]string{
		"extra.txt":                   "weg damit",
		"alt/region/r.9.9.mca":        "alter Ordner",
		"DIM-1":                       "Datei statt Ordner",
		"level.dat":                   "veraltet",
		"players/data/abc.dat":        "spieler",                          // schon richtig
		"region/r.0.1.mca":            strings.Repeat("REGION-0-1 ", 300), // gleiche Größe, falscher Inhalt
		"data/.wanderwelt-123.tmp":    "Rest",
		"session.lock":                "gesperrt",
		"datapacks/x/pack.mcmeta/zuv": "Ordner statt Datei",
	})
	// Nennt ein Manifest session.lock (der Koordinator nimmt das an), bleibt
	// die lokale Sperrdatei trotzdem unberührt.
	withLock := *gen
	withLock.Files = append(slices.Clone(gen.Files), api.FileEntry{Path: "session.lock", Size: 7, SHA256: shaHex([]byte("spieler"))})
	if err := worldsync.Download(ctx, h.c, &withLock, target, hc, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	want := withoutLock(world)
	want["session.lock"] = "gesperrt"
	if got := readTree(t, target); !maps.Equal(got, want) {
		t.Fatalf("nach Download:\n got %q\nwant %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	if _, err := os.Stat(filepath.Join(target, "alt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("alter Ordner noch da: %v", err)
	}
}
