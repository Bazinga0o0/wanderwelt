//go:build windows

package winsys

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procSetThreadExecutionState    = kernel32.NewProc("SetThreadExecutionState")
	procGetSystemPowerStatus       = kernel32.NewProc("GetSystemPowerStatus")
	procAttachConsole              = kernel32.NewProc("AttachConsole")
	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procSendMessageW               = user32.NewProc("SendMessageW")
	procRegisterWindowMessageW     = user32.NewProc("RegisterWindowMessageW")
	procShutdownBlockReasonCreate  = user32.NewProc("ShutdownBlockReasonCreate")
	procShutdownBlockReasonDestroy = user32.NewProc("ShutdownBlockReasonDestroy")
	procShutdownBlockReasonQuery   = user32.NewProc("ShutdownBlockReasonQuery")
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001

	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmApp             = 0x8000
	// wmFinished: Finish ist fertig (oder die Frist ist um), der Grund für
	// das Aufhalten kann weg. Kommt aus einer Goroutine; ShutdownBlockReason…
	// darf nur der Thread des Fensters aufrufen.
	wmFinished = wmApp + 1

	wsPopup        = 0x80000000
	wsExToolWindow = 0x00000080

	sessionClass = "wanderwelt-sitzung"
	systrayClass = "SystrayClass" // Fensterklasse von fyne.io/systray
)

// ---- Eine Instanz ----

// SingleInstance belegt das benannte Mutex name (etwa
// `Local\wanderwelt-<hash>`). ok false: Ein anderer Prozess hält es schon.
// Das Mutex verschwindet mit dem letzten Handle, also spätestens mit dem
// Ende des Prozesses (auch bei einem Absturz).
func SingleInstance(name string) (release func(), ok bool, err error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("Mutex %s: %w", name, err)
	}
	var once sync.Once
	return func() { once.Do(func() { windows.CloseHandle(h) }) }, true, nil
}

// ---- Wach halten ----

var awake struct {
	once sync.Once
	ch   chan bool
}

// KeepAwake hält den PC wach (on) bzw. erlaubt wieder den Energiesparmodus.
// SetThreadExecutionState gilt pro Thread; alle Aufrufe laufen deshalb über
// einen eigenen, festgehaltenen Thread.
func KeepAwake(on bool) {
	awake.once.Do(func() {
		awake.ch = make(chan bool, 16)
		go func() {
			runtime.LockOSThread()
			for v := range awake.ch {
				flags := uintptr(esContinuous)
				if v {
					flags |= esSystemRequired
				}
				if r, _, err := procSetThreadExecutionState.Call(flags); r == 0 {
					log.Printf("winsys: SetThreadExecutionState(%v): %v", v, err)
				}
			}
		}()
	})
	awake.ch <- on
}

// ---- Stromversorgung ----

// systemPowerStatus ist SYSTEM_POWER_STATUS.
type systemPowerStatus struct {
	ACLineStatus        byte // 0 Akku, 1 Netz, 255 unbekannt
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var powerErrOnce sync.Once

// OnACPower meldet, ob der PC am Netz hängt (GetSystemPowerStatus). Nur
// „läuft auf Akku“ zählt als nicht am Netz; ein unbekannter Status (etwa ein
// Desktop-PC ohne Akku-Angaben) oder eine gescheiterte Abfrage zählen als am
// Netz (der Fehler steht einmal im Log). Antwortet sofort.
func OnACPower() bool {
	var st systemPowerStatus
	if r, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		powerErrOnce.Do(func() { log.Printf("winsys: GetSystemPowerStatus: %v – gilt als am Netz", err) })
		return true
	}
	return st.ACLineStatus != 0
}

// ---- Herunterfahren abfangen ----

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

var session struct {
	mu    sync.Mutex
	hwnd  uintptr
	hooks ShutdownHooks
	// blocking gehört dem Fenster-Thread: Das Herunterfahren wird gerade
	// aufgehalten.
	blocking bool
}

