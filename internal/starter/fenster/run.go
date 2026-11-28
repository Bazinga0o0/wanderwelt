//go:build windows

package fenster

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

const (
	// fensterTitel: Titel des Fensters (FocusWindow sucht danach, zusammen
	// mit der Fensterklasse).
	fensterTitel = "wanderwelt"
	// fokusWarten: So lange sucht ein zweites Öffnen das erste Fenster (es
	// kann gerade erst entstehen).
	fokusWarten = 3 * time.Second
)

// fensterKlasse und fensterSperre gelten je Home() (wie die Sperre der
// Instanz): ein Fenster pro Datenverzeichnis.
func fensterKlasse() string { return "wanderwelt-fenster-" + group.HomeKey() }
func fensterSperre() string { return `Local\wanderwelt-fenster-` + group.HomeKey() }

// Run öffnet das Fenster und kehrt zurück, wenn es geschlossen ist. Ist
// schon ein Fenster offen, holt Run es nach vorn und kehrt sofort zurück.
// exe ist die wanderwelt.exe, die gestartet wird, wenn keine Instanz läuft;
// version ist die Version dieses Programms (Abgleich mit der Instanz).
func Run(ctx context.Context, exe, version string) error {
	release, ok, err := winsys.SingleInstance(fensterSperre())
	if err != nil {
		return err
	}
	if !ok {
		// Das vorhandene Fenster nach vorn holen; hängt dabei etwas, endet
		// dieser Prozess trotzdem nach der Frist.
		fertig := make(chan struct{})
		go func() {
			defer close(fertig)
			for ende := time.Now().Add(fokusWarten); !winsys.FocusWindow(fensterKlasse(), fensterTitel) && time.Now().Before(ende); {
				time.Sleep(100 * time.Millisecond)
			}
		}()
		select {
		case <-fertig:
		case <-time.After(fokusWarten + time.Second):
		}
		return nil
	}
	defer release()
	// Leicht bleiben: Der Heap wächst nur um die Hälfte über das Lebende
	// hinaus (statt um das Doppelte); das Fenster erzeugt wenig Müll.
	debug.SetGCPercent(50)

	th, err := neuesThema(winsys.DarkMode())
	if err != nil {
		return err
	}
	// Fensterklasse statt des exe-Namens: Daran erkennt FocusWindow das
	// Fenster. Gio liest app.ID erst beim Anlegen des ersten Fensters.
	app.ID = fensterKlasse()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := new(app.Window)
	w.Option(
		app.Title(fensterTitel),
		app.Size(unit.Dp(900), unit.Dp(620)),
		app.MinSize(unit.Dp(720), unit.Dp(520)),
	)
	var hwnd atomic.Uintptr
	d := neueDaten(func() error { return starteInstanz(exe) }, w.Invalidate)
	d.autoStart = true
	d.eigeneVersion = version
	d.installiertIst = func(v string) bool { return installierteVersion(installierteExe()) == v }
	d.neustart = func() error {
		// Erst die Sperre freigeben, dann das neue Fenster starten: Es
		// findet die Sperre frei und übernimmt.
		release()
		if err := starteFenster(installierteExe()); err != nil {
			return err
		}
		w.Perform(system.ActionClose)
		return nil
	}
	d.minimiert = func() bool { return winsys.Minimized(hwnd.Load()) }
	u := neueOberflaeche(th, d)
	u.karte.wurzel = ctx
	defer u.beenden() // Karte: Abrufe abbrechen, Goroutinen abwarten
	go d.lauf(ctx)

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.layout(gtx)
			e.Frame(gtx.Ops)
			d.gezeichnet()
		default:
			if h, ok := fensterHandle(e); ok && h != 0 {
				hwnd.Store(h)
				if th.dunkel {
					winsys.DarkTitleBar(h, true)
				}
			}
		}
	}
}

// starteInstanz startet exe ohne Argumente, losgelöst vom Fenster: Es
// darf enden, ohne die Instanz mitzunehmen.
func starteInstanz(exe string) error {
	cmd := exec.Command(exe)
	cmd.Dir = group.Home()
	cmd.SysProcAttr = losgeloest()
	if err := cmd.Start(); err != nil {
		// Home() fehlt vor der Einrichtung noch.
		cmd = exec.Command(exe)
		cmd.SysProcAttr = losgeloest()
		if err2 := cmd.Start(); err2 != nil {
			return fmt.Errorf("%s: %w", exe, err)
		}
	}
	return cmd.Process.Release()
}

// fensterHandle liefert das HWND aus Gios Win32ViewEvent.
func fensterHandle(e event.Event) (uintptr, bool) {
	if v, ok := e.(app.Win32ViewEvent); ok {
		return v.HWND, true
	}
	return 0, false
}

// losgeloest: eigene Prozessgruppe, keine Konsole (auch nicht die eines
// Aufrufers aus cmd.exe).
func losgeloest() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
}

// installierteExe: die installierte Kopie, Home()/wanderwelt.exe.
func installierteExe() string { return filepath.Join(group.Home(), "wanderwelt.exe") }

// installierteVersion fragt die exe nach ihrer Version („wanderwelt X“);
// leer, wenn sie fehlt oder nicht antwortet.
func installierteVersion(exe string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	var out bytes.Buffer
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return ""
	}
	f := strings.Fields(out.String())
	if len(f) < 2 || f[0] != "wanderwelt" {
		return ""
	}
	return f[1]
}

// starteFenster startet exe mit dem Argument „fenster“, losgelöst.
func starteFenster(exe string) error {
	cmd := exec.Command(exe, "fenster")
	cmd.Dir = group.Home()
	cmd.SysProcAttr = losgeloest()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", exe, err)
	}
	return cmd.Process.Release()
}
