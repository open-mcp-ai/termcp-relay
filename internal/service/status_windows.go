//go:build windows

package service

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// queryService inspects the SCM for name using only SERVICE_QUERY_STATUS.
//
// The kardianos library's Status() opens the service with SERVICE_START and
// SERVICE_STOP as well, which a non-elevated user does not hold — so an
// installed service would report "Access is denied" instead of its state.
// Reading the state needs no privileges, and `service status` must work from a
// plain shell.
func queryService(name string) (Status, error) {
	st := Status{Name: name}

	mgr, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return st, fmt.Errorf("open service manager: %w", err)
	}
	defer windows.CloseServiceHandle(mgr)

	svcName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return st, err
	}
	svc, err := windows.OpenService(mgr, svcName, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return st, nil
		}
		return st, fmt.Errorf("open service %q: %w", name, err)
	}
	defer windows.CloseServiceHandle(svc)

	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svc, &status); err != nil {
		return st, fmt.Errorf("query service %q: %w", name, err)
	}

	st.Installed = true
	switch status.CurrentState {
	case windows.SERVICE_RUNNING, windows.SERVICE_START_PENDING, windows.SERVICE_CONTINUE_PENDING:
		st.Running = true
	case windows.SERVICE_STOPPED:
		// Distinguish "never started" (normal right after install) from a
		// crash-looping unit, which SCM reports with a Win32 exit code.
		if code := status.Win32ExitCode; code != 0 && code != uint32(windows.ERROR_SERVICE_NEVER_STARTED) {
			st.Detail = fmt.Sprintf("last start failed with Win32 error %d; check the Event Log", code)
		}
	}
	return st, nil
}

// serviceAccountName returns the ServiceStartName configured for the service
// in the SCM (e.g. ".\anonymous" or "LocalSystem"). Read-only: it needs only
// SERVICE_QUERY_CONFIG, so it works from an unprivileged shell.
func serviceAccountName(name string) (string, error) {
	mgr, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	defer windows.CloseServiceHandle(mgr)

	sName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	svc, err := windows.OpenService(mgr, sName, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return "", err
	}
	defer windows.CloseServiceHandle(svc)

	// The first call reports the required buffer size in needed.
	var needed uint32
	_ = windows.QueryServiceConfig(svc, nil, 0, &needed)
	if needed == 0 {
		return "", errors.New("QueryServiceConfig reported a zero-sized buffer")
	}

	buf := make([]byte, needed)
	cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if err := windows.QueryServiceConfig(svc, cfg, needed, &needed); err != nil {
		return "", err
	}
	return windows.UTF16PtrToString(cfg.ServiceStartName), nil
}
