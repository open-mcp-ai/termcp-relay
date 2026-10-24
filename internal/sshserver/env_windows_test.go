//go:build windows

package sshserver

import (
	"os"
	"strings"
	"testing"
)

// These run only on Windows, against the real registry. They are the tests that
// would have caught the original bug: the composed block must reflect what the
// machine has now, not what the process inherited at start.

func TestSessionEnvCarriesTheRegistryPath(t *testing.T) {
	machine := readEnvKey(machineRegistryKey(), machineEnvKey)
	var machinePath string
	for _, v := range machine {
		if strings.EqualFold(v.name, "Path") {
			machinePath = expandPercent(v.value, newEnvLookup(os.Environ()))
		}
	}
	if machinePath == "" {
		t.Skip("no machine Path in the registry to compare against")
	}

	got := envMap(sessionEnv())
	for _, entry := range strings.Split(machinePath, envPathSeparator) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(got["PATH"], entry) {
			t.Errorf("machine Path entry %q is missing from the session PATH", entry)
		}
	}
}

func TestSessionEnvKeepsVariablesThatLiveOutsideTheEnvironmentKeys(t *testing.T) {
	// USERPROFILE, APPDATA and COMPUTERNAME come from HKCU\Volatile Environment,
	// which is per logon session and is not one of the two keys read here. If the
	// block were rebuilt from the registry alone they would vanish.
	got := envMap(sessionEnv())
	for _, name := range []string{"USERPROFILE", "COMPUTERNAME", "SystemRoot"} {
		if _, ok := os.LookupEnv(name); !ok {
			continue // not set in this context; nothing to preserve
		}
		if got[strings.ToUpper(name)] == "" {
			t.Errorf("%s was dropped from the session environment", name)
		}
	}
}

func TestReadEnvKeyFindsValues(t *testing.T) {
	values := readEnvKey(machineRegistryKey(), machineEnvKey)
	if len(values) == 0 {
		t.Fatal("readEnvKey returned nothing for the machine Environment key")
	}
	found := false
	for _, v := range values {
		if strings.EqualFold(v.name, "Path") {
			found = true
			if v.value == "" {
				t.Error("machine Path is empty")
			}
		}
	}
	if !found {
		t.Error("readEnvKey did not return Path")
	}
}

func TestSessionEnvCarriesEveryMachineVariable(t *testing.T) {
	// The test that separates "rebuild the block" from "patch PATH": every
	// variable the machine key defines has to arrive, PSModulePath and whatever
	// product home an installer set included. Only presence is asserted - an
	// expandable value is compared after expansion, so equality with the raw
	// registry string would be wrong.
	got := envMap(sessionEnv())
	for _, v := range readEnvKey(machineRegistryKey(), machineEnvKey) {
		if _, ok := got[strings.ToUpper(v.name)]; !ok {
			t.Errorf("machine variable %s is missing from the session environment", v.name)
		}
	}
}
