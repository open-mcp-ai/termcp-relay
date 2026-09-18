package service

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	kservice "github.com/kardianos/service"

	"github.com/open-mcp-ai/termcp-relay/internal/config"
)

func TestKServicConfigBakesRuntimeFlags(t *testing.T) {
	opts := Options{
		ConfigPath: filepath.Join(string(filepath.Separator), "etc", "termcp-relay", "config.toml"),
		LogFile:    filepath.Join(string(filepath.Separator), "var", "log", "termcp-relay.log"),
		LogLevel:   "debug",
		WorkDir:    filepath.Join(string(filepath.Separator), "home", "relay"),
	}
	cfg := kserviceConfig(opts)

	if cfg.Name != Name {
		t.Errorf("Name = %q, want %q", cfg.Name, Name)
	}
	wantArgs := []string{"--config", opts.ConfigPath, "--log-file", opts.LogFile, "--work-dir", opts.WorkDir, "--log-level", "debug"}
	if !reflect.DeepEqual(cfg.Arguments, wantArgs) {
		t.Errorf("Arguments = %q, want %q", cfg.Arguments, wantArgs)
	}
	if wantDir := filepath.Dir(opts.ConfigPath); cfg.WorkingDirectory != wantDir {
		t.Errorf("WorkingDirectory = %q, want %q", cfg.WorkingDirectory, wantDir)
	}
}

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

func TestResolveWorkDirPrecedence(t *testing.T) {
	cfgDir := filepath.Join(string(filepath.Separator), "opt", "relay")
	cfgPath := filepath.Join(cfgDir, "config.toml")

	// 1. server.work_dir in config.toml wins over everything.
	cfg := &config.Config{Server: config.ServerConfig{WorkDir: "/explicit/from/config"}}
	opts := Options{ConfigPath: cfgPath, WorkDir: "/from/opts"}
	if got := resolveWorkDir(opts, cfg); got != "/explicit/from/config" {
		t.Errorf("got %q, want /explicit/from/config", got)
	}

	// 2. --work-dir from opts wins when config is empty.
	cfg = &config.Config{}
	if got := resolveWorkDir(opts, cfg); got != "/from/opts" {
		t.Errorf("got %q, want /from/opts", got)
	}

	// 3. Fallback to config directory when both are empty.
	opts = Options{ConfigPath: cfgPath}
	got := resolveWorkDir(opts, cfg)
	// It will either be the current user's non-service home or cfgDir; both are
	// valid useful directories, neither is a service profile.
	if isServiceProfile(got) {
		t.Errorf("resolveWorkDir fell back to service profile %q", got)
	}
}

func TestKServicConfigNameOverride(t *testing.T) {
	cfg := kserviceConfig(Options{Name: "termcp-relay-alt"})
	if cfg.Name != "termcp-relay-alt" {
		t.Errorf("Name = %q, want override", cfg.Name)
	}
	if len(cfg.Arguments) != 0 {
		t.Errorf("Arguments = %q, want none without a config path", cfg.Arguments)
	}
}

// TestQueryUninstalledService exercises the real host service manager (no root
// required): a service name that does not exist must report installed=false
// rather than an error. Skipped where no service manager is available.
func TestQueryUninstalledService(t *testing.T) {
	const name = "termcp-relay-test-definitely-not-installed"
	st, err := Query(name)
	if errors.Is(err, kservice.ErrNoServiceSystemDetected) {
		t.Skip("no service manager on this host")
	}
	if err != nil {
		t.Fatalf("Query(%q) error: %v", name, err)
	}
	if st.Installed {
		t.Fatalf("Query(%q).Installed = true, want false", name)
	}
	if st.Name != name {
		t.Fatalf("Query(%q).Name = %q", name, st.Name)
	}
}
