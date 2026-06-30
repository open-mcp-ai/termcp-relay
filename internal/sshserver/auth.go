package sshserver

import (
	"crypto/subtle"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Auth holds the configured authentication methods for the relay server.
// Any method that is nil/empty is considered disabled. When NoAuth is true,
// all clients are accepted without challenge.
type Auth struct {
	NoAuth         bool
	Username       string // if set, password auth also requires this exact user
	Password       string
	authorizedKeys [][]byte // marshaled form of each accepted public key

	mu sync.Mutex // guards reload state if Reload is called concurrently
}

// LoadAuthorizedKeys reads and parses an OpenSSH authorized_keys file, storing
// the marshaled public keys for constant-time comparison during auth.
func (a *Auth) LoadAuthorizedKeys(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var keys [][]byte
	rest := raw
	for {
		pub, _, _, rest2, err := gossh.ParseAuthorizedKey(rest)
		if err != nil {
			break
		}
		keys = append(keys, pub.Marshal())
		rest = rest2
	}
	if len(keys) == 0 {
		return errors.New("no valid public keys found in authorized_keys file")
	}
	a.mu.Lock()
	a.authorizedKeys = keys
	a.mu.Unlock()
	return nil
}

// PasswordOK verifies the username (when configured) and password in constant
// time. When Username is empty, any username is accepted as long as the
// password matches.
func (a *Auth) PasswordOK(user, password string) bool {
	if a.Password == "" {
		return false
	}
	if a.Username != "" {
		if len(user) != len(a.Username) || subtle.ConstantTimeCompare([]byte(user), []byte(a.Username)) != 1 {
			return false
		}
	}
	if len(password) != len(a.Password) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(a.Password)) == 1
}

// PublicKeyOK returns true if the offered key matches any authorized key.
func (a *Auth) PublicKeyOK(key ssh.PublicKey) bool {
	a.mu.Lock()
	keys := a.authorizedKeys
	a.mu.Unlock()
	if len(keys) == 0 {
		return false
	}
	offered := key.Marshal()
	for _, k := range keys {
		if len(k) == len(offered) && subtle.ConstantTimeCompare(k, offered) == 1 {
			return true
		}
	}
	return false
}
