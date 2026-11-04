//go:build windows

package httpserver

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const sessionFileAllAccess = 0x001f01ff

// privateSessionDescriptor uses a protected DACL containing only the current
// user. SYSTEM, Administrators and inherited grants are deliberately excluded.
func privateSessionDescriptor(dir bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, errSessionStorage
	}
	sid := user.User.Sid.String()
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;" + inherit + ";FA;;;" + sid + ")")
	if err != nil {
		return nil, errSessionStorage
	}
	return sd, nil
}

// Create directories with their final ACL atomically; never tighten an existing
// directory or propagate an ACL change into unrelated files.
func createPrivateSessionDir(path string) error {
	return createPrivateSessionDirectories(filepath.Clean(path), true)
}

// Existing ancestors belong to the account store, not the session store. They
// may retain their normal ACL; only newly created ancestors and the requested
// session directory must have the protected DACL. Never repair existing ACLs.
func createPrivateSessionDirectories(path string, requirePrivate bool) error {
	sd, err := privateSessionDescriptor(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errSessionStorage
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	err = windows.CreateDirectory(name, &sa)
	runtime.KeepAlive(sd)
	if errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		parent := filepath.Dir(path)
		if parent == path {
			return errSessionStorage
		}
		if err := createPrivateSessionDirectories(parent, false); err != nil {
			return err
		}
		err = windows.CreateDirectory(name, &sa)
		runtime.KeepAlive(sd)
	}
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) && !errors.Is(err, windows.ERROR_FILE_EXISTS) {
		return errSessionStorage
	}
	if requirePrivate || err == nil {
		return checkPrivateSessionPath(path, true)
	}
	h, openErr := openSessionSecurityHandle(path, windows.FILE_READ_ATTRIBUTES)
	if openErr != nil {
		return openErr
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errSessionStorage
	}
	return nil
}

func openSessionSecurityHandle(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, errSessionStorage
	}
	h, err := windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return windows.InvalidHandle, os.ErrNotExist
	}
	if err != nil {
		return windows.InvalidHandle, errSessionStorage
	}
	return h, nil
}

func checkPrivateSessionPath(path string, dir bool) error {
	h, err := openSessionSecurityHandle(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return checkPrivateSessionHandle(h, dir)
}

func checkPrivateSessionHandle(h windows.Handle, dir bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return errSessionStorage
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir {
		return errSessionStorage
	}
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		return errSessionStorage
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errSessionStorage
	}
	return checkPrivateSessionDescriptor(sd, dir)
}

func checkPrivateSessionDescriptor(sd *windows.SECURITY_DESCRIPTOR, dir bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return errSessionStorage
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return errSessionStorage
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errSessionStorage
	}
	acl, _, err := sd.DACL()
	// A missing/null DACL grants everyone access. An empty DACL cannot support
	// this store. Accept only the explicit owner-only full-control ACE.
	if err != nil || acl == nil || acl.AceCount != 1 {
		return errSessionStorage
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil || ace == nil {
		return errSessionStorage
	}
	allowedFlags := uint8(0)
	if dir {
		// Require both inheritance flags: otherwise a newly created child could
		// receive a broader token-default DACL before protectSessionFile runs.
		allowedFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != allowedFlags || ace.Mask != sessionFileAllAccess || int(ace.Header.AceSize) < 8+user.User.Sid.Len() {
		return errSessionStorage
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	valid := aceSID.IsValid() && aceSID.Equals(user.User.Sid)
	runtime.KeepAlive(sd)
	if !valid {
		return errSessionStorage
	}
	return nil
}

// protectSessionFile is only for newly created files. Reopen with security
// rights (os.CreateTemp's handle lacks WRITE_DAC) and prove it is the same file
// before changing its ACL. No session bytes may be written before this returns.
func protectSessionFile(f *os.File) error {
	original, err := f.Stat()
	if err != nil || !original.Mode().IsRegular() {
		return errSessionStorage
	}
	h, err := openSessionSecurityHandle(f.Name(), windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return errSessionStorage
	}
	securityFile := os.NewFile(uintptr(h), "session")
	if securityFile == nil {
		windows.CloseHandle(h)
		return errSessionStorage
	}
	defer securityFile.Close()
	actual, err := securityFile.Stat()
	if err != nil || !os.SameFile(original, actual) {
		return errSessionStorage
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errSessionStorage
	}
	sd, err := privateSessionDescriptor(false)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return errSessionStorage
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return errSessionStorage
	}
	err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, acl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		return errSessionStorage
	}
	return checkPrivateSessionHandle(h, false)
}
