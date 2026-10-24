package sshserver

import (
	"strings"
	"testing"
)

func TestMergePathPutsMachineBeforeUser(t *testing.T) {
	sep := envPathSeparator
	got := mergePath("a"+sep+"b", "c")
	want := "a" + sep + "b" + sep + "c"
	if got != want {
		t.Fatalf("mergePath = %q, want %q", got, want)
	}
}

func TestMergePathDropsEmptyAndDuplicateEntries(t *testing.T) {
	sep := envPathSeparator
	// Empty fragments, a trailing separator, and the same entry in both halves
	// with different case: all three are noise a real PATH accumulates.
	got := mergePath(sep+"a"+sep+sep+"b"+sep, "A"+sep+"c")
	want := "a" + sep + "b" + sep + "c"
	if got != want {
		t.Fatalf("mergePath = %q, want %q", got, want)
	}
}

func TestMergePathKeepsEmptyWhenEverythingIsEmpty(t *testing.T) {
	if got := mergePath("", ""); got != "" {
		t.Fatalf("mergePath of two empty fragments = %q, want empty", got)
	}
}

func TestReplaceEnvIsCaseInsensitiveAndKeepsPosition(t *testing.T) {
	env := []string{"A=1", "Path=old", "B=2"}
	got := replaceEnv(env, "PATH", "new")
	want := []string{"A=1", "PATH=new", "B=2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("replaceEnv = %q, want %q", got, want)
	}
}

func TestReplaceEnvAppendsWhenAbsent(t *testing.T) {
	got := replaceEnv([]string{"A=1"}, "PATH", "new")
	want := []string{"A=1", "PATH=new"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("replaceEnv = %q, want %q", got, want)
	}
}

func TestReplaceEnvDoesNotDuplicateAnExistingKey(t *testing.T) {
	// A block with the same name twice already exists in the wild (installers
	// append); one replacement is the contract.
	got := replaceEnv([]string{"PATH=old", "PATH=older"}, "PATH", "new")
	if len(got) != 1 || got[0] != "PATH=new" {
		t.Fatalf("replaceEnv = %q, want exactly [PATH=new]", got)
	}
}
