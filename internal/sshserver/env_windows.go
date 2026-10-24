//go:build windows

package sshserver

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// sessionEnv returns the environment a session's process is started with, with
// PATH re-read from the registry.
//
// The relay runs as a Windows service, so os.Environ() is a snapshot taken when
// the service started - usually at boot. A package manager installing something
// later updates the machine PATH, but this process keeps the block it was born
// with and every shell it spawns inherits the stale one. The symptom is a
// terminal that cannot find a tool the machine plainly has, which reads as
// "installed but broken" and costs an hour to explain.
//
// Windows composes a process PATH as machine value + user value, so both are
// read. The user value is the one the service's own account owns, which is also
// the account its children run as.
func sessionEnv() []string {
	machine := readRegistryPath(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
	user := readRegistryPath(registry.CURRENT_USER, `Environment`)

	path := mergePath(machine, user)
	if path == "" {
		// Nothing readable: the inherited block is still better than no PATH.
		return os.Environ()
	}
	return replaceEnv(os.Environ(), "PATH", path)
}

// readRegistryPath reads one Path value and expands it. The machine PATH is
// stored as REG_EXPAND_SZ and contains %SystemRoot% and friends, so the raw
// value is not a usable PATH.
func readRegistryPath(root registry.Key, subKey string) string {
	key, err := registry.OpenKey(root, subKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()

	value, valueType, err := key.GetStringValue("Path")
	if err != nil {
		return ""
	}
	if valueType == registry.EXPAND_SZ {
		if expanded, err := registry.ExpandString(value); err == nil {
			return expanded
		}
	}
	return value
}
