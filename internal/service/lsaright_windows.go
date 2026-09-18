//go:build windows

package service

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LSA policy access rights needed to add an account right.
const (
	policyCreateAccount   = 0x00000010
	policyCreatePrivilege = 0x00000040
	policyLookupNames     = 0x00000800
)

// seServiceLogonRight lets an account log on as a service
// (LOGON32_LOGON_SERVICE). The SCM requires it to start a service that runs as
// a named account; without it the first start fails with
// ERROR_SERVICE_LOGON_FAILED (1069) and event 7041 reports
// "the user has not been granted the requested logon type".
const seServiceLogonRight = "SeServiceLogonRight"

// lsaUnicodeString mirrors LSA_UNICODE_STRING.
type lsaUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

// lsaObjectAttributes mirrors LSA_OBJECT_ATTRIBUTES. Only Length is used.
type lsaObjectAttributes struct {
	Length                   uint32
	RootDirectory            uintptr
	ObjectName               *lsaUnicodeString
	Attributes               uint32
	SecurityDescriptor       uintptr
	SecurityQualityOfService uintptr
}

var (
	procLsaOpenPolicy             = windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaOpenPolicy")
	procLsaAddAccountRights       = windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaAddAccountRights")
	procLsaEnumerateAccountRights = windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaEnumerateAccountRights")
	procLsaClose                  = windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaClose")
)

// openLocalPolicy opens a handle to the local security policy. POLICY_LOOKUP_NAMES
// is enough to read an account's rights; adding rights needs the create bits.
func openLocalPolicy(access uint32) (windows.Handle, error) {
	var attrs lsaObjectAttributes
	attrs.Length = uint32(unsafe.Sizeof(attrs))

	var policy windows.Handle
	if status, _, _ := procLsaOpenPolicy.Call(
		0,
		uintptr(unsafe.Pointer(&attrs)),
		uintptr(access),
		uintptr(unsafe.Pointer(&policy)),
	); status != 0 {
		return 0, fmt.Errorf("open LSA policy: %s", lsaStatus(status))
	}
	return policy, nil
}

// grantServiceLogonRight gives accountName the right to log on as a service.
//
// Windows does not grant SeServiceLogonRight when a service is created through
// the SCM API, so the first start fails with a logon failure; the Services MMC
// grants it when an account is set in the Log On tab, and this mirrors that.
// Adding a right the account already holds succeeds silently. The caller must
// be elevated.
func grantServiceLogonRight(accountName string) error {
	sid, err := lookupAccountSID(accountName)
	if err != nil {
		return err
	}

	policy, err := openLocalPolicy(policyCreateAccount | policyCreatePrivilege | policyLookupNames)
	if err != nil {
		return err
	}
	defer procLsaClose.Call(uintptr(policy))

	right, err := windows.UTF16FromString(seServiceLogonRight)
	if err != nil {
		return err
	}
	rightStr := lsaUnicodeString{
		Length:        uint16((len(right) - 1) * 2),
		MaximumLength: uint16(len(right) * 2),
		Buffer:        &right[0],
	}

	if status, _, _ := procLsaAddAccountRights.Call(
		uintptr(policy),
		uintptr(unsafe.Pointer(sid)),
		uintptr(unsafe.Pointer(&rightStr)),
		1,
	); status != 0 {
		return fmt.Errorf("grant %s to %s: %s", seServiceLogonRight, accountName, lsaStatus(status))
	}
	return nil
}

// serviceLogonRightHeld reports whether accountName already holds the right to
// log on as a service. Read-only; works from an unelevated shell.
func serviceLogonRightHeld(accountName string) (bool, error) {
	sid, err := lookupAccountSID(accountName)
	if err != nil {
		return false, err
	}

	policy, err := openLocalPolicy(policyLookupNames)
	if err != nil {
		return false, err
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
		for _, r := range unsafe.Slice(rights, count) {
			if windows.UTF16PtrToString(r.Buffer) == seServiceLogonRight {
				return true, nil
			}
		}
		return false, nil
	case 0xC0000034: // STATUS_OBJECT_NAME_NOT_FOUND: account holds no rights
		return false, nil
	}
	return false, fmt.Errorf("enumerate rights for %s: %s", accountName, lsaStatus(status))
}

// lookupAccountSID resolves "user", ".\user" or "DOMAIN\user" to its SID. The
// returned SID points into the Go heap and must not be freed.
func lookupAccountSID(accountName string) (*windows.SID, error) {
	domain, account := splitAccount(accountName)
	if domain == "" {
		// Bare name or ".\user": bind to local machine explicitly. When computer
		// name matches user name (e.g. machine ANONYMOUS with user anonymous),
		// LookupAccountName with a bare name resolves the machine domain SID
		// rather than the user's RID (1001), which causes LsaAddAccountRights
		// to fail with STATUS_NO_SUCH_USER.
		domain, _ = windows.ComputerName()
	}
	queryName := account
	if domain != "" {
		queryName = domain + `\` + account
	}

	name, err := windows.UTF16PtrFromString(queryName)
	if err != nil {
		return nil, err
	}

	var sidLen, domainLen, use uint32
	err = windows.LookupAccountName(nil, name, nil, &sidLen, nil, &domainLen, &use)
	if err != nil && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return nil, fmt.Errorf("look up account %s: %w", accountName, err)
	}
	if sidLen == 0 {
		return nil, fmt.Errorf("look up account %s: no SID", accountName)
	}

	sidBuf := make([]byte, sidLen)
	domainBuf := make([]uint16, domainLen)
	if err := windows.LookupAccountName(nil, name,
		(*windows.SID)(unsafe.Pointer(&sidBuf[0])), &sidLen,
		&domainBuf[0], &domainLen, &use); err != nil {
		return nil, fmt.Errorf("look up account %s: %w", accountName, err)
	}
	return (*windows.SID)(unsafe.Pointer(&sidBuf[0])), nil
}

// lsaStatus renders an NTSTATUS for humans. Success (0) never reaches it.
func lsaStatus(status uintptr) string {
	switch uint32(status) {
	case 0xC0000064:
		return "STATUS_NO_SUCH_USER (0xC0000064)"
	case 0xC0000022:
		return "STATUS_ACCESS_DENIED (0xC0000022) — run as Administrator"
	}
	return fmt.Sprintf("NTSTATUS 0x%X", uint32(status))
}
