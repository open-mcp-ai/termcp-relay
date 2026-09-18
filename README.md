# termcp-relay

A standalone SSH daemon extracted from [termcp](https://github.com/open-mcp-ai/termcp)'s
built-in (in-process) sshd. Where termcp's internal server listens on an in-memory
pipe and mints one-time credentials, termcp-relay binds a real TCP port and
authenticates clients with a static password and/or `authorized_keys`. It preserves
the full feature set of the original:

- Interactive PTY shell sessions
- Exec channels (`ssh host command`)
- SFTP subsystem (`scp`, `sftp`)
- Direct-tcpip port forwarding (`ssh -L`)
- Reverse / tcpip-forward port forwarding (`ssh -R`)
- Client signal forwarding (TERM / INT / KILL / HUP)

All configuration lives in a single TOML file. The host key and `authorized_keys`
are co-located with `config.toml`: their paths in the TOML are resolved relative to
the config file's directory, so one folder holds `config.toml` + `host_key.pem` +
`authorized_keys`.

## Install

```bash
go install github.com/open-mcp-ai/termcp-relay@latest
termcp-relay --config config.toml
```

## Build from source

```bash
make          # cross-compile linux/darwin/windows (amd64+arm64) into dist/
make test     # run tests
make clean    # remove dist/ and host binaries
```

Or without make:

```bash
go build -o termcp-relay .
./termcp-relay --config config.toml
```

The binary reports its build: `termcp-relay version`.

## Configure

Generate a commented template config (refuses to overwrite an existing file):

```sh
./termcp-relay init
# or: ./termcp-relay init /path/to/config.toml
```

You can also copy the checked-in example:

```sh
cp config.example.toml config.toml
```

Edit `config.toml`; for password auth, set `auth.username` and `auth.password`.
`authorized_keys` is commented out by default and can be uncommented when public-key
auth is needed.

Key material:

- `host_key.pem` — the SSH server identity key. If the configured path does not
  exist, termcp-relay auto-generates an RSA-2048 private key with 0600 perms on
  first run and reuses it on later runs for a stable server fingerprint. No
  external tools such as `ssh-keygen` are required. Keep it on the target machine;
  do not commit deployment keys.
- `authorized_keys` — standard OpenSSH format, one key per line. Required only
  when `auth.authorized_keys` is uncommented/set.

### Auth modes

- **Password** — `auth.username` + `auth.password`. Username comparison is
  constant-time; when `username` is empty, any username is accepted as long as
  the password matches.
- **Public key** — set `auth.authorized_keys` to a path; offered keys are
  compared constant-time against the file.
- **None** — `auth.no_auth = true` accepts all clients. Refused on
  non-loopback bind addresses unless explicitly enabled.

## Run

```sh
./termcp-relay --config config.toml
# override the configured log level without editing the file:
./termcp-relay --config config.toml --log-level debug
# see full usage:
./termcp-relay help
```

The server logs a connection hint for each listenable IPv4 address, e.g.:

```
connect: ssh -p 2222 ops@10.0.0.5
```

## Install as a system service

`termcp-relay service` registers the daemon with the host service manager so it
starts at boot and restarts after a crash:

```sh
# register + enable (needs root on Linux/macOS, Administrator on Windows)
sudo ./termcp-relay service install --config /etc/termcp-relay/config.toml
sudo ./termcp-relay service start

# is it already installed / running on this machine?
./termcp-relay service status

# lifecycle
sudo ./termcp-relay service stop|restart|uninstall
```

`status` answers the common "is this box already serving SSH via termcp-relay?"
question. It never needs privileges and exits 0 in all three states — it prints
`not installed`, `installed, stopped`, or `installed, running`, plus the native
command for cross-checking (`systemctl status`, `sc query`, `launchctl list`).

On Windows the mutating actions re-launch themselves through the UAC prompt
when the current shell is not elevated, so `service install` works from a plain
terminal. The config is validated before elevation, so a typo fails without a
prompt; the elevated child's output is replayed in the original terminal. On
Linux and macOS run the same commands with `sudo`.

Backends, chosen automatically by the library:

| Platform | Backend | Service file |
|---|---|---|
| Windows | SCM | registry entry, auto-restart on failure |
| Linux | systemd (fallback Upstart/SysV/OpenRC) | `/etc/systemd/system/termcp-relay.service` |
| macOS | launchd | `/Library/LaunchDaemons/termcp-relay.plist` |

Installing stores the **absolute** path of `--config`, because the service
manager starts the process with a different working directory than your shell.
The config is validated at install time, so a broken config fails immediately
instead of silently at boot.

**Windows auto-elevation:** `install`/`uninstall`/`start`/`stop`/`restart`
require an Administrator token. When the current shell is not elevated, the
command re-launches itself through a UAC prompt (config is validated first, so
bad flags never cost a prompt), and the elevated pass's output is printed back
in your terminal. `status` never needs privileges. On Linux/macOS run the
mutating commands with `sudo` instead.

### Logs

On Linux and macOS the service manager captures stderr (journald / launchd's
`StandardErrorPath`). A Windows service has no console, so `install` defaults
`--log-file` to `<config dir>/termcp-relay.log`; pass `--log-file PATH`
to override. On every platform the flag makes the daemon write to stderr *and*
that file.

```sh
sudo ./termcp-relay service install --config /etc/termcp-relay/config.toml \
     --log-file /var/log/termcp-relay.log
```

### Service account

The service runs as a real account, and **that account's rights are the SSH
shell's rights**. Three modes:

| Flags | Account | Password |
|---|---|---|
| *(none)* | **LocalSystem** (Windows) / **root** (Unix) — same default as nssm | never |
| `--system` | LocalSystem (Windows) / root (Unix), spelled out | never |
| `--user NAME` | that account (`current` = the installing account) | Windows asks unless it is a built-in account |

```sh
# default: LocalSystem/root, unattended, no password (nssm-style)
.\termcp-relay service install --config C:\termcp-relay\config.toml

# dedicated account (SSH clients inherit its limited rights)
.\termcp-relay service install --config C:\termcp-relay\config.toml --user .\relayuser
# ...or your own account, typing its Windows password once
.\termcp-relay service install --config C:\termcp-relay\config.toml --user current
.\termcp-relay service install --config C:\termcp-relay\config.toml --user "NT AUTHORITY\LocalService"
```

### Working directory for SSH sessions

By default SSH sessions start in **the installing user's home directory**
(`C:\Users\<you>`, or the flag `--work-dir` / `server.work_dir` in config.toml).
That mirrors nssm's `AppDirectory`: clients land in a useful place instead of
`C:\Windows\System32`, even when the service itself runs as LocalSystem. SFTP
sessions are rooted there too.

```toml
[server]
work_dir = "D:\work"   # optional; leave empty for the installer's home
```

Windows specifics:

- The SCM only starts a service whose `ServiceStartName` it holds a password
  for, so when a **named** account is chosen (`--user NAME`) the install
  prompts for the account's password (read without echo). An empty password
  works only for accounts that genuinely have none — a local administrator
  still needs theirs. The default LocalSystem needs nothing.
- Windows requires the service account to hold the **"Log on as a service"**
  right (`SeServiceLogonRight`). Normal accounts (even administrators) do not
  have it by default; the installer grants it via the LSA policy automatically
  during installation (or self-heals it on the first `service start`), mirroring
  what the Services MMC (`services.msc`) does. The LocalSystem default never
  needs it.
- When a password is required it is **verified at install time** with the same
  `LogonUser` check the SCM uses (up to 3 attempts), so a typo is caught
  immediately instead of surfacing later as a service that will not start.
- Under Git Bash / MSYS the prompt reads from `CONIN$` (the real console)
  rather than stdin, which is a pseudo-terminal there: reading stdin would echo
  the password and could capture nothing at all.
- The password never touches argv: it is handed to the elevated install through
  a 0600 temp file and stored by the SCM as an LSA secret. It is not written to
  logs or the service definition.
- The SCM re-validates credentials at **first start**; a password changed since
  install surfaces as a hinted `logon failure` on `service start`. A *missing*
  logon right looks identical from the SCM but is granted automatically, so the
  hint is only shown when the right is already present (i.e. the password really
  is the problem). `service status` reports a missing right up front.
- Built-in accounts — `LocalSystem`, `NT AUTHORITY\LocalService`,
  `NT AUTHORITY\NetworkService` — are passwordless and can be named directly.
  `LocalService` is a handy low-privilege choice, but the config directory,
  host key and log file must then be readable/writable by that account.

A note on privilege: the default LocalSystem account hands the SSH shell
**SYSTEM rights** — maximum local power, no UAC filtering. That is convenient
(nssm behaves the same) but it is not a security boundary: anyone who can log
in via SSH controls the machine. For a real boundary create a standard
(non-admin) account and use `--user`; for a middle ground use
`NT AUTHORITY\LocalService`.

On **Unix**, `--user` needs no password (systemd `User=` / launchd `UserName`),
and the host key, `authorized_keys` and log file live under whatever account
runs the daemon — a `--user relay` install must also make the config directory
readable by `relay`.

### Multiple instances

`--name` registers a second service with its own config:

```sh
sudo ./termcp-relay service install --name termcp-relay-alt --config /etc/termcp-relay/alt.toml
./termcp-relay service status --name termcp-relay-alt
```

On hosts without a detected service manager (containers, exotic init systems)
`install` reports that no service system was found and `status` says so; run the
binary in the foreground instead, where the process supervisor handles restarts.

## Default shell

When a client requests an interactive shell (no command), the default shell is
detected the same way as termcp's `detect_shell`:

- **Windows**: `pwsh.exe` -> `powershell.exe` -> `cmd.exe` (via `PATH` lookup)
- **Unix**: `$SHELL` (if present in `PATH`), else `/bin/zsh` -> `/bin/bash` ->
  `/bin/sh` (via `stat`)

Override with `server.shell` (whitespace-split into argv). Interactive shells are
spawned bare — no flags are injected — so shells that reject foreign options
(dash/busybox `/bin/sh` does not know bash's `+o histexpand`) start cleanly.
Explicit commands are passed through untouched as well, so client-provided `!`
is preserved verbatim.

## Port forwarding

- `allow_local_forward` gates `ssh -L` (direct-tcpip).
- `allow_remote_forward` gates `ssh -R` (tcpip-forward).

Both log the destination/bind at INFO when allowed, and at DEBUG when denied.

## Project layout

```
main.go                       CLI: init/service subcommands, logging, connection hints
internal/config/              TOML config, defaults, validation, path resolution
internal/sshserver/           SSH daemon: auth, host key, sessions, PTY, SFTP, forwards
internal/service/             Service-manager integration (SCM/systemd/launchd)
```
