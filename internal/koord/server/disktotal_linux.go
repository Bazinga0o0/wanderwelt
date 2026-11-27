//go:build linux

package server

import "syscall"

// diskTotal liefert die Gesamtgröße des Dateisystems von dir in Bytes; ok ist
// false, wenn sich das nicht ermitteln lässt.
func diskTotal(dir string) (total int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Blocks) * int64(st.Bsize), true
}
