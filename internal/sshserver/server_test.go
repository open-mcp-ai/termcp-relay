package sshserver

import (
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func ptyShellLine() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe -NoLogo -NoProfile"
	}
	return "/bin/bash"
}

func ptyInput(s string) string {
	if runtime.GOOS == "windows" {
		return s + "\r\n"
	}
	return s + "\n"
}

// startTestServer brings up a relay on a random loopback port with no auth,
// returning the server and a ready-to-use *ssh.ClientConfig.
func startTestServer(t *testing.T) (*Server, *gossh.ClientConfig) {
	t.Helper()
	srv, err := New(Options{
		Addr:           "127.0.0.1:0",
		HostKeyPath:    t.TempDir() + "/host_key",
		Auth:           &Auth{NoAuth: true},
		AllowLocalFwd:  true,
		AllowRemoteFwd: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	cfg := &gossh.ClientConfig{
		User:            "test",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	return srv, cfg
}

func dialTest(t *testing.T, srv *Server, cfg *gossh.ClientConfig) *gossh.Client {
	t.Helper()
	client, err := gossh.Dial("tcp", srv.Addr(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// startTestServerAuth brings up a relay on a random loopback port with the
// given Auth (no-auth disabled, real credentials required).
func startTestServerAuth(t *testing.T, auth *Auth) *Server {
	t.Helper()
	srv, err := New(Options{
		Addr:        "127.0.0.1:0",
		HostKeyPath: t.TempDir() + "/host_key",
		Auth:        auth,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv
}

func TestServer_PasswordAuth(t *testing.T) {
	srv := startTestServerAuth(t, &Auth{Username: "ops", Password: "s3cret-pass"})

	good := &gossh.ClientConfig{
		User:            "ops",
		Auth:            []gossh.AuthMethod{gossh.Password("s3cret-pass")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := gossh.Dial("tcp", srv.Addr(), good)
	if err != nil {
		t.Fatalf("expected successful auth, got: %v", err)
	}
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output("echo pwd_auth_ok")
	_ = sess.Close()
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "pwd_auth_ok" {
		t.Fatalf("expected pwd_auth_ok, got %q", out)
	}

	// Wrong password must be rejected at handshake.
	bad := &gossh.ClientConfig{
		User:            "ops",
		Auth:            []gossh.AuthMethod{gossh.Password("wrong")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}
	if _, err := gossh.Dial("tcp", srv.Addr(), bad); err == nil {
		t.Fatal("expected wrong password to fail auth")
	}

	// Wrong username must be rejected too.
	wrongUser := &gossh.ClientConfig{
		User:            "not-ops",
		Auth:            []gossh.AuthMethod{gossh.Password("s3cret-pass")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	}
	if _, err := gossh.Dial("tcp", srv.Addr(), wrongUser); err == nil {
		t.Fatal("expected wrong username to fail auth")
	}
}

func TestServer_ExecEcho(t *testing.T) {
	srv, cfg := startTestServer(t)
	client := dialTest(t, srv, cfg)

	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	out, err := session.Output("echo hello_relay")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "hello_relay" {
		t.Fatalf("expected hello_relay, got %q", string(out))
	}
}

func TestServer_PtyTerm(t *testing.T) {
	srv, cfg := startTestServer(t)
	client := dialTest(t, srv, cfg)

	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{
		gossh.ECHO:          0,
		gossh.TTY_OP_ISPEED: 14400,
		gossh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		t.Fatal(err)
	}

	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Start(ptyShellLine()); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)
	_, _ = stdin.Write([]byte(ptyInput("echo pty_ok")))

	deadline := time.Now().Add(5 * time.Second)
	var all string
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) && !strings.Contains(all, "pty_ok") {
		n, _ := stdout.Read(buf)
		all += string(buf[:n])
	}
	if !strings.Contains(all, "pty_ok") {
		t.Fatalf("expected pty_ok in output, got %q", all)
	}
	_, _ = stdin.Write([]byte(ptyInput("exit")))
	_ = session.Wait()
}

func TestServer_LocalPortForward(t *testing.T) {
	srv, cfg := startTestServer(t)
	client := dialTest(t, srv, cfg)

	echo, err := echoListener(t)
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()

	echoAddr := echo.Addr().String()
	ln, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			remote, err := client.Dial("tcp", echoAddr)
			if err != nil {
				return
			}
			defer remote.Close()
			pipe(c, remote)
		}(c)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := []byte("forward-test\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	recv := make([]byte, 256)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(recv)
	if err != nil {
		t.Fatal(err)
	}
	if string(recv[:n]) != string(msg) {
		t.Fatalf("expected %q, got %q", msg, recv[:n])
	}
}
