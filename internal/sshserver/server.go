// Package sshserver implements a standalone SSH daemon extracted from
// termcp's built-in (in-process) sshd. Unlike termcp's internal server, which
// uses an in-memory net.Pipe listener and one-time minted credentials, this
// server binds a real TCP port and authenticates clients via a static password
// and/or authorized_keys. It preserves the full feature set of the original:
// PTY shell sessions, exec channels, the SFTP subsystem, direct-tcpip (ssh -L)
// and tcpip-forward (ssh -R) port forwarding, and signal forwarding.
package sshserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/ssh"
)

// Options configures a Server.
type Options struct {
	Addr           string
	HostKeyPath    string
	Auth           *Auth
	ShellOverride  string
	Banner         string
	AllowLocalFwd  bool
	AllowRemoteFwd bool
}

// Server is a TCP-listening SSH daemon.
type Server struct {
	opts          Options
	shellOverride string
	auth          *Auth

	server   *ssh.Server
	listener net.Listener
	started  atomic.Bool
	mu       sync.Mutex
}

// New constructs an unstarted Server. The host key is loaded/generated on
// Start() so that constructor errors stay cheap and non-fatal.
func New(opts Options) (*Server, error) {
	if strings.TrimSpace(opts.Addr) == "" {
		return nil, errors.New("sshserver: addr is required")
	}
	if opts.Auth == nil {
		opts.Auth = &Auth{NoAuth: true}
	}
	s := &Server{
		opts:          opts,
		shellOverride: strings.TrimSpace(opts.ShellOverride),
		auth:          opts.Auth,
	}

	forwardedTCP := &ssh.ForwardedTCPHandler{}

	srv := &ssh.Server{
		Handler: func(sess ssh.Session) {
			s.handleSession(sess)
		},
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": handleSftpSubsystem,
		},
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"session":      ssh.DefaultSessionHandler,
			"direct-tcpip": ssh.DirectTCPIPHandler,
		},
		RequestHandlers: map[string]ssh.RequestHandler{
			"tcpip-forward":        forwardedTCP.HandleSSHRequest,
			"cancel-tcpip-forward": forwardedTCP.HandleSSHRequest,
		},
		LocalPortForwardingCallback: func(ctx ssh.Context, dHost string, dPort uint32) bool {
			if !opts.AllowLocalFwd {
				slog.Debug("local port forward denied (disabled)", "dest", net.JoinHostPort(dHost, fmt.Sprint(dPort)))
				return false
			}
			slog.Info("local port forward", "user", ctx.User(), "dest", net.JoinHostPort(dHost, fmt.Sprint(dPort)))
			return true
		},
		ReversePortForwardingCallback: func(ctx ssh.Context, bHost string, bPort uint32) bool {
			if !opts.AllowRemoteFwd {
				slog.Debug("reverse port forward denied (disabled)", "bind", net.JoinHostPort(bHost, fmt.Sprint(bPort)))
				return false
			}
			slog.Info("reverse port forward", "user", ctx.User(), "bind", net.JoinHostPort(bHost, fmt.Sprint(bPort)))
			return true
		},
	}

	// Authentication handlers. When NoAuth is true, leaving all auth handlers
	// nil tells charmbracelet/ssh to accept any client without challenge.
	if !s.auth.NoAuth {
		if s.auth.Password != "" {
			srv.PasswordHandler = func(ctx ssh.Context, password string) bool {
				ok := s.auth.PasswordOK(ctx.User(), password)
				slog.Info("password auth", "user", ctx.User(), "ok", ok)
				return ok
			}
		}
		if len(s.auth.authorizedKeys) > 0 {
			srv.PublicKeyHandler = func(ctx ssh.Context, key ssh.PublicKey) bool {
				ok := s.auth.PublicKeyOK(key)
				slog.Info("publickey auth", "user", ctx.User(), "ok", ok)
				return ok
			}
		}
	}

	if strings.TrimSpace(opts.Banner) != "" {
		srv.Banner = opts.Banner
	}

	if err := srv.SetOption(ssh.AllocatePty()); err != nil {
		return nil, fmt.Errorf("allocate pty option: %w", err)
	}
	s.server = srv
	return s, nil
}

// Start binds the TCP listener, loads/generates the host key, and begins
// serving SSH connections. It returns when the server stops.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.Load() {
		return errors.New("sshserver: already started")
	}

	signer, err := loadOrCreateHostKey(s.opts.HostKeyPath)
	if err != nil {
		return err
	}
	s.server.AddHostKey(signer)
	slog.Info("host key ready", "path", s.opts.HostKeyPath)

	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.opts.Addr, err)
	}
	s.listener = ln
	s.started.Store(true)

	slog.Info("ssh server listening", "addr", ln.Addr().String())
	go func() {
		if err := s.server.Serve(ln); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Error("ssh server stopped", "err", err)
			} else {
				slog.Info("ssh server stopped")
			}
		}
	}()
	return nil
}

// Addr returns the listener's actual bound address (useful when Addr had port
// :0). Returns the configured Addr string before Start.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.opts.Addr
}

// Stop gracefully shuts the server down, closing the listener and all active
// connections.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started.Load() {
		return nil
	}
	s.started.Store(false)
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}
