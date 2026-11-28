package karte

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// ---- Fake-Programm (Helfermodus des Testprogramms) ----

const (
	helperEnv = "KARTE_TEST_HELPER" // Modus: ok | fail | hang | big
	helperLog = "KARTE_TEST_LOG"    // Datei für die Aufrufe
)

// TestMain: Im Helfermodus ist das Testprogramm das Programm „karte“. Es
// schreibt jeden Aufruf (Argumente, Regionsdateien, only-regions) in
// helperLog und erzeugt eine Pyramide aus je einer Kachel 0/X/Z je Region.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

type helperCall struct {
	Args    []string `json:"args"`
	Regions []string `json:"regions"` // Dateien in --regions
	Only    string   `json:"only"`    // Inhalt von --only-regions
	Prev    bool     `json:"prev"`    // --previous-manifest lesbar
}

func helperMain(mode string) {
	args := os.Args[1:]
	opt := map[string]string{}
	for i := 1; i+1 < len(args); i += 2 {
		opt[args[i]] = args[i+1]
	}
	call := helperCall{Args: args}
	if ents, err := os.ReadDir(opt["--regions"]); err == nil {
		for _, e := range ents {
			call.Regions = append(call.Regions, e.Name())
		}
	}
	if b, err := os.ReadFile(opt["--only-regions"]); err == nil {
		call.Only = string(b)
	}
	tiles := map[string]tileEntry{}
	if b, err := os.ReadFile(opt["--previous-manifest"]); err == nil {
		call.Prev = true
		if m, err := parseManifest(b); err == nil {
			tiles = m.Tiles
		}
	}
	if lf := os.Getenv(helperLog); lf != "" {
		line, _ := json.Marshal(call)
		f, _ := os.OpenFile(lf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		f.Write(append(line, '\n'))
		f.Close()
	}
	out := opt["--out"]
	switch mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "kaputt: Token=geheim\x00 Ende")
		os.Exit(3)
	case "hang":
		// Halb fertig, dann hängen: Der Lauf muss ohne Spuren abbrechen.
		os.MkdirAll(filepath.Join(out, "tiles", "0", "9"), 0o755)
		os.WriteFile(filepath.Join(out, "tiles", "0", "9", "9.png"), []byte("halb"), 0o644)
		time.Sleep(time.Hour)
	}
	fmt.Println(strings.Repeat("x", 200_000)) // überlange Zeile
	fmt.Println(`{"ev":"start","regions":2}`)
	render := call.Regions
	if only := strings.Fields(call.Only); len(only) > 0 {
		render = nil
		for i := 0; i+1 < len(only); i += 2 {
			render = append(render, "r."+only[i]+"."+only[i+1]+".mca")
		}
	}
	for _, name := range render {
		p := strings.Split(name, ".")
		x, z := p[1], p[2]
		data, err := os.ReadFile(filepath.Join(opt["--regions"], name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		sum := sha256.Sum256(data)
		body := []byte("png-" + hex.EncodeToString(sum[:4]))
		if mode == "big" {
			body = bytes.Repeat([]byte{7}, 2<<20)
		}
		dir := filepath.Join(out, "tiles", "0", x)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, z+".png"), body, 0o644)
		h := sha256.Sum256(body)
		tiles["0/"+x+"/"+z] = tileEntry{SHA256: hex.EncodeToString(h[:]), Size: int64(len(body))}
		fmt.Printf("{\"ev\":\"region\",\"x\":%s,\"z\":%s,\"ms\":1}\n", x, z)
	}
	m := manifestFile{Format: 1, Dimension: "overworld", TileSize: 256, Levels: []int{0, 1, 2, 3, 4, 5, 6},
		Source: opt["--source"], Generated: time.Date(2026, 11, 27, 20, 0, 0, 0, time.UTC),
		Bounds: map[string]api.KarteBounds{"0": {XMin: -1, XMax: 3, YMin: -2, YMax: 4}}, Tiles: tiles}
	data, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(out, "karte.json"), data, 0o644)
	fmt.Printf("{\"ev\":\"done\",\"tiles\":%d,\"changed\":%d,\"ms\":2}\n", len(tiles), len(render))
	os.Exit(0)
}

// ---- Fake-Speicher ----

type fakeQuelle struct {
	mu     sync.Mutex
	blobs  map[string][]byte
	worlds []store.WorldEntry
	gens   map[string]*api.Generation // Welt der Gruppe → Head
}

func newQuelle() *fakeQuelle {
	return &fakeQuelle{blobs: map[string][]byte{}, gens: map[string]*api.Generation{},
		worlds: []store.WorldEntry{{ID: "w1", Name: "Welt", StoreWorld: "sw1"}}}
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (q *fakeQuelle) OpenBlob(sha string) (io.ReadSeekCloser, int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	b, ok := q.blobs[sha]
	if !ok {
		return nil, 0, fmt.Errorf("%w: Blob %s", store.ErrNotFound, sha)
	}
	return nopCloser{bytes.NewReader(b)}, int64(len(b)), nil
}

func (q *fakeQuelle) GroupWorlds() []store.WorldEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.worlds)
}

func (q *fakeQuelle) HeadRefOf(world string) (string, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if g := q.gens[world]; g != nil {
		return "sw1", g.Number
	}
	return "", 0
}

