package karte

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// TestEndeZuEndeMitEchtemProgramm läuft nur mit KARTE_E2E_BIN (Pfad von
// karte.exe) und KARTE_E2E_WORLD (Weltordner, nur lesen): Die Regionsdateien
// kommen in den Fake-Speicher, der Dienst rendert mit dem echten Programm
// (ganze Welt), dann eine Nachführung nach der Änderung einer Region.
func TestEndeZuEndeMitEchtemProgramm(t *testing.T) {
	bin, world := os.Getenv("KARTE_E2E_BIN"), os.Getenv("KARTE_E2E_WORLD")
	if bin == "" || world == "" {
		t.Skip("KARTE_E2E_BIN/KARTE_E2E_WORLD nicht gesetzt")
	}
	ents, err := os.ReadDir(filepath.Join(world, "dimensions", "minecraft", "overworld", "region"))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, "", func(o *Optionen) {
		o.Programm, o.Farben, o.Umgebung, o.Threads, o.MaxBytes = bin, "", []string{"SystemRoot=" + os.Getenv("SystemRoot")}, 2, 1<<30
		o.Zeitlimit = 10 * time.Minute
	})
	var files []api.FileEntry
	regs := map[Region][]byte{}
	for _, de := range ents {
		r, ok := parseRegionPath(RegionPrefix + de.Name())
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(world, "dimensions", "minecraft", "overworld", "region", de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		regs[r] = data
		if f, ok := tryPutRegion(e.q, r, data); ok {
			files = append(files, f)
		} else {
			f := e.q.put(RegionPrefix+r.Name(), data)
			files = append(files, f)
		}
	}
	e.q.setHead(1, files...)
	start := time.Now()
	e.d.pruefe(context.Background())
	t.Logf("voller Lauf: %s, Regionen %d", time.Since(start).Round(time.Millisecond), len(files))
	info := e.d.Info("w1")
	if info == nil {
		t.Fatalf("kein Stand: %+v", e.d.Gesundheit())
	}
	t.Logf("Info: %+v bounds %+v; Gesundheit %+v", info, info.Bounds, e.d.Gesundheit())

	// Nachführung: ein Chunk in einer Region ändern (Daten nach dem Kopf).
	var pick Region
	for r, d := range regs {
		if len(d) > 100000 {
			pick = r
			break
		}
	}
	mod := append([]byte(nil), regs[pick]...)
	mod[len(mod)-5000] ^= 0xff
	for i := range files {
		if files[i].Path == RegionPrefix+pick.Name() {
			if f, ok := tryPutRegion(e.q, pick, mod); ok {
				files[i] = f
			}
		}
	}
	e.q.setHead(2, files...)
	e.advance(time.Hour)
	start = time.Now()
	e.d.pruefe(context.Background())
	t.Logf("Nachführung (%v): %s", pick, time.Since(start).Round(time.Millisecond))
	if got := e.d.Info("w1"); got == nil || got.Source != "sw1/2" {
		t.Fatalf("nach Nachführung: %+v, %+v", got, e.d.Gesundheit())
	}
	if h := e.d.Gesundheit(); h.Error != "" {
		t.Fatal(h.Error)
	}
	if st := strings.TrimSpace(e.d.stand("w1").st.Source); st != "sw1/2" {
		t.Fatal(st)
	}
}

func tryPutRegion(q *fakeQuelle, r Region, data []byte) (api.FileEntry, bool) {
	return q.putRegionOK(r, data)
}
