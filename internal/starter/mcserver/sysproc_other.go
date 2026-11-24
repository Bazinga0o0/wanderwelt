//go:build !windows

package mcserver

import "syscall"

// sysProcAttr: eigene Prozessgruppe (Entwicklung außerhalb von Windows).
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
