//go:build !windows

package winsys

import (
	"fmt"
	"runtime"
)

// SingleInstance: außerhalb von Windows ohne Sperre (nur Entwicklung).
func SingleInstance(name string) (release func(), ok bool, err error) {
	return func() {}, true, nil
}

// KeepAwake tut außerhalb von Windows nichts.
func KeepAwake(on bool) {}

// OnACPower: außerhalb von Windows immer am Netz.
func OnACPower() bool { return true }

// OnShutdown tut außerhalb von Windows nichts.
func OnShutdown(h ShutdownHooks) error { return nil }

// SimulateQueryEndSession gibt es nur unter Windows.
func SimulateQueryEndSession() (allow bool, err error) { return true, ErrUnsupported }

// SimulateEndSession gibt es nur unter Windows.
func SimulateEndSession(ending bool) error { return ErrUnsupported }

// ActiveBlockReason: außerhalb von Windows nie.
func ActiveBlockReason() (reason string, ok bool) { return "", false }

// RestoreTrayIcon gibt es nur unter Windows.
func RestoreTrayIcon() error { return ErrUnsupported }

// AttachParentConsole: Außerhalb von Windows gibt es die Konsole schon.
func AttachParentConsole() bool { return true }

// SetAutostart gibt es nur unter Windows.
func SetAutostart(exe string) error { return ErrUnsupported }

// Autostart gibt es nur unter Windows.
func Autostart() (string, error) { return "", ErrUnsupported }

// RemoveAutostart gibt es nur unter Windows.
func RemoveAutostart() error { return ErrUnsupported }

// DesktopDir gibt es nur unter Windows.
func DesktopDir() (string, error) { return "", ErrUnsupported }

// FreeSpace gibt es nur unter Windows.
func FreeSpace(path string) (int64, error) { return 0, ErrUnsupported }

// DocumentsDir gibt es nur unter Windows.
func DocumentsDir() (string, error) { return "", ErrUnsupported }

// DownloadsDir gibt es nur unter Windows.
func DownloadsDir() (string, error) { return "", ErrUnsupported }

// SystemInfo beschreibt außerhalb von Windows nur Betriebssystem und Kerne.
func SystemInfo() string {
	return fmt.Sprintf("System: %s/%s, %d logische Kerne\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
}

// CreateShortcut gibt es nur unter Windows.
func CreateShortcut(lnk, target, args, workDir, description string) error { return ErrUnsupported }

// OpenInEditor gibt es nur unter Windows.
func OpenInEditor(path string) error { return ErrUnsupported }

// OpenURL gibt es nur unter Windows.
func OpenURL(url string) error { return ErrUnsupported }

// DarkMode: außerhalb von Windows immer hell.
func DarkMode() bool { return false }

// FocusWindow gibt es nur unter Windows.
func FocusWindow(class, title string) bool { return false }

// AllowForeground tut außerhalb von Windows nichts.
func AllowForeground() {}

// DarkTitleBar gibt es nur unter Windows.
func DarkTitleBar(hwnd uintptr, dark bool) error { return ErrUnsupported }

// Minimized: außerhalb von Windows nie.
func Minimized(hwnd uintptr) bool { return false }
