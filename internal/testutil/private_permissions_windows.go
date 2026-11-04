//go:build windows

package testutil

import (
	"testing"

	"golang.org/x/sys/windows"
)

// LoosenPermissions gives Everyone read access to this fixture.
// Windows Chmod does not set a DACL. Give Everyone read access to this fixture
// instead, then restore the exact original DACL before testing metadata guards.
func LoosenPermissions(t testing.TB, path string) func() {
	t.Helper()
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	original, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	set := func(sd *windows.SECURITY_DESCRIPTOR, protected bool) {
		t.Helper()
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		protection := windows.SECURITY_INFORMATION(windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
		if protected {
			protection = windows.PROTECTED_DACL_SECURITY_INFORMATION
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags|protection, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
	}
	set(sd, true)
	control, _, err := original.Control()
	if err != nil {
		t.Fatal(err)
	}
	return func() { set(original, control&windows.SE_DACL_PROTECTED != 0) }
}
