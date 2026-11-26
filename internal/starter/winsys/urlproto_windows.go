//go:build windows

package winsys

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// RegisterURLProtocol trägt das URL-Protokoll scheme:// für den aktuellen
// Benutzer ein (HKCU\Software\Classes\<scheme>, keine Adminrechte): Ein Link
// scheme://… startet exe mit der Adresse als Argument. Schon passende
// Einträge bleiben unberührt (changed false). So startet die Beitreten-Seite
// den Starter (wanderwelt://beitreten).
func RegisterURLProtocol(scheme, exe, description string) (changed bool, err error) {
	root := `Software\Classes\` + scheme
	cmdKey := root + `\shell\open\command`
	want := `"` + exe + `" "%1"`
	if k, err := registry.OpenKey(registry.CURRENT_USER, cmdKey, registry.QUERY_VALUE); err == nil {
		cur, _, gerr := k.GetStringValue("")
		k.Close()
		if gerr == nil && cur == want {
			return false, nil
		}
	}
	set := func(path, name, value string) error {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		return k.SetStringValue(name, value)
	}
	for _, e := range []struct{ path, name, value string }{
		{root, "", "URL:" + description},
		{root, "URL Protocol", ""},
		{root + `\DefaultIcon`, "", `"` + exe + `",0`},
		{cmdKey, "", want},
	} {
		if err := set(e.path, e.name, e.value); err != nil {
			return false, fmt.Errorf("URL-Protokoll %s: %w", scheme, err)
		}
	}
	return true, nil
}
