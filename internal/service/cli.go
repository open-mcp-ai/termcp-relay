package service

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	kservice "github.com/kardianos/service"

	"github.com/open-mcp-ai/termcp-relay/internal/config"
)

// Usage is the help text for the "service" subcommand tree.
const Usage = `termcp-relay service — install/remove the relay as a system service

Usage:
  termcp-relay service install   [flags]   Register and enable the service.
  termcp-relay service uninstall [flags]   Stop and remove the service.
  termcp-relay service start     [flags]   Start the installed service.
  termcp-relay service stop      [flags]   Stop the running service.
  termcp-relay service restart   [flags]   Restart the service.
  termcp-relay service status    [flags]   Show whether it is installed/running.
  termcp-relay service help                Show this help.

Flags:
  --config path    Path to config.toml (default "config.toml").
                   Stored as an absolute path at install time, because the
                   service manager starts the process in a different directory.
  --log-file path  Append logs to this file (in addition to stderr).
                   Required to see logs on Windows, where services have no
                   console. Defaults to <config dir>/termcp-relay.log on Windows.
  --work-dir path  Working directory for SSH sessions. Defaults to the
                   installing user's home (e.g. C:\Users\<you> on Windows),
                   so clients drop into your files instead of System32. Can
                   also be set via server.work_dir in config.toml.
  --log-level lvl  Override server.log_level: debug | info | warn | error.
  --system         Run as the platform system account: LocalSystem on Windows,
                   root on Unix. This is already the default.
  --user name      Run as a dedicated account instead of LocalSystem/root.
                   On Windows, non-built-in accounts prompt for their password.
  --name name      Service name (default "termcp-relay"), for multiple instances.

Service backends: Windows SCM; systemd / Upstart / SysV / OpenRC on Linux;
launchd on macOS.

Service account: defaults to LocalSystem on Windows (like nssm) and root on
Linux/macOS, so installation is unattended and needs no password. Use --user
to isolate into a dedicated lower-privileged account if desired. SSH sessions
start in --work-dir (by default your user home) so running commands feels
just like a local login.

Privileges: install/uninstall/start/stop/restart need an elevated account. On
Windows the command re-launches itself through the UAC prompt (config is
validated first, so a typo costs no prompt); on Linux/macOS run it with sudo.
status never needs privileges and works everywhere.

Check the current state at any time:
  termcp-relay service status
`

// elevatedOutputFlag is the hidden flag used to hand the elevated child a file
// to write its output to. The child's console is hidden and exits immediately,
// so without this its diagnostics would be lost.
const elevatedOutputFlag = "--elevated-output"

// credentialFlag carries an account password from the unelevated parent to the
// elevated child. The SCM needs the password to configure ServiceStartName, but
// it must never travel on the command line: anything in argv is visible to any
// user via WMI/Task Manager. The parent therefore writes it to a 0600 temp file
// that the child reads and deletes before doing anything else.
const credentialFlag = "--elevated-credential"

// Command runs the "service" subcommand and returns a process exit code.
func Command(args []string) int {
	// An elevated child carries the hidden output flag; it must never try to
	// elevate itself again.
	isElevatedChild := hasFlag(args, elevatedOutputFlag)
	args = redirectElevatedOutput(args)
	args, credPath := takeCredentialArg(args)
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, Usage)
		return 2
	}
	action := args[0]
	switch action {
	case "help", "-h", "--help":
		fmt.Print(Usage)
		return 0
	}

	fs := flag.NewFlagSet("service "+action, flag.ExitOnError)
	configPath := fs.String("config", "config.toml", "Path to config.toml")
	logFile := fs.String("log-file", "", "Append logs to this file")
	logLevel := fs.String("log-level", "", "Override server.log_level")
	workDir := fs.String("work-dir", "", "Working directory for SSH sessions (default: installer's home)")
	userName := fs.String("user", "", "Run as NAME (Windows asks for its password); \"current\" = the installing account")
	system := fs.Bool("system", false, "Run as the system account (LocalSystem on Windows, root on Unix; default)")
	name := fs.String("name", Name, "Service name")
	fs.Usage = func() { fmt.Print(Usage) }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *system && strings.TrimSpace(*userName) != "" {
		fmt.Fprint(os.Stderr, "service: --system and --user are mutually exclusive\n")
		return 2
	}

	opts := Options{Name: *name, LogLevel: *logLevel, LogFile: *logFile, WorkDir: *workDir, UserName: *userName, System: *system}
	// --system clears any account so the platform default (root) applies on
	// Unix; on Windows the account resolver maps it to LocalSystem.
	if *system {
		opts.UserName = ""
	}
	// status/start/stop only need a path to print in hints; install additionally
	// requires it to exist and stores it as an absolute path.
	opts.ConfigPath = *configPath

	switch action {
	case "install":
		opts, err := prepareInstall(opts, credPath)
		if err != nil {
			return fail(err)
		}
		if code, done := maybeElevate(action, opts, isElevatedChild); done {
			return code
		}
		return install(opts)
	case "uninstall", "start", "stop", "restart", "status":
		if code, done := maybeElevate(action, opts, isElevatedChild); done {
			return code
		}
		return control(action, opts)
	default:
		fmt.Fprintf(os.Stderr, "service: unknown action %q\n\n%s", action, Usage)
		return 2
	}
}

