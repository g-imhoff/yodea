//go:build windows

package client

import (
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// restrictSessionFile locks the session file ACL to the current user only.
// Unix permission bits (0600) do not restrict other users on Windows, so
// replace the inherited DACL with a protected owner-only DACL granting the
// current user full file access and nobody else any access.
func restrictSessionFile(path string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()

	tu, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sid := tu.User.Sid

	var pinner runtime.Pinner
	pinner.Pin(sid)
	defer pinner.Unpin()

	trustee := windows.TRUSTEE{
		TrusteeForm:  windows.TRUSTEE_IS_SID,
		TrusteeType:  windows.TRUSTEE_IS_USER,
		TrusteeValue: windows.TrusteeValueFromSID(sid),
	}
	grant := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee:           trustee,
	}
	sd, err := windows.BuildSecurityDescriptor(nil, nil, []windows.EXPLICIT_ACCESS{grant}, nil, nil)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// PROTECTED_DACL blocks inheritable ACEs from the parent directory so
	// only the entry above applies.
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
