//go:build windows

package mcp

import (
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/windows"
)

func tightenMediaDirectory(path string) error { return tightenMediaACL(path, nil, true) }
func tightenMediaFile(f *os.File) error       { return tightenMediaACL(f.Name(), f, false) }

func tightenMediaStaging(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return tightenMediaACL(filepath.Join(root.Name(), path), f, true)
}

// Chmod only changes the read-only bit on Windows. Apply a protected owner-only
// DACL instead, with inheritable grants on directories. Pin the checked object
// without following reparse points and never take ownership of foreign data.
// For files returned by the engine, prove identity with the open read handle.
func tightenMediaACL(path string, original *os.File, dir bool) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return os.ErrPermission
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES|windows.FILE_READ_DATA,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return os.ErrPermission
	}
	checked := os.NewFile(uintptr(h), "media")
	if checked == nil {
		windows.CloseHandle(h)
		return os.ErrPermission
	}
	defer checked.Close()
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir {
		return os.ErrPermission
	}
	if original != nil {
		before, err := original.Stat()
		if err != nil {
			return os.ErrPermission
		}
		after, err := checked.Stat()
		if err != nil || !os.SameFile(before, after) {
			return os.ErrPermission
		}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return os.ErrPermission
	}
	existing, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return os.ErrPermission
	}
	owner, _, err := existing.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return os.ErrPermission
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + inherit + ";FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return os.ErrPermission
	}
	// Unlike SetSecurityInfo, this does not recursively propagate ACL changes
	// into unrelated existing children. Each app-owned child is checked in turn.
	err = windows.SetKernelObjectSecurity(h, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, sd)
	runtime.KeepAlive(sd)
	if err != nil {
		return os.ErrPermission
	}
	return nil
}
