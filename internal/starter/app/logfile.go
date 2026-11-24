package app

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// maxLogSize: Ab dieser Größe wird wanderwelt.log zu wanderwelt.log.1
// (die vorige .1 fällt weg).
const maxLogSize = 10 << 20

// LogPath ist das Log des Starters: Home()/logs/wanderwelt.log.
func LogPath() string { return filepath.Join(group.Home(), "logs", "wanderwelt.log") }

// rotatingLog schreibt an eine Datei und rollt sie bei maxLogSize einmal
// weiter. Schreibfehler werden verschluckt: Ein Log darf den Starter nie
// aufhalten.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotatingLog(path string, max int64) (*rotatingLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	l := &rotatingLog{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, info.Size()
	return nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil && l.size+int64(len(p)) > l.max && l.size > 0 {
		l.f.Close()
		l.f = nil
		old := l.path + ".1"
		os.Remove(old)
		os.Rename(l.path, old)
		if err := l.open(); err != nil {
			return len(p), nil
		}
	}
	if l.f == nil {
		if err := l.open(); err != nil {
			return len(p), nil
		}
	}
	n, _ := l.f.Write(p)
	l.size += int64(n)
	return len(p), nil
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// setupLog leitet das Standard-Log in Home()/logs/wanderwelt.log um; ist
// echo gesetzt, auch nach stderr.
func setupLog(echo bool) (io.Closer, error) {
	rl, err := openRotatingLog(LogPath(), maxLogSize)
	if err != nil {
		return nil, err
	}
	var w io.Writer = rl
	if echo {
		w = io.MultiWriter(rl, os.Stderr)
	}
	log.SetOutput(w)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return rl, nil
}