// OnShutdown legt ein verstecktes Fenster an (eigener, festgehaltener
// Thread mit Nachrichtenschleife), das die Nachrichten zum Ende der
// Windows-Sitzung bekommt:
//
//   - WM_QUERYENDSESSION: Hostet der PC (hooks.Hosting), setzt es
//     ShutdownBlockReasonCreate(BlockReason), antwortet FALSE und ruft
//     hooks.Finish in einer Goroutine auf. Ist Finish fertig oder
//     FinishTimeout um, nimmt es den Grund wieder weg
//     (ShutdownBlockReasonDestroy). Hostet er nicht, antwortet es TRUE.
//   - WM_ENDSESSION: geht an hooks.Ended.
//
// Das Fenster ist ein verstecktes Fenster oberster Ebene und kein
// Nur-Nachrichten-Fenster (HWND_MESSAGE): Nur Fenster oberster Ebene
// bekommen WM_QUERYENDSESSION.
func OnShutdown(h ShutdownHooks) error {
	if h.Hosting == nil || h.Finish == nil {
		return errors.New("winsys: OnShutdown ohne Hosting oder Finish")
	}
	session.mu.Lock()
	if session.hwnd != 0 {
		session.hooks = h
		session.mu.Unlock()
		return nil
	}
	session.hooks = h
	session.mu.Unlock()

	ready := make(chan error, 1)
	go sessionThread(ready)
	return <-ready
}

