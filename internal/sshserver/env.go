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
// Empty fragments and duplicate entries are dropped. This is the one place the
// composition deliberately departs from what Windows does verbatim: an empty
// PATH entry means "the current directory" and a duplicate is noise, so neither
// is worth carrying into every process on the host. Order is otherwise kept.
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

// envValue is one value read from an Environment registry key.
type envValue struct {
	name       string
	value      string
	expandable bool // REG_EXPAND_SZ: the value may contain %NAME% references
}

// envEntry is a variable to set, carrying the spelling the registry used.
type envEntry struct {
	name  string
	value string
}

// envLookup is a case-insensitive variable table that grows as a layer is
// expanded, so a value can reference one defined earlier in the same key.
type envLookup map[string]string

func newEnvLookup(base []string) envLookup {
	lookup := make(envLookup, len(base)+16)
	for _, kv := range base {
		if i := strings.Index(kv, "="); i > 0 {
			lookup[strings.ToUpper(kv[:i])] = kv[i+1:]
		}
	}
	return lookup
}

func (l envLookup) get(name string) (string, bool) {
	value, ok := l[strings.ToUpper(name)]
	return value, ok
}

func (l envLookup) set(name, value string) { l[strings.ToUpper(name)] = value }

// expandPercent expands %NAME% references in a single pass, leaving a name it
// cannot resolve verbatim - the same contract as ExpandEnvironmentStrings,
// which leaves an unknown reference alone rather than dropping it.
//
// One pass, not a fixed point: a value that expands to "%OTHER%" does not then
// expand OTHER. That is also what ExpandEnvironmentStrings does, and matching it
// keeps a self-referential variable from looping.
func expandPercent(s string, lookup envLookup) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '%' {
			b.WriteByte(s[i])
			i++
			continue
		}
		rest := s[i+1:]
		end := strings.IndexByte(rest, '%')
		if end < 0 {
			// Unterminated reference: the rest is literal.
			b.WriteString(s[i:])
			break
		}
		name := rest[:end]
		if name == "" {
			// "%%": nothing to expand, and it is not a reference.
			b.WriteString(s[i : i+2])
			i += 2
			continue
		}
		if value, ok := lookup.get(name); ok {
			b.WriteString(value)
		} else {
			b.WriteString(s[i : i+2+end])
		}
		i += end + 2
	}
	return b.String()
}

// expandLayer turns one key's values into entries, resolving %NAME% against
// everything known so far and adding each result to the lookup.
//
// Values that are not expandable are seeded first, so an expandable value can
// reference one that happens to be stored after it. A registry key's enumeration
// order is not a guarantee, and machine PATH references %SystemRoot%.
func expandLayer(values []envValue, lookup envLookup) []envEntry {
	entries := make([]envEntry, 0, len(values))
	for _, v := range values {
		if !v.expandable {
			lookup.set(v.name, v.value)
		}
	}
	for _, v := range values {
		value := v.value
		if v.expandable {
			value = expandPercent(v.value, lookup)
		}
		lookup.set(v.name, value)
		entries = append(entries, envEntry{name: v.name, value: value})
	}
	return entries
}

// composeEnv applies registry entries over the inherited block.
//
// The order is the one Windows uses to build a session's environment:
//
//  1. the inherited block is the base. It is not redundant: the per-logon
//     variables (USERPROFILE, APPDATA, COMPUTERNAME, LOGONSERVER...) live in
//     HKCU\Volatile Environment, which is per logon session and is not one of the
//     two keys read here. Starting from an empty block would lose them.
//  2. machine entries override the base.
//  3. user entries override the machine's - except PATH, which Windows composes
//     as machine + ";" + user rather than replacing.
func composeEnv(base []string, machine, user []envEntry) []string {
	values := make(map[string]string, len(base)+len(machine)+len(user))
	spelling := make(map[string]string, len(base)+len(machine)+len(user))
	order := make([]string, 0, len(base)+len(machine)+len(user))

	add := func(name, value string) {
		key := strings.ToUpper(name)
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = value
		spelling[key] = name
	}

	for _, kv := range base {
		if i := strings.Index(kv, "="); i > 0 {
			add(kv[:i], kv[i+1:])
		}
	}
	for _, e := range machine {
		add(e.name, e.value)
	}
	for _, e := range user {
		key := strings.ToUpper(e.name)
		if key == "PATH" {
			add(e.name, mergePath(values[key], e.value))
			continue
		}
		add(e.name, e.value)
	}

	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, spelling[key]+"="+values[key])
	}
	return out
}
