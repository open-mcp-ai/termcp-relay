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
//	termcp-relay service <action>     install/remove/control the system service
//	termcp-relay [--config path]      load config.toml and serve
//
// Run "termcp-relay help" or "termcp-relay -h" for full usage.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/open-mcp-ai/termcp-relay/internal/config"
	relayservice "github.com/open-mcp-ai/termcp-relay/internal/service"
)

const usage = `termcp-relay — standalone SSH daemon

Usage:
  termcp-relay [flags]              Start the SSH server.
  termcp-relay init [config-path]   Write a template config.toml (refuses to overwrite).
  termcp-relay service <action>     Install/remove/control the system service.
  termcp-relay version              Print the build version.
  termcp-relay help, -h, --help     Show this help.

Flags:
  --config path   Path to config.toml (default "config.toml").
  --log-level lvl Override server.log_level: debug | info | warn | error.
  --log-file path Append logs to this file (in addition to stderr).

The host key and authorized_keys paths in config.toml are resolved relative to
the config file's directory, so co-locate them with config.toml.

Run "termcp-relay service help" for service management (install, status, ...).
`

// version identifies the build. Plain `go build` leaves it as "dev"; the
// Makefile stamps it from git:
//
//	go build -ldflags "-X main.version=v1.2.3" .
var version = "dev"

func main() {
	args := os.Args[1:]

	if len(args) >= 1 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Print(usage)
			return
		case "version", "--version":
			fmt.Printf("termcp-relay %s\n", version)
			return
		case "init":
			runInit(args[1:])
			return
		case "service":
			os.Exit(relayservice.Command(args[1:]))
		}
	}

	fs := flag.NewFlagSet("termcp-relay", flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config.toml")
	logLevelOverride := fs.String("log-level", "", "Override server.log_level (debug|info|warn|error)")
	logFile := fs.String("log-file", "", "Append logs to this file (in addition to stderr)")
	workDir := fs.String("work-dir", "", "Working directory for SSH sessions (overrides home / default)")
	fs.Usage = func() { fmt.Print(usage) }
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	opts := relayservice.Options{
		ConfigPath: *configPath,
		LogLevel:   *logLevelOverride,
		LogFile:    *logFile,
		WorkDir:    *workDir,
	}

	// Under a service manager (or on hosts without one) kservice drives the
	// lifecycle: Start on boot, Stop on SIGTERM/service-stop. Started from a
	// terminal, Run attaches to the console and stops on Ctrl-C.
	svc, err := relayservice.New(opts)
	if err != nil {
		if errors.Is(err, relayservice.ErrNoServiceSystemDetected) {
			if err := relayservice.RunForeground(opts); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "service: %v\n", err)
		os.Exit(1)
	}
	if err := svc.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
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
	fmt.Printf("or install it as a service: termcp-relay service install --config %s\n", *configPath)
}