func sessionThread(ready chan<- error) {
	runtime.LockOSThread() // nie wieder frei: Fenster und Schleife gehören diesem Thread
	cls, _ := windows.UTF16PtrFromString(sessionClass)
	title, _ := windows.UTF16PtrFromString("wanderwelt")
	var inst windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &inst); err != nil {
		ready <- fmt.Errorf("winsys: GetModuleHandleEx: %w", err)
		return
	}
	wc := wndClassEx{
		WndProc:   windows.NewCallback(sessionWndProc),
		Instance:  inst,
		ClassName: cls,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		ready <- fmt.Errorf("winsys: RegisterClassExW: %w", err)
		return
	}
	hwnd, _, err := procCreateWindowExW.Call(
		wsExToolWindow,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		0, 0, 0, 0,
		0, 0, uintptr(inst), 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("winsys: CreateWindowExW: %w", err)
		return
	}
	session.mu.Lock()
	session.hwnd = hwnd
	session.mu.Unlock()
	ready <- nil

	var m winMsg
	for {
		r, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(r) {
		case -1:
			log.Printf("winsys: GetMessageW: %v", err)
			return
		case 0:
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func sessionWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmQueryEndSession:
		session.mu.Lock()
		hooks := session.hooks
		session.mu.Unlock()
		if session.blocking {
			return 0
		}
		if !hooks.Hosting() {
			log.Printf("winsys: Windows-Sitzung soll enden (lParam %#x), es wird nicht gehostet", lparam)
			return 1
		}
		log.Printf("winsys: Windows-Sitzung soll enden (lParam %#x), Herunterfahren wird aufgehalten, bis der Schluss-Stand hochgeladen ist", lparam)
		reason, _ := windows.UTF16PtrFromString(BlockReason)
		if r, _, err := procShutdownBlockReasonCreate.Call(hwnd, uintptr(unsafe.Pointer(reason))); r == 0 {
			log.Printf("winsys: ShutdownBlockReasonCreate: %v", err)
		}
		session.blocking = true
		go func() {
			done := make(chan struct{})
			go func() {
				defer close(done)
				hooks.Finish()
			}()
			select {
			case <-done:
				log.Printf("winsys: Hosten beendet, Herunterfahren wird freigegeben")
			case <-time.After(FinishTimeout):
				log.Printf("winsys: Hosten nach %v nicht beendet, Herunterfahren wird trotzdem freigegeben", FinishTimeout)
			}
			procPostMessageW.Call(hwnd, wmFinished, 0, 0)
		}()
		return 0
	case wmFinished:
		if r, _, err := procShutdownBlockReasonDestroy.Call(hwnd); r == 0 {
			log.Printf("winsys: ShutdownBlockReasonDestroy: %v", err)
		}
		session.blocking = false
		return 0
	case wmEndSession:
		session.mu.Lock()
		ended := session.hooks.Ended
		session.mu.Unlock()
		log.Printf("winsys: WM_ENDSESSION (endet: %v, lParam %#x)", wparam != 0, lparam)
		if ended != nil {
			ended(wparam != 0)
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// SimulateQueryEndSession schickt dem eigenen Fenster ein synthetisches
// WM_QUERYENDSESSION (Prüfhilfe) und liefert die Antwort (true: Windows
// darf herunterfahren).
func SimulateQueryEndSession() (allow bool, err error) {
	hwnd := sessionHWND()
	if hwnd == 0 {
		return false, errors.New("winsys: kein Sitzungsfenster (OnShutdown)")
	}
	r, _, _ := procSendMessageW.Call(hwnd, wmQueryEndSession, 0, 0)
	return r != 0, nil
}

// SimulateEndSession schickt dem eigenen Fenster ein synthetisches
// WM_ENDSESSION (Prüfhilfe).
func SimulateEndSession(ending bool) error {
	hwnd := sessionHWND()
	if hwnd == 0 {
		return errors.New("winsys: kein Sitzungsfenster (OnShutdown)")
	}
	w := uintptr(0)
	if ending {
		w = 1
	}
	procSendMessageW.Call(hwnd, wmEndSession, w, 0)
	return nil
}

// ActiveBlockReason liefert den Grund, mit dem das Herunterfahren gerade
// aufgehalten wird (ShutdownBlockReasonQuery), oder ok false.
func ActiveBlockReason() (reason string, ok bool) {
	hwnd := sessionHWND()
	if hwnd == 0 {
		return "", false
	}
	var n uint32
	if r, _, _ := procShutdownBlockReasonQuery.Call(hwnd, 0, uintptr(unsafe.Pointer(&n))); r == 0 || n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	if r, _, _ := procShutdownBlockReasonQuery.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

func sessionHWND() uintptr {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.hwnd
}

// ---- Tray-Symbol wiederherstellen ----

var trayEnum struct {
	once sync.Once
	cb   uintptr
	mu   sync.Mutex
	pid  uint32
	hits []windows.HWND
}

// RestoreTrayIcon schickt dem Fenster von fyne.io/systray in diesem Prozess
// die Nachricht "TaskbarCreated", auf die es sein Symbol neu anlegt.
//
// Nötig, weil systray bei jedem WM_ENDSESSION – auch wenn das
// Herunterfahren abgebrochen wurde – sein Symbol entfernt.
func RestoreTrayIcon() error {
	trayEnum.once.Do(func() {
		trayEnum.cb = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
			var pid uint32
			windows.GetWindowThreadProcessId(hwnd, &pid)
			if pid != trayEnum.pid {
				return 1
			}
			buf := make([]uint16, 64)
			n, _ := windows.GetClassName(hwnd, &buf[0], int32(len(buf)))
			if n > 0 && windows.UTF16ToString(buf[:n]) == systrayClass {
				trayEnum.hits = append(trayEnum.hits, hwnd)
			}
			return 1
		})
	})
	trayEnum.mu.Lock()
	defer trayEnum.mu.Unlock()
	trayEnum.pid = windows.GetCurrentProcessId()
	trayEnum.hits = nil
	if err := windows.EnumWindows(trayEnum.cb, nil); err != nil {
		return fmt.Errorf("winsys: EnumWindows: %w", err)
	}
	if len(trayEnum.hits) == 0 {
		return errors.New("winsys: kein Tray-Fenster gefunden")
	}
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	m, _, err := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	if m == 0 {
		return fmt.Errorf("winsys: RegisterWindowMessage: %w", err)
	}
	for _, h := range trayEnum.hits {
		procPostMessageW.Call(uintptr(h), m, 0, 0)
	}
	return nil
}

// ---- Konsole ----

// AttachParentConsole verbindet Ausgaben mit der Konsole des aufrufenden
// Programms, wenn das Programm ohne eigene Konsole gebaut ist
// (-H=windowsgui) und keine Umleitung besteht. Ergebnis: ob es jetzt eine
// Ausgabe gibt.
func AttachParentConsole() bool {
	if validStd(windows.STD_OUTPUT_HANDLE) && validStd(windows.STD_ERROR_HANDLE) {
		return true
	}
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return validStd(windows.STD_OUTPUT_HANDLE)
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		if !validStd(windows.STD_OUTPUT_HANDLE) {
			os.Stdout = f
		}
		if !validStd(windows.STD_ERROR_HANDLE) {
			os.Stderr = f
		}
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil && !validStd(windows.STD_INPUT_HANDLE) {
		os.Stdin = f
	}
	return true
}

func validStd(which uint32) bool {
	h, err := windows.GetStdHandle(which)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return false
	}
	t, err := windows.GetFileType(h)
	return err == nil && t != windows.FILE_TYPE_UNKNOWN
}

// ---- Autostart ----

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart trägt exe (ohne Argumente) unter HKCU\…\Run\wanderwelt ein.
func SetAutostart(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("Autostart: %w", err)
	}
	defer k.Close()
	if err := k.SetStringValue(RunValueName, `"`+exe+`"`); err != nil {
		return fmt.Errorf("Autostart: %w", err)
	}
	return nil
}

