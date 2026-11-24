//go:build !linux

package server

// diskFree ist außerhalb von Linux unbekannt (0, false); der Koordinator
// läuft auf Linux, unter Windows nur in Tests und lokal.
func diskFree(string) (int64, bool) { return 0, false }
