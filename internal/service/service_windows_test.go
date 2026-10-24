//go:build windows

package service

import "testing"

// The Windows-path cases live in a Windows-only file: isServiceProfile answers
// false for every path elsewhere ("always false elsewhere" in service.go), so
// asserting the Windows answers on a Unix host tested the stub, not the rule.

func TestIsServiceProfile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`C:\Windows\System32\config\systemprofile`, true},
		{`c:\windows\system32\config\systemprofile`, true},
		{`C:/Windows/System32/config/systemprofile`, true},
		{`C:\Windows\ServiceProfiles\LocalService`, true},
		{`C:\Windows\ServiceProfiles\NetworkService`, true},
		{`C:\Users\anonymous`, false},
		{`D:\home\anonymous`, false},
		{`/home/relay`, false},
		{`/root`, false},
		{"", true},
	}
	for _, tc := range cases {
		got := isServiceProfile(tc.path)
		if got != tc.want {
			t.Errorf("isServiceProfile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
