//go:build windows

package service

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestLookupAccountSID resolves the running user through the same path
// grantServiceLogonRight uses. Running unprivileged is the point: resolution
// must work before the elevated LSA call happens.
func TestLookupAccountSID(t *testing.T) {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser: %v", err)
	}

	account, domainName, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		t.Fatalf("LookupAccount: %v", err)
	}

	sid, err := lookupAccountSID(`.\` + account)
	if err != nil {
		t.Fatalf("lookupAccountSID(.\\%s): %v", account, err)
	}
	if sid.String() != user.User.Sid.String() {
		t.Fatalf("lookupAccountSID resolved %s to %s, want %s",
			account, sid.String(), user.User.Sid.String())
	}
	t.Logf("resolved %s\\%s -> %s", domainName, account, sid.String())
}

// TestAccountRights is a diagnostic: it lists the user rights an account holds
// so a missing SeServiceLogonRight (the cause of event 7041 "the user has not
// been granted the requested logon type") can be confirmed without elevation.
// Run with: go test ./internal/service -run TestAccountRights -v
func TestAccountRights(t *testing.T) {
	account := os.Getenv("TERMCP_TEST_ACCOUNT")
	if account == "" {
		t.Skip("set TERMCP_TEST_ACCOUNT=user to list its rights")
	}

	sid, err := lookupAccountSID(account)
	if err != nil {
		t.Fatalf("lookupAccountSID(%s): %v", account, err)
	}

	policy, err := openLocalPolicy(policyLookupNames)
	if err != nil {
		t.Fatalf("open LSA policy: %v", err)
	}
	defer procLsaClose.Call(uintptr(policy))

	var rights *lsaUnicodeString
	var count uint32
	status, _, _ := procLsaEnumerateAccountRights.Call(
		uintptr(policy),
		uintptr(unsafe.Pointer(sid)),
		uintptr(unsafe.Pointer(&rights)),
		uintptr(unsafe.Pointer(&count)),
	)
	switch uint32(status) {
	case 0:
		// rights is valid
	case 0xC0000034: // STATUS_OBJECT_NAME_NOT_FOUND: account holds no rights
		count = 0
	default:
		t.Fatalf("LsaEnumerateAccountRights: %s", lsaStatus(status))
	}

	t.Logf("%s holds %d right(s) (sid %s)", account, count, sid)
	found := false
	if count > 0 {
		for i, r := range unsafe.Slice(rights, count) {
			name := windows.UTF16PtrToString(r.Buffer)
			t.Logf("  %d. %s", i+1, name)
			if name == seServiceLogonRight {
				found = true
			}
		}
	}
	t.Logf("%s present: %v", seServiceLogonRight, found)

	if !found {
		t.Logf("=> `service start` would fail with logon failure (event 7041); granting the right is required")
	}
}
