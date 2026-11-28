// Package winsys bindet den Starter an Windows an: nur eine Instanz pro
// Datenverzeichnis, PC wach halten, Stromversorgung (Netz oder Akku),
// Herunterfahren abfangen, Autostart, Desktop-Verknüpfung, Konsole für
// Unterbefehle, Hilfen für das Fenster (dunkles Design, nach vorn holen).
//
// Außerhalb von Windows (Entwicklung, Tests) tun die Funktionen nichts oder
// melden ErrUnsupported.
package winsys

import (
	"errors"
	"time"
)

// ErrUnsupported: Die Funktion gibt es nur unter Windows.
var ErrUnsupported = errors.New("nur unter Windows")

// BlockReason zeigt Windows beim Herunterfahren (Sperrbildschirm „wanderwelt
// verhindert das Herunterfahren“), solange der Starter den Schluss-Stand
// hochlädt.
const BlockReason = "wanderwelt stoppt den Server und lädt die Welt hoch (dauert bis zu 90 Sekunden) …"

// FinishTimeout: So lange hält der Starter das Herunterfahren höchstens auf.
const FinishTimeout = 90 * time.Second

// RunValueName ist der Name des Autostart-Eintrags unter
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run.
const RunValueName = "wanderwelt"

// ShutdownHooks verbinden das Ende der Windows-Sitzung mit dem Starter.
type ShutdownHooks struct {
	// Hosting meldet, ob dieser PC gerade hostet. Nur dann wird das
	// Herunterfahren aufgehalten. Muss sofort antworten.
	Hosting func() bool
	// Finish beendet das Hosten sauber (Server stoppen, Schluss-Stand
	// hochladen, Zuschlag zurückgeben) und kehrt danach zurück. Nach
	// FinishTimeout gibt winsys das Herunterfahren trotzdem frei.
	Finish func()
	// Ended (darf nil sein) bekommt WM_ENDSESSION: ending true heißt, die
	// Sitzung endet wirklich (der Prozess kann danach jederzeit enden);
	// false heißt, das Herunterfahren wurde abgebrochen. Muss sofort
	// zurückkehren.
	Ended func(ending bool)
}
