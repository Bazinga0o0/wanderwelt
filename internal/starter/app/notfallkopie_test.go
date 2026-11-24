package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

// notfallUmgebung: Home und Dokumente im Temp-Ordner.
func notfallUmgebung(t *testing.T) (docs string) {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", filepath.Join(t.TempDir(), "home"))
	docs = filepath.Join(t.TempDir(), "Dokumente")
	t.Setenv(notfallDokumenteEnv, docs)
	return docs
}

// notfallStand legt einen Bereitstellungsordner mit den Dateien an.
func notfallStand(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".wanderwelt-staging")
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func lies(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// warteKopie wartet, bis alle Hintergrundkopien fertig sind.
func warteKopie(t *testing.T, a *App) {
	t.Helper()
	done := make(chan struct{})
	go func() { a.notfall.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Notfallkopie wird nicht fertig")
	}
}

func TestNotfallkopieNachSitzungsende(t *testing.T) {
	docs := notfallUmgebung(t)
	a := &App{ctx: t.Context()}
	stand := notfallStand(t, map[string]string{
		"level.dat":                        "stand7",
		"region/r.0.0.mca":                 "chunks",
		"playerdata/abc.dat":               "spieler",
		".wanderwelt-x.tmp":                "temp",
		"session.lock":                     "lock",
		"datapacks/pack/data/f.mcfunction": "x",
	})
	a.onCommitted(host.Committed{
		Dir: stand, WorldID: "w-1", Generation: 7, MinecraftVersion: "26.1", At: time.Now(),
	})
	warteKopie(t, a)

	kopie := filepath.Join(docs, notfallName)
	for name, want := range map[string]string{
		"level.dat": "stand7", "region/r.0.0.mca": "chunks", "playerdata/abc.dat": "spieler",
	} {
		if got := lies(t, filepath.Join(kopie, filepath.FromSlash(name))); got != want {
			t.Errorf("%s = %q, erwartet %q", name, got, want)
		}
	}
	for _, name := range []string{".wanderwelt-x.tmp", "session.lock"} {
		if _, err := os.Stat(filepath.Join(kopie, name)); err == nil {
			t.Errorf("%s gehört nicht in die Kopie", name)
		}
	}
	lm := lies(t, filepath.Join(kopie, notfallLiesmich))
	for _, want := range []string{"w-1", "7", "26.1", "saves", "Welt übernehmen"} {
		if !strings.Contains(lm, want) {
			t.Errorf("LIESMICH.txt nennt %q nicht:\n%s", want, lm)
		}
	}
	if ents, _ := os.ReadDir(docs); len(ents) != 1 {
		t.Errorf("Dokumente enthalten %d Einträge, erwartet nur die Kopie", len(ents))
	}
	want := int64(len("stand7") + len("chunks") + len("spieler") + 1)
	n := a.notfallZustand()
	if !n.Behalten || !n.Vorhanden || n.Generation != 7 || n.WorldID != "w-1" || n.Ordner != kopie || n.Groesse != want {
		t.Errorf("Zustand %+v", n)
	}
}

func TestNotfallkopieUebergabeStandErsetztAlte(t *testing.T) {
	docs := notfallUmgebung(t)
	a := &App{ctx: t.Context()}
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "schluss", "alt.dat": "nur alt"}), WorldID: "w", Generation: 3})
	warteKopie(t, a)
	// Kopiert wird, was die Sitzung als Übergabe-Stand meldet (nicht der
	// lokale Weltordner mit Spectator-Daten).
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "uebergabe"}), WorldID: "w", Generation: 4, Handover: true})
	warteKopie(t, a)

	kopie := filepath.Join(docs, notfallName)
	if got := lies(t, filepath.Join(kopie, "level.dat")); got != "uebergabe" {
		t.Fatalf("level.dat = %q, erwartet den Übergabe-Stand", got)
	}
	if _, err := os.Stat(filepath.Join(kopie, "alt.dat")); err == nil {
		t.Fatal("Datei der alten Kopie ist noch da")
	}
	if lm := lies(t, filepath.Join(kopie, notfallLiesmich)); !strings.Contains(lm, "Übergabe-Stand") || !strings.Contains(lm, "4") {
		t.Fatalf("LIESMICH.txt:\n%s", lm)
	}
	if ents, _ := os.ReadDir(docs); len(ents) != 1 {
		t.Fatalf("Reste neben der Kopie: %v", ents)
	}
	if n := a.notfallZustand(); n.Generation != 4 {
		t.Fatalf("Zustand %+v", n)
	}
}

