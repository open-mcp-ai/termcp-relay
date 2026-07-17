package sshserver

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"github.com/charmbracelet/ssh"
	"github.com/pkg/sftp"
)

// sshSignalToOSSig maps an SSH signal name to a local OS signal.
func sshSignalToOSSig(sig ssh.Signal) os.Signal {
	switch sig {
	case "TERM":
		return syscall.SIGTERM
	case "INT":
		return syscall.SIGINT
	case "KILL":
		return syscall.SIGKILL
	case "HUP":
		return syscall.SIGHUP
	default:
		return nil
	}
}

// defaultShellArgs resolves the shell argv when the client requests an
// interactive shell (no command). The server.shell override wins; otherwise
// the platform default is detected. On Windows the detection order mirrors
// termcp's detect_shell tool (pwsh -> powershell -> cmd -> %ComSpec%); on Unix
// $SHELL is honored (termcp parity).
//
// Shell options such as history expansion are left to the user's shell config
// (~/.bashrc, ~/.zshrc, etc.). The daemon does not inject +o histexpand / NO_BANG_HIST.
func (s *Server) defaultShellArgs() []string {
	if strings.TrimSpace(s.shellOverride) != "" {
		return strings.Fields(s.shellOverride)
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe", "cmd.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return []string{p}
			}
		}
		return []string{"cmd.exe"}
	}
	if sh := strings.TrimSpace(os.Getenv("SHELL")); sh != "" {
		if _, err := exec.LookPath(sh); err == nil {
			return strings.Fields(sh)
		}
	}
	for _, sh := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return []string{sh}
		}
	}
	return []string{"/bin/sh"}
}

// handleSession is the entry point for each SSH session channel. Subsystem
// requests (e.g. sftp) are routed by SubsystemHandlers on the server and do
// not reach this handler.
func (s *Server) handleSession(sess ssh.Session) {
	cmdArgs := sess.Command()
	if len(cmdArgs) == 0 {
		// Interactive shell: start the configured/detected shell as-is.
		cmdArgs = s.defaultShellArgs()
	}

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)

	// Forward signals from the client to the local process.
	sigCh := make(chan ssh.Signal, 8)
	sess.Signals(sigCh)
	go func() {
		for sig := range sigCh {
			if cmd.Process == nil {
				continue
			}
			if osSig := sshSignalToOSSig(sig); osSig != nil {
				_ = cmd.Process.Signal(osSig)
			}
		}
	}()

	ppty, winCh, isPty := sess.Pty()
	if isPty {
		go func() {
			for range winCh {
			}
		}()
		setPtySysProcAttr(cmd)
		cmd.Env = append(os.Environ(), "TERM="+ppty.Term)
		if err := ppty.Start(cmd); err != nil {
			io.WriteString(sess, err.Error()+"\n")
			sess.Exit(1)
			return
		}
	} else {
		cmd.Stdin = sess
		cmd.Stdout = sess
		cmd.Stderr = sess.Stderr()
		if err := cmd.Start(); err != nil {
			io.WriteString(sess, err.Error()+"\n")
			sess.Exit(1)
			return
		}
	}

	_ = cmd.Wait()

	exitCode := 127
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	_ = sess.Exit(exitCode)
}

// handleSftpSubsystem is registered as the "sftp" SubsystemHandler on the
// server. It mirrors termcp's built-in sshd SFTP support.
func handleSftpSubsystem(sess ssh.Session) {
	srv, err := sftp.NewServer(sess)
	if err != nil {
		slog.Error("sftp server start", "err", err)
		_ = sess.Exit(1)
		return
	}
	if err := srv.Serve(); err != nil {
		slog.Debug("sftp server stopped", "err", err)
	}
	_ = srv.Close()
	_ = sess.Exit(0)
}
