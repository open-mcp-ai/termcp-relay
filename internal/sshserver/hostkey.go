package sshserver

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gossh "golang.org/x/crypto/ssh"
)

// loadOrCreateHostKey loads the PEM host key from path, or generates a fresh
// RSA 2048 key, persists it to path (0600), and returns the signer.
// Persistence keeps the server's host fingerprint stable across restarts.
func loadOrCreateHostKey(path string) (gossh.Signer, error) {
	if path == "" {
		return nil, errors.New("host key path is empty")
	}
	if raw, err := os.ReadFile(path); err == nil {
		signer, perr := gossh.ParsePrivateKey(raw)
		if perr != nil {
			return nil, fmt.Errorf("parse host key %q: %w", path, perr)
		}
		return signer, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read host key %q: %w", path, err)
	}

	signer, pemBytes, err := generateHostKeyPEM()
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("mkdir host key dir: %w", err)
		}
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write host key %q: %w", path, err)
	}
	return signer, nil
}

// generateHostKeyPEM generates an RSA 2048 private key and returns both the
// gossh signer and the PEM-encoded bytes for persistence.
func generateHostKeyPEM() (gossh.Signer, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	pemBytes := pem.EncodeToMemory(block)
	signer, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, nil, err
	}
	return signer, pemBytes, nil
}