func TestNotfallkopieErsetzenAtomar(t *testing.T) {
	docs := notfallUmgebung(t)
	hinweise := countNotify(t)
	a := &App{ctx: t.Context()}
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "gut"}), WorldID: "w", Generation: 1})
	warteKopie(t, a)

	// Die neue Kopie scheitert mitten im Kopieren: Die alte bleibt, nichts
	// bleibt halb zurück, der Hinweis kommt nur einmal.
	old := notfallCopyFile
	var n atomic.Int32
	notfallCopyFile = func(ctx context.Context, src, dst string) (int64, error) {
		if n.Add(1)%2 == 0 {
			return 0, errors.New("Platte voll")
		}
		return old(ctx, src, dst)
	}
	t.Cleanup(func() { notfallCopyFile = old })
	neu := notfallStand(t, map[string]string{"a.dat": "1", "b.dat": "2", "level.dat": "neu"})
	for range 2 {
		a.onCommitted(host.Committed{Dir: neu, WorldID: "w", Generation: 2})
		warteKopie(t, a)
	}

	kopie := filepath.Join(docs, notfallName)
	if got := lies(t, filepath.Join(kopie, "level.dat")); got != "gut" {
		t.Fatalf("level.dat = %q, erwartet die alte Kopie", got)
	}
	if ents, _ := os.ReadDir(docs); len(ents) != 1 {
		t.Fatalf("Reste neben der Kopie: %v", ents)
	}
	if n := a.notfallZustand(); n.Generation != 1 || !n.Vorhanden {
		t.Fatalf("Zustand %+v, erwartet weiter die alte Kopie", n)
	}
	waitFor(t, "Hinweis", func() bool { return len(hinweise()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	if h := hinweise(); len(h) != 1 || !strings.Contains(h[0], "Platte voll") {
		t.Fatalf("Hinweise %q, erwartet genau einen", h)
	}
}

// Ein Rest eines abgebrochenen Tauschs (nur .alt da) wird wieder zur Kopie,
// auch wenn die neue scheitert.
func TestNotfallkopieRestEinesAbbruchs(t *testing.T) {
	docs := notfallUmgebung(t)
	if err := os.MkdirAll(filepath.Join(docs, notfallName+".alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, notfallName+".alt", "level.dat"), []byte("alt"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := notfallCopyFile
	notfallCopyFile = func(context.Context, string, string) (int64, error) { return 0, errors.New("kaputt") }
	t.Cleanup(func() { notfallCopyFile = old })
	stand := notfallStand(t, map[string]string{"level.dat": "x"})
	if _, err := schreibeNotfallkopie(t.Context(), docs, host.Committed{Dir: stand}, time.Now()); err == nil {
		t.Fatal("kein Fehler")
	}
	if got := lies(t, filepath.Join(docs, notfallName, "level.dat")); got != "alt" {
		t.Fatalf("level.dat = %q, erwartet die wiederhergestellte alte Kopie", got)
	}
}

func TestNotfallkopieHakenAus(t *testing.T) {
	docs := notfallUmgebung(t)
	a := &App{ctx: t.Context()}
	if !a.notfallAn() {
		t.Fatal("Standard muss an sein")
	}
	if err := a.setNotfall(false); err != nil {
		t.Fatal(err)
	}
	if a.notfallAn() || a.notfallZustand().Behalten {
		t.Fatal("Haken aus nicht übernommen")
	}
	var holds atomic.Int32
	var unlocked atomic.Bool
	oldHold := notfallHold
	notfallHold = func(context.Context) (func(), error) {
		holds.Add(1)
		return func() { unlocked.Store(true) }, nil
	}
	t.Cleanup(func() { notfallHold = oldHold })

	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "x"}), WorldID: "w", Generation: 1})
	warteKopie(t, a)
	if holds.Load() != 0 {
		t.Fatal("der Platz wurde belegt, obwohl der Haken aus ist")
	}
	if _, err := os.Stat(filepath.Join(docs, notfallName)); err == nil {
		t.Fatal("Kopie trotz ausgeschaltetem Haken")
	}

	// Wieder an: Kopie entsteht, danach wird der Platz freigegeben.
	if err := a.setNotfall(true); err != nil {
		t.Fatal(err)
	}
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "x"}), WorldID: "w", Generation: 2})
	warteKopie(t, a)
	if holds.Load() != 1 || !unlocked.Load() {
		t.Fatalf("Platz belegt %d mal, freigegeben %v", holds.Load(), unlocked.Load())
	}
	if _, err := os.Stat(filepath.Join(docs, notfallName, "level.dat")); err != nil {
		t.Fatalf("keine Kopie bei gesetztem Haken: %v", err)
	}
}

func TestNotfallkopieFensterAktionen(t *testing.T) {
	ft := startFenster(t, nil)
	docs := filepath.Join(t.TempDir(), "Dokumente")
	t.Setenv(notfallDokumenteEnv, docs)
	ctx := t.Context()

	z, err := ft.fc.Zustand(ctx)
	if err != nil || !z.Notfallkopie.Behalten || z.Notfallkopie.Vorhanden || z.Notfallkopie.Ordner != filepath.Join(docs, notfallName) {
		t.Fatalf("Zustand %+v (%v), erwartet Haken an, keine Kopie", z.Notfallkopie, err)
	}
	if err := ft.fc.Aktion(ctx, "notfallordner", nil); err == nil || !strings.Contains(err.Error(), "noch keine Notfallkopie") {
		t.Fatalf("Ordner öffnen ohne Kopie: %v", err)
	}
	if err := ft.fc.Aktion(ctx, "notfallkopie", map[string]bool{"an": false}); err != nil {
		t.Fatal(err)
	}
	if z, _ := ft.fc.Zustand(ctx); z.Notfallkopie.Behalten {
		t.Fatal("Haken nach „aus“ noch an")
	}
	stand := notfallStand(t, map[string]string{"level.dat": "x"})
	if _, err := schreibeNotfallkopie(t.Context(), docs, host.Committed{Dir: stand, WorldID: "w", Generation: 5}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := speichereNotfall(notfallEinstellung{Aus: true, WorldID: "w", Generation: 5, Zeit: time.Now(), Groesse: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ft.fc.Aktion(ctx, "notfallordner", nil); err != nil {
		t.Fatal(err)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.opened) != 1 || ft.opened[0] != filepath.Join(docs, notfallName) {
		t.Fatalf("geöffnet %v", ft.opened)
	}
}
