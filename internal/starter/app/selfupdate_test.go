package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
)

// releaseExe ist die „neue Version“ in den Tests.
var releaseExe = []byte("MZ wanderwelt 0.5.0 – neue Version")

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func release(version string) *api.StarterRelease {
	return &api.StarterRelease{Version: version, URL: "wanderwelt:wanderwelt-" + version + ".exe",
		SHA256: sha256Hex(releaseExe), Size: int64(len(releaseExe))}
}

// suCoord ist ein echter Koordinator (Dev-Anmeldung) mit Manifest-Eintrag
// starter; er zählt die Downloads von Artefakten.
type suCoord struct {
	c         *client.Client
	srv       *server.Server
	artifacts string // Artefakt-Ordner
	downloads atomic.Int32
}

func newSUCoord(t *testing.T, rel *api.StarterRelease) *suCoord {
	t.Helper()
	dir := t.TempDir()
	manifest := "minecraft_version: \"26.2\"\n"
	if rel != nil {
		manifest += fmt.Sprintf("starter:\n  version: %q\n  url: %q\n  sha256: %q\n  size: %d\n", rel.Version, rel.URL, rel.SHA256, rel.Size)
	}
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &suCoord{artifacts: server.ArtifactDir(dir)}
	if rel != nil {
		_, name, _ := strings.Cut(rel.URL, ":")
		if err := os.MkdirAll(e.artifacts, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.artifacts, name), releaseExe, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{DataDir: dir, StorageBudget: 1 << 30, ManifestFile: path,
		Members: []config.Member{{Email: "admin@example.org", Minecraft: "Admin", Admin: true}}}
	cfg.Auth.Mode = "dev"
	lm, err := lease.New(filepath.Join(dir, "lease.json"), server.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = server.New(cfg, lm, st, portholder.Noop(), nil)
	if err := e.srv.ReloadManifest(); err != nil { // wie beim Start
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/artifacts/") {
			e.downloads.Add(1)
		}
		e.srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	e.c = client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "admin@example.org"})
	return e
}

// suProbe ersetzt Log, Hinweise, Uhr und den Start der neuen exe.
type suProbe struct {
	mu      sync.Mutex
	logs    []string
	started []string
	notes   chan string
	now     time.Time
	// onStart läuft im Start der neuen exe (nil: nichts).
	onStart func()
}

func newSUProbe(t *testing.T) *suProbe {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir()) // nie die echte Installation
	p := &suProbe{notes: make(chan string, 16), now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local)}
	oldLog, oldNotify, oldStart, oldNow := suLogf, notify, startNewVersion, selfUpdateNow
	oldPoll, oldHandover := selfUpdatePoll, selfUpdateHandover
	t.Cleanup(func() {
		suLogf, notify, startNewVersion, selfUpdateNow = oldLog, oldNotify, oldStart, oldNow
		selfUpdatePoll, selfUpdateHandover = oldPoll, oldHandover
	})
	selfUpdatePoll = 5 * time.Millisecond
	selfUpdateHandover = 10 * time.Second
	suLogf = func(format string, args ...any) {
		p.mu.Lock()
		p.logs = append(p.logs, fmt.Sprintf(format, args...))
		p.mu.Unlock()
	}
	notify = func(s string) { p.notes <- s }
	selfUpdateNow = func() time.Time {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.now
	}
	startNewVersion = func(exe string) error {
		p.mu.Lock()
		p.started = append(p.started, exe)
		f := p.onStart
		p.mu.Unlock()
		if f != nil {
			f()
		}
		return nil
	}
	return p
}

func (p *suProbe) log() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.logs, "\n")
}

func (p *suProbe) count(sub string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, l := range p.logs {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func (p *suProbe) startedExes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.started...)
}

func (p *suProbe) advance(d time.Duration) {
	p.mu.Lock()
	p.now = p.now.Add(d)
	p.mu.Unlock()
}

// note wartet auf einen Hinweis mit sub.
func (p *suProbe) note(t *testing.T, sub string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case n := <-p.notes:
			if strings.Contains(n, sub) {
				return
			}
		case <-deadline:
			t.Fatalf("kein Hinweis mit %q\nLog:\n%s", sub, p.log())
		}
	}
}

