//go:build windows

package service

import (
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Keep the checked directory chain pinned while preparing the database. Walk
// from the volume root inward so no unchecked parent can replace a checked leaf.
func protectDataDirectory(path string, info os.FileInfo) (func(), error) {
	path, err := filepath.Abs(path)
	if err != nil || !info.IsDir() {
		return nil, os.ErrPermission
	}
	paths := []string{path}
	for parent := filepath.Dir(path); parent != paths[len(paths)-1]; parent = filepath.Dir(parent) {
		paths = append(paths, parent)
	}
	var handles []windows.Handle
	unlock := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			windows.CloseHandle(handles[i])
		}
		handles = nil
	}
	for i := len(paths) - 1; i >= 0; i-- {
		h, err := openDataPathACL(paths[i], true, i == 0)
		if err != nil {
			unlock()
			return nil, err
		}
		handles = append(handles, h)
	}
	return unlock, nil
}

func prepareDataFile(path string) error { return checkDataACL(path, false) }

func checkPrivateDataPermissions(path string, _ os.FileInfo, dir bool) error {
	return checkDataACL(path, dir)
}

// Windows reports synthetic Unix mode bits, not the NTFS access rules. Validate
// the actual ACL without rewriting it: the current user, SYSTEM and local
// administrators are the only accepted trustees, including inherited grants.
func checkDataACL(path string, dir bool) error {
	h, err := openDataPathACL(path, dir, true)
	if err == nil {
		windows.CloseHandle(h)
	}
	return err
}

func openDataPathACL(path string, dir, private bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, os.ErrPermission
	}
	// FILE_READ_DATA also means FILE_LIST_DIRECTORY. Metadata-only handles do
	// not enforce the sharing exclusion needed to pin a directory's identity.
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_READ_DATA,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, os.ErrPermission
	}
	valid := false
	defer func() {
		if !valid {
			windows.CloseHandle(h)
		}
	}()
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir {
		return 0, os.ErrPermission
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return 0, os.ErrPermission
	}
	if err := checkDataDescriptorAccess(sd, private); err != nil {
		return 0, err
	}
	valid = true
	return h, nil
}

func checkDataDescriptor(sd *windows.SECURITY_DESCRIPTOR) error {
	return checkDataDescriptorAccess(sd, true)
}

func checkDataDescriptorAccess(sd *windows.SECURITY_DESCRIPTOR, private bool) error {
	if sd == nil {
		return os.ErrPermission
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return os.ErrPermission
	}
	var installer *windows.SID
	if !private {
		// The Windows volume root is normally owned by this local OS service.
		installer, _, _, err = windows.LookupSID("", `NT SERVICE\TrustedInstaller`)
		if err != nil {
			return os.ErrPermission
		}
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && sid.IsValid() && (sid.Equals(user.User.Sid) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) || (!private && installer != nil && sid.Equals(installer)))
	}
	owner, _, err := sd.Owner()
	if err != nil || !trusted(owner) {
		return os.ErrPermission
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		return os.ErrPermission
	}
	userAccess := false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil {
			return os.ErrPermission
		}
		// Denials cannot expose data. Unknown/object/callback ACEs fail closed.
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || int(ace.Header.AceSize) < 20 {
			return os.ErrPermission
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			if private { // Inherited grants must also keep future database files private.
				if !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
					return os.ErrPermission
				}
			}
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() {
			return os.ErrPermission
		}
		// Ancestors may be readable by others, but cannot be replaced, converted
		// to a reparse point or have their ownership/ACL rewritten by them.
		const fileDeleteChild = 0x0040 // FILE_DELETE_CHILD is not exported by x/sys/windows.
		const mutate = windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | fileDeleteChild | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.GENERIC_WRITE | windows.GENERIC_ALL
		if !trusted(sid) && (private || ace.Mask&mutate != 0) {
			return os.ErrPermission
		}
		if sid.Equals(user.User.Sid) && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 && ace.Mask&0x001f01ff == 0x001f01ff {
			userAccess = true
		}
	}
	runtime.KeepAlive(sd)
	if private && !userAccess {
		return os.ErrPermission
	}
	return nil
}
