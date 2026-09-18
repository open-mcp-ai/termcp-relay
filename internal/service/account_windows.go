//go:build windows

package service

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// promptPassword reads the service account's password without echoing it.
//
// The source is chosen in this order:
//  1. redirected stdin (pipe or file), so `echo pw | termcp-relay service
//     install` and `< pw.txt` keep working for automation;
//  2. the real console (CONIN$), because Git Bash and MSYS hand the process a
//     pseudo-terminal stdin where a password would be echoed — reading it as a
//     stream then yields an empty string, which is exactly the failure that
//     makes a service install cleanly and then fail to log on at start;
//  3. stdin when it is a genuine terminal.
func promptPassword(user string) (string, error) {
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode().IsRegular() || fi.Mode()&os.ModeNamedPipe != 0) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		defer f.Close()
		if fd := int(f.Fd()); term.IsTerminal(fd) {
			return readPasswordFromTerminal(fd, user)
		}
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no console available to read the password; pipe it in or use --system")
	}
	return readPasswordFromTerminal(fd, user)
}

// readPasswordFromTerminal prints the prompt and reads without echo.
func readPasswordFromTerminal(fd int, user string) (string, error) {
	fmt.Fprintf(os.Stderr, "password for service account %q (verified now; stored by Windows): ", user)
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

// writeCredentialFile stores the password where only the current user can read
// it and returns the path. The file is removed by readCredentialFile.
func writeCredentialFile(password string) (string, error) {
	f, err := os.CreateTemp("", "termcp-relay-cred-*.tmp")
	if err != nil {
		return "", err
	}
	defer f.Close()
	// CreateTemp already uses 0600; tighten explicitly in case of umask.
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if _, err := f.WriteString(password); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// readCredentialFile consumes the handoff file: it reads the password and
// deletes the file immediately, whether or not the read succeeded.
func readCredentialFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	_ = os.Remove(path)
	if err != nil {
		return "", fmt.Errorf("read credential file: %w", err)
	}
	return string(data), nil
}

// currentUserToken returns "DOMAIN\user" for the account running this process.
func currentUserToken() (string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	account, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		return "", err
	}
	if domain == "" {
		return account, nil
	}
	return domain + `\` + account, nil
}
