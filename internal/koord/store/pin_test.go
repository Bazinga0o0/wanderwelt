package store

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Pin hält das Ziel eines ausstehenden Zurückholens über jeden Aufräumlauf
// – auch über dem Budget, in der aktiven Welt wie im Archiv –, bis Pin es
// wieder freigibt. Usage meldet Belegung und Über-Budget des letzten Laufs.
func TestPrunePinnedAndUsage(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	if used, over := s.Usage(); used != 0 || over {
		t.Fatalf("Usage vor dem ersten Prune: %d, %v", used, over)
	}
	blob := func(label string) api.FileEntry {
		data := make([]byte, 1000)
		copy(data, label)
		return putBlob(t, s, "level.dat", data)
	}

	// Archiv: 3 Stände. Aktive Welt: 1–3 in Stunde X (3 ist deren letzter,
	// also ein Stundenstand außerhalb der letzten 24), 4–30 in Stunde X+1.
	// Jeder Stand hat einen eigenen Blob von 1000 Bytes.
	var archive, active string
	for k := int64(0); k < 3; k++ {
		archive = commit(t, s, k, false, t0.Add(time.Duration(k)*time.Minute), blob(fmt.Sprintf("archiv %d", k))).WorldID
	}
	for i := int64(1); i <= 30; i++ {
		at := t0.Add(24*time.Hour + time.Duration(i)*time.Minute)
		if i > 3 {
			at = at.Add(time.Hour)
		}
		active = commit(t, s, i-1, i == 1, at, blob(fmt.Sprintf("aktiv %d", i))).WorldID
	}
	now := t0.Add(26 * time.Hour)

	exists := func(phase string, world string, n int64, want bool) {
		t.Helper()
		if got := genExists(t, s, world, n); got != want {
			t.Errorf("%s: %s/%d vorhanden = %v, want %v", phase, world, n, got, want)
		}
	}
	usage := func(phase string, wantUsed int64, wantOver bool) {
		t.Helper()
		if used, over := s.Usage(); used != wantUsed || over != wantOver {
			t.Errorf("%s: Usage = %d, %v; want %d, %v", phase, used, over, wantUsed, wantOver)
		}
	}
	prune := func(budget int64, pins ...api.GenRef) {
		t.Helper()
		s.budget = budget
		s.Pin(pins...)
		if err := s.Prune(now); err != nil {
			t.Fatalf("Prune: %v", err)
		}
	}

	// Ohne Budget-Druck: Es bleiben 7–30, der Stundenstand 3 und der
	// gepinnte Stand 2; 1, 4, 5, 6 fallen. Das Archiv bleibt ganz.
	prune(1<<30, api.GenRef{WorldID: active, Number: 2})
	for n := int64(1); n <= 7; n++ {
		exists("ohne Druck", active, n, n == 2 || n == 3 || n == 7)
	}
	usage("ohne Druck", 29_000, false)

	// Winziges Budget, gepinnt sind jetzt aktiv 3 (Stundenstand) und Archiv
	// 1: Sie bleiben; 2 ist nicht mehr gepinnt und fällt, ebenso Archiv 2.
	prune(1000, api.GenRef{WorldID: active, Number: 3}, api.GenRef{WorldID: archive, Number: 1})
	exists("Budget 1000", active, 2, false)
	exists("Budget 1000", active, 3, true)
	exists("Budget 1000", archive, 1, true)
	exists("Budget 1000", archive, 2, false)
	exists("Budget 1000", archive, 3, true)
	usage("Budget 1000", 27_000, true)

	// Freigegeben: Beim nächsten Lauf fallen beide.
	prune(1000)
	exists("freigegeben", active, 3, false)
	exists("freigegeben", archive, 1, false)
	usage("freigegeben", 25_000, true)

	// Über-Budget-Grenze: belegt 25 000, Budget knapp darunter und genau.
	prune(24_999)
	usage("Budget 24999", 25_000, true)
	prune(25_000)
	usage("Budget 25000", 25_000, false)
}

// HeadInfo liefert Welt, Nummer und Zeit des Heads.
func TestHeadInfo(t *testing.T) {
	s := openStore(t, t.TempDir(), 1<<30)
	if w, n, c := s.HeadInfo(); w != "" || n != 0 || !c.IsZero() {
		t.Fatalf("HeadInfo ohne Welt: %q %d %v", w, n, c)
	}
	f := putBlob(t, s, "level.dat", []byte("stand"))
	commit(t, s, 0, false, t0, f)
	g := commit(t, s, 1, false, t0.Add(time.Minute), f)
	if w, n, c := s.HeadInfo(); w != g.WorldID || n != 2 || !c.Equal(g.Created) {
		t.Fatalf("HeadInfo: %q %d %v, want %q 2 %v", w, n, c, g.WorldID, g.Created)
	}
}

// files listet alle Dateien unter dir mit Inhalt.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		out[p] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// RefreshUsage rechnet die Belegung wie Prune aus, ohne etwas zu löschen:
// auch keine Stände, die Prune wegräumen würde, keine fremden Blobs, keine
// Temp-Dateien und keine angefangenen Uploads.
func TestRefreshUsageDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	blob := func(label string) api.FileEntry {
		data := make([]byte, 1000)
		copy(data, label)
		return putBlob(t, s, "level.dat", data)
	}
	// 30 Stände in einer Stunde, je ein eigener Blob: Prune behielte 7–30.
	var world string
	for i := int64(1); i <= 30; i++ {
		world = commit(t, s, i-1, false, t0.Add(time.Duration(i)*time.Minute), blob(fmt.Sprintf("stand %d", i))).WorldID
	}
	now := t0.Add(time.Hour)
	blob("frischer Blob ohne Stand")
	if _, err := s.PutBlobPart(shaOf([]byte("angefangen")), 0, 20, strings.NewReader("angefangen")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tmpPrefix+"liegengeblieben"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := files(t, dir)

	if err := s.RefreshUsage(now); err != nil {
		t.Fatalf("RefreshUsage: %v", err)
	}
	if used, over := s.Usage(); used != 24_000 || over {
		t.Fatalf("Usage = %d, %v; want 24000, false", used, over)
	}
	if after := files(t, dir); !maps.Equal(before, after) {
		t.Fatalf("RefreshUsage hat Dateien verändert: vorher %d, nachher %d", len(before), len(after))
	}
	if !genExists(t, s, world, 1) {
		t.Fatal("Stand 1 weg")
	}

	s.budget = 23_999
	if err := s.RefreshUsage(now); err != nil {
		t.Fatalf("RefreshUsage: %v", err)
	}
	if used, over := s.Usage(); used != 24_000 || !over {
		t.Fatalf("Usage bei Budget 23999 = %d, %v; want 24000, true", used, over)
	}
	s.budget = 24_000
	if err := s.RefreshUsage(now); err != nil {
		t.Fatalf("RefreshUsage: %v", err)
	}
	if used, over := s.Usage(); used != 24_000 || over {
		t.Fatalf("Usage bei Budget 24000 = %d, %v; want 24000, false", used, over)
	}
	if after := files(t, dir); !maps.Equal(before, after) {
		t.Fatal("RefreshUsage über dem Budget hat Dateien verändert")
	}
}
