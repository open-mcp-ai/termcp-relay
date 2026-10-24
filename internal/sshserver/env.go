package sshserver

import (
	"os"
	"strings"
)

// envPathSeparator is what PATH entries are joined with. It is a variable so a
// test can exercise both platforms' shapes on either one.
var envPathSeparator = string(os.PathListSeparator)

// mergePath joins PATH fragments in the order Windows itself composes them: the
// machine value first, then the user value.
//
// Empty fragments and duplicate entries are dropped. A path that appears twice
// is harmless to the OS and expensive to a reader: the first thing anyone does
// with an unexpected PATH is look for the mistake, and a stale duplicate is the
// most common one.
func mergePath(fragments ...string) string {
	var out []string
	seen := make(map[string]bool)
	for _, fragment := range fragments {
		for _, part := range strings.Split(fragment, envPathSeparator) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := strings.ToLower(part)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, part)
		}
	}
	return strings.Join(out, envPathSeparator)
}

// replaceEnv returns env with key set to value, keeping the entry's position and
// matching the name case-insensitively, because Windows environment names are
// case-insensitive and the inherited block is not consistently cased.
func replaceEnv(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), prefix) {
			if !replaced {
				out = append(out, key+"="+value)
				replaced = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, key+"="+value)
	}
	return out
}