// prepareInstall resolves the paths an installed service must remember and
// collects the service account's password when one is needed. It runs before
// any elevation so a broken config, typo or empty password never costs the user
// a UAC prompt.
func prepareInstall(opts Options, credPath string) (Options, error) {
	abs, err := resolveConfigPath(opts.ConfigPath)
	if err != nil {
		return opts, err
	}
	opts.ConfigPath = abs
	if _, err := config.Load(abs); err != nil {
		return opts, fmt.Errorf("config %q is not usable: %w", abs, err)
	}
	if opts.LogFile == "" && runtime.GOOS == "windows" {
		// A Windows service has no console, so stderr goes nowhere.
		opts.LogFile = filepath.Join(filepath.Dir(abs), Name+".log")
	}
	if opts.LogFile != "" {
		logAbs, err := filepath.Abs(opts.LogFile)
		if err != nil {
			return opts, fmt.Errorf("resolve --log-file: %w", err)
		}
		opts.LogFile = logAbs
	}

	// Default the session working directory to the installing user's home, so
	// an installed service (typically LocalSystem) drops SSH clients into the
	// user's own directory instead of C:\Windows\System32. config server.work_dir
	// overrides it at runtime.
	if strings.TrimSpace(opts.WorkDir) == "" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			opts.WorkDir = home
		}
	}
	if strings.TrimSpace(opts.WorkDir) != "" {
		wd, err := filepath.Abs(opts.WorkDir)
		if err != nil {
			return opts, fmt.Errorf("resolve --work-dir: %w", err)
		}
		opts.WorkDir = wd
	}

	// The elevated child receives the password through a file, not argv.
	if err := resolveAccount(&opts, credPath); err != nil {
		return opts, err
	}
	return opts, nil
}

// takeCredentialArg strips the hidden --elevated-credential flag and returns
// the path it carried, if any.
func takeCredentialArg(args []string) ([]string, string) {
	pruned := make([]string, 0, len(args))
	path := ""
	for i := 0; i < len(args); i++ {
		if args[i] == credentialFlag && i+1 < len(args) {
			path = args[i+1]
			i++
			continue
		}
		pruned = append(pruned, args[i])
	}
	return pruned, path
}

// resolveAccount fills in the Windows service account and its password.
//
// Default (no --system, no --user): LocalSystem, exactly like nssm's default.
// It is a built-in account, so there is no password to prompt for, no
// SeServiceLogonRight to grant and nothing to expire — the service installs and
// starts unattended. --user NAME selects a specific account instead (the SCM
// needs its password, and that password must be right at first start).
//
// A service configured with ServiceStartName only starts if the SCM also holds
// that account's password (an empty password is allowed and works for accounts
// without one), so any non-built-in account prompts for it. The built-in
// accounts SYSTEM/LocalService/NetworkService are passwordless by design. On
// Unix the account name alone is enough.
func resolveAccount(opts *Options, credPath string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	// The elevated child gets the password the parent already collected.
	if credPath != "" {
		pw, err := readCredentialFile(credPath)
		if err != nil {
			return err
		}
		opts.Password = pw
		opts.NeedsCredential = false
		return nil
	}

	user := strings.TrimSpace(opts.UserName)
	// LocalSystem is the default: empty UserName means "let the SCM use its
	// built-in LocalSystem account", which needs no password at all.
	if user == "" || opts.System || builtinServiceAccount(user) {
		if builtinServiceAccount(user) && !strings.EqualFold(user, "system") && !strings.EqualFold(user, `NT AUTHORITY\SYSTEM`) && !strings.EqualFold(user, "LocalSystem") {
			// LocalService / NetworkService: keep the name, still no password.
			opts.Password = ""
			return nil
		}
		opts.UserName = ""
		opts.Password = ""
		return nil
	}

	// Explicit --user current: the account running the install.
	if strings.EqualFold(user, "current") {
		token, err := currentUserToken()
		if err != nil {
			return fmt.Errorf("determine current account: %w", err)
		}
		user = token
		opts.UserName = token
	}
	pw, ok := promptVerifiedPassword(user)
	if !ok {
		return errors.New("no valid password supplied for " + user)
	}
	opts.Password = pw
	// Even an empty password must reach the elevated child: without this flag
	// the child would try to prompt on a console it does not have.
	opts.NeedsCredential = true
	return nil
}

