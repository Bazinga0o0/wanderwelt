//go:build windows

package host

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileAllAccess entspricht SDDL "FA" (FILE_ALL_ACCESS).
const fileAllAccess = 0x1F01FF

// checkOwnerOnly: Die DACL von path ist geschützt (nichts vererbt) und
// erlaubt nur dem eigenen Benutzer und SYSTEM vollen Zugriff. Verglichen wird
// semantisch über SIDs, nicht über den SDDL-Text (der Aliase wie LA/SY nutzt).
func checkOwnerOnly(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("%s: keine DACL (%v), SDDL %s", path, err, sd.String())
	}
	fail := func(why string) {
		t.Helper()
		t.Fatalf("%s: DACL %s (%s), erwartet nur eigener Benutzer und SYSTEM", path, sd.String(), why)
	}
	if ctrl&windows.SE_DACL_PROTECTED == 0 {
		fail("nicht geschützt")
	}
	if acl.AceCount != 2 {
		fail("falsche Anzahl Einträge")
	}
	var gotUser, gotSystem bool
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			fail("kein Allow-Eintrag")
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			fail("geerbter Eintrag")
		}
		if ace.Mask != fileAllAccess {
			fail("kein voller Zugriff")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case windows.EqualSid(sid, user.User.Sid):
			gotUser = true
		case windows.EqualSid(sid, system):
			gotSystem = true
		default:
			fail("fremde SID " + sid.String())
		}
	}
	if !gotUser || !gotSystem {
		fail("Benutzer oder SYSTEM fehlt")
	}
}

// konsolenHelfer (Helfermodus "konsole", nur Windows): meldet auf stdout, an
// wie vielen Prozessen seine Konsole hängt (0: keine Konsole), und endet.
func konsolenHelfer() {
	var ids [8]uint32
	n, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList").
		Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	fmt.Printf("konsole=%d\n", n)
	os.Exit(0)
}

// Der Tunnel bekommt wie der Server keine Konsole (DETACHED_PROCESS statt
// CREATE_NO_WINDOW): Beim Herunterfahren baut Windows versteckte Konsolen
// sofort ab und beendet damit alle Prozesse daran.
func TestHiddenProcAttrOhneKonsole(t *testing.T) {
	a := hiddenProcAttr()
	if a.CreationFlags&detachedProcess == 0 || a.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("DETACHED_PROCESS und CREATE_NEW_PROCESS_GROUP erwartet: %#x", a.CreationFlags)
	}
	if a.CreationFlags&0x08000000 != 0 { // CREATE_NO_WINDOW
		t.Errorf("CREATE_NO_WINDOW darf nicht gesetzt sein: %#x", a.CreationFlags)
	}
	cmd := helperCmd(t.Context(), testExe(t))
	cmd.Env = append(os.Environ(), helperEnv+"=konsole")
	cmd.SysProcAttr = a
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "konsole=0" {
		t.Fatalf("Helfer meldet %q, erwartet konsole=0", out)
	}
}
