package group

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
)

// restrictToOwner entfernt die vererbten Rechte von path und lässt nur das
// aktuelle Windows-Konto zu.
func restrictToOwner(path string) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	cmd := exec.Command(icaclsPath(), path, "/inheritance:r", "/grant:r", u.Username+":F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("icacls: %w: %s", err, out)
	}
	return nil
}

// icaclsPath: das Systemprogramm aus System32, nicht per PATH gesucht.
func icaclsPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "icacls.exe")
	}
	return "icacls"
}
