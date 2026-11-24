package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

// blockierendeKopie ersetzt das Kopieren einer Datei: meldet sich in started
// und wartet auf ctx oder gate (dann echt kopieren).
func blockierendeKopie(t *testing.T) (started chan struct{}, gate chan struct{}) {
	t.Helper()
	started, gate = make(chan struct{}, 8), make(chan struct{})
	old := notfallCopyFile
	notfallCopyFile = func(ctx context.Context, src, dst string) (int64, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return 0, context.Cause(ctx)
		case <-gate:
			return old(ctx, src, dst)
		}
	}
	t.Cleanup(func() { notfallCopyFile = old })
	return started, gate
}

// Programmende: waitNotfall wartet auf eine Kopie, die gleich fertig wird,
// und bricht eine hängende nach der Frist ab; die alte Kopie bleibt.
func TestNotfallkopieProgrammende(t *testing.T) {
	docs := notfallUmgebung(t)
	a := &App{ctx: t.Context()}
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "alt"}), WorldID: "w", Generation: 1})
	warteKopie(t, a)

	started, gate := blockierendeKopie(t)
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "neu"}), WorldID: "w", Generation: 2})
	<-started
	go func() { time.Sleep(100 * time.Millisecond); close(gate) }()
	a.waitNotfall(10 * time.Second)
	if got := lies(t, filepath.Join(docs, notfallName, "level.dat")); got != "neu" {
		t.Fatalf("level.dat = %q, erwartet die fertige neue Kopie (waitNotfall hat nicht gewartet)", got)
	}

	// Hängende Kopie: nach der Frist abgebrochen, die vorige bleibt, kein .neu.
	started, _ = blockierendeKopie(t)
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "dritte"}), WorldID: "w", Generation: 3})
	<-started
	a.waitNotfall(100 * time.Millisecond)
	warteKopie(t, a)
	if got := lies(t, filepath.Join(docs, notfallName, "level.dat")); got != "neu" {
		t.Fatalf("level.dat = %q, erwartet die vorige Kopie", got)
	}
	if ents, _ := os.ReadDir(docs); len(ents) != 1 {
		t.Fatalf("Reste: %v", ents)
	}
}

// Braucht eine neue Sitzung (oder Recover, Import) den Platz, wird die
// laufende Kopie abgebrochen statt zu warten.
func TestNotfallkopieAbbruchBeiNeuerSitzung(t *testing.T) {
	docs := notfallUmgebung(t)
	a := &App{ctx: t.Context()}
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "alt"}), WorldID: "w", Generation: 1})
	warteKopie(t, a)

	host.SetSyncContention(a.abortNotfall)
	t.Cleanup(func() { host.SetSyncContention(nil) })
	started, _ := blockierendeKopie(t)
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "neu"}), WorldID: "w", Generation: 2})
	<-started

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	unlock, err := host.HoldSync(ctx) // wie der Download einer neuen Sitzung
	if err != nil {
		t.Fatalf("HoldSync blockiert trotz abbrechbarer Kopie: %v", err)
	}
	unlock()
	warteKopie(t, a)
	if got := lies(t, filepath.Join(docs, notfallName, "level.dat")); got != "alt" {
		t.Fatalf("level.dat = %q, erwartet die alte Kopie", got)
	}
	if ents, _ := os.ReadDir(docs); len(ents) != 1 {
		t.Fatalf("Reste: %v", ents)
	}
}

// Scheiterte der Upload, enthält die Kopie den lokalen Stand mit Vermerk.
func TestNotfallkopieNichtHochgeladen(t *testing.T) {
	docs := notfallUmgebung(t)
	a := &App{ctx: t.Context()}
	a.onCommitted(host.Committed{
		Dir: notfallStand(t, map[string]string{"level.dat": "lokal"}), WorldID: "w", Generation: 9, NotUploaded: true,
	})
	warteKopie(t, a)
	lm := lies(t, filepath.Join(docs, notfallName, notfallLiesmich))
	if !strings.Contains(lm, "NICHT hochgeladen (eventuell nur hier vorhanden)") || !strings.Contains(lm, "Basis-Generation") {
		t.Fatalf("LIESMICH.txt:\n%s", lm)
	}
	if n := a.notfallZustand(); !n.NichtHochgeladen || !n.Vorhanden {
		t.Fatalf("Zustand %+v", n)
	}
}

func TestNotfallkopieOneDriveUndPlatz(t *testing.T) {
	t.Setenv(notfallDokumenteEnv, "")
	// Pfade mit dem Trenner der Plattform bauen (die CI läuft auch unter Linux).
	home := filepath.Join(string(filepath.Separator)+"Users", "x")
	oneDrive := filepath.Join(home, "OneDrive")
	t.Setenv("OneDrive", oneDrive)
	t.Setenv("USERPROFILE", home)
	old := dokumenteDir
	dokumenteDir = func() (string, error) { return filepath.Join(oneDrive, "Dokumente"), nil }
	t.Cleanup(func() { dokumenteDir = old })
	if r, err := notfallRoot(); err != nil || r != home {
		t.Fatalf("notfallRoot = %q, %v, erwartet USERPROFILE", r, err)
	}
	other := filepath.Join(home, "OneDrive2", "Dokumente")
	dokumenteDir = func() (string, error) { return other, nil }
	if r, _ := notfallRoot(); r != other {
		t.Fatalf("notfallRoot = %q, OneDrive2 ist nicht OneDrive", r)
	}

	// Zu wenig Platz: Fehler vorab, nichts angelegt.
	docs := notfallUmgebung(t)
	oldFree := notfallFree
	notfallFree = func(string) (int64, error) { return 3, nil }
	t.Cleanup(func() { notfallFree = oldFree })
	stand := notfallStand(t, map[string]string{"level.dat": "viel zu gross"})
	_, err := schreibeNotfallkopie(t.Context(), docs, host.Committed{Dir: stand}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "zu wenig freier Platz") {
		t.Fatalf("Fehler %v", err)
	}
	if ents, _ := os.ReadDir(docs); len(ents) != 0 {
		t.Fatalf("Reste: %v", ents)
	}
}

// Ein Abbruch, der kommt, während die Kopie noch auf den Platz wartet, geht
// nicht verloren (cancel steht vor dem Belegen bereit).
func TestNotfallkopieAbbruchWaehrendWartenAufPlatz(t *testing.T) {
	docs := notfallUmgebung(t)
	a := &App{ctx: t.Context()}
	warten := make(chan struct{})
	old := notfallHold
	notfallHold = func(ctx context.Context) (func(), error) {
		close(warten)
		<-ctx.Done()
		return nil, context.Cause(ctx)
	}
	t.Cleanup(func() { notfallHold = old })
	go func() { <-warten; a.abortNotfall() }()
	a.onCommitted(host.Committed{Dir: notfallStand(t, map[string]string{"level.dat": "x"}), WorldID: "", Generation: 1})
	warteKopie(t, a)
	if _, err := os.Stat(filepath.Join(docs, notfallName)); err == nil {
		t.Fatal("Kopie trotz Abbruch")
	}
}

func TestNotfallkopieLiesmichOhneWeltID(t *testing.T) {
	lm := liesmichText(host.Committed{NotUploaded: true}, time.Now())
	if !strings.Contains(lm, "neue Welt (noch ohne ID)") || strings.Contains(lm, "nachgereicht") {
		t.Fatalf("LIESMICH.txt:\n%s", lm)
	}
}
