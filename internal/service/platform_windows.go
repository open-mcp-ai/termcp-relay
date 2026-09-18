//go:build windows

package service

import (
	"errors"

	kservice "github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

// applyPlatformOptions tunes the Windows service definition: restart the
// service automatically when it exits unexpectedly (SCM recovery options).
func applyPlatformOptions(o kservice.KeyValue) {
	o[kservice.OnFailure] = kservice.OnFailureRestart
	o[kservice.OnFailureDelayDuration] = "5s"
}

// logonFailure reports whether err means the SCM rejected the configured
// service account's credentials. The message is localized, so match the errno.
// Install-time CreateService reports ERROR_LOGON_FAILURE (1326); Start reports
// ERROR_SERVICE_LOGON_FAILED (1069); an invalid password can also surface as
// ERROR_INVALID_PASSWORD (86).
func logonFailure(err error) bool {
	return errors.Is(err, windows.ERROR_LOGON_FAILURE) ||
		errors.Is(err, windows.ERROR_SERVICE_LOGON_FAILED) ||
		errors.Is(err, windows.ERROR_INVALID_PASSWORD) ||
		errors.Is(err, windows.ERROR_ACCOUNT_EXPIRED) ||
		errors.Is(err, windows.ERROR_PASSWORD_EXPIRED)
}
