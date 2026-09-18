//go:build windows

package service

import (
	"os"
	"testing"
)

// TestShellExecuteExWait exercises the ShellExecuteExW plumbing with the
// non-elevating "open" verb: process wait and exit-code propagation must work
// without any UAC prompt.
func TestShellExecuteExWait(t *testing.T) {
	cmd := os.Getenv("ComSpec")
	if cmd == "" {
		t.Skip("ComSpec not set")
	}
	code, err := shellExecuteEx("open", cmd, []string{"/c", "exit 7"})
	if err != nil {
		t.Fatalf("shellExecuteEx: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

func TestJoinArgsQuoting(t *testing.T) {
	// EscapeArg only wraps arguments containing whitespace; quotes and
	// backslashes are escaped in place, matching CreateProcess semantics.
	got := joinArgs([]string{"a b", `c"d`, "e", ""})
	want := `"a b" c\"d e ""`
	if got != want {
		t.Fatalf("joinArgs = %q, want %q", got, want)
	}
}
