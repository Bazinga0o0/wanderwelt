//go:build linux

package server

import "testing"

// Die echte Abfrage unter Linux liefert für data_dir eine Gesamtgröße und
// weniger freie Bytes als insgesamt.
func TestDiskTotalLinux(t *testing.T) {
	dir := t.TempDir()
	total, ok := diskTotal(dir)
	free, ok2 := diskFree(dir)
	if !ok || !ok2 || total <= 0 || free < 0 || free > total {
		t.Fatalf("Gesamt %d (%v), frei %d (%v)", total, ok, free, ok2)
	}
	if _, ok := diskTotal(dir + "/gibt-es-nicht"); ok {
		t.Error("fehlender Ordner liefert eine Größe")
	}
}
