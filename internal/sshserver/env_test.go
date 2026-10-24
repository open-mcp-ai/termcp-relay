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

func TestExpandPercentResolvesKnownNames(t *testing.T) {
	lookup := newEnvLookup([]string{"SystemRoot=C:\\Windows"})
	got := expandPercent(`%SystemRoot%\system32`, lookup)
	if want := `C:\Windows\system32`; got != want {
		t.Fatalf("expandPercent = %q, want %q", got, want)
	}
}

func TestExpandPercentLeavesUnknownNamesVerbatim(t *testing.T) {
	// ExpandEnvironmentStrings leaves an unresolvable reference alone. Dropping
	// it would silently turn a broken reference into a valid-looking path.
	lookup := newEnvLookup(nil)
	got := expandPercent(`%NOPE%\x`, lookup)
	if want := `%NOPE%\x`; got != want {
		t.Fatalf("expandPercent = %q, want %q", got, want)
	}
}

func TestExpandPercentIsSinglePass(t *testing.T) {
	// %A% expands to the literal "%B%": the result is not rescanned. A fixed
	// point would loop forever on A=%A%.
	lookup := newEnvLookup([]string{"A=%B%", "B=x"})
	if got := expandPercent("%A%", lookup); got != "%B%" {
		t.Fatalf("expandPercent = %q, want %%B%%", got)
	}
	lookup = newEnvLookup([]string{"A=%A%"})
	if got := expandPercent("%A%", lookup); got != "%A%" {
		t.Fatalf("self-reference: expandPercent = %q, want %%A%%", got)
	}
}

func TestExpandPercentHandlesUnterminatedReference(t *testing.T) {
	lookup := newEnvLookup([]string{"A=1"})
	if got := expandPercent("tail%A", lookup); got != "tail%A" {
		t.Fatalf("expandPercent = %q, want the literal tail%%A", got)
	}
	if got := expandPercent("%%", lookup); got != "%%" {
		t.Fatalf("expandPercent = %q, want %%%%", got)
	}
}

func TestExpandLayerSeedsPlainValuesFirst(t *testing.T) {
	// The expandable value is stored BEFORE the plain one it references, which is
	// the shape machine PATH and %SystemRoot% can have.
	values := []envValue{
		{name: "Path", value: `%SystemRoot%\bin`, expandable: true},
		{name: "SystemRoot", value: `C:\Windows`},
	}
	got := expandLayer(values, newEnvLookup(nil))
	if got[0].value != `C:\Windows\bin` {
		t.Fatalf("Path = %q, want C:\\Windows\\bin", got[0].value)
	}
}

func TestExpandLayerLetsALaterValueUseAnEarlierOne(t *testing.T) {
	values := []envValue{
		{name: "Foo", value: `C:\Foo`, expandable: true},
		{name: "Bar", value: `%Foo%\bin`, expandable: true},
	}
	got := expandLayer(values, newEnvLookup(nil))
	if got[1].value != `C:\Foo\bin` {
		t.Fatalf("Bar = %q, want C:\\Foo\\bin", got[1].value)
	}
}

func TestComposeEnvMachineOverridesBaseAndUserOverridesMachine(t *testing.T) {
	base := []string{"KEEP=base", "SHARED=base"}
	machine := []envEntry{{"SHARED", "machine"}, {"MACHINE_ONLY", "m"}}
	user := []envEntry{{"SHARED", "user"}, {"USER_ONLY", "u"}}

	got := envMap(composeEnv(base, machine, user))
	want := map[string]string{
		"KEEP": "base", "SHARED": "user", "MACHINE_ONLY": "m", "USER_ONLY": "u",
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
}

func TestComposeEnvComposesPathInsteadOfReplacing(t *testing.T) {
	sep := envPathSeparator
	base := []string{"PATH=stale"}
	machine := []envEntry{{"Path", "m" + sep + "shared"}}
	user := []envEntry{{"Path", "u" + sep + "shared"}}

	got := envMap(composeEnv(base, machine, user))
	want := "m" + sep + "shared" + sep + "u"
	if got["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", got["PATH"], want)
	}
}

func TestComposeEnvKeepsBaseOrderAndAppendsNewNames(t *testing.T) {
	base := []string{"A=1", "B=2"}
	got := composeEnv(base, []envEntry{{"C", "3"}}, nil)
	want := []string{"A=1", "B=2", "C=3"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("composeEnv = %q, want %q", got, want)
	}
}

func TestComposeEnvMatchesNamesCaseInsensitively(t *testing.T) {
	// The inherited block and the registry disagree on casing in practice; a
	// second entry for the same name would be a bug the OS resolves arbitrarily.
	got := composeEnv([]string{"Path=stale"}, []envEntry{{"PATH", "machine"}}, nil)
	if len(got) != 1 {
		t.Fatalf("composeEnv = %q, want a single PATH entry", got)
	}
	if got[0] != "PATH=machine" {
		t.Fatalf("composeEnv = %q, want PATH=machine", got)
	}
}

func TestComposeEnvIgnoresMalformedBaseEntries(t *testing.T) {
	got := composeEnv([]string{"NOEQUALS", "=novalue", "OK=1"}, nil, nil)
	if strings.Join(got, "|") != "OK=1" {
		t.Fatalf("composeEnv = %q, want [OK=1]", got)
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.Index(kv, "="); i > 0 {
			out[strings.ToUpper(kv[:i])] = kv[i+1:]
		}
	}
	return out
}