// noNote: in kurzer Zeit kein Hinweis.
func (p *suProbe) noNote(t *testing.T) {
	t.Helper()
	select {
	case n := <-p.notes:
		t.Fatalf("unerwarteter Hinweis: %q", n)
	case <-time.After(100 * time.Millisecond):
	}
}

func suApp(t *testing.T, c *client.Client, version string) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &App{version: version, ctx: ctx, cancel: cancel, c: c,
		world: make(chan struct{}, 1), refreshCh: make(chan struct{}, 1),
		playCh: make(chan struct{}, 1), quitCh: make(chan struct{})}
}

// handOver lässt den Start der neuen exe wie die echte wirken: Sie schickt
// „update“, die laufende Instanz endet (quitForUpdate).
func (p *suProbe) handOver(a *App) {
	p.mu.Lock()
	p.onStart = func() { go a.onControl(actionUpdate, nil) }
	p.mu.Unlock()
}

// runSelfUpdate führt eine Prüfung aus und wartet höchstens 10 s darauf.
func runSelfUpdate(t *testing.T, a *App) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.selfUpdate(a.ctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("selfUpdate kehrt nicht zurück")
	}
}

func writeInstalled(t *testing.T, data []byte) {
	t.Helper()
	if err := os.WriteFile(installedExe(), data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func checkInstalled(t *testing.T, want []byte) {
	t.Helper()
	if got, err := os.ReadFile(installedExe()); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("installierte Kopie verändert: %q, %v", got, err)
	}
}

// Nur Release-Versionen "MAJOR.MINOR.PATCH" (auch mit "v") sind
// vergleichbar.
func TestCompareSemVer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.5.0", "0.4.0", 1, true},
		{"0.4.0", "0.5.0", -1, true},
		{"0.5.0", "v0.5.0", 0, true},
		{"v1.0.0", "0.99.99", 1, true},
		{"0.10.0", "0.9.0", 1, true},
		{"0.4.10", "0.4.9", 1, true},
		{"1.2.3", "1.2.3", 0, true},
		{"dev", "0.5.0", 0, false},
		{"0.5.0", "1d27c24", 0, false},
		{"0.5.0", "0.0.0-SNAPSHOT-1d27c24", 0, false},
		{"0.5.0-rc.1", "0.4.0", 0, false},
		{"0.5", "0.4.0", 0, false},
		{"0.5.0.1", "0.4.0", 0, false},
		{"01.5.0", "0.4.0", 0, false},
		{"0.5.x", "0.4.0", 0, false},
		{"", "0.4.0", 0, false},
		{"0.5.0+build", "0.4.0", 0, false},
		{"1234567890.0.0", "0.4.0", 0, false},
	} {
		got, ok := compareSemVer(tc.a, tc.b)
		if got != tc.want || ok != tc.ok {
			t.Errorf("compareSemVer(%q, %q) = %d, %v; erwartet %d, %v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

// Laufend 0.4.0, Manifest 0.5.0: Download nach Home()/update, Hash geprüft,
// neue exe gestartet, die laufende Instanz endet für die Übergabe; ein
// Hinweis „aktualisiert sich“.
func TestSelfUpdateNewer(t *testing.T) {
	p := newSUProbe(t)
	k := newSUCoord(t, release("0.5.0"))
	writeInstalled(t, []byte("MZ alte Version 0.4.0"))
	a := suApp(t, k.c, "0.4.0")
	p.handOver(a)

	runSelfUpdate(t, a)

	want := filepath.Join(group.Home(), "update", "wanderwelt-0.5.0.exe")
	if got := p.startedExes(); len(got) != 1 || got[0] != want {
		t.Fatalf("gestartet: %v, erwartet %s\nLog:\n%s", got, want, p.log())
	}
	if b, err := os.ReadFile(want); err != nil || !bytes.Equal(b, releaseExe) {
		t.Fatalf("geladene exe: %q, %v", b, err)
	}
	if n := k.downloads.Load(); n != 1 {
		t.Fatalf("%d Downloads, erwartet 1", n)
	}
	if !a.isQuitting() {
		t.Fatal("die laufende Instanz endet nicht für die Übergabe")
	}
	p.note(t, "aktualisiert sich auf Version 0.5.0")
	if st := loadUpdateState(); st.TriedVersion != "0.5.0" || st.TriedSHA256 != sha256Hex(releaseExe) {
		t.Fatalf("Merker: %+v", st)
	}
	// Ersetzt wird die installierte Kopie erst von der neuen exe selbst.
	checkInstalled(t, []byte("MZ alte Version 0.4.0"))
}

// Gleiche oder ältere Version im Manifest, Entwicklungsstände (dev,
// Commit-Kurzform, Snapshot, Vorabversion) und kein Eintrag: nichts laden,
// nichts starten; die Log-Zeile kommt einmal.
func TestSelfUpdateSkips(t *testing.T) {
	for _, tc := range []struct {
		name, running string
		rel           *api.StarterRelease
		logLine       string
	}{
		{"gleich", "0.5.0", release("0.5.0"), "aktuell"},
		{"gleich mit v", "v0.5.0", release("0.5.0"), "aktuell"},
		{"Manifest älter", "0.6.0", release("0.5.0"), "aktuell"},
		{"dev", "dev", release("0.5.0"), "keine Release-Version"},
		{"Commit-Kurzform", "1d27c24", release("0.5.0"), "keine Release-Version"},
		{"Snapshot", "0.0.0-SNAPSHOT-1d27c24", release("0.5.0"), "keine Release-Version"},
		{"Vorabversion", "0.4.0-rc.1", release("0.5.0"), "keine Release-Version"},
		{"kein Eintrag", "0.4.0", nil, "keine Starter-Version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newSUProbe(t)
			k := newSUCoord(t, tc.rel)
			a := suApp(t, k.c, tc.running)
			runSelfUpdate(t, a)
			runSelfUpdate(t, a)
			if got := p.startedExes(); len(got) != 0 {
				t.Fatalf("gestartet: %v", got)
			}
			if n := k.downloads.Load(); n != 0 {
				t.Fatalf("%d Downloads", n)
			}
			if entries, _ := os.ReadDir(updateDir()); len(entries) != 0 {
				t.Fatalf("Dateien in update: %v", entries)
			}
			if n := p.count(tc.logLine); n != 1 {
				t.Fatalf("Log-Zeile %q %d-mal, erwartet einmal:\n%s", tc.logLine, n, p.log())
			}
			if a.isQuitting() {
				t.Fatal("Instanz endet")
			}
			p.noNote(t)
		})
	}
}

// Die Datei passt nicht mehr zum Hash (nach der Prüfung des Koordinators
// ausgetauscht): nichts ersetzt, nichts gestartet, Log, ein Hinweis – und
// am selben Tag kein zweiter.
func TestSelfUpdateBadHash(t *testing.T) {
	p := newSUProbe(t)
	k := newSUCoord(t, release("0.5.0"))
	bad := bytes.Clone(releaseExe)
	bad[len(bad)-1] ^= 1 // gleiche Größe, anderer Hash
	if err := os.WriteFile(filepath.Join(k.artifacts, "wanderwelt-0.5.0.exe"), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	writeInstalled(t, []byte("MZ alte Version 0.4.0"))
	a := suApp(t, k.c, "0.4.0")

	runSelfUpdate(t, a)
	if got := p.startedExes(); len(got) != 0 {
		t.Fatalf("gestartet: %v", got)
	}
	if !strings.Contains(p.log(), "gescheitert") || !strings.Contains(p.log(), "Hash passt nicht") {
		t.Fatalf("Log:\n%s", p.log())
	}
	p.note(t, "konnte sich nicht auf Version 0.5.0 aktualisieren")
	if entries, _ := os.ReadDir(updateDir()); len(entries) != 1 || entries[0].Name() != updateStateFile {
		t.Fatalf("in update: %v", entries)
	}
	checkInstalled(t, []byte("MZ alte Version 0.4.0"))

	// Nächste Prüfungen am selben Tag (alle 6 h, auch nach einem Neustart):
	// kein neuer Download desselben Stands, kein Hinweis. Einen Tag später
	// ein neuer Versuch und wieder ein Hinweis.
	runSelfUpdate(t, a)
	runSelfUpdate(t, suApp(t, k.c, "0.4.0"))
	if n := k.downloads.Load(); n != 1 {
		t.Fatalf("%d Downloads am selben Tag, erwartet 1:\n%s", n, p.log())
	}
	if n := p.count("gescheitert"); n != 1 || !strings.Contains(p.log(), "schon am") {
		t.Fatalf("Log:\n%s", p.log())
	}
	p.noNote(t)
	p.advance(25 * time.Hour)
	runSelfUpdate(t, a)
	if n := k.downloads.Load(); n != 2 {
		t.Fatalf("%d Downloads nach einem Tag, erwartet 2", n)
	}
	p.note(t, "konnte sich nicht")
	if a.isQuitting() {
		t.Fatal("Instanz endet trotz Fehler")
	}
}

// Das Manifest nennt eine Version, die Datei fehlt aber (404): nichts
// ersetzt, nichts gestartet, Log.
func TestSelfUpdateMissing(t *testing.T) {
	p := newSUProbe(t)
	k := newSUCoord(t, release("0.5.0"))
	if err := os.Remove(filepath.Join(k.artifacts, "wanderwelt-0.5.0.exe")); err != nil {
		t.Fatal(err)
	}
	writeInstalled(t, []byte("MZ alte Version 0.4.0"))
	a := suApp(t, k.c, "0.4.0")

	runSelfUpdate(t, a)
	if got := p.startedExes(); len(got) != 0 {
		t.Fatalf("gestartet: %v", got)
	}
	if l := p.log(); !strings.Contains(l, "gescheitert") || !strings.Contains(l, "404") {
		t.Fatalf("Log:\n%s", l)
	}
	p.note(t, "konnte sich nicht")
	if _, err := os.Stat(filepath.Join(updateDir(), "wanderwelt-0.5.0.exe")); err == nil {
		t.Fatal("exe trotz 404 abgelegt")
	}
	checkInstalled(t, []byte("MZ alte Version 0.4.0"))
	if a.isQuitting() {
		t.Fatal("Instanz endet trotz Fehler")
	}
	// Die nächste Prüfung am selben Tag fragt nicht noch einmal nach der
	// Datei; ein Tag später schon.
	runSelfUpdate(t, a)
	if n := k.downloads.Load(); n != 1 {
		t.Fatalf("%d Anfragen nach der Datei am selben Tag, erwartet 1", n)
	}
	p.advance(25 * time.Hour)
	runSelfUpdate(t, a)
	if n := k.downloads.Load(); n != 2 {
		t.Fatalf("%d Anfragen nach einem Tag, erwartet 2", n)
	}
}

// Ein Netzfehler beim Laden ist kein endgültiger Fehlschlag: Die nächste
// Prüfung lädt wieder.
func TestSelfUpdateNetworkErrorRetries(t *testing.T) {
	p := newSUProbe(t)
	k := newSUCoord(t, release("0.5.0"))
	var fail atomic.Bool
	fail.Store(true)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && strings.HasPrefix(r.URL.Path, "/v1/artifacts/") {
			// Verbindung mitten im Körper abbrechen.
			w.Header().Set("Content-Length", strconv.Itoa(len(releaseExe)))
			w.WriteHeader(http.StatusOK)
			w.Write(releaseExe[:5])
			panic(http.ErrAbortHandler)
		}
		k.srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	c := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "admin@example.org"})
	a := suApp(t, c, "0.4.0")
	runSelfUpdate(t, a)
	if len(p.startedExes()) != 0 || !strings.Contains(p.log(), "gescheitert") {
		t.Fatalf("gestartet %v\n%s", p.startedExes(), p.log())
	}
	if st := loadUpdateState(); st.TriedSHA256 != "" {
		t.Fatalf("Netzfehler als Versuch gemerkt: %+v", st)
	}
	fail.Store(false)
	p.handOver(a)
	runSelfUpdate(t, a)
	if len(p.startedExes()) != 1 {
		t.Fatalf("nach dem Netzfehler kein neuer Versuch:\n%s", p.log())
	}
}

// Hostet der PC (oder läuft eine Aktion, spielt er mit, benutzt jemand den
// Welt-Ordner), wartet das Update bis danach; dann belegt es die Aktion,
// sodass weder Spielen noch Dauerbetrieb dazwischenkommt.
func TestSelfUpdateWaitsForHosting(t *testing.T) {
	for _, tc := range []struct {
		name         string
		set, release func(a *App)
	}{
		{"hostet", func(a *App) { a.hostCancel = func() {} }, func(a *App) { a.hostCancel = nil }},
		{"Aktion läuft", func(a *App) { a.busy = "Welt übernehmen" }, func(a *App) { a.busy = "" }},
		{"spielt mit", func(a *App) { a.joinCancel = func() {} }, func(a *App) { a.joinCancel = nil }},
		{"Welt-Ordner belegt", func(a *App) { a.worldHolder = "recover" }, func(a *App) { a.worldHolder = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newSUProbe(t)
			k := newSUCoord(t, release("0.5.0"))
			a := suApp(t, k.c, "0.4.0")
			a.mu.Lock()
			tc.set(a)
			a.mu.Unlock()
			blocked := make(chan string, 1)
			p.mu.Lock()
			p.onStart = func() {
				// Während der Übergabe ist die Aktion belegt.
				_, running := a.begin("Spielen")
				blocked <- running
				go a.onControl(actionUpdate, nil)
			}
			p.mu.Unlock()

			done := make(chan struct{})
			go func() {
				defer close(done)
				a.selfUpdate(a.ctx)
			}()
			deadline := time.Now().Add(5 * time.Second)
			for p.count("wartet, bis dieser PC frei ist") == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("wartet nicht:\n%s", p.log())
				}
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond) // zehn Runden des Wartens
			if got := p.startedExes(); len(got) != 0 {
				t.Fatalf("gestartet, obwohl der PC nicht frei ist: %v", got)
			}
			if n := k.downloads.Load(); n != 1 {
				t.Fatalf("%d Downloads, erwartet 1 (Laden darf vorher)", n)
			}

			a.mu.Lock()
			tc.release(a)
			a.mu.Unlock()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("kein Update nach dem Ende:\n%s", p.log())
			}
			if got := p.startedExes(); len(got) != 1 {
				t.Fatalf("gestartet: %v", got)
			}
			if running := <-blocked; running != selfUpdateLabel {
				t.Fatalf("während der Übergabe ging Spielen (läuft: %q)", running)
			}
		})
	}
}

