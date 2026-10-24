//go:build windows

package sshserver

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// The two keys Windows composes a session's environment from. The third source,
// HKCU\Volatile Environment, is per logon session and is deliberately not read:
// its values are already in the inherited block, and re-reading them from the
// registry would substitute a copy for the live thing.
const (
	machineEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvKey    = `Environment`
)

// sessionEnv returns the environment a session's process is started with.
//
// The relay runs as a Windows service, so os.Environ() is a snapshot of the boot
// it started in. A package manager installing something later writes the
// registry, not this process, so every variable it changed stayed invisible to
// every shell the relay spawned: PATH first, then PSModulePath, then whatever
// home the next SDK set. A terminal that cannot find something the machine
// plainly has is the failure this prevents.
//
// So the block is rebuilt the way Windows builds one, per session: the inherited
// block as the base, machine entries over it, then the user's. composeEnv has the
// order and the reasoning.
func sessionEnv() []string {
	base := os.Environ()

	lookup := newEnvLookup(base)
	machine := expandLayer(readEnvKey(registry.LOCAL_MACHINE, machineEnvKey), lookup)
	user := expandLayer(readEnvKey(registry.CURRENT_USER, userEnvKey), lookup)

	return composeEnv(base, machine, user)
}

// readEnvKey reads every value of one Environment key, in the order the key
// stores them.
//
// A value that cannot be read as a string is skipped rather than fatal: the key
// can hold non-string types, and one of those is not a reason to hand the
// session no environment at all.
func readEnvKey(root registry.Key, subKey string) []envValue {
	key, err := registry.OpenKey(root, subKey, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()

	names, err := key.ReadValueNames(-1)
	if err != nil {
		return nil
	}
	values := make([]envValue, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		value, valueType, err := key.GetStringValue(name)
		if err != nil {
			continue
		}
		values = append(values, envValue{
			name:       name,
			value:      value,
			expandable: valueType == registry.EXPAND_SZ,
		})
	}
	return values
}

// machineRegistryKey is split out so a test can name the hive without importing
// the registry package itself.
func machineRegistryKey() registry.Key { return registry.LOCAL_MACHINE }
