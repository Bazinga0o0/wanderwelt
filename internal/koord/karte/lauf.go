package karte

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// errWartung: Der Lauf wurde abgebrochen, weil eine Wartung begann.
var errWartung = errors.New("Wartung")

// lauf rendert die Karte der Welt aus ihrem Head-Stand (ein Aufruf des
// Programms) und tauscht sie ein. Panics werden zu Fehlern.
func (d *Dienst) lauf(ctx context.Context, welt string) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("Absturz im Karten-Dienst: %v", p)
		}
	}()
	if err := d.programmPruefen(); err != nil {
		return err
	}
	storeWorld, gen, err := d.o.Quelle.HeadGenOf(welt)
	if err != nil {
		return fmt.Errorf("Head lesen: %w", err)
	}
	if gen == nil {
		return nil
	}
	source := fmt.Sprintf("%s/%d", storeWorld, gen.Number)
	have := Regionen(gen.Files)
	cur := d.stand(welt)

	var changed []Region
	full := true
	if cur != nil {
		changed, full = Geaenderte(cur.st.regionHashes(), have)
	} else {
		changed, _ = Geaenderte(nil, have)
	}
	if len(have) == 0 {
		d.log.Info("Karte: der Stand hat keine Oberwelt-Regionen, nichts zu rendern", "welt", welt, "quelle", source)
		return nil
	}
	if !full && len(changed) == 0 {
		// Nur anderes (Spielstand, Daten) hat sich geändert: Karte bleibt, die
		// Quelle wird nachgetragen.
		return d.nurQuelle(welt, cur, source, storeWorld, gen, have)
	}

	// Zeitlimit und Wartung.
	runCtx, cancel := context.WithTimeout(ctx, d.o.Zeitlimit)
	defer cancel()
	var wartung atomic.Bool
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if d.gesperrt() {
					wartung.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	root := d.o.Verzeichnis
	work := filepath.Join(root, ".work-"+welt)
	staging := filepath.Join(root, ".staging-"+welt)
	os.RemoveAll(work)
	os.RemoveAll(staging)
	defer os.RemoveAll(work)
	defer os.RemoveAll(staging) // nach dem Austausch ist er schon weg
	regionsDir := filepath.Join(work, "regions")
	if err := os.MkdirAll(regionsDir, 0o700); err != nil {
		return err
	}

	// Regionen aus dem Speicher zusammensetzen: bei einer Nachführung die
	// geänderten samt Nachbarn (für die Schattierung am Rand), sonst alle.
	need := changed
	if !full {
		need = MitNachbarn(changed, have)
	}
	for _, r := range need {
		if err := RegionDatei(runCtx, d.o.Quelle, have[r], r, regionsDir); err != nil {
			return d.abbruch(runCtx, &wartung, err)
		}
	}

	args := []string{"pyramid", "--regions", regionsDir, "--out", staging, "--source", source}
	if d.o.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(d.o.Threads))
	}
	if d.o.Farben != "" {
		args = append(args, "--colors", d.o.Farben)
	}
	if full {
		if err := os.MkdirAll(staging, 0o750); err != nil {
			return err
		}
	} else {
		if err := copyTree(cur.dir, staging); err != nil {
			return fmt.Errorf("bisherigen Stand kopieren: %w", err)
		}
		only := filepath.Join(work, "only-regions.txt")
		var b strings.Builder
		for _, r := range changed {
			b.WriteString(regionLine(r))
		}
		prev := filepath.Join(work, "previous-karte.json")
		if err := os.WriteFile(only, []byte(b.String()), 0o600); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(cur.dir, manifestName), prev); err != nil {
			return err
		}
		args = append(args, "--only-regions", only, "--previous-manifest", prev)
	}

	start := time.Now()
	if err := d.starte(runCtx, args, welt); err != nil {
		return d.abbruch(runCtx, &wartung, err)
	}
	if err := runCtx.Err(); err != nil {
		return d.abbruch(runCtx, &wartung, err)
	}

	// Prüfen, bevor etwas ausgeliefert wird.
	neu, err := loadStand(staging, true)
	if err != nil {
		return fmt.Errorf("Ausgabe des Programms unbrauchbar: %w", err)
	}
	if neu.bytes > d.o.MaxBytes {
		return fmt.Errorf("%w: %d Bytes, erlaubt %d (Lauf verworfen)", ErrZuGross, neu.bytes, d.o.MaxBytes)
	}
	regs := make(map[string]string, len(have))
	for r, f := range have {
		regs[stateKey(r)] = f.SHA256
	}
	neu.st = state{Format: 1, Source: source, StoreWorld: storeWorld, Generation: gen.Number, Regions: regs,
		LastRun: d.o.Jetzt().UTC(), DurationMs: time.Since(start).Milliseconds()}
	if err := writeJSONFile(filepath.Join(staging, stateName), neu.st); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return d.abbruch(runCtx, &wartung, err)
	}
	if err := d.tausche(welt, staging, neu); err != nil {
		return fmt.Errorf("Austausch: %w", err)
	}
	d.log.Info("Karte erzeugt", "welt", welt, "quelle", source, "regionen", len(have), "geändert", len(changed),
		"vollständig", full, "kacheln", len(neu.tiles), "bytes", neu.bytes, "dauer", time.Since(start).Round(time.Millisecond))
	return nil
}

