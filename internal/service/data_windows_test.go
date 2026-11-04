//go:build windows

package service

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrepareDataWindowsFirstUse(t *testing.T) {
	root := windowsDataTestRoot(t)
	dir := filepath.Join(root, "data")
	if err := PrepareData(dir); err != nil {
		t.Fatalf("first use with a private Windows ACL: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareData(dir); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func windowsDataTestRoot(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "telegram-mcp-first-run-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	setWindowsTestACL(t, root, "")
	return root
}

// Only change ACLs of newly created fixtures, never an existing user folder.
func setWindowsTestACL(t *testing.T, root, extra string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")" + extra)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, user.User.Sid, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsDataRejectsWritableParent(t *testing.T) {
	root := windowsDataTestRoot(t)
	dir := filepath.Join(root, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	setWindowsTestACL(t, dir, "")
	// Even a private leaf can be replaced through FILE_DELETE_CHILD on its
	// parent. Do not accept custom data paths with this untrusted ancestry.
	setWindowsTestACL(t, root, "(A;;0x40;;;WD)")
	if err := PrepareData(dir); err == nil {
		t.Fatal("accepted private data beneath a parent writable by everyone")
	}
}

func TestWindowsDataDirectoryPinnedDuringPreparation(t *testing.T) {
	root := windowsDataTestRoot(t)
	dir := filepath.Join(root, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := protectDataDirectory(dir, info)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := os.Rename(dir, dir+"-replaced"); err == nil {
		t.Fatal("validated data directory was replaceable while preparing its database")
	}
	if err := os.Rename(root, root+"-replaced"); err == nil {
		t.Fatal("validated ancestor was replaceable while preparing its database")
	}
	unlock()
	if err := os.Rename(dir, dir+"-released"); err != nil {
		t.Fatalf("directory handle leaked after preparation: %v", err)
	}
}

func TestDataWindowsACLPolicy(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	for _, tt := range []struct {
		name, sddl string
		valid      bool
	}{
		{"user only", "O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")", true},
		{"normal inherited LocalAppData", "O:" + sid + "D:AI(A;OICIID;FA;;;SY)(A;OICIID;FA;;;BA)(A;OICIID;FA;;;" + sid + ")", true},
		{"everyone read", "O:" + sid + "D:(A;;FA;;;" + sid + ")(A;;FR;;;WD)", false},
		{"users read", "O:" + sid + "D:(A;;FA;;;" + sid + ")(A;;FR;;;BU)", false},
		{"null dacl", "O:" + sid + "D:NO_ACCESS_CONTROL", false},
		{"empty dacl", "O:" + sid + "D:", false},
		{"foreign owner", "O:WDD:(A;;FA;;;" + sid + ")", false},
		{"inherit only user", "O:" + sid + "D:(A;OICIIO;FA;;;" + sid + ")", false},
		{"read only user", "O:" + sid + "D:(A;;FR;;;" + sid + ")", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tt.sddl)
			if err != nil {
				t.Fatal(err)
			}
			err = checkDataDescriptor(sd)
			if (err == nil) != tt.valid {
				t.Fatalf("accepted=%v, want %v", err == nil, tt.valid)
			}
		})
	}
}
