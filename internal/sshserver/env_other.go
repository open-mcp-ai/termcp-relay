//go:build !windows

package sshserver

import "os"

// sessionEnv returns the environment a session's process is started with.
//
// A service's environment is as static on Unix as on Windows, but the equivalent
// problem does not arise the same way: login shells read their own profile, and a
// package manager that changes PATH writes it where the next login picks it up.
// The inherited block is therefore what a session should get, and the Windows
// path is the one that has to reconstruct anything.
func sessionEnv() []string { return os.Environ() }
