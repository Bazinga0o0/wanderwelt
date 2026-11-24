//go:build windows

package host

import (
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