// promptVerifiedPassword asks for the account password and checks it with the
// same credential check the SCM uses. A wrong password is reported immediately
// and the prompt repeats, so a typo never turns into a service that installs
// fine but cannot start.
func promptVerifiedPassword(user string) (string, bool) {
	const attempts = 3
	for i := 1; i <= attempts; i++ {
		pw, err := promptPassword(user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "service: %v\n", err)
			return "", false
		}
		if err := verifyLogon(user, pw); err != nil {
			fmt.Fprintf(os.Stderr, "service: %v\n", err)
			if i < attempts {
				fmt.Fprintf(os.Stderr, "try again (%d/%d)\n", i+1, attempts)
			}
			continue
		}
		return pw, true
	}
	return "", false
}

// builtinServiceAccount reports whether name is one of the passwordless
// accounts the SCM knows natively.
func builtinServiceAccount(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "LOCALSYSTEM", `NT AUTHORITY\SYSTEM`, "SYSTEM",
		"LOCALSERVICE", `NT AUTHORITY\LOCALSERVICE`,
		"NETWORKSERVICE", `NT AUTHORITY\NETWORKSERVICE`:
		return true
	}
	return false
}

// requiresAdmin lists the actions that need an elevated token on Windows:
// every SCM write. status only reads and works unprivileged.
func requiresAdmin(action string) bool {
	switch action {
	case "install", "uninstall", "start", "stop", "restart":
		return true
	}
	return false
}

// maybeElevate re-launches the command through the UAC prompt when the host
// requires it, replays the child's output, and reports its exit code. The
// second return value reports whether the caller should stop.
func maybeElevate(action string, opts Options, isElevatedChild bool) (int, bool) {
	if isElevatedChild || !requiresAdmin(action) || elevated() {
		return 0, false
	}
	// Nothing to control (and no reason to prompt) when the service is absent;
	// the unprivileged command below reports that cleanly.
	if action != "install" {
		if st, err := Query(opts.serviceName()); err == nil && !st.Installed {
			return 0, false
		}
	}
	fmt.Fprintf(os.Stderr, "administrator privileges are required; asking for elevation (UAC)...\n")
	child := childArgs(action, opts)
	if opts.NeedsCredential {
		// Hand the password to the elevated child through a private file: argv
		// is visible to every user on the machine. The child deletes it.
		credFile, err := writeCredentialFile(opts.Password)
		if err != nil {
			fmt.Fprintf(os.Stderr, "service: %v\n", err)
			return 1, true
		}
		defer os.Remove(credFile)
		child = append(child, credentialFlag, credFile)
	}
	code, err := relaunchElevated(child)
	if err != nil {
		fmt.Fprintf(os.Stderr, "service: %v\nhint: open an Administrator terminal and rerun the command\n", err)
		return 1, true
	}
	return code, true
}

// hasFlag reports whether name appears in args.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// childArgs rebuilds the command line for the elevated instance from the
// already-parsed and resolved options.
func childArgs(action string, opts Options) []string {
	args := []string{"service", action, "--config", opts.ConfigPath}
	if opts.LogFile != "" {
		args = append(args, "--log-file", opts.LogFile)
	}
	if opts.WorkDir != "" {
		args = append(args, "--work-dir", opts.WorkDir)
	}
	if opts.LogLevel != "" {
		args = append(args, "--log-level", opts.LogLevel)
	}
	if opts.System {
		args = append(args, "--system")
	}
	if opts.UserName != "" {
		args = append(args, "--user", opts.UserName)
	}
	if opts.Name != "" && opts.Name != Name {
		args = append(args, "--name", opts.Name)
	}
	return args
}

