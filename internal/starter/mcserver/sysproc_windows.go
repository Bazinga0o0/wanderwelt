//go:build windows

package mcserver

import "syscall"

// createNoWindow ist CREATE_NO_WINDOW aus der Windows-API.
const createNoWindow = 0x08000000

// sysProcAttr: Java ohne Konsolenfenster und in eigener Prozessgruppe
// (Strg+C beim Starter erreicht den Server nicht; er wird über "stop"
// beendet).
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
