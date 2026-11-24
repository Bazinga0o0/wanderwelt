package mcserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	logName    = "wanderwelt-server.log"
	oldLogName = "wanderwelt-server.old.log"
	// logMaxBytes: darüber wird die Datei zu oldLogName (eine alte bleibt).
	logMaxBytes = 20 << 20
	// tailBytes: so viel der letzten Ausgabe bleibt für Fehlermeldungen.
	tailBytes = 8 << 10
)

// logFile nimmt stdout und stderr des Servers auf: angehängt an
// <Dir>/logs/wanderwelt-server.log, über 20 MB umbenannt in
// wanderwelt-server.old.log (die vorige alte wird ersetzt).
//
// Write meldet nie einen Fehler: Sonst hörte exec auf, die Pipe zu leeren,
// und der Server bliebe beim Schreiben hängen. Was nicht in die Datei geht,
// wird verworfen.
type logFile struct {
	mu   sync.Mutex
	path string
	old  string
	f    *os.File
	size int64
	tail []byte
}

func openLog(dir string) (*logFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &logFile{path: filepath.Join(dir, logName), old: filepath.Join(dir, oldLogName)}
	if err := l.open(false); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *logFile) open(truncate bool) error {
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if truncate {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(l.path, flag, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keepTail(p)
	if l.f != nil && l.size > 0 && l.size+int64(len(p)) > logMaxBytes {
		l.rotate()
	}
	if l.f != nil {
		n, _ := l.f.Write(p)
		l.size += int64(n)
	}
	return len(p), nil
}

// rotate macht aus der vollen Datei die alte und beginnt eine neue.
func (l *logFile) rotate() {
	l.f.Close()
	l.f = nil
	os.Remove(l.old)
	if err := os.Rename(l.path, l.old); err != nil {
		// Umbenennen ging nicht (Datei woanders offen): neu beginnen, damit
		// die Größe begrenzt bleibt.
		_ = l.open(true)
		return
	}
	_ = l.open(false)
}

func (l *logFile) keepTail(p []byte) {
	if len(p) >= tailBytes {
		l.tail = append(l.tail[:0], p[len(p)-tailBytes:]...)
		return
	}
	if over := len(l.tail) + len(p) - tailBytes; over > 0 {
		l.tail = append(l.tail[:0], l.tail[over:]...)
	}
	l.tail = append(l.tail, p...)
}

// mark schreibt eine eigene Zeile mit Zeitstempel ins Log.
func (l *logFile) mark(msg string) {
	line := fmt.Sprintf("---- wanderwelt %s: %s ----\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	l.Write([]byte(line))
}

// lastLines liefert die letzten n Zeilen der Ausgabe für Fehlermeldungen
// (leer, wenn es keine gibt), jede Zeile gekürzt.
func (l *logFile) lastLines(n int) string {
	l.mu.Lock()
	t := string(l.tail)
	l.mu.Unlock()
	var lines []string
	for _, s := range strings.Split(t, "\n") {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "---- wanderwelt ") {
			continue
		}
		if len(s) > 300 {
			s = s[:300] + " …"
		}
		lines = append(lines, s)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return ""
	}
	return "; letzte Ausgabe:\n  " + strings.Join(lines, "\n  ")
}

func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