// redirectElevatedOutput redirects stdout/stderr to the file named by the
// hidden --elevated-output flag and strips the flag. The elevated instance has
// no visible console, so the parent replays this file on its own terminal.
func redirectElevatedOutput(args []string) []string {
	pruned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == elevatedOutputFlag && i+1 < len(args) {
			if f, err := os.OpenFile(args[i+1], os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600); err == nil {
				os.Stdout = f
				os.Stderr = f
			}
			i++
			continue
		}
		pruned = append(pruned, args[i])
	}
	return pruned
}

// install registers the service and reports the resulting definition.
func install(opts Options) int {
	s, err := New(opts)
	if err != nil {
		return fail(err)
	}
	if err := s.Install(); err != nil {
		if os.IsPermission(err) {
			return fail(fmt.Errorf("%w\nhint: installing a service needs root on Linux/macOS or an Administrator shell on Windows", err))
		}
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return fail(fmt.Errorf("%w\nhint: a previous install is still registered; check with 'termcp-relay service status' and remove it with 'termcp-relay service uninstall' first", err))
		}
		// The SCM rejects a wrong --user password with a localized message;
		// surface what is most likely wrong.
		if opts.UserName != "" && logonFailure(err) {
			return fail(fmt.Errorf("%w\nhint: the Windows account password was rejected; rerun with the correct --user account/password", err))
		}
		return fail(err)
	}

	// Windows: ensure the service account has "Log on as a service" right
	// (SeServiceLogonRight). Without it the SCM refuses to start the service
	// even with the right password (event 7041: "user has not been granted the
	// requested logon type").
	if opts.UserName != "" && !builtinServiceAccount(opts.UserName) {
		if err := grantServiceLogonRight(opts.UserName); err != nil {
			fmt.Fprintf(os.Stderr, "service: warning: failed to grant SeServiceLogonRight to %s: %v\n", opts.UserName, err)
		}
	}
	fmt.Printf("installed service %q\n", opts.serviceName())
	fmt.Printf("  config:   %s\n", opts.ConfigPath)
	if opts.LogFile != "" {
		fmt.Printf("  log file: %s\n", opts.LogFile)
	}
	fmt.Printf("  run as:   %s\n", accountDisplay(opts))
	if opts.WorkDir != "" {
		fmt.Printf("  work dir: %s (SSH sessions start here)\n", opts.WorkDir)
	}
	fmt.Printf("\nwarning: SSH clients get a shell as %s — that account's rights are the\n         shell's rights. Use key auth and bind to a trusted interface.\n", accountDisplay(opts))
	fmt.Printf("\nstart it with:  termcp-relay service start%s\n", nameFlag(opts))
	fmt.Printf("check it with:  termcp-relay service status%s\n", nameFlag(opts))
	return 0
}

// accountDisplay names the account the service will run as, including the
// platform default when --user was not given.
func accountDisplay(opts Options) string {
	if opts.UserName != "" {
		return opts.UserName
	}
	if runtime.GOOS == "windows" {
		return "LocalSystem (SYSTEM)"
	}
	return "root"
}

// grantLogonRightAndRetry handles the Windows case where the SCM reports a
// logon failure not because the password is wrong but because the account lacks
// the "Log on as a service" right (SeServiceLogonRight). Windows reports both
// with the same generic error (event 7041 names the missing logon type), so:
// read the account from the service definition, grant the right, and start
// again. The original error is returned when that does not resolve it.
func grantLogonRightAndRetry(s kservice.Service, name string, startErr error) error {
	acct, err := serviceAccountName(name)
	if err != nil || acct == "" || builtinServiceAccount(acct) {
		return startErr
	}
	if held, err := serviceLogonRightHeld(acct); err == nil && held {
		// Right is present, so this really is a credential problem.
		return startErr
	}
	if err := grantServiceLogonRight(acct); err != nil {
		return startErr
	}
	// The SCM resolves the account's rights per start, so a retry sees the
	// grant immediately; the short pause only covers policy propagation.
	time.Sleep(500 * time.Millisecond)
	if err := s.Start(); err != nil {
		return startErr
	}
	fmt.Printf("granted SeServiceLogonRight (\"Log on as a service\") to %s\n", acct)
	return nil
}

