//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// configOwnerUID ist der Besitzer, den die Konfiguration haben muss: root
// (Tests setzen den eigenen Benutzer).
var configOwnerUID = 0

// checkConfigFile prüft die Konfiguration, bevor „mitglied“ ihr folgt: Es
// läuft als root und führt reload_command (als root) und tunneld_bin aus
// ihr aus. Gehört sie nicht root oder dürfen Gruppe oder andere sie
// schreiben, könnte so jemand anderes Befehle als root unterschieben – dann
// Abbruch vor jeder Änderung. Lesen darf die Gruppe (root:wanderwelt 0640).
func checkConfigFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: Besitzer nicht lesbar", path)
	}
	const why = "„mitglied“ führt reload_command und tunneld_bin aus dieser Datei als root aus"
	if int(st.Uid) != configOwnerUID {
		return fmt.Errorf("%s gehört nicht root (Besitzer uid %d): %s. Erst „chown root:wanderwelt %s“ und "+
			"„chmod 0640 %s“; nichts geändert", path, st.Uid, why, path, path)
	}
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%s ist für Gruppe oder andere beschreibbar (%04o): %s. Erst „chmod go-w %s“; "+
			"nichts geändert", path, perm, why, path)
	}
	return nil
}
