// Package config loads termcp-relay configuration from a TOML file.
//
// All server settings live in config.toml. The host key and authorized_keys
// files are co-located with config.toml: paths in the TOML are resolved
// relative to the config file's directory, so a single folder holds
// config.toml plus its host_key and authorized_keys files.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ServerConfig holds SSH server / listener settings.
type ServerConfig struct {
	Addr             string `toml:"addr"`
	HostKey          string `toml:"host_key"`
	Shell            string `toml:"shell"`
	Banner           string `toml:"banner"`
	LogLevel         string `toml:"log_level"`
	AllowLocalFwd   bool   `toml:"allow_local_forward"`
	AllowRemoteFwd  bool   `toml:"allow_remote_forward"`
}

// AuthConfig holds authentication settings.
type AuthConfig struct {
	NoAuth         bool   `toml:"no_auth"`
	Username       string `toml:"username"`
	Password       string `toml:"password"`
	AuthorizedKeys string `toml:"authorized_keys"`
}

// Config is the parsed and resolved configuration.
type Config struct {
	Server    ServerConfig `toml:"server"`
	Auth      AuthConfig   `toml:"auth"`
	configDir string // absolute dir of the TOML file, for path resolution
}

// Defaults returns a Config populated with sensible defaults, ready to be
// written out as a template config.toml.
func Defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Addr:            "0.0.0.0:2222",
			HostKey:         "host_key.pem",
			LogLevel:        "info",
			AllowLocalFwd:  true,
			AllowRemoteFwd: true,
		},
		Auth: AuthConfig{},
	}
}

// Load reads and parses the TOML file at path, applies defaults for any unset
// fields, and resolves the host_key and authorized_keys paths relative to the
// config file's directory. Missing fields keep their defaults.
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		path = "config.toml"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	cfg := Defaults()
	cfg.configDir = filepath.Dir(abs)

	if _, err := toml.DecodeFile(abs, cfg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", abs, err)
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalize fills empty/zero fields with defaults and resolves relative paths.
func (c *Config) normalize() {
	if strings.TrimSpace(c.Server.Addr) == "" {
		c.Server.Addr = "0.0.0.0:2222"
	}
	if strings.TrimSpace(c.Server.HostKey) == "" {
		c.Server.HostKey = "host_key.pem"
	}
	if strings.TrimSpace(c.Server.LogLevel) == "" {
		c.Server.LogLevel = "info"
	}
	// Resolve relative paths against the config file's directory so the key
	// files live alongside config.toml ("密钥文件也放里面").
	c.Server.HostKey = c.resolve(c.Server.HostKey)
	if strings.TrimSpace(c.Auth.AuthorizedKeys) != "" {
		c.Auth.AuthorizedKeys = c.resolve(c.Auth.AuthorizedKeys)
	}
}

func (c *Config) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if c.configDir == "" {
		return p
	}
	return filepath.Join(c.configDir, p)
}

// Validate checks the resolved config for correctness.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server.Addr) == "" {
		return errors.New("server.addr must not be empty")
	}
	if c.Auth.NoAuth && (c.Auth.Password != "" || c.Auth.AuthorizedKeys != "") {
		return errors.New("auth.no_auth is mutually exclusive with auth.password / auth.authorized_keys")
	}
	if !c.Auth.NoAuth && c.Auth.Password == "" && c.Auth.AuthorizedKeys == "" {
		host, _ := splitHostPort(c.Server.Addr)
		if !isLoopback(host) {
			return errors.New("refusing to run without authentication on a non-loopback address; set auth.no_auth=true or configure auth.password / auth.authorized_keys")
		}
	}
	if c.Auth.AuthorizedKeys != "" {
		if _, err := os.Stat(c.Auth.AuthorizedKeys); err != nil {
			return fmt.Errorf("auth.authorized_keys file %q: %w", c.Auth.AuthorizedKeys, err)
		}
	}
	switch c.Server.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("server.log_level %q must be debug|info|warn|error", c.Server.LogLevel)
	}
	return nil
}

// WriteTemplate writes a commented, ready-to-edit config.toml to path (creating
// parent dirs). It refuses to clobber an existing file. The host key itself is
// not generated here — it is created on first run when the server starts.
func WriteTemplate(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(configTemplate)
	return err
}

// configTemplate is the commented template written by WriteTemplate and shipped
// as config.example.toml. Keep both in sync.
const configTemplate = `# termcp-relay configuration file.
#
# Paths for host_key and authorized_keys are resolved relative to this file's
# own directory, so co-locate the key files here. After editing, start with:
#
#   termcp-relay --config config.toml

[server]
# Address to listen on. "0.0.0.0:2222" accepts from the network;
# "127.0.0.1:2222" is local-only.
addr = "0.0.0.0:2222"

# RSA host key. Auto-generated (RSA-2048, 0600 perms) on first run if the file
# is missing, then reused for a stable server fingerprint. No external tools
# (ssh-keygen) required.
host_key = "host_key.pem"

# Override the interactive shell, whitespace-split into argv, e.g. "pwsh -NoLogo".
# Leave empty to auto-detect: pwsh -> powershell -> cmd on Windows; $SHELL on Unix.
shell = ""

# Banner shown to the client before authentication.
banner = "termcp-relay"

# Log level: debug | info | warn | error
log_level = "info"

# Permit ssh -L (direct-tcpip) local port forwarding.
allow_local_forward = true

# Permit ssh -R (tcpip-forward) reverse port forwarding.
allow_remote_forward = true


# Authentication. Configure at least one method, or set no_auth = true. Running
# without auth on a non-loopback addr is refused.
[auth]
# Accept every client without challenge. Refused on non-loopback binds.
no_auth = false

# Password auth (constant-time compared). When username is empty, any username
# is accepted as long as the password matches.
username = ""
password = ""

# Public-key auth: path to an OpenSSH authorized_keys file (one key per line).
# Uncomment and point at a file to enable.
# authorized_keys = "authorized_keys"
`

func splitHostPort(addr string) (string, string) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, ""
	}
	return addr[:i], addr[i+1:]
}

func isLoopback(host string) bool {
	switch strings.TrimSpace(host) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	default:
		return false
	}
}
