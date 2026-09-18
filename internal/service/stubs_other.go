//go:build !windows

package service

import "errors"

// The Unix service managers (systemd, launchd, ...) take the account name and
// run as root: there is no password handoff, no user-rights bookkeeping and no
// UAC re-launch. Everything below exists only to satisfy the shared code paths,
// so its Windows counterpart holds the real implementation.

// promptPassword is never called on Unix: the account name is enough.
func promptPassword(string) (string, error) { return "", nil }

func writeCredentialFile(string) (string, error) { return "", nil }

func readCredentialFile(string) (string, error) { return "", nil }

func currentUserToken() (string, error) { return "", nil }

// verifyLogon: the install runs as root, which already holds every privilege.
func verifyLogon(string, string) error { return nil }

// logonFailure has no meaning off Windows: there is no password handoff.
func logonFailure(error) bool { return false }

// grantServiceLogonRight is Windows-only: no user rights are involved.
func grantServiceLogonRight(string) error { return nil }

// serviceLogonRightHeld: Unix always considers the account usable.
func serviceLogonRightHeld(string) (bool, error) { return true, nil }

// serviceAccountName is Windows-only (SCM ServiceStartName); Unix account
// handling never needs to read it back.
func serviceAccountName(string) (string, error) { return "", nil }

// elevated always reports true off Windows: service management there is done
// with sudo/root, which a process cannot request for itself.
func elevated() bool { return true }

// relaunchElevated is unreachable off Windows because elevated() returns true.
func relaunchElevated([]string) (int, error) {
	return 0, errors.New("elevation is not supported on this platform")
}