// Kein Update-Kreislauf: Nach der Übergabe meldet die neue Instanz 0.5.0
// und lädt nichts mehr; eine Instanz, die weiter 0.4.0 meldet (die alte
// zweite Instanz oder eine exe mit falscher Version), stößt denselben Stand
// nicht noch einmal an – erst einen Tag später wieder.
func TestNoUpdateLoop(t *testing.T) {
	p := newSUProbe(t)
	k := newSUCoord(t, release("0.5.0"))
	old := suApp(t, k.c, "0.4.0")
	p.handOver(old)
	runSelfUpdate(t, old)
	if len(p.startedExes()) != 1 || k.downloads.Load() != 1 {
		t.Fatalf("erste Übergabe: %v, %d Downloads", p.startedExes(), k.downloads.Load())
	}

	// Die neue Instanz (gleiches Home) meldet 0.5.0.
	newer := suApp(t, k.c, "0.5.0")
	p.handOver(newer)
	runSelfUpdate(t, newer)
	if len(p.startedExes()) != 1 || k.downloads.Load() != 1 {
		t.Fatalf("neue Instanz lädt erneut: %v, %d Downloads", p.startedExes(), k.downloads.Load())
	}
	if newer.isQuitting() {
		t.Fatal("neue Instanz endet")
	}
	if _, err := os.Stat(filepath.Join(updateDir(), "wanderwelt-0.5.0.exe")); err == nil {
		t.Fatal("geladene exe nach dem Update nicht aufgeräumt")
	}

	// Eine Instanz meldet weiter 0.4.0: kein zweiter Anstoß am selben Tag.
	again := suApp(t, k.c, "0.4.0")
	p.handOver(again)
	runSelfUpdate(t, again)
	runSelfUpdate(t, again)
	if len(p.startedExes()) != 1 || k.downloads.Load() != 1 || again.isQuitting() {
		t.Fatalf("zweiter Anstoß: %v, %d Downloads\n%s", p.startedExes(), k.downloads.Load(), p.log())
	}
	if !strings.Contains(p.log(), "schon am") {
		t.Fatalf("Log:\n%s", p.log())
	}
	// Ein Tag später: ein neuer Versuch (höchstens einer pro Tag).
	p.advance(25 * time.Hour)
	runSelfUpdate(t, again)
	if len(p.startedExes()) != 2 || !again.isQuitting() {
		t.Fatalf("nach einem Tag: %v", p.startedExes())
	}
	// Der Hinweis „aktualisiert sich“ kam nur einmal für 0.5.0.
	p.note(t, "aktualisiert sich auf Version 0.5.0")
	p.noNote(t)
}