// control maps a CLI action onto the kservice.Service methods.
func control(action string, opts Options) int {
	name := opts.serviceName()

	if action == "status" {
		return printStatus(opts)
	}

	s, err := New(opts)
	if err != nil {
		return fail(err)
	}

	switch action {
	case "uninstall", "start", "stop", "restart":
		if st, err := Query(name); err == nil && !st.Installed {
			fmt.Printf("service %q is not installed\n", name)
			return 0
		}
	}

	switch action {
	case "uninstall":
		// disable/delete alone leaves the process running: stop it first.
		if st, err := s.Status(); err == nil && st == kservice.StatusRunning {
			if err := s.Stop(); err != nil {
				return fail(fmt.Errorf("stop running service before uninstall: %w", err))
			}
			fmt.Printf("stopped service %q\n", name)
		}
		if err := s.Uninstall(); err != nil {
			return fail(err)
		}
		fmt.Printf("removed service %q\n", name)
	case "start":
		err := s.Start()
		if err != nil && logonFailure(err) {
			// Windows: the account may simply lack "Log on as a service"
			// (SeServiceLogonRight, event 7041) — the SCM reports that with the
			// same generic logon failure as a wrong password. Grant the right and
			// retry once; only if that does not help is the password the culprit.
			err = grantLogonRightAndRetry(s, name, err)
		}
		if err != nil {
			if logonFailure(err) {
				return fail(fmt.Errorf("%w\nhint: the SCM could not log on the service account; its password is wrong or stale. Reinstall to update it: termcp-relay service install --user <account> ...", err))
			}
			return fail(err)
		}
		fmt.Printf("started service %q\n", name)
	case "stop":
		if err := s.Stop(); err != nil {
			return fail(err)
		}
		fmt.Printf("stopped service %q\n", name)
	case "restart":
		if err := s.Restart(); err != nil {
			return fail(err)
		}
		fmt.Printf("restarted service %q\n", name)
	}
	return 0
}

// printStatus answers "is this machine already running termcp-relay as a
// service?" and always exits 0 unless the query itself failed: a missing
// service is a normal, non-error answer.
func printStatus(opts Options) int {
	st, err := Query(opts.serviceName())
	if err != nil {
		if errors.Is(err, kservice.ErrNoServiceSystemDetected) {
			fmt.Printf("service %q: unknown — no supported service manager detected on this host\n", opts.serviceName())
			fmt.Println("run the relay in the foreground instead: termcp-relay --config " + opts.ConfigPath)
			return 0
		}
		return fail(err)
	}

	switch {
	case !st.Installed:
		fmt.Printf("service %q: not installed\n", st.Name)
		fmt.Printf("install it with: termcp-relay service install --config %s\n", opts.ConfigPath)
	case st.Running:
		fmt.Printf("service %q: installed, running\n", st.Name)
	case st.Detail != "":
		fmt.Printf("service %q: installed, not running (%s)\n", st.Name, st.Detail)
	default:
		fmt.Printf("service %q: installed, stopped\n", st.Name)
	}
	if st.Installed {
		if acct, err := serviceAccountName(opts.serviceName()); err == nil && acct != "" {
			fmt.Printf("configured account:     %s\n", acct)
			if !builtinServiceAccount(acct) {
				if held, err := serviceLogonRightHeld(acct); err == nil && !held {
					fmt.Printf("warning: %s lacks SeServiceLogonRight (\"Log on as a service\");\n", acct)
					fmt.Printf("         the SCM will refuse to start it until granted.\n")
					fmt.Printf("         fix it by starting once: termcp-relay service start (will auto-grant)\n")
				}
			}
		}
		if hint := manualCheckHint(st.Name); hint != "" {
			fmt.Printf("check it directly with: %s\n", hint)
		}
	}
	return 0
}

// manualCheckHint returns the native command for cross-checking service state.
func manualCheckHint(name string) string {
	switch runtime.GOOS {
	case "windows":
		return "sc query " + name
	case "darwin":
		return "launchctl list | grep " + name
	case "linux":
		switch {
		case hasSystemctl():
			return "systemctl status " + name
		case dirExists("/etc/init.d"):
			return "/etc/init.d/" + name + " status"
		}
	}
	return ""
}

func hasSystemctl() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// resolveConfigPath makes the config path absolute and verifies it exists.
func resolveConfigPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		p = "config.toml"
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("config %q: %w\nhint: create one with: termcp-relay init %s", abs, err, p)
	}
	return abs, nil
}

func nameFlag(opts Options) string {
	if opts.Name == "" || opts.Name == Name {
		return ""
	}
	return " --name " + opts.Name
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "service: %v\n", err)
	return 1
}