// abbruch übersetzt einen Fehler: Wurde der Lauf wegen einer Wartung
// abgebrochen, ist das errWartung, bei Zeitüberschreitung ein Fehler mit der
// Frist.
func (d *Dienst) abbruch(ctx context.Context, wartung *atomic.Bool, err error) error {
	switch {
	case wartung.Load():
		return errWartung
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("Zeitlimit von %s überschritten", d.o.Zeitlimit)
	}
	return err
}

// nurQuelle trägt einen neuen Stand ein, der keine Region geändert hat.
func (d *Dienst) nurQuelle(welt string, cur *stand, source, storeWorld string, gen *api.Generation, have map[Region]api.FileEntry) error {
	regs := make(map[string]string, len(have))
	for r, f := range have {
		regs[stateKey(r)] = f.SHA256
	}
	st := cur.st
	st.Source, st.StoreWorld, st.Generation, st.Regions, st.Format = source, storeWorld, gen.Number, regs, 1
	st.LastRun = d.o.Jetzt().UTC()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.staende[welt] != cur {
		return nil // inzwischen ersetzt
	}
	if err := writeJSONFile(filepath.Join(cur.dir, stateName), st); err != nil {
		return err
	}
	c := *cur
	c.st = st
	d.staende[welt] = &c
	return nil
}

// tausche ersetzt den Kartenordner der Welt durch staging. Unter der
// Schreibsperre: Leser sehen nie einen Zustand ohne Ordner. Schlägt das
// zweite Umbenennen fehl, kommt der alte Ordner zurück.
func (d *Dienst) tausche(welt, staging string, neu *stand) error {
	live := d.weltDir(welt)
	old := live + ".alt"
	d.mu.Lock()
	os.RemoveAll(old)
	_, statErr := os.Stat(live)
	hadLive := statErr == nil
	if hadLive {
		if err := renameRetry(live, old); err != nil {
			d.mu.Unlock()
			return err
		}
	}
	if err := renameRetry(staging, live); err != nil {
		if hadLive {
			if rerr := os.Rename(old, live); rerr != nil {
				d.log.Error("KARTE: alter Stand nicht zurückgelegt", "welt", welt, "err", rerr)
			}
		}
		d.mu.Unlock()
		return err
	}
	neu.dir = live
	d.staende[welt] = neu
	d.mu.Unlock()
	os.RemoveAll(old)
	return nil
}

// events zählt, was das Programm meldet.
type events struct {
	regions, tiles, changed int
	doneMs                  int64
	done                    bool
}

// starte ruft das Programm auf (mit nice, wenn eingestellt und möglich),
// liest seine Ausgabe (JSON-Zeilen) und wartet auf das Ende. Ein Exit-Code
// ungleich 0 ist ein Fehler mit dem Ende der Fehlerausgabe (ohne Steuerzeichen,
// begrenzt); die Umgebung des Programms ist klein und enthält nichts aus der
// Umgebung des Koordinators.
func (d *Dienst) starte(ctx context.Context, args []string, welt string) error {
	name, full := d.o.Programm, args
	if d.o.Nice > 0 && runtime.GOOS == "linux" {
		if nice, err := exec.LookPath("nice"); err == nil {
			name, full = nice, append([]string{"-n", strconv.Itoa(d.o.Nice), d.o.Programm}, args...)
		} else {
			d.log.Warn("Karte: nice nicht gefunden, das Programm läuft ohne", "err", err)
		}
	}
	cmd := exec.CommandContext(ctx, name, full...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "LANG=C", "RUST_BACKTRACE=0"}, d.o.Umgebung...)
	cmd.Stdin = nil
	cmd.WaitDelay = 5 * time.Second
	stderr := &tailBuffer{max: 1500}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Programm nicht gestartet: %w", err)
	}
	ev := readEvents(out)
	werr := cmd.Wait()
	if werr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var xe *exec.ExitError
		code := -1
		if errors.As(werr, &xe) {
			code = xe.ExitCode()
		}
		return fmt.Errorf("Programm endete mit Exit-Code %d: %s", code, stderr.text())
	}
	if !ev.done {
		d.log.Warn("Karte: Programm meldete kein „done“", "welt", welt)
	}
	d.log.Info("Karte: Programm fertig", "welt", welt, "regionen", ev.regions, "kacheln", ev.tiles, "geändert", ev.changed, "ms", ev.doneMs)
	return nil
}

// readEvents liest die Ausgabe des Programms (eine JSON-Zeile je Ereignis).
// Zu lange und ungültige Zeilen werden übergangen; der Rest wird immer
// gelesen, damit das Programm nie an einer vollen Pipe hängt.
func readEvents(r io.Reader) events {
	var ev events
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// Zu lang: den Rest der Zeile verwerfen.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if err != nil {
				return ev
			}
			continue
		}
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var e struct {
				Ev      string `json:"ev"`
				Regions int    `json:"regions"`
				Tiles   int    `json:"tiles"`
				Changed int    `json:"changed"`
				Ms      int64  `json:"ms"`
			}
			if json.Unmarshal(t, &e) == nil {
				switch e.Ev {
				case "start":
					ev.regions = e.Regions
				case "done":
					ev.tiles, ev.changed, ev.doneMs, ev.done = e.Tiles, e.Changed, e.Ms, true
				}
			}
		}
		if err != nil {
			return ev
		}
	}
}

// tailBuffer behält die letzten max Bytes.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// text ist das Ende der Ausgabe auf einer Zeile: Steuerzeichen werden zu
// Leerzeichen.
func (t *tailBuffer) text() string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return ' '
		}
		return r
	}, string(t.buf))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "(keine Fehlerausgabe)"
	}
	return s
}
