//go:build windows

package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
	"golang.org/x/sys/windows"
)

func assertMediaPrivate(t *testing.T, path string, dir bool) {
	t.Helper()
	if err := service.CheckPrivateDataPath(path, dir); err != nil {
		t.Fatalf("media DACL is not private: %v", err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if acl == nil || acl.AceCount != 1 {
		t.Fatal("media must grant only the current owner")
	}
}

func TestWindowsMediaTighteningRejectsReplacedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// An adversarial engine can return a read handle which permits deletion;
	// ordinary os.Open on Windows itself prevents replacement while open.
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	testutil.LoosenPermissions(t, path)
	if err := tightenMediaFile(f); err == nil {
		t.Fatal("changed the replacement ACL instead of refusing an identity mismatch")
	}
	if err := service.CheckPrivateDataPath(path, false); err == nil {
		t.Fatal("replacement ACL was changed")
	}
}

func TestWindowsMediaTighteningRejectsReparseDirectory(t *testing.T) {
	target := t.TempDir()
	testutil.LoosenPermissions(t, target)
	link := filepath.Join(t.TempDir(), "media")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink privilege: %v", err)
	}
	if err := tightenMediaDirectory(link); err == nil {
		t.Fatal("followed a reparse directory")
	}
	if err := service.CheckPrivateDataPath(target, true); err == nil {
		t.Fatal("changed the target ACL")
	}
}

func TestWindowsMediaTighteningDoesNotPropagateIntoExistingChildren(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "unrelated")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	testutil.LoosenPermissions(t, child)
	if err := tightenMediaDirectory(root); err != nil {
		t.Fatal(err)
	}
	assertMediaPrivate(t, root, true)
	if err := service.CheckPrivateDataPath(child, true); err == nil {
		t.Fatal("rewrote an unrelated child ACL")
	}
}
