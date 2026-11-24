//go:build linux

package server

import "syscall"

// diskFree liefert die für Nicht-root freien Bytes auf dem Dateisystem von
// dir; ok ist false, wenn sich das nicht ermitteln lässt.
func diskFree(dir string) (free int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
