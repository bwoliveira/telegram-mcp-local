package service

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
)

// AccountSecrets shares the HTTP credential's pinned backend and external key
// directory, but never its namespace. It does not read a Telegram session.
func AccountSecrets(dataDir, account string) (secrets.Store, error) {
	var result secrets.Store
	err := WithAccountSecrets(dataDir, account, func(s secrets.Store) error { result = s; return nil })
	return result, err
}

// WithAccountSecrets serializes the complete credential read/validate/write
// transaction across processes, not merely backend selection.
func WithAccountSecrets(dataDir, account string, fn func(secrets.Store) error) error {
	if dataDir == "" || account == "" {
		return secrets.ErrInvalid
	}
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return secrets.ErrInvalid
	}
	if err := PrepareData(dir); err != nil {
		return err
	}
	unlock, err := lockToken(filepath.Join(dir, "http-token.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	mode, err := tokenBackend(dir)
	if err != nil {
		return err
	}
	if err := persistTokenBackend(dir, mode); err != nil {
		return err
	}
	s, err := secrets.Open("telegram:"+dir+":"+account, secrets.Options{Mode: mode, DataDir: filepath.Join(dir, "secrets"), KeyDir: tokenKeyDir(dir)})
	if err != nil {
		return err
	}
	// Reject an unexpected absence of the pinned marker rather than choosing a
	// new backend during a concurrent external filesystem change.
	if _, err := os.Lstat(filepath.Join(dir, "http-token.backend")); errors.Is(err, os.ErrNotExist) {
		return secrets.ErrStorage
	}
	return fn(s)
}
