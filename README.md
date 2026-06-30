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

## Build

```sh
go build -o termcp-relay .
```

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

## Default shell

When a client requests an interactive shell (no command), the default shell is
detected the same way as termcp's `detect_shell`:

- **Windows**: `pwsh.exe` -> `powershell.exe` -> `cmd.exe` (via `PATH` lookup)
- **Unix**: `$SHELL` (if present in `PATH`), else `/bin/zsh` -> `/bin/bash` ->
  `/bin/sh` (via `stat`)

Override with `server.shell` (whitespace-split into argv). `!` history expansion
is suppressed for interactive shells (zsh `-o NO_BANG_HIST`, bash/sh `+o histexpand`);
explicit commands are left untouched so client-provided `!` is preserved verbatim.

## Port forwarding

- `allow_local_forward` gates `ssh -L` (direct-tcpip).
- `allow_remote_forward` gates `ssh -R` (tcpip-forward).

Both log the destination/bind at INFO when allowed, and at DEBUG when denied.

## Project layout

```
main.go                       CLI: init subcommand, --config, logging, connection hints
internal/config/              TOML config, defaults, validation, path resolution
internal/sshserver/           SSH daemon: auth, host key, sessions, PTY, SFTP, forwards
```
