//go:build !linux

package server

// diskTotal ist außerhalb von Linux unbekannt (0, false), wie diskFree; der
// Koordinator läuft auf Linux, unter Windows nur in Tests und lokal.
func diskTotal(string) (int64, bool) { return 0, false }