// Die exe im Manifest ist dieselbe wie die installierte (sie meldet eine
// andere Version als das Manifest): keine Übergabe, Merker gesetzt, Log
// und Hinweis.
func TestSelfUpdateSameAsInstalled(t *testing.T) {
	p := newSUProbe(t)
	k := newSUCoord(t, release("0.5.0"))
	writeInstalled(t, releaseExe)
	a := suApp(t, k.c, "0.4.0")
	runSelfUpdate(t, a)
	runSelfUpdate(t, a)
	if got := p.startedExes(); len(got) != 0 {
		t.Fatalf("gestartet: %v", got)
	}
	if n := k.downloads.Load(); n != 1 {
		t.Fatalf("%d Downloads, erwartet 1", n)
	}
	if !strings.Contains(p.log(), "dieselbe Datei wie die installierte") {
		t.Fatalf("Log:\n%s", p.log())
	}
	p.note(t, "konnte sich nicht")
	p.noNote(t)
}

// Übernimmt die neue exe nicht (sie schickt kein „update“), gibt die
// Instanz die Aktion wieder frei und läuft weiter.
func TestSelfUpdateHandoverTimeout(t *testing.T) {
	p := newSUProbe(t)
	selfUpdateHandover = 50 * time.Millisecond
	k := newSUCoord(t, release("0.5.0"))
	a := suApp(t, k.c, "0.4.0")
	runSelfUpdate(t, a)
	if len(p.startedExes()) != 1 {
		t.Fatalf("gestartet: %v", p.startedExes())
	}
	if ok, running := a.begin("Spielen"); !ok {
		t.Fatalf("Aktion bleibt belegt: %q", running)
	}
	if !strings.Contains(p.log(), "nicht übernommen") || a.isQuitting() {
		t.Fatalf("Log:\n%s", p.log())
	}
}

