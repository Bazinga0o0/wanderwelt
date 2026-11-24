//go:build !windows

package host

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Außerhalb von Windows (Entwicklung) prüft und beendet Recover keine
// fremden Prozesse: openProcess meldet jede PID als nicht laufend, ein
// verwaister Server bleibt also unberührt.

type procHandle struct{}

func openProcess(int) (*procHandle, error) { return nil, errNoProcess }

// bootTime ist außerhalb von Windows unbekannt.
func bootTime() time.Time { return time.Time{} }

func (*procHandle) close()                    {}
func (*procHandle) exe() (string, error)      { return "", errors.ErrUnsupported }
func (*procHandle) created() (int64, error)   { return 0, errors.ErrUnsupported }
func (*procHandle) running() bool             { return false }
func (*procHandle) wait(time.Duration) bool   { return false }
func (*procHandle) exitCode() (uint32, error) { return 0, errors.ErrUnsupported }
func (*procHandle) kill() error               { return errors.ErrUnsupported }

// procJob tut außerhalb von Windows nichts.
type procJob struct{}

func newProcJob() (*procJob, error)         { return &procJob{}, nil }
func (*procJob) assign(int) error           { return nil }
func (*procJob) contains(int) (bool, error) { return false, errors.ErrUnsupported }
func (*procJob) close()                     {}
func hiddenProcAttr() *syscall.SysProcAttr  { return nil }

// restrictFile: nur der Eigentümer darf lesen und schreiben.
func restrictFile(path string) error { return os.Chmod(path, 0o600) }
