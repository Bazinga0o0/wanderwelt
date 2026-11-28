//go:build windows

package host

import (
	"errors"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procJob ist ein Job-Objekt mit JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: Wird
// sein letzter Handle geschlossen – auch weil der Starter abstürzt oder
// beendet wird –, beendet Windows alle Prozesse darin.
type procJob struct{ h windows.Handle }

func newProcJob() (*procJob, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &procJob{h: h}, nil
}

// assign nimmt den Prozess pid (ein eigenes, gerade gestartetes Kind) in den
// Job auf.
func (j *procJob) assign(pid int) error {
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(p)
	return windows.AssignProcessToJobObject(j.h, p)
}

// contains meldet, ob pid im Job ist.
func (j *procJob) contains(pid int) (bool, error) {
	var list struct {
		Assigned, Listed uint32
		IDs              [256]uintptr
	}
	if err := windows.QueryInformationJobObject(j.h, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil); err != nil {
		return false, err
	}
	for _, id := range list.IDs[:list.Listed] {
		if int(id) == pid {
			return true, nil
		}
	}
	return false, nil
}

// close schließt den Handle; was noch im Job läuft, beendet Windows.
func (j *procJob) close() { windows.CloseHandle(j.h) }

// detachedProcess ist DETACHED_PROCESS aus der Windows-API: keine Konsole.
const detachedProcess = 0x00000008

// creationFlags: DETACHED_PROCESS und CREATE_NEW_PROCESS_GROUP. Der Tunnel
// bekommt wie der Server keine Konsole, auch keine versteckte
// (CREATE_NO_WINDOW): Beim Herunterfahren beendet Windows alle Prozesse an
// einer Konsole sofort, siehe mcserver.creationFlags.
const creationFlags = detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP

// procHandle ist ein offener Handle auf einen fremden Prozess. Solange er
// offen ist, vergibt Windows dessen PID nicht neu: Prüfen, Warten und
// Beenden treffen also sicher denselben Prozess.
type procHandle struct{ h windows.Handle }

// openProcess öffnet den Prozess pid zum Prüfen, Warten und Beenden. Gibt
// es ihn nicht, ist der Fehler errNoProcess; jeder andere Fehler (etwa
// Zugriff verweigert) heißt: Er läuft vielleicht.
func openProcess(pid int) (*procHandle, error) {
	if pid <= 0 {
		return nil, errors.New("keine gültige PID")
	}
	h, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE,
		false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil, errNoProcess // so meldet Windows eine unbekannte PID
	}
	if err != nil {
		return nil, err
	}
	return &procHandle{h: h}, nil
}

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// bootTime ist der Zeitpunkt des letzten Starts des PCs (Null, wenn
// unbekannt).
func bootTime() time.Time {
	if procGetTickCount64.Find() != nil {
		return time.Time{}
	}
	ms, _, _ := procGetTickCount64.Call()
	return time.Now().Add(-time.Duration(ms) * time.Millisecond)
}

func (p *procHandle) close() { windows.CloseHandle(p.h) }

// exe liefert den vollen Programmpfad, wie Windows ihn meldet.
func (p *procHandle) exe() (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p.h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// created liefert die Startzeit des Prozesses (Unix-Nanosekunden).
func (p *procHandle) created() (int64, error) {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(p.h, &c, &e, &k, &u); err != nil {
		return 0, err
	}
	return c.Nanoseconds(), nil
}

// running meldet, ob der Prozess noch läuft.
func (p *procHandle) running() bool {
	ev, err := windows.WaitForSingleObject(p.h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// wait wartet höchstens d auf das Ende des Prozesses und meldet, ob er
// beendet ist.
func (p *procHandle) wait(d time.Duration) bool {
	ev, err := windows.WaitForSingleObject(p.h, uint32(d.Milliseconds()))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// kill beendet den Prozess hart.
func (p *procHandle) kill() error { return windows.TerminateProcess(p.h, 1) }

// exitCode liefert den Exit-Code des beendeten Prozesses.
func (p *procHandle) exitCode() (uint32, error) {
	var code uint32
	if err := windows.GetExitCodeProcess(p.h, &code); err != nil {
		return 0, err
	}
	if code == 259 { // STILL_ACTIVE: Der Prozess läuft noch.
		return 0, errors.New("Prozess läuft noch")
	}
	return code, nil
}

// hiddenProcAttr: tunnel.exe ohne Konsole und ohne Fenster.
func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: creationFlags}
}

// restrictFile beschränkt den Zugriff auf path auf den eigenen Benutzer und
// SYSTEM (geschützte DACL, nichts vererbt): Die Datei enthält den
// persönlichen Token.
func restrictFile(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
