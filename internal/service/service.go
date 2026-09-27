// Package service integrates termcp-relay with the host service manager
// (Windows SCM; systemd/Upstart/SysV/OpenRC on Linux; launchd on macOS) via
// github.com/kardianos/service.
//
// One Program type serves both execution modes: when the service manager starts
// the executable, Program.Start boots the SSH server and Program.Stop tears it
// down; when the executable is started from a terminal the library's Run helper
// attaches it to the console and stops it on SIGINT/SIGTERM.
package service

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"

	kservice "github.com/kardianos/service"

	"github.com/open-mcp-ai/termcp-relay/internal/config"
	"github.com/open-mcp-ai/termcp-relay/internal/sshserver"
)

// ErrNoServiceSystemDetected is returned by New on hosts without a supported
// service manager (containers, exotic init systems). Callers should fall back
// to RunForeground.
var ErrNoServiceSystemDetected = kservice.ErrNoServiceSystemDetected

// Identity registered with the host service manager. The same name is used on
// every platform so status/uninstall commands address the same service.
const (
	Name        = "termcp-relay"
	DisplayName = "termcp-relay SSH daemon"
	Description = "Standalone SSH daemon: PTY shells, exec, SFTP and TCP port forwarding."
)

// Options are the runtime settings for one relay instance. "service install"
// bakes the same flags into the service definition, so the manager starts the
// daemon exactly like a manual run.
type Options struct {
	Name       string // service name; defaults to Name (override allows several instances)
	ConfigPath string // --config; absolute path when installed
	LogLevel   string // --log-level override; empty uses server.log_level
	LogFile    string // --log-file; append logs here in addition to stderr
	WorkDir    string // --work-dir; session working directory (config server.work_dir wins)
	System     bool   // --system: run as the platform system account (LocalSystem/root)
	UserName   string // --user; service account (systemd User= / SCM ServiceStartName)
	Password   string // account password; Windows only, never logged or persisted

	// NeedsCredential records that the account password must be carried across
	// the UAC re-launch. Set by the account resolver; not a CLI flag.
	NeedsCredential bool
}

// serviceName returns the effective service name.
func (o Options) serviceName() string {
	if strings.TrimSpace(o.Name) != "" {
		return strings.TrimSpace(o.Name)
	}
	return Name
}

// validate rejects combinations the host platform cannot honor.
func (o Options) validate() error {
	if runtime.GOOS != "windows" && o.Password != "" {
		return errors.New("account passwords are only used on Windows; remove the password input")
	}
	return nil
}

// resolveWorkDir decides the directory SSH sessions start in, most specific
// first:
//
//  1. server.work_dir in config.toml — the user's runtime knob, so editing the
//     config and restarting is enough (no reinstall);
//  2. --work-dir, i.e. what `service install` baked into the service
//     definition (the installing user's home by default);
//  3. the home directory of the account the daemon runs as, unless that is a
//     Windows service profile (LocalSystem's C:\Windows\System32\config\
//     systemprofile is a poor place to land);
//  4. the config file's directory — never leave sessions in the service
//     manager's default working directory, C:\Windows\System32.
func resolveWorkDir(opts Options, cfg *config.Config) string {
	if d := strings.TrimSpace(cfg.Server.WorkDir); d != "" {
		return d
	}
	if d := strings.TrimSpace(opts.WorkDir); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && !isServiceProfile(home) {
		if st, err := os.Stat(home); err == nil && st.IsDir() {
			return home
		}
	}
	return filepath.Dir(absOrSelf(opts.ConfigPath))
}

