package worldsync

import "time"

// SetChtimes ersetzt in Tests die Funktion, mit der Stage die
// Änderungszeit der bereitgestellten Datei setzt; restore stellt die
// vorige wieder her.
func SetChtimes(f func(name string, atime, mtime time.Time) error) (restore func()) {
	old := chtimes
	chtimes = f
	return func() { chtimes = old }
}

// MaxSegment liefert die Grenze, ab der ein Segment nicht gebündelt wird.
func MaxSegment() int64 { return maxSegment }

// SetMaxSegment setzt in Tests die Größe, ab der ein Segment nicht mehr
// gebündelt übertragen wird; restore stellt die vorige wieder her.
func SetMaxSegment(n int64) (restore func()) {
	old := maxSegment
	maxSegment = n
	return func() { maxSegment = old }
}
