//go:build windows

package httpserver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSessionPermissionsWindowsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "sessions")
	if err := createPrivateSessionDir(path); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(path, true); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(filepath.Dir(path), true); err != nil {
		t.Fatal(err)
	}
	if err := createPrivateSessionDir(path); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(path, false); err == nil {
		t.Fatal("accepted directory as file")
	}
	if err := checkPrivateSessionPath(filepath.Join(path, "missing"), false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestSessionPermissionsWindowsNormalAccountRoot(t *testing.T) {
	// The account's existing DataDir normally has an inherited DACL. It is not
	// the private browser store and must neither be rejected nor repaired.
	root := t.TempDir()
	before, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "accounts", "primary", "browser")
	if err := createPrivateSessionDir(path); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, "accounts"), filepath.Join(root, "accounts", "primary"), path} {
		if err := checkPrivateSessionPath(dir, true); err != nil {
			t.Fatal(err)
		}
	}
	after, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatal("changed the existing account root ACL")
	}
}

func TestSessionPermissionsWindowsProtectFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := createPrivateSessionDir(dir); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, "new-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := protectSessionFile(f); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(f.Name(), false); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(f.Name(), true); err == nil {
		t.Fatal("accepted file as directory")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := protectSessionFile(f); err == nil {
		t.Fatal("accepted closed file")
	}
}

func TestSessionPermissionsWindowsRejectUnsafeACL(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	for name, dacl := range map[string]string{
		"everyone":         "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FR;;;WD)",
		"administrators":   "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;BA)",
		"system":           "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)",
		"null":             "D:NO_ACCESS_CONTROL",
		"no-inherit":       "D:P(A;;FA;;;" + sid + ")",
		"files-only":       "D:P(A;OI;FA;;;" + sid + ")",
		"directories-only": "D:P(A;CI;FA;;;" + sid + ")",
		"unprotected":      "D:(A;OICI;FA;;;" + sid + ")",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions")
			if err := createPrivateSessionDir(path); err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString("O:" + sid + dacl)
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			flags := windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
			if name == "unprotected" {
				flags = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.SECURITY_INFORMATION(flags), nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkPrivateSessionPath(path, true); err == nil {
				t.Fatal("accepted unsafe ACL")
			}
			if err := createPrivateSessionDir(path); err == nil {
				t.Fatal("repaired unsafe existing directory")
			}
			after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			if before.String() != after.String() {
				t.Fatal("modified existing ACL")
			}
		})
	}
}

func TestSessionPermissionsWindowsRejectReparsePoint(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := createPrivateSessionDir(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	if err := checkPrivateSessionPath(link, true); err == nil {
		t.Fatal("accepted directory reparse point")
	}
	if err := createPrivateSessionDir(link); err == nil {
		t.Fatal("accepted existing reparse point")
	}
}
