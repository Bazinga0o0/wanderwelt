//go:build windows

package winsys

import (
	"testing"
	"unsafe"
)

// setShutdownLevel stellt die Beendigungsstufe des Prozesses auf 0x3FF
// (vor den 0x280 von Server und Tunnel).
func TestSetShutdownLevel(t *testing.T) {
	var level, flags uint32
	get := kernel32.NewProc("GetProcessShutdownParameters")
	call := func() {
		t.Helper()
		if r, _, err := get.Call(uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&flags))); r == 0 {
			t.Fatal(err)
		}
	}
	call()
	if level != 0x280 {
		t.Skipf("Ausgangsstufe %#x statt 0x280", level)
	}
	setShutdownLevel()
	call()
	if level != 0x3FF {
		t.Fatalf("Stufe %#x, erwartet 0x3FF", level)
	}
}
