//go:build windows

package client

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestRestrictSessionFileOwnerOnlyDACL runs only on Windows: after saving,
// the session file must carry a present, inheritance-protected DACL.
func TestRestrictSessionFileOwnerOnlyDACL(t *testing.T) {
	p := isolateSession(t)
	if err := SaveSession(Session{Server: "http://127.0.0.1:8093", Token: "tok-win", UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	if sd == nil {
		t.Fatal("no security descriptor returned")
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("security descriptor Control: %v", err)
	}
	if control&windows.SE_DACL_PRESENT == 0 {
		t.Fatal("session file DACL is not present")
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("session file DACL is not protected from inherited ACEs")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("security descriptor DACL: %v", err)
	}
	if dacl == nil || dacl.AceCount == 0 {
		t.Fatal("session file DACL has no ACEs")
	}
	// The lockdown must not lock out the owner.
	got, err := LoadSession()
	if err != nil {
		t.Fatalf("session unreadable after ACL lockdown: %v", err)
	}
	if got.Token != "tok-win" {
		t.Fatalf("round-trip token = %q, want tok-win", got.Token)
	}
}
