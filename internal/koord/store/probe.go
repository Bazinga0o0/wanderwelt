package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// PruneReport sagt, was ein Aufräumlauf löschen würde.
type PruneReport struct {
	Generations int   // Generationsdateien (Stände)
	Blobs       int   // Blobs, die keine behaltene Generation mehr braucht
	Bytes       int64 // deren Größe
	Junk        int   // liegengebliebene Temp-Dateien neben den Generationen
}

// Empty meldet, dass nichts zu löschen wäre.
func (r PruneReport) Empty() bool { return r == PruneReport{} }

// PruneProbe rechnet den Aufräumlauf wie Prune durch und meldet, was er
// löschen würde – ohne etwas zu löschen oder zu verändern (auch Belegung
// und Angaben für Worlds bleiben, wie sie sind). Gedacht für den ersten
// Start nach einem Update: Neue Aufbewahrungsregeln sollen nie unbemerkt
// Stände wegnehmen. Ein nicht lesbares Rezept ist wie bei Prune ein Fehler.
func (s *Store) PruneProbe(now time.Time) (PruneReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	plan, err := s.planPrune(now)
	if err != nil {
		return PruneReport{}, err
	}
	var rep PruneReport
	for _, gens := range plan.worlds {
		for _, g := range gens {
			if !plan.keep[g] {
				rep.Generations++
			}
		}
	}
	rep.Junk = len(plan.junk)

	blobRoot := filepath.Join(s.dir, "blobs")
	prefixes, err := os.ReadDir(blobRoot)
	if err != nil {
		return rep, err
	}
	var errs []error
	for _, p := range prefixes {
		if !p.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(blobRoot, p.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			sha := e.Name()
			if e.IsDir() || !validSHA(sha) || sha[:2] != p.Name() {
				continue
			}
			if _, ok := s.fresh[sha]; ok || plan.refs.has(sha) {
				continue
			}
			rep.Blobs++
			if info, err := e.Info(); err == nil {
				rep.Bytes += info.Size()
			} else if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return rep, errors.Join(errs...)
}

// OpenUploads liefert die Zahl der Blobs, deren Teile gerade geschrieben
// werden.
func (s *Store) OpenUploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploading)
}