// Vor dem Start wird die geladene exe noch einmal geprüft: Wurde sie
// während des Wartens verändert, startet nichts.
func TestSelfUpdateRechecksBeforeStart(t *testing.T) {
	p := newSUProbe(t)
	k := newSUCoord(t, release("0.5.0"))
	a := suApp(t, k.c, "0.4.0")
	a.mu.Lock()
	a.hostCancel = func() {}
	a.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.selfUpdate(a.ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for p.count("wartet, bis dieser PC frei ist") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("wartet nicht:\n%s", p.log())
		}
		time.Sleep(5 * time.Millisecond)
	}
	exe := filepath.Join(updateDir(), "wanderwelt-0.5.0.exe")
	bad := bytes.Clone(releaseExe)
	bad[0] ^= 1
	if err := os.WriteFile(exe, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.hostCancel = nil
	a.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("selfUpdate kehrt nicht zurück")
	}
	if got := p.startedExes(); len(got) != 0 {
		t.Fatalf("veränderte exe gestartet: %v", got)
	}
	if _, err := os.Stat(exe); err == nil {
		t.Fatal("veränderte exe liegt noch da")
	}
	if ok, running := a.begin("Spielen"); !ok {
		t.Fatalf("Aktion bleibt belegt: %q", running)
	}
	p.note(t, "konnte sich nicht")
}