func (q *fakeQuelle) HeadGenOf(world string) (string, *api.Generation, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	g := q.gens[world]
	if g == nil {
		return "", nil, nil
	}
	c := *g
	c.Files = slices.Clone(g.Files)
	return "sw1", &c, nil
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// put legt eine Datei als Blob (ganz) ab.
func (q *fakeQuelle) put(path string, data []byte) api.FileEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.blobs[sha(data)] = data
	return api.FileEntry{Path: path, Size: int64(len(data)), SHA256: sha(data)}
}

// putRegion legt eine Regionsdatei zerlegt ab (Rezept und Segmente).
func (q *fakeQuelle) putRegion(t testing.TB, r Region, data []byte) api.FileEntry {
	t.Helper()
	f, ok := q.putRegionOK(r, data)
	if !ok {
		t.Fatal("Region nicht zerlegbar")
	}
	return f
}

func (q *fakeQuelle) putRegionOK(r Region, data []byte) (api.FileEntry, bool) {
	segs, parts, ok := worldsync.SplitRegion(data)
	if !ok || len(segs) < 2 {
		return api.FileEntry{}, false
	}
	rec, err := api.EncodeRecipe(segs)
	if err != nil {
		return api.FileEntry{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, s := range segs {
		q.blobs[s.SHA256] = parts[i]
	}
	q.blobs[sha(rec)] = rec
	return api.FileEntry{Path: RegionPrefix + r.Name(), Size: int64(len(data)), SHA256: sha(data), Recipe: sha(rec)}, true
}

func (q *fakeQuelle) setHead(n int64, files ...api.FileEntry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.gens["w1"] = &api.Generation{Number: n, WorldID: "sw1", Files: files}
	q.worlds[0].Head = n
}

// makeRegion baut eine gültige Regionsdatei: Kopf, dann je Chunk (Index →
// Zahl der Sektoren) die Daten, mit einer Lücke nach dem ersten Chunk.
func makeRegion(seed byte, chunks map[int]int) []byte {
	idx := make([]int, 0, len(chunks))
	for i := range chunks {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	head := make([]byte, 8192)
	body := []byte{}
	sector := 2
	for n, i := range idx {
		c := chunks[i]
		binary.BigEndian.PutUint32(head[4*i:], uint32(sector<<8|c))
		for j := 0; j < c*4096; j++ {
			body = append(body, seed+byte(i)+byte(j%251))
		}
		sector += c
		if n == 0 { // Lücke
			body = append(body, make([]byte, 4096)...)
			sector++
		}
	}
	for i := 0; i < 1024; i++ { // Zeitstempel
		binary.BigEndian.PutUint32(head[4096+4*i:], uint32(seed)+uint32(i))
	}
	return append(head, body...)
}

// ---- Test-Umgebung ----

type env struct {
	t   *testing.T
	d   *Dienst
	q   *fakeQuelle
	dir string
	log string // Aufrufe des Programms
	now time.Time
	mu  sync.Mutex
	// wartung: Ergebnis von Gesperrt.
	wartung bool
}

func newEnv(t *testing.T, mode string, tweak func(*Optionen)) *env {
	t.Helper()
	e := &env{t: t, q: newQuelle(), dir: filepath.Join(t.TempDir(), "karte"), now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	e.log = filepath.Join(t.TempDir(), "aufrufe.jsonl")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	o := Optionen{
		Verzeichnis: e.dir, Programm: exe, Farben: "/etc/farben.json", Threads: 2, MinAbstand: 30 * time.Minute,
		MaxBytes: 1 << 20, Quelle: e.q,
		Gesperrt:  func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.wartung },
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jetzt:     func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Zeitlimit: time.Minute, Wiederholung: time.Minute,
		Umgebung: []string{helperEnv + "=" + mode, helperLog + "=" + e.log, "SystemRoot=" + os.Getenv("SystemRoot")},
	}
	if tweak != nil {
		tweak(&o)
	}
	e.d, err = Neu(o)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) advance(dt time.Duration) { e.mu.Lock(); e.now = e.now.Add(dt); e.mu.Unlock() }
func (e *env) setWartung(v bool)        { e.mu.Lock(); e.wartung = v; e.mu.Unlock() }

// calls liest die aufgezeichneten Aufrufe des Programms.
func (e *env) calls() []helperCall {
	e.t.Helper()
	b, err := os.ReadFile(e.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var out []helperCall
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var c helperCall
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// regionen legt Regionen r (mit Seed) im Fake-Speicher ab.
func (e *env) regionen(seed byte, rs ...Region) []api.FileEntry {
	var files []api.FileEntry
	for _, r := range rs {
		files = append(files, e.q.putRegion(e.t, r, makeRegion(seed+byte(r.X*7+r.Z*13), map[int]int{0: 1, 5: 2, 1023: 1})))
	}
	return files
}

func (e *env) pruefe() { e.t.Helper(); e.d.pruefe(context.Background()) }

func argAfter(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

// ---- Zusammensetzen ----

func TestZusammensetzenBitgleich(t *testing.T) {
	q := newQuelle()
	data := makeRegion(3, map[int]int{0: 1, 7: 3, 1023: 2})
	f := q.putRegion(t, Region{1, -2}, data)
	if f.Recipe == "" {
		t.Fatal("kein Rezept")
	}
	var out bytes.Buffer
	if err := Zusammensetzen(context.Background(), q, f, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatal("zusammengesetzte Regionsdatei ist nicht bitgleich zur Quelle")
	}
	// Datei auf der Platte (Name aus den Zahlen).
	dir := t.TempDir()
	if err := RegionDatei(context.Background(), q, f, Region{1, -2}, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "r.1.-2.mca"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Datei: %v, gleich: %v", err, bytes.Equal(got, data))
	}
	// Ganzer Blob (nicht zerlegt).
	whole := q.put(RegionPrefix+"r.0.0.mca", []byte("klein"))
	out.Reset()
	if err := Zusammensetzen(context.Background(), q, whole, &out); err != nil || out.String() != "klein" {
		t.Fatalf("ganzer Blob: %v %q", err, out.String())
	}
	// Leere Datei (kommt in echten Welten vor).
	empty := q.put(RegionPrefix+"r.9.9.mca", nil)
	out.Reset()
	if err := Zusammensetzen(context.Background(), q, empty, &out); err != nil || out.Len() != 0 {
		t.Fatalf("leere Datei: %v", err)
	}
}

func TestZusammensetzenFehler(t *testing.T) {
	data := makeRegion(3, map[int]int{0: 1, 7: 3})
	t.Run("Segment fehlt", func(t *testing.T) {
		q := newQuelle()
		f := q.putRegion(t, Region{}, data)
		segs, _, _ := worldsync.SplitRegion(data)
		delete(q.blobs, segs[2].SHA256)
		if err := Zusammensetzen(context.Background(), q, f, io.Discard); !errors.Is(err, ErrRegionKaputt) {
			t.Fatalf("Fehler: %v", err)
		}
	})
	t.Run("Segment verfälscht", func(t *testing.T) {
		q := newQuelle()
		f := q.putRegion(t, Region{}, data)
		segs, _, _ := worldsync.SplitRegion(data)
		bad := slices.Clone(q.blobs[segs[2].SHA256])
		bad[10] ^= 1
		q.blobs[segs[2].SHA256] = bad
		if err := Zusammensetzen(context.Background(), q, f, io.Discard); !errors.Is(err, ErrRegionKaputt) {
			t.Fatalf("Fehler: %v", err)
		}
	})
	t.Run("falscher Gesamthash", func(t *testing.T) {
		q := newQuelle()
		f := q.putRegion(t, Region{}, data)
		f.SHA256 = strings.Repeat("0", 64)
		if err := Zusammensetzen(context.Background(), q, f, io.Discard); !errors.Is(err, ErrRegionKaputt) {
			t.Fatalf("Fehler: %v", err)
		}
	})
	t.Run("Abbruch", func(t *testing.T) {
		q := newQuelle()
		f := q.putRegion(t, Region{}, data)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Zusammensetzen(ctx, q, f, io.Discard); !errors.Is(err, context.Canceled) {
			t.Fatalf("Fehler: %v", err)
		}
	})
}

// ---- Änderungen zwischen zwei Generationen ----

func TestGeaenderteRegionen(t *testing.T) {
	q := newQuelle()
	mk := func(seed byte, r Region) api.FileEntry {
		return q.putRegion(t, r, makeRegion(seed, map[int]int{0: 1, 4: 2}))
	}
	r00, r10, r01 := Region{0, 0}, Region{1, 0}, Region{0, 1}
	gen1 := []api.FileEntry{mk(1, r00), mk(2, r10), mk(3, r01),
		q.put("level.dat", []byte("lvl")), q.put("dimensions/minecraft/the_nether/region/r.0.0.mca", []byte("nether")),
		q.put(RegionPrefix+"r.01.0.mca", []byte("unsauber")), q.put(RegionPrefix+"sub/r.1.1.mca", []byte("sub"))}
	have1 := Regionen(gen1)
	if len(have1) != 3 {
		t.Fatalf("Regionen: %v (Nether, Unfug und Unterordner zählen nicht)", have1)
	}
	hashes := func(m map[Region]api.FileEntry) map[Region]string {
		out := map[Region]string{}
		for r, f := range m {
			out[r] = f.SHA256
		}
		return out
	}

	// Erster Lauf: alle.
	ch, alle := Geaenderte(nil, have1)
	if !alle || len(ch) != 3 {
		t.Fatalf("erster Lauf: %v %v", ch, alle)
	}
	// Unverändert: nichts.
	if ch, alle := Geaenderte(hashes(have1), have1); alle || len(ch) != 0 {
		t.Fatalf("unverändert: %v %v", ch, alle)
	}
	// Ein Chunk in r.1.0 ändert sich; r.2.2 kommt neu hinzu.
	data := makeRegion(2, map[int]int{0: 1, 4: 2})
	data[8192+4096+100] ^= 0xff // im Chunk-Bereich (hinter der Lücke)
	r10new := q.putRegion(t, r10, data)
	r22 := mk(9, Region{2, 2})
	gen2 := []api.FileEntry{gen1[0], r10new, gen1[2], r22}
	have2 := Regionen(gen2)
	ch, alle = Geaenderte(hashes(have1), have2)
	if alle || !slices.Equal(ch, []Region{{1, 0}, {2, 2}}) {
		t.Fatalf("Nachführung: %v %v", ch, alle)
	}
	// Eine Region verschwindet: nicht nachführbar, alles neu.
	ch, alle = Geaenderte(hashes(have2), Regionen(gen2[:2]))
	if !alle || len(ch) != 2 {
		t.Fatalf("Region verschwunden: %v %v", ch, alle)
	}
	// Die Rezepte der beiden Stände unterscheiden sich in genau einem Segment.
	s1, _, _ := worldsync.SplitRegion(makeRegion(2, map[int]int{0: 1, 4: 2}))
	s2, _, _ := worldsync.SplitRegion(data)
	diff := 0
	for i := range s1 {
		if s1[i].SHA256 != s2[i].SHA256 {
			diff++
		}
	}
	if len(s1) != len(s2) || diff != 1 {
		t.Fatalf("Segmente: %d/%d, verschieden: %d", len(s1), len(s2), diff)
	}
	// Nachbarn: (1,0) hat in have2 die Nachbarn (0,0), (0,1).
	nb := MitNachbarn([]Region{{1, 0}}, have2)
	if !slices.Equal(nb, []Region{{0, 0}, {0, 1}, {1, 0}}) {
		t.Fatalf("Nachbarn: %v", nb)
	}
}

func TestRegionPfad(t *testing.T) {
	ok := map[string]Region{
		RegionPrefix + "r.0.0.mca":    {0, 0},
		RegionPrefix + "r.-1.-2.mca":  {-1, -2},
		RegionPrefix + "r.12.345.mca": {12, 345},
	}
	for p, want := range ok {
		if r, good := parseRegionPath(p); !good || r != want {
			t.Errorf("%s: %v %v", p, r, good)
		}
	}
	for _, p := range []string{
		"r.0.0.mca", RegionPrefix + "r.0.0.mcax", RegionPrefix + "r.0.mca", RegionPrefix + "r.+1.0.mca",
		RegionPrefix + "r.01.0.mca", RegionPrefix + "r.-0.0.mca", RegionPrefix + "r..0.mca",
		RegionPrefix + "r.1.0.mca/x", RegionPrefix + "../r.0.0.mca", RegionPrefix + "r.9999999999.0.mca",
		"dimensions/minecraft/the_nether/region/r.0.0.mca", RegionPrefix + "R.0.0.mca", RegionPrefix + "r. 1.0.mca",
	} {
		if r, good := parseRegionPath(p); good {
			t.Errorf("%q sollte abgelehnt werden, ist %v", p, r)
		}
	}
}

// ---- Der Dienst ruft das Programm auf ----

func TestDienstRuftProgrammMitErwartetenArgumenten(t *testing.T) {
	e := newEnv(t, "ok", func(o *Optionen) { o.Nice = 0 })
	rs := []Region{{0, 0}, {1, 0}, {5, 5}, {-1, 0}}
	files := e.regionen(1, rs...)
	e.q.setHead(1, files...)
	e.pruefe()

	calls := e.calls()
	if len(calls) != 1 {
		t.Fatalf("Aufrufe: %d", len(calls))
	}
	c := calls[0]
	if c.Args[0] != "pyramid" {
		t.Fatalf("Argumente: %v", c.Args)
	}
	regions, _ := argAfter(c.Args, "--regions")
	if v, _ := argAfter(c.Args, "--source"); v != "sw1/1" {
		t.Errorf("--source: %q", v)
	}
	if v, _ := argAfter(c.Args, "--threads"); v != "2" {
		t.Errorf("--threads: %q", v)
	}
	if v, _ := argAfter(c.Args, "--colors"); v != "/etc/farben.json" {
		t.Errorf("--colors: %q", v)
	}
	out, _ := argAfter(c.Args, "--out")
	if filepath.Dir(out) != e.dir || !strings.HasPrefix(filepath.Base(out), ".staging-") || filepath.Dir(regions) == "" {
		t.Errorf("--out %q (Staging neben dem Kartenordner erwartet), --regions %q", out, regions)
	}
	if strings.Contains(strings.Join(c.Args, " "), "blobs") || strings.Contains(out, "blobs") {
		t.Error("nichts darf in blobs/ liegen")
	}
	if slices.Contains(c.Args, "--only-regions") || slices.Contains(c.Args, "--previous-manifest") {
		t.Errorf("der erste Lauf rendert alles: %v", c.Args)
	}
	if want := []string{"r.-1.0.mca", "r.0.0.mca", "r.1.0.mca", "r.5.5.mca"}; len(c.Regions) != 4 {
		t.Errorf("Regionsdateien: %v, erwartet %v", c.Regions, want)
	}
	// Ergebnis ausgeliefert, Aufräumen.
	info := e.d.Info("w1")
	if info == nil || info.Source != "sw1/1" || info.Tiles != 4 || info.Version != 1 || info.Bounds == nil || info.Bounds.XMax != 3 || len(info.Levels) != 7 {
		t.Fatalf("Info: %+v", info)
	}
	ents, _ := os.ReadDir(e.dir)
	if names := dirNames(ents); !slices.Equal(names, []string{"w1"}) {
		t.Fatalf("Kartenordner: %v (Staging und Arbeitsordner müssen weg sein)", names)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "w1", "state.json")); err != nil {
		t.Fatal(err)
	}

	// Zweiter Stand: nur r.1.0 hat sich geändert. Vor Ablauf des Abstands
	// passiert nichts, danach eine Nachführung mit Nachbarn.
	data := makeRegion(77, map[int]int{0: 1, 3: 1})
	files[1] = e.q.putRegion(t, Region{1, 0}, data)
	e.q.setHead(2, files...)
	e.advance(10 * time.Minute)
	e.pruefe()
	if len(e.calls()) != 1 {
		t.Fatal("innerhalb von min_interval darf nicht gerendert werden")
	}
	e.advance(25 * time.Minute)
	e.pruefe()
	calls = e.calls()
	if len(calls) != 2 {
		t.Fatalf("Aufrufe: %d", len(calls))
	}
	c = calls[1]
	if strings.TrimSpace(c.Only) != "1 0" || !c.Prev {
		t.Errorf("only-regions %q, previous-manifest lesbar: %v", c.Only, c.Prev)
	}
	// Geändert (1,0) samt Nachbarn (0,0) – (5,5) und (-1,0) sind keine Nachbarn von (1,0).
	if !slices.Equal(c.Regions, []string{"r.0.0.mca", "r.1.0.mca"}) {
		t.Errorf("Regionsdateien der Nachführung: %v", c.Regions)
	}
	info = e.d.Info("w1")
	if info.Source != "sw1/2" || info.Tiles != 4 {
		t.Fatalf("Info nach Nachführung: %+v", info)
	}
	// Die alten Kacheln sind noch da (kopiert), die neue hat anderen Inhalt.
	for _, r := range rs {
		if _, err := os.Stat(filepath.Join(e.dir, "w1", "tiles", "0", strconv.Itoa(r.X), strconv.Itoa(r.Z)+".png")); err != nil {
			t.Errorf("Kachel %v fehlt: %v", r, err)
		}
	}

	// Gleicher Head: kein Lauf, auch nach langer Zeit.
	e.advance(5 * time.Hour)
	e.pruefe()
	if len(e.calls()) != 2 {
		t.Fatal("ohne neuen Stand wird nicht gerendert")
	}
	// Neuer Stand ohne Regionsänderung: kein Programmaufruf, Quelle nachgetragen.
	e.q.setHead(3, files...)
	e.pruefe()
	if len(e.calls()) != 2 || e.d.stand("w1").st.Source != "sw1/3" {
		t.Fatalf("nur Quelle: Aufrufe %d, Quelle %s", len(e.calls()), e.d.stand("w1").st.Source)
	}
}

func dirNames(ents []os.DirEntry) []string {
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestSchlussStandUmgehtMindestabstand(t *testing.T) {
	e := newEnv(t, "ok", nil)
	e.q.setHead(1, e.regionen(1, Region{0, 0})...)
	e.pruefe()
	files := e.regionen(2, Region{0, 0})
	e.q.setHead(2, files...)
	e.advance(time.Minute)
	e.pruefe()
	if len(e.calls()) != 1 {
		t.Fatal("zu früh gerendert")
	}
	e.d.Schluss()
	e.pruefe()
	if len(e.calls()) != 2 {
		t.Fatalf("nach dem Schluss-Stand muss gerendert werden: %d Aufrufe", len(e.calls()))
	}
	// Einmalig: ein weiterer neuer Stand wartet wieder auf den Abstand.
	e.q.setHead(3, e.regionen(3, Region{0, 0})...)
	e.advance(time.Minute)
	e.pruefe()
	if len(e.calls()) != 2 {
		t.Fatal("der Schluss-Stand gilt nur einmal")
	}
}

func TestFehlerLogOhneGeheimnisUndWiederholung(t *testing.T) {
	var logbuf bytes.Buffer
	e := newEnv(t, "fail", func(o *Optionen) { o.Log = slog.New(slog.NewTextHandler(&logbuf, nil)) })
	e.q.setHead(1, e.regionen(1, Region{0, 0})...)
	e.pruefe()
	h := e.d.Gesundheit()
	if h == nil || !strings.Contains(h.Error, "Exit-Code 3") || h.Tiles != 0 {
		t.Fatalf("Gesundheit: %+v", h)
	}
	if strings.ContainsRune(h.Error, 0) || strings.Contains(logbuf.String(), "\x00") {
		t.Error("Steuerzeichen in Fehlertext")
	}
	if e.d.Info("w1") != nil {
		t.Error("ein gescheiterter Lauf darf nichts ausliefern")
	}
	if !strings.Contains(logbuf.String(), "KARTE: LAUF FEHLGESCHLAGEN") {
		t.Errorf("Log: %s", logbuf.String())
	}
	// Erneuter Versuch erst nach der Wiederholungsfrist.
	e.pruefe()
	if len(e.calls()) != 1 {
		t.Fatal("nach einem Fehler gilt die Wiederholungsfrist")
	}
	e.advance(2 * time.Minute)
	e.pruefe()
	if len(e.calls()) != 2 {
		t.Fatal("nach der Frist wird neu versucht")
	}
	// Staging und Arbeitsordner sind weg.
	ents, _ := os.ReadDir(e.dir)
	if len(ents) != 0 {
		t.Fatalf("Reste: %v", dirNames(ents))
	}
}

func TestAbbruchMitteImLaufLaesstAltenStandIntakt(t *testing.T) {
	// Ein guter Stand ...
	e := newEnv(t, "ok", nil)
	files := e.regionen(1, Region{0, 0}, Region{3, 3})
	e.q.setHead(1, files...)
	e.pruefe()
	before, err := os.ReadFile(filepath.Join(e.dir, "w1", "karte.json"))
	if err != nil {
		t.Fatal(err)
	}
	// ... dann ein Lauf, der hängt und am Zeitlimit abgebrochen wird.
	e.d.o.Zeitlimit = 1500 * time.Millisecond
	e.d.o.Umgebung = []string{helperEnv + "=hang", helperLog + "=" + e.log, "SystemRoot=" + os.Getenv("SystemRoot")}
	e.q.setHead(2, e.regionen(5, Region{0, 0}, Region{3, 3})...)
	e.advance(time.Hour)
	e.pruefe()
	after, err := os.ReadFile(filepath.Join(e.dir, "w1", "karte.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("der alte Stand muss unverändert bleiben: %v", err)
	}
	if e.d.Info("w1").Source != "sw1/1" {
		t.Fatalf("Info: %+v", e.d.Info("w1"))
	}
	if _, err := os.Stat(filepath.Join(e.dir, "w1", "tiles", "0", "9", "9.png")); err == nil {
		t.Fatal("Halbfertiges im ausgelieferten Ordner")
	}
	ents, _ := os.ReadDir(e.dir)
	if names := dirNames(ents); !slices.Equal(names, []string{"w1"}) {
		t.Fatalf("Reste: %v", names)
	}
	if h := e.d.Gesundheit(); h == nil || !strings.Contains(h.Error, "Zeitlimit") {
		t.Fatalf("Gesundheit: %+v", h)
	}
	// Auslieferung funktioniert weiter.
	rec := httptest.NewRecorder()
	e.d.Kachel(rec, httptest.NewRequest("GET", "/", nil), "w1", "0", "3", "3.png")
	if rec.Code != 200 {
		t.Fatalf("Kachel: %d", rec.Code)
	}
}

func TestMaxBytesVerwirftLauf(t *testing.T) {
	e := newEnv(t, "big", func(o *Optionen) { o.MaxBytes = 1 << 20 }) // Kachel hat 2 MiB
	e.q.setHead(1, e.regionen(1, Region{0, 0})...)
	e.pruefe()
	if e.d.Info("w1") != nil {
		t.Fatal("die zu große Karte darf nicht ausgeliefert werden")
	}
	h := e.d.Gesundheit()
	if h == nil || !strings.Contains(h.Error, "karte.max_bytes") {
		t.Fatalf("Gesundheit: %+v", h)
	}
	ents, _ := os.ReadDir(e.dir)
	if len(ents) != 0 {
		t.Fatalf("Reste: %v", dirNames(ents))
	}
	// Mit genug Platz geht es (nach der Wiederholungsfrist).
	e.d.o.MaxBytes = 8 << 20
	e.advance(2 * time.Minute)
	e.pruefe()
	if e.d.Info("w1") == nil {
		t.Fatal("mit größerem Limit muss es gehen")
	}
	if h := e.d.Gesundheit(); h.Error != "" || h.SizeBytes < 2<<20 {
		t.Fatalf("Gesundheit nach Erfolg: %+v", h)
	}
}

func TestWartungKeinLaufUndAbbruch(t *testing.T) {
	e := newEnv(t, "ok", nil)
	e.q.setHead(1, e.regionen(1, Region{0, 0})...)
	e.setWartung(true)
	e.pruefe()
	if len(e.calls()) != 0 || e.d.Info("w1") != nil {
		t.Fatal("während der Wartung wird nicht gerendert")
	}
	e.setWartung(false)
	e.pruefe()
	if len(e.calls()) != 1 || e.d.Info("w1") == nil {
		t.Fatal("nach der Wartung wird gerendert")
	}

	// Beginn der Wartung mitten im Lauf: Abbruch ohne Fehler, alter Stand bleibt.
	h := newEnv(t, "hang", nil)
	h.q.setHead(1, h.regionen(1, Region{0, 0})...)
	done := make(chan struct{})
	go func() { defer close(done); h.pruefe() }()
	deadline := time.Now().Add(20 * time.Second)
	for len(h.calls()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("das Programm wurde nicht gestartet")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.setWartung(true)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("die Wartung bricht den Lauf nicht ab")
	}
	if hh := h.d.Gesundheit(); hh == nil || hh.Error != "" {
		t.Fatalf("ein Abbruch durch die Wartung ist kein Fehler: %+v", hh)
	}
	ents, _ := os.ReadDir(h.dir)
	if len(ents) != 0 {
		t.Fatalf("Reste: %v", dirNames(ents))
	}
}

func TestNeustartLiefertVorhandenenStandUndOhneProgrammNurAus(t *testing.T) {
	e := newEnv(t, "ok", nil)
	e.q.setHead(1, e.regionen(1, Region{0, 0}, Region{1, 1})...)
	e.pruefe()
	want := e.d.Info("w1")
	if want == nil {
		t.Fatal("kein Stand")
	}
	// Neustart mit Programm: Stand ist da, ohne Lauf (gleicher Head).
	d2, err := Neu(Optionen{Verzeichnis: e.dir, Programm: e.d.o.Programm, Quelle: e.q, Umgebung: e.d.o.Umgebung, MinAbstand: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if got := d2.Info("w1"); got == nil || *got.Bounds != *want.Bounds || got.Source != want.Source {
		t.Fatalf("nach Neustart: %+v", got)
	}
	d2.pruefe(context.Background())
	if len(e.calls()) != 1 {
		t.Fatal("gleicher Head nach Neustart: kein neuer Lauf")
	}
	// Neustart ohne Karten-Binary: nur die Erzeugung ist aus.
	d3, err := Neu(Optionen{Verzeichnis: e.dir, Quelle: e.q})
	if err != nil {
		t.Fatal(err)
	}
	if d3.Erzeugung() {
		t.Fatal("ohne Binary keine Erzeugung")
	}
	done := make(chan struct{})
	go func() { d3.Lauf(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Lauf ohne Binary muss sofort enden")
	}
	rec := httptest.NewRecorder()
	d3.Kachel(rec, httptest.NewRequest("GET", "/", nil), "w1", "0", "1", "1.png")
	if rec.Code != 200 {
		t.Fatalf("Auslieferung ohne Binary: %d", rec.Code)
	}
	// Binary konfiguriert, aber nicht vorhanden: Lauf schaltet sich aus, Auslieferung bleibt.
	d4, _ := Neu(Optionen{Verzeichnis: e.dir, Programm: filepath.Join(e.dir, "gibt-es-nicht"), Quelle: e.q})
	d4.Lauf(context.Background()) // kehrt zurück
	if d4.Info("w1") == nil {
		t.Fatal("Stand weg")
	}
}

func TestNeustartNimmtUnterbrochenenAustauschZurueck(t *testing.T) {
	e := newEnv(t, "ok", nil)
	e.q.setHead(1, e.regionen(1, Region{0, 0})...)
	e.pruefe()
	live := filepath.Join(e.dir, "w1")
	// Absturz zwischen den beiden Umbenennungen: nur „.alt“ und Staging da.
	if err := os.Rename(live, live+".alt"); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(e.dir, ".staging-w1", "tiles"), 0o755)
	os.MkdirAll(filepath.Join(e.dir, ".work-w1", "regions"), 0o755)
	d2, err := Neu(Optionen{Verzeichnis: e.dir, Quelle: e.q})
	if err != nil {
		t.Fatal(err)
	}
	if d2.Info("w1") == nil {
		t.Fatal("der alte Stand muss zurückkommen")
	}
	ents, _ := os.ReadDir(e.dir)
	if names := dirNames(ents); !slices.Equal(names, []string{"w1"}) {
		t.Fatalf("Reste: %v", names)
	}
	// Ein kaputter Stand wird nicht ausgeliefert.
	os.WriteFile(filepath.Join(live, "karte.json"), []byte("{"), 0o644)
	d3, _ := Neu(Optionen{Verzeichnis: e.dir, Quelle: e.q})
	if d3.Info("w1") != nil {
		t.Fatal("kaputtes Manifest")
	}
}

// ---- Endpunkte (Paketebene) ----

func (e *env) fertigerStand() {
	e.q.setHead(1, e.regionen(1, Region{0, 0}, Region{-1, 4})...)
	e.pruefe()
	if e.d.Info("w1") == nil {
		e.t.Fatal("kein Stand")
	}
}

func TestEndpunkteKachelUndManifest(t *testing.T) {
	e := newEnv(t, "ok", nil)
	e.fertigerStand()
	do := func(method, welt, z, x, y string, hdr http.Header) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/", nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		e.d.Kachel(rec, req, welt, z, x, y)
		return rec
	}

	rec := do("GET", "w1", "0", "-1", "4.png", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("Kachel: %d %v", rec.Code, rec.Header())
	}
	body := rec.Body.Bytes()
	if etag := rec.Header().Get("ETag"); etag != `"`+sha(body)+`"` {
		t.Errorf("ETag %s ist nicht der Hash der Kachel", etag)
	}
	if rec.Header().Get("Cache-Control") != "private, max-age=60" {
		t.Errorf("Cache-Control: %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length: %q", rec.Header().Get("Content-Length"))
	}
	// If-None-Match → 304 ohne Körper.
	for _, inm := range []string{rec.Header().Get("ETag"), "W/" + rec.Header().Get("ETag"), `"x", ` + rec.Header().Get("ETag"), "*"} {
		r2 := do("GET", "w1", "0", "-1", "4.png", http.Header{"If-None-Match": {inm}})
		if r2.Code != 304 || r2.Body.Len() != 0 || r2.Header().Get("ETag") == "" {
			t.Errorf("If-None-Match %q: %d, Körper %d", inm, r2.Code, r2.Body.Len())
		}
	}
	if r2 := do("GET", "w1", "0", "-1", "4.png", http.Header{"If-None-Match": {`"anders"`}}); r2.Code != 200 {
		t.Errorf("anderer ETag: %d", r2.Code)
	}
	// Fehlende Kacheln: 404.
	for _, c := range [][3]string{{"0", "7", "7.png"}, {"1", "0", "0.png"}, {"6", "-1", "4.png"}, {"0", "-1", "5.png"}} {
		if r := do("GET", "w1", c[0], c[1], c[2], nil); r.Code != 404 {
			t.Errorf("%v: %d, erwartet 404", c, r.Code)
		}
	}
	if r := do("GET", "w2", "0", "0", "0.png", nil); r.Code != 404 {
		t.Errorf("Welt ohne Karte: %d", r.Code)
	}
	// Ungültige Zahlen und Pfadtricks: nie ein Dateizugriff, immer 400.
	bad := [][3]string{
		{"..", "0", "0.png"}, {"0", "..", "0.png"}, {"0", "0", "...png"}, {"0", "0", "..%2f..%2fkarte.json.png"},
		{"0", "0", "0"}, {"0", "0", "0.PNG"}, {"0", "0", "0.png.png"}, {"0", "0", ".png"}, {"0", "0", "-.png"},
		{"+0", "0", "0.png"}, {"00", "0", "0.png"}, {"0", "-0", "0.png"}, {"0", "0", "00.png"}, {"0", "0", " 0.png"},
		{"0", "0", "0 .png"}, {"0x0", "0", "0.png"}, {"0", "1e2", "0.png"}, {"0", "0", "../0.png"}, {`..\`, "0", "0.png"},
		{"0", "-1/../-1", "4.png"}, {"0", "0", "99999999999999999999.png"}, {"0", "1234567890", "0.png"},
		{"0", "0", "9223372036854775808.png"}, {"0", "-9223372036854775809", "0.png"}, {"", "0", "0.png"}, {"0", "", "0.png"},
		{"0", "0", "0.png\x00"}, {"\x00", "0", "0.png"}, {"0", "0", "0.png/"},
	}
	for _, c := range bad {
		if r := do("GET", "w1", c[0], c[1], c[2], nil); r.Code != 400 {
			t.Errorf("%q: %d, erwartet 400", c, r.Code)
		}
	}
	for _, welt := range []string{"", "..", "w", "w1/../w1", "W1", "w1 ", "../karte", "w-1", "w99999999999999"} {
		if r := do("GET", welt, "0", "0", "0.png", nil); r.Code != 400 {
			t.Errorf("Welt %q: %d, erwartet 400", welt, r.Code)
		}
	}

	// Manifest: gleiche Bytes wie die Datei, ETag, 304.
	rec = httptest.NewRecorder()
	e.d.Manifest(rec, httptest.NewRequest("GET", "/", nil), "w1")
	disk, _ := os.ReadFile(filepath.Join(e.dir, "w1", "karte.json"))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), disk) || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Manifest: %d", rec.Code)
	}
	if rec.Header().Get("ETag") != `"`+sha(disk)+`"` {
		t.Errorf("ETag des Manifests: %s", rec.Header().Get("ETag"))
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec2 := httptest.NewRecorder()
	e.d.Manifest(rec2, req, "w1")
	if rec2.Code != 304 {
		t.Errorf("Manifest If-None-Match: %d", rec2.Code)
	}
	rec3 := httptest.NewRecorder()
	e.d.Manifest(rec3, httptest.NewRequest("GET", "/", nil), "w2")
	if rec3.Code != 404 {
		t.Errorf("Manifest ohne Karte: %d", rec3.Code)
	}
	rec3 = httptest.NewRecorder()
	e.d.Manifest(rec3, httptest.NewRequest("GET", "/", nil), "../x")
	if rec3.Code != 400 {
		t.Errorf("Manifest, ungültige Welt: %d", rec3.Code)
	}
}

func TestKachelWirdNurAusDemManifestGeliefert(t *testing.T) {
	// Eine Datei, die im Kartenordner liegt, aber nicht im Manifest steht,
	// wird nie ausgeliefert (kein Verzeichnis-Zugriff).
	e := newEnv(t, "ok", nil)
	e.fertigerStand()
	os.WriteFile(filepath.Join(e.dir, "w1", "tiles", "0", "0", "99.png"), []byte("geheim"), 0o644)
	os.WriteFile(filepath.Join(e.dir, "w1", "state.json"), []byte("{}"), 0o644)
	rec := httptest.NewRecorder()
	e.d.Kachel(rec, httptest.NewRequest("GET", "/", nil), "w1", "0", "0", "99.png")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "geheim") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Austausch unter Last: Leser sehen immer einen ganzen Stand.
func TestAustauschUnterLast(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t, "ok", nil)
	e.fertigerStand()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomicCounter
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := httptest.NewRecorder()
				e.d.Kachel(rec, httptest.NewRequest("GET", "/", nil), "w1", "0", "0", "0.png")
				if rec.Code != 200 && rec.Code != 404 { // 404: nur wenn Datei gerade fehlt – nie erlaubt
					bad.add()
				}
				if rec.Code == 404 {
					bad.add()
				}
			}
		}()
	}
	for i := 2; i <= 6; i++ {
		e.q.setHead(int64(i), e.regionen(byte(i), Region{0, 0}, Region{-1, 4})...)
		e.advance(time.Hour)
		e.pruefe()
	}
	close(stop)
	wg.Wait()
	if n := bad.get(); n != 0 {
		t.Fatalf("%d Anfragen sahen keinen ganzen Stand", n)
	}
	if e.d.Info("w1").Source != "sw1/6" {
		t.Fatalf("Info: %+v", e.d.Info("w1"))
	}
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (a *atomicCounter) add() { a.mu.Lock(); a.n++; a.mu.Unlock() }
func (a *atomicCounter) get() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// ---- Ratenbegrenzung ----

func TestRatenbegrenzung(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := newBegrenzer(func() time.Time { return now })
	n := 0
	for b.erlaube("a@x") {
		n++
		if n > 1000 {
			t.Fatal("keine Grenze")
		}
	}
	if n != int(AnfragenSpitze) {
		t.Fatalf("Spitze: %d", n)
	}
	if !b.erlaube("b@x") {
		t.Fatal("ein anderes Mitglied wird nicht mitbegrenzt")
	}
	now = now.Add(time.Second) // 50 neue Marken
	k := 0
	for b.erlaube("a@x") {
		k++
	}
	if k != int(AnfragenProSekunde) {
		t.Fatalf("nach 1 s: %d Marken", k)
	}
}

// ---- Manifest-Prüfung ----

func TestManifestPruefung(t *testing.T) {
	h := strings.Repeat("a", 64)
	good := `{"format":1,"dimension":"overworld","tile_size":256,"levels":[0,1],"tiles":{"0/-1/2":{"sha256":"` + h + `","size":5}}}`
	if _, err := parseManifest([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]string{
		"Format":        strings.Replace(good, `"format":1`, `"format":2`, 1),
		"Dimension":     strings.Replace(good, "overworld", "the_nether", 1),
		"Schlüssel ..":  strings.Replace(good, "0/-1/2", "0/../2", 1),
		"Schlüssel 01":  strings.Replace(good, "0/-1/2", "0/-1/02", 1),
		"Stufe 7":       strings.Replace(good, "0/-1/2", "7/-1/2", 1),
		"Hash":          strings.Replace(good, h, "XYZ", 1),
		"Größe negativ": strings.Replace(good, `"size":5`, `"size":-5`, 1),
		"Stufe":         strings.Replace(good, `[0,1]`, `[0,9]`, 1),
		"kein JSON":     "{",
	} {
		if _, err := parseManifest([]byte(m)); err == nil {
			t.Errorf("%s: sollte abgelehnt werden", name)
		}
	}
}
