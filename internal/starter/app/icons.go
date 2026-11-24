package app

import (
	"bytes"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/symbol"
)

// Symbole: das wanderwelt-Symbol (Paket symbol) im Tray mit einem Punkt in
// der Farbe des Zustands, sonst ohne. Dieselbe Grafik steckt als Ressource
// in der exe (cmd/wanderwelt, erzeugt mit go run ./tools/makeicon).

var phaseColors = map[phase]color.RGBA{
	phReady:     {0x3f, 0xa3, 0x4d, 0xff}, // grün
	phStarting:  {0xe0, 0x9a, 0x1a, 0xff}, // orange
	phHosting:   {0x2f, 0x6f, 0xdb, 0xff}, // blau
	phJoined:    {0x17, 0xa2, 0xb8, 0xff}, // türkis
	phUploading: {0xe0, 0x9a, 0x1a, 0xff}, // orange
	phError:     {0xd6, 0x45, 0x41, 0xff}, // rot
}

var iconCache = map[phase][]byte{}

// phaseIcon ist das Tray-Symbol (ICO, 16 und 32 Pixel) mit Zustandspunkt.
func phaseIcon(p phase) []byte {
	if b, ok := iconCache[p]; ok {
		return b
	}
	c, ok := phaseColors[p]
	if !ok {
		c = phaseColors[phReady]
	}
	b := symbol.BadgeICO(c)
	iconCache[p] = b
	return b
}

// appIconICO ist das Programmsymbol ohne Zustandspunkt (16 bis 256 Pixel).
var appIconICO = sync.OnceValue(symbol.AppICO)

// notifyPNG ist das Bild für Hinweise: das 48er-Symbol vierfach
// vergrößert, damit es bei 100 und 200 % Skalierung scharf bleibt.
// Windows-Hinweise zeigen PNG, aber kein ICO.
func notifyPNG() []byte { return symbol.PNG(symbol.Scaled(48, 4)) }

// Dateinamen der Symbole in Home().
const (
	iconFileICO = "wanderwelt.ico"
	iconFilePNG = "wanderwelt.png"
)

// iconFiles legt die Symbole einmal je Prozess in Home() ab (das ICO für
// die Titelleisten der Dialoge, das PNG für die Hinweise) und gibt die Pfade
// zurück; leer, wenn eine Datei fehlt und sich nicht schreiben ließ.
var iconFiles = sync.OnceValues(func() (ico, png string) {
	dir := group.Home()
	return ensureIconFile(filepath.Join(dir, iconFileICO), appIconICO()),
		ensureIconFile(filepath.Join(dir, iconFilePNG), notifyPNG())
})

// ensureIconFile schreibt data nach path, wenn die Datei fehlt oder
// abweicht (über eine Zwischendatei, damit nie eine halbe Datei liegt).
// Gelingt das nicht, bleibt eine vorhandene Datei in Gebrauch.
func ensureIconFile(path string, data []byte) string {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return path
	}
	if err := writeFileAtomic(path, data, 0o644); err != nil {
		log.Printf("Symbol %s nicht geschrieben: %v", filepath.Base(path), err)
		if _, err := os.Stat(path); err != nil {
			return ""
		}
	}
	return path
}

// windowIcon setzt das wanderwelt-Symbol in die Titelleiste (und auf die
// Taskleiste) eines zenity-Dialogs; ohne Datei bleibt es beim Standard.
func windowIcon() zenity.Option {
	if ico, _ := iconFiles(); ico != "" {
		return zenity.WindowIcon(ico)
	}
	return zenity.WindowIcon(zenity.NoIcon)
}

// notifyIcon ist das Bild für Hinweise; ohne Datei das Info-Symbol.
func notifyIcon() zenity.Option {
	if _, png := iconFiles(); png != "" {
		return zenity.Icon(png)
	}
	return zenity.InfoIcon
}
