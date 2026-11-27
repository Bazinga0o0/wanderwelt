package mcserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// chatConfigPath ist die Konfig-Datei der Server-Mod chatverlauf (siehe
// mods/chatverlauf, Config.java), relativ zum Server-Ordner.
const chatConfigPath = "config/wanderwelt-chatverlauf.json"

const mib = 1 << 20

// applyChatConfig schreibt die Chat-Einstellung der Welt (an/aus, Größengrenze)
// in die Konfig-Datei der Mod: genau die Schlüssel "aktiv" und "max_bytes".
// Was ein Admin sonst in der Datei gesetzt hat (wiedergabe_zeilen, zeit,
// segment_bytes, …), bleibt erhalten; fehlt die Datei oder ist sie kein
// gültiges JSON-Objekt, entsteht sie neu mit nur diesen beiden Schlüsseln.
// cs == nil (älterer Koordinator): nichts wird angefasst. Der Verlauf selbst
// (world/wanderwelt-chatverlauf/) wird nie gelöscht, auch nicht bei „aus“.
func applyChatConfig(dir string, cs *api.ChatSettings) error {
	if cs == nil {
		return nil
	}
	mb := cs.MiB
	if mb < api.ChatMiBMin || mb > api.ChatMiBMax {
		mb = api.ChatMiBStandard
	}
	target := filepath.Join(dir, filepath.FromSlash(chatConfigPath))

	obj := map[string]json.RawMessage{}
	old, err := readLimited(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		old = nil
	case err != nil:
		return fmt.Errorf("%s: %w", chatConfigPath, err)
	default:
		if json.Unmarshal(old, &obj) != nil || obj == nil {
			log.Printf("mcserver: %s ist kein gültiges JSON-Objekt, wird mit der Chat-Einstellung neu geschrieben", chatConfigPath)
			obj = map[string]json.RawMessage{}
		}
	}
	obj["aktiv"] = json.RawMessage(fmt.Sprintf("%t", !cs.Off))
	obj["max_bytes"] = json.RawMessage(fmt.Sprintf("%d", int64(mb)*mib))
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if bytes.Equal(old, data) {
		return nil
	}

	if fi, err := os.Lstat(target); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: Ziel ist keine normale Datei (Ordner oder Link)", chatConfigPath)
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if err := insideDir(dir, parent); err != nil {
		return err
	}
	if err := writeFileAtomic(target, data, 0o644); err != nil {
		return err
	}
	log.Printf("mcserver: Chat-Verlauf %s (Grenze %d MiB) in %s eingetragen", chatText(cs.Off), mb, chatConfigPath)
	return nil
}

func chatText(off bool) string {
	if off {
		return "aus"
	}
	return "an"
}
