//go:build windows

package winsys

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Hilfen für das wanderwelt-Fenster („wanderwelt.exe fenster“).

var (
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procFindWindowW              = user32.NewProc("FindWindowW")
	procIsIconic                 = user32.NewProc("IsIconic")
	procShowWindowAsync          = user32.NewProc("ShowWindowAsync")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procFlashWindowEx            = user32.NewProc("FlashWindowEx")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	swRestore = 9

	swpNoSize         = 0x0001
	swpNoMove         = 0x0002
	swpNoActivate     = 0x0010
	swpAsyncWindowPos = 0x4000
	flashwAll         = 0x00000003
	flashwTimerNoFG   = 0x0000000C
	asfwAny           = ^uintptr(0) // ASFW_ANY = (DWORD)-1
	// dwmwaUseImmersiveDarkMode: DWMWA_USE_IMMERSIVE_DARK_MODE (ab
	// Windows 10 20H1; davor 19, wird hier nicht mehr bedient).
	dwmwaUseImmersiveDarkMode = 20
)

// DarkMode meldet, ob Windows für Apps das dunkle Design eingestellt hat
// (HKCU\…\Themes\Personalize\AppsUseLightTheme = 0). Fehlt der Wert, ist
// es hell.
func DarkMode() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// FocusWindow holt das Fenster der Klasse class mit dem Titel title (das
// wanderwelt-Fenster eines anderen Prozesses) nach vorn und stellt es
// wieder her, falls es minimiert ist. Es geht nur asynchron an das Fenster
// (ShowWindowAsync, SWP_ASYNCWINDOWPOS), hängt also nicht, wenn dessen
// Prozess hängt. false: kein solches Fenster.
func FocusWindow(class, title string) bool {
	cls, err := windows.UTF16PtrFromString(class)
	if err != nil {
		return false
	}
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return false
	}
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindowAsync.Call(hwnd, swRestore)
	}
	if r, _, _ := procSetForegroundWindow.Call(hwnd); r == 0 {
		// Kein Vordergrund-Recht (wir sind nicht der Vordergrund-Prozess):
		// wenigstens nach oben und in der Taskleiste blinken.
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpAsyncWindowPos)
		fi := flashInfo{hwnd: hwnd, flags: flashwAll | flashwTimerNoFG, count: 3}
		fi.size = uint32(unsafe.Sizeof(fi))
		procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	}
	return true
}

// flashInfo ist FLASHWINFO.
type flashInfo struct {
	size    uint32
	hwnd    uintptr
	flags   uint32
	count   uint32
	timeout uint32
}

// AllowForeground erlaubt jedem Prozess, das nächste Fenster in den
// Vordergrund zu holen. Der Tray ruft es vor dem Start des Fensters auf:
// Nach einem Klick aufs Symbol darf der Tray-Prozess das, ein von ihm
// gestarteter Prozess ohne diese Weitergabe nicht.
func AllowForeground() {
	procAllowSetForegroundWindow.Call(asfwAny)
}

// DarkTitleBar schaltet die Titelleiste von hwnd auf dunkel (dark) bzw.
// hell. Ältere Windows-Versionen ignorieren es.
func DarkTitleBar(hwnd uintptr, dark bool) error {
	if hwnd == 0 {
		return errors.New("winsys: kein Fenster")
	}
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		return err
	}
	var v uint32
	if dark {
		v = 1
	}
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
	if r != 0 {
		return fmt.Errorf("winsys: DwmSetWindowAttribute: HRESULT 0x%08x", uint32(r))
	}
	return nil
}

// Minimized meldet, ob hwnd gerade minimiert ist.
func Minimized(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	r, _, _ := procIsIconic.Call(hwnd)
	return r != 0
}