// Autostart liefert den Eintrag HKCU\…\Run\wanderwelt ("" wenn keiner).
func Autostart() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(RunValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}

// RemoveAutostart löscht HKCU\…\Run\wanderwelt (fehlt er, ist das kein
// Fehler).
func RemoveAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(RunValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// ---- Verknüpfung, Editor, Browser ----

// FreeSpace liefert die freien Bytes (für den Benutzer) am Ort path.
func FreeSpace(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, err
	}
	return int64(free), nil
}

// DocumentsDir liefert den Dokumente-Ordner des Benutzers (Known Folder, kann
// umgeleitet sein, etwa nach OneDrive).
func DocumentsDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
}

// DesktopDir liefert den Desktop-Ordner des Benutzers.
func DesktopDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
}

// CreateShortcut legt die Verknüpfung lnk an (oder ersetzt sie): Ziel
// target mit args, Arbeitsordner workDir. Über WScript.Shell, ohne
// Administratorrechte.
func CreateShortcut(lnk, target, args, workDir, description string) error {
	// Eigener Thread: Der aufrufende (etwa der Haupt-Thread mit den
	// Dialogen) kann COM schon in einem anderen Modus initialisiert haben.
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		done <- createShortcut(lnk, target, args, workDir, description)
	}()
	return <-done
}

func createShortcut(lnk, target, args, workDir, description string) error {
	const (
		sFalse        = 0x00000001
		rpcChangeMode = 0x80010106 // RPC_E_CHANGED_MODE: COM ist hier schon anders initialisiert
	)
	switch err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); {
	case err == nil:
		defer ole.CoUninitialize()
	default:
		oe, ok := err.(*ole.OleError)
		switch {
		case ok && oe.Code() == sFalse:
			defer ole.CoUninitialize()
		case ok && uint32(oe.Code()) == rpcChangeMode:
			// COM ist benutzbar; nicht wieder abmelden.
		default:
			return fmt.Errorf("Verknüpfung: CoInitializeEx: %w", err)
		}
	}

	unk, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("Verknüpfung: WScript.Shell: %w", err)
	}
	defer unk.Release()
	shell, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("Verknüpfung: %w", err)
	}
	defer shell.Release()
	v, err := oleutil.CallMethod(shell, "CreateShortcut", lnk)
	if err != nil {
		return fmt.Errorf("Verknüpfung: CreateShortcut: %w", err)
	}
	sc := v.ToIDispatch()
	defer sc.Release()
	for _, p := range []struct{ name, val string }{
		{"TargetPath", target},
		{"Arguments", args},
		{"WorkingDirectory", workDir},
		{"Description", description},
		{"IconLocation", target + ",0"},
	} {
		if _, err := oleutil.PutProperty(sc, p.name, p.val); err != nil {
			return fmt.Errorf("Verknüpfung: %s: %w", p.name, err)
		}
	}
	if _, err := oleutil.CallMethod(sc, "Save"); err != nil {
		return fmt.Errorf("Verknüpfung speichern: %w", err)
	}
	return nil
}

// OpenInEditor öffnet path im Windows-Editor.
func OpenInEditor(path string) error {
	editor := "notepad.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		editor = filepath.Join(root, "notepad.exe")
	}
	cmd := exec.Command(editor, path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Editor: %w", err)
	}
	return cmd.Process.Release()
}

// OpenURL öffnet url im Standard-Browser.
func OpenURL(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	u, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, u, nil, nil, windows.SW_SHOWNORMAL)
}
