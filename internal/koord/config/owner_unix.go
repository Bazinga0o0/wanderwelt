//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// copyOwner gibt path Besitzer und Gruppe aus fi: Die Konfiguration gehört
// oft root:wanderwelt (0640), und eine neue Datei von root wäre für den
// Koordinator sonst nicht mehr lesbar.
func copyOwner(path string, fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Lchown(path, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("Besitzer übernehmen: %w", err)
	}
	return nil
}
