//go:build !windows && !linux

package service

import kservice "github.com/kardianos/service"

// applyPlatformOptions is a no-op on platforms without SCM/systemd recovery
// options to set.
func applyPlatformOptions(kservice.KeyValue) {}
