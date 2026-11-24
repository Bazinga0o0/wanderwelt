// Command makeicon schreibt das wanderwelt-Symbol (internal/starter/symbol)
// als cmd/wanderwelt/wanderwelt.ico und baut daraus
// cmd/wanderwelt/rsrc_windows_amd64.syso: die Symbol-Ressource, die der
// Go-Linker in die exe übernimmt (Explorer, Verknüpfung, Fenster). Beide
// Dateien sind eingecheckt; nach Änderungen am Symbol neu erzeugen:
//
//	go run ./tools/makeicon
//
// (oder go generate ./cmd/wanderwelt). Ohne Werkzeuge außerhalb von Go.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tc-hib/winres"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/symbol"
)

const (
	icoName  = "wanderwelt.ico"
	sysoName = "rsrc_windows_amd64.syso"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "makeicon:", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	ico, syso, err := build()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "cmd", "wanderwelt")
	for name, data := range map[string][]byte{icoName: ico, sysoName: syso} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("%s (%d Bytes)\n", path, len(data))
	}
	return nil
}

// build liefert das Programmsymbol als ICO und als COFF-Objekt mit der
// Symbolgruppe 1 – die erste und einzige, also nimmt Windows sie für die exe.
func build() (ico, syso []byte, err error) {
	ico = symbol.AppICO()
	icon, err := winres.LoadICO(bytes.NewReader(ico))
	if err != nil {
		return nil, nil, err
	}
	var rs winres.ResourceSet
	if err := rs.SetIcon(winres.ID(1), icon); err != nil {
		return nil, nil, err
	}
	var b bytes.Buffer
	if err := rs.WriteObject(&b, winres.ArchAMD64); err != nil {
		return nil, nil, err
	}
	return ico, b.Bytes(), nil
}

// moduleRoot sucht von hier aufwärts die go.mod (go run aus dem Modul oder
// go generate aus cmd/wanderwelt).
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod nicht gefunden – im Modul aufrufen")
		}
		dir = parent
	}
}
