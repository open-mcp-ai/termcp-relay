//go:build windows

package service

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LogonUserW constants. LOGON32_LOGON_INTERACTIVE needs no SeTcbPrivilege,
// unlike LOGON32_LOGON_SERVICE, so an ordinary elevated installer can use it.
const (
	logon32LogonInteractive = 2
	logon32ProviderDefault  = 0
)

var procLogonUserW = windows.NewLazySystemDLL("advapi32.dll").NewProc("LogonUserW")

// verifyLogon checks user/password against the local machine (or the account's
// domain) using LogonUserW — the same credential check the SCM performs when it
// starts a service. Running it at install time turns "the service silently
// fails to start later" into "the password is wrong, try again".
//
// An empty password is valid only for accounts that genuinely have none; for a
// password-protected account LogonUser rejects it, which is exactly the signal
// the caller needs.
func verifyLogon(user, password string) error {
	domain, account := splitAccount(user)
	if account == "" {
		return fmt.Errorf("empty account name")
	}

	pUser, err := windows.UTF16PtrFromString(account)
	if err != nil {
		return err
	}
	var pDomain *uint16
	if domain != "" {
		if pDomain, err = windows.UTF16PtrFromString(domain); err != nil {
			return err
		}
	}
	pPass, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return err
	}

	var token windows.Token
	r1, _, callErr := procLogonUserW.Call(
		uintptr(unsafe.Pointer(pUser)),
		uintptr(unsafe.Pointer(pDomain)),
		uintptr(unsafe.Pointer(pPass)),
		uintptr(logon32LogonInteractive),
		uintptr(logon32ProviderDefault),
		uintptr(unsafe.Pointer(&token)),
	)
	if r1 == 0 {
		if callErr == windows.ERROR_SUCCESS {
			callErr = windows.ERROR_LOGON_FAILURE
		}
		return fmt.Errorf("password rejected for %s: %w", user, callErr)
	}
	defer windows.CloseHandle(windows.Handle(token))
	return nil
}

// splitAccount turns "DOMAIN\user", ".\user" or "user" into the domain and
// account names LogonUserW expects. A "." domain means the local machine, for
// which LogonUser wants a NULL domain.
func splitAccount(user string) (domain, account string) {
	user = strings.TrimSpace(user)
	i := strings.LastIndex(user, `\`)
	if i < 0 {
		return "", user
	}
	domain, account = user[:i], user[i+1:]
	if domain == "." {
		domain = ""
	}
	return domain, account
}