// isServiceProfile reports whether dir is a Windows service account's profile
// (…\config\systemprofile, …\ServiceProfiles\LocalService, …), which is not a
// useful home for an SSH session. Always false elsewhere.
func isServiceProfile(dir string) bool {
	// Normalize separators by hand: filepath.ToSlash only rewrites the host's own
	// separator, so on Linux/macOS a Windows path keeps its backslashes and never
	// matches the checks below. This function must recognize Windows profiles on
	// every platform (resolveWorkDir calls it unconditionally).
	lower := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(dir), `\`, "/"))
	if lower == "" {
		return true
	}
	return strings.Contains(lower, "/config/systemprofile") ||
		strings.Contains(lower, "/serviceprofiles/")
}

// Program implements kservice.Interface. The SSH server lives for as long as
// the service manager keeps the process alive.
type Program struct {
	Opts Options

	mu        sync.Mutex
	srv       *sshserver.Server
	logCloser io.Closer
}

// Start boots the SSH server. It must not block: binding the listener is fast,
// and the accept loop runs in its own goroutine inside sshserver.
func (p *Program) Start(_ kservice.Service) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		return errors.New("termcp-relay: service already started")
	}

	cfg, err := config.Load(p.Opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config %q: %w", p.Opts.ConfigPath, err)
	}
	level := cfg.Server.LogLevel
	if strings.TrimSpace(p.Opts.LogLevel) != "" {
		level = strings.TrimSpace(p.Opts.LogLevel)
	}
	handler, closer, err := newLogHandler(level, p.Opts.LogFile)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(handler))
	p.logCloser = closer

	workDir := resolveWorkDir(p.Opts, cfg)

	slog.Info("termcp-relay starting",
		"config", absOrSelf(p.Opts.ConfigPath),
		"addr", cfg.Server.Addr,
		"work_dir", workDir,
		"host_key", cfg.Server.HostKey,
		"authorized_keys", cfg.Auth.AuthorizedKeys,
		"no_auth", cfg.Auth.NoAuth,
		"password_auth", cfg.Auth.Password != "",
		"publickey_auth", cfg.Auth.AuthorizedKeys != "",
		"local_forward", cfg.Server.AllowLocalFwd,
		"remote_forward", cfg.Server.AllowRemoteFwd,
	)

	auth := &sshserver.Auth{
		NoAuth:   cfg.Auth.NoAuth,
		Username: cfg.Auth.Username,
		Password: cfg.Auth.Password,
	}
	if err := auth.LoadAuthorizedKeys(cfg.Auth.AuthorizedKeys); err != nil {
		return fmt.Errorf("load authorized_keys %q: %w", cfg.Auth.AuthorizedKeys, err)
	}

	srv, err := sshserver.New(sshserver.Options{
		Addr:           cfg.Server.Addr,
		HostKeyPath:    cfg.Server.HostKey,
		Auth:           auth,
		ShellOverride:  cfg.Server.Shell,
		WorkDir:        workDir,
		Banner:         cfg.Server.Banner,
		AllowLocalFwd:  cfg.Server.AllowLocalFwd,
		AllowRemoteFwd: cfg.Server.AllowRemoteFwd,
	})
	if err != nil {
		return fmt.Errorf("build ssh server: %w", err)
	}
	if err := srv.Start(); err != nil {
		return err
	}
	p.srv = srv

	bound := srv.Addr()
	slog.Info("termcp-relay ready", "listen", bound)
	loginUser := currentUser()
	if cfg.Auth.Username != "" {
		loginUser = cfg.Auth.Username
	}
	for _, ip := range listenableIPv4Hints(bound) {
		slog.Info(fmt.Sprintf("connect: ssh -p %s %s@%s", portOf(bound), loginUser, ip))
	}
	return nil
}

// Stop shuts the SSH server down and closes the optional log file.
func (p *Program) Stop(_ kservice.Service) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var stopErr error
	if p.srv != nil {
		slog.Info("shutting down")
		if err := p.srv.Stop(); err != nil && !errors.Is(err, net.ErrClosed) {
			stopErr = err
		}
		p.srv = nil
	}
	if p.logCloser != nil {
		_ = p.logCloser.Close()
		p.logCloser = nil
	}
	return stopErr
}

// New builds the kservice.Service for these options. The returned value backs
// Run, Install, Uninstall, Start, Stop, Restart and Status.
func New(opts Options) (kservice.Service, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	return kservice.New(&Program{Opts: opts}, kserviceConfig(opts))
}

// kserviceConfig turns relay options into a service definition. Paths must
// already be absolute: the service manager starts the process with a different
// working directory than the installing shell.
func kserviceConfig(opts Options) *kservice.Config {
	cfg := &kservice.Config{
		Name:        opts.serviceName(),
		DisplayName: DisplayName,
		Description: Description,
		Option:      kservice.KeyValue{},
	}
	if opts.ConfigPath != "" {
		cfg.Arguments = append(cfg.Arguments, "--config", opts.ConfigPath)
		cfg.WorkingDirectory = filepath.Dir(opts.ConfigPath)
	}
	if opts.LogFile != "" {
		cfg.Arguments = append(cfg.Arguments, "--log-file", opts.LogFile)
	}
	if opts.WorkDir != "" {
		cfg.Arguments = append(cfg.Arguments, "--work-dir", opts.WorkDir)
	}
	if opts.LogLevel != "" {
		cfg.Arguments = append(cfg.Arguments, "--log-level", opts.LogLevel)
	}
	if opts.UserName != "" {
		// systemd User= / launchd UserName / SCM ServiceStartName. Windows
		// additionally needs the account password, which the SCM stores as an
		// LSA secret (see the "Password" option below).
		cfg.UserName = opts.UserName
		if runtime.GOOS == "windows" {
			cfg.Option["Password"] = opts.Password
		}
	}

	switch runtime.GOOS {
	case "linux":
		// systemd: Restart=on-failure (covers SysV/OpenRC, which ignore it).
		cfg.Option["Restart"] = "on-failure"
	case "darwin":
		// launchd: restart on crash and start as soon as the job is loaded.
		cfg.Option["KeepAlive"] = true
		cfg.Option["RunAtLoad"] = true
	}
	applyPlatformOptions(cfg.Option)
	return cfg
}

// Status reports whether the service is registered with the host service
// manager and whether it is currently running.
type Status struct {
	Name      string
	Installed bool
	Running   bool
	Detail    string // extra context, e.g. a failed unit
}

// Query inspects the host service manager for the named service (default
// Name). It answers the "is this machine already running termcp-relay as a
// service?" question without touching config or network state, so it is safe to
// run as an unprivileged user.
func Query(name string) (Status, error) {
	if strings.TrimSpace(name) == "" {
		name = Name
	}
	return queryService(strings.TrimSpace(name))
}

// RunForeground runs the relay attached to the current terminal. It is the
// fallback for hosts without a detected service manager (containers, exotic
// init systems), where kservice.New returns ErrNoServiceSystemDetected.
func RunForeground(opts Options) error {
	p := &Program{Opts: opts}
	if err := p.Start(nil); err != nil {
		return err
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	return p.Stop(nil)
}

// newLogHandler returns the slog handler plus an optional closer for the log
// file. Logs always go to stderr (journald captures it for systemd units and
// launchd redirects it on macOS); --log-file adds a durable copy, which is the
// only way to see logs when running as a Windows service.
func newLogHandler(level, logFile string) (slog.Handler, io.Closer, error) {
	var minLevel slog.Level
	switch strings.TrimSpace(level) {
	case "debug":
		minLevel = slog.LevelDebug
	case "warn":
		minLevel = slog.LevelWarn
	case "error":
		minLevel = slog.LevelError
	default:
		minLevel = slog.LevelInfo
	}

	var w io.Writer = os.Stderr
	var closer io.Closer
	if strings.TrimSpace(logFile) != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file %q: %w", logFile, err)
		}
		// File first: stderr writes fail under the Windows SCM and must not
		// suppress the durable copy.
		w = io.MultiWriter(f, os.Stderr)
		closer = f
	}
	return slog.NewTextHandler(w, &slog.HandlerOptions{Level: minLevel}), closer, nil
}

// listenableIPv4Hints returns friendly connection hints derived from the bound
// address. For 0.0.0.0 it enumerates non-loopback IPv4s on the machine.
func listenableIPv4Hints(bound string) []string {
	host, _, err := net.SplitHostPort(bound)
	if err != nil {
		return nil
	}
	host = strings.TrimSpace(host)
	if host != "" && host != "0.0.0.0" && host != "::" && host != "[::]" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			v4 := ip.To4()
			if v4 == nil || v4.IsLoopback() {
				continue
			}
			if !seen[v4.String()] {
				seen[v4.String()] = true
				out = append(out, v4.String())
			}
		}
	}
	sort.Strings(out)
	return out
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

func currentUser() string {
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	if u := strings.TrimSpace(os.Getenv("USERNAME")); u != "" {
		return u
	}
	return "user"
}

func absOrSelf(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
