//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// elevated reports whether the current process holds an elevated (Administrator)
// token. Registering and controlling services on Windows requires one.
func elevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// seeMaskNoCloseProcess asks ShellExecuteEx to return a process handle instead
// of closing it immediately, which is what lets us wait for the elevated child
// and read its exit code.
const seeMaskNoCloseProcess = 0x00000040

// shellExecuteInfo mirrors SHELLEXECUTEINFOW. It is declared locally because
// x/sys/windows does not wrap ShellExecuteEx; ShellExecute alone returns no
// handle, so the exit code of the elevated process would be lost.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellExecuteEx runs file with args under verb, waits for it to exit, and
// returns its exit code. Shared by relaunchElevated and the test, which
// exercises the same path with the non-elevating "open" verb.
func shellExecuteEx(verb, file string, args []string) (int, error) {
	v, err := windows.UTF16PtrFromString(verb)
	if err != nil {
		return 0, err
	}
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return 0, err
	}
	var params *uint16
	if len(args) > 0 {
		p, err := windows.UTF16PtrFromString(joinArgs(args))
		if err != nil {
			return 0, err
		}
		params = p
	}

	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       v,
		lpFile:       f,
		lpParameters: params,
		// The child writes its output to the file we pass explicitly; if that file
		// is not writable (elevating as a different user), its console is the
		// fallback, so keep it visible rather than hidden.
		nShow: windows.SW_SHOWNORMAL,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))

	if r1, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei))); r1 == 0 {
		return 0, err
	}
	if sei.hProcess == 0 {
		return 0, nil
	}
	defer windows.CloseHandle(sei.hProcess)

	if _, err := windows.WaitForSingleObject(sei.hProcess, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}

// relaunchElevated starts this executable again with the same arguments through
// the UAC consent prompt, waits for it, replays the child's output on our own
// stdout/stderr, and returns its exit code.
func relaunchElevated(args []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate executable: %w", err)
	}

	report, err := os.CreateTemp("", "termcp-relay-elevated-*.log")
	if err != nil {
		return 0, fmt.Errorf("create elevation log: %w", err)
	}
	reportPath := report.Name()
	_ = report.Close()
	defer os.Remove(reportPath)

	child := append(append([]string{}, args...), elevatedOutputFlag, reportPath)
	code, runErr := shellExecuteEx("runas", exe, child)

	if data, err := os.ReadFile(reportPath); err == nil && len(data) > 0 {
		_, _ = os.Stdout.Write(data)
	}
	if runErr != nil {
		if errors.Is(runErr, windows.ERROR_CANCELLED) {
			return 0, errors.New("elevation was declined")
		}
		return 0, runErr
	}
	return code, nil
}

// joinArgs quotes arguments the way CreateProcess expects.
func joinArgs(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(syscall.EscapeArg(a))
	}
	return b.String()
}
