//go:build windows

package mcserver

import "syscall"

// detachedProcess ist DETACHED_PROCESS aus der Windows-API: Der Prozess
// bekommt keine Konsole.
const detachedProcess = 0x00000008

// creationFlags sind die Erzeugungs-Flags des Servers: DETACHED_PROCESS und
// CREATE_NEW_PROCESS_GROUP.
//
// Bewusst keine Konsole, auch keine versteckte (CREATE_NO_WINDOW): Eine
// versteckte Konsole gehört einem conhost.exe, und beim Herunterfahren oder
// Abmelden schickt Windows allen Prozessen an einer Konsole sofort das
// Konsolen-Ereignis (CTRL_LOGOFF/CTRL_SHUTDOWN); ohne Handler beendet der
// Standardhandler sie (Exit-Code 0x40010004). Der Server war so schon tot,
// bevor der Starter ihn per RCON stoppen und den Schluss-Stand hochladen
// konnte. Ohne Konsole erreicht ihn dieses Ereignis nicht. Befehle bekommt
// er weiter per RCON, die Ausgabe geht per Pipe (stdout/stderr) ins
// Server-Log, "stop" als Rückfall per stdin-Pipe. DETACHED_PROCESS und
// CREATE_NO_WINDOW schließen sich aus.
const creationFlags = detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP

// sysProcAttr: Java ohne Konsole und in eigener Prozessgruppe (Strg+C beim
// Starter erreicht den Server nicht; er wird über "stop" beendet).
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: creationFlags,
	}
}
