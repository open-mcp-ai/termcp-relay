package sshserver

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestServer_PipeStdinNeverClosedDoesNotHang guards the pipe-mode deadlock
// described in issue #283: a client that opens stdin and never closes it must
// still see the command exit.
//
// WHY THE CLIENT'S STDIN IS THE SUBJECT HERE. x/crypto's Wait returns when the
// server sends exit-status, and a server that wires the process stdin directly
// to the channel (cmd.Stdin = sess) cannot send it: os/exec's copy goroutine
// reads the channel to EOF before Wait returns, and EOF only arrives when the
// client closes its write side. termcp keeps stdin open for the whole life of a
// channel, so it parks the copy forever and the exit-status is never reached --
// the symptom is a handle reporting `running` minutes after the process is gone.
//
// THE ASSERTION IS BOUNDED AND IT IS THE WAITING THAT IS THE BUG. A hang is what
// regressing this line produces, so the test waits on Wait() through a channel
// and times out. A healthy relay answers immediately, and this stays fast no
// matter how loaded the runner is; the timeout is a hang detector, not a budget.
//
// It is a relay-shaped mirror of termcp's TestServer_PipeSession_StdinNeverClosed
// in its own sshserver package: the extraction that produced this repo dropped
// both the code fix and its test, which is how the bug came back.
func TestServer_PipeStdinNeverClosedDoesNotHang(t *testing.T) {
	srv, cfg := startTestServer(t)
	client := dialTest(t, srv, cfg)

	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	_ = stdin // never written to, never closed early — replicates the termcp client

	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := sess.Start("echo relay-deadlock-guard"); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		out string
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		b, readErr := io.ReadAll(stdout)
		// The read side has its own EOF question (server closed stdout after
		// Wait, or channel closed). The deadlock this test is about shows up
		// in Wait(), not in the read.
		_ = readErr
		ch <- outcome{strings.TrimRight(string(b), "\r\n"), sess.Wait()}
	}()

	var got outcome
	select {
	case got = <-ch:
	case <-time.After(15 * time.Second):
		t.Fatal("pipe mode with client stdin left open: Wait never returned; the server is parked in os/exec's stdin-copy goroutine and never sent exit-status")
	}
	if got.out != "relay-deadlock-guard" {
		t.Errorf("pipe output = %q, want relay-deadlock-guard", got.out)
	}
	// exit-status arrived: for a successful echo, Wait returns nil. A server that
	// never sends the status returns an error here once the transport closes, or
	// the test already failed above.
	if got.err != nil {
		t.Errorf("Wait returned %v after the output was complete; the server should report a clean exit status", got.err)
	}
}
