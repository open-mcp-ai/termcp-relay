// Command termcp-relay is a standalone SSH daemon extracted from termcp's
// built-in sshd. It listens on a TCP port and serves interactive PTY shells,
// exec channels, the SFTP subsystem, and direct/reverse TCP port forwarding —
// the full feature set of termcp's internal SSH server, exposed over the
// network instead of in-process pipes.
//
// All configuration lives in a TOML file (default: config.toml in the working
// directory). The host key and authorized_keys files are co-located with the
// config file: their paths in config.toml are resolved relative to the file's
// own directory, so one folder holds config.toml + host_key.pem + authorized_keys.
//
// Usage:
//
//	termcp-relay init [config-path]   write a template config.toml (refuses to overwrite)
//	termcp-relay [--config path]      load config.toml and serve
//
// Run "termcp-relay help" or "termcp-relay -h" for full usage.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/open-mcp-ai/termcp-relay/internal/config"
	"github.com/open-mcp-ai/termcp-relay/internal/sshserver"
)

const usage = `termcp-relay — standalone SSH daemon

Usage:
  termcp-relay [flags]              Start the SSH server.
  termcp-relay init [config-path]   Write a template config.toml (refuses to overwrite).
  termcp-relay help, -h, --help     Show this help.

Flags:
  --config path   Path to config.toml (default "config.toml").
  --log-level lvl Override server.log_level: debug | info | warn | error.

The host key and authorized_keys paths in config.toml are resolved relative to
the config file's directory, so co-locate them with config.toml.
`

func main() {
	args := os.Args[1:]

	if len(args) >= 1 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Print(usage)
			return
		case "init":
			runInit(args[1:])
			return
		}
	}

	fs := flag.NewFlagSet("termcp-relay", flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config.toml")
	logLevelOverride := fs.String("log-level", "", "Override server.log_level (debug|info|warn|error)")
	fs.Usage = func() { fmt.Print(usage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config %q: %v\n", *configPath, err)
		os.Exit(2)
	}
	if strings.TrimSpace(*logLevelOverride) != "" {
		cfg.Server.LogLevel = strings.TrimSpace(*logLevelOverride)
	}

	slog.SetDefault(slog.New(buildLogHandler(cfg.Server.LogLevel)))
	slog.Info("termcp-relay starting",
		"config", absOrSelf(*configPath),
		"addr", cfg.Server.Addr,
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
		slog.Error("failed to load authorized_keys", "path", cfg.Auth.AuthorizedKeys, "err", err)
		os.Exit(1)
	}

	srv, err := sshserver.New(sshserver.Options{
		Addr:           cfg.Server.Addr,
		HostKeyPath:    cfg.Server.HostKey,
		Auth:           auth,
		ShellOverride:  cfg.Server.Shell,
		Banner:         cfg.Server.Banner,
		AllowLocalFwd:  cfg.Server.AllowLocalFwd,
		AllowRemoteFwd: cfg.Server.AllowRemoteFwd,
	})
	if err != nil {
		slog.Error("failed to build ssh server", "err", err)
		os.Exit(1)
	}
	if err := srv.Start(); err != nil {
		slog.Error("failed to start ssh server", "err", err)
		os.Exit(1)
	}

	bound := srv.Addr()
	slog.Info("termcp-relay ready", "listen", bound)
	loginUser := currentUser()
	if cfg.Auth.Username != "" {
		loginUser = cfg.Auth.Username
	}
	for _, ip := range listenableIPv4Hints(bound) {
		slog.Info(fmt.Sprintf("connect: ssh -p %s %s@%s", portOf(bound), loginUser, ip))
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	slog.Info("shutting down")
	if err := srv.Stop(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Error("ssh server stop", "err", err)
	}
}

// runInit writes a template config.toml. It accepts an optional positional
// path (`termcp-relay init [path]`) and a --config flag for compatibility.
// It refuses to clobber an existing file so edits and generated host keys
// are never overwritten.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to write config.toml")
	fs.Usage = func() { fmt.Print(usage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	// Positional argument overrides the --config default.
	if fs.NArg() >= 1 {
		*configPath = fs.Arg(0)
	}

	if err := config.WriteTemplate(*configPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(os.Stderr, "init: %s already exists (remove it first to regenerate)\n", *configPath)
		} else {
			fmt.Fprintf(os.Stderr, "init: %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Printf("wrote %s\nedit it, then run: termcp-relay --config %s\n", *configPath, *configPath)
}

func buildLogHandler(level string) slog.Handler {
	var minLevel slog.Level
	switch level {
	case "debug":
		minLevel = slog.LevelDebug
	case "warn":
		minLevel = slog.LevelWarn
	case "error":
		minLevel = slog.LevelError
	default:
		minLevel = slog.LevelInfo
	}
	return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: minLevel})
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
