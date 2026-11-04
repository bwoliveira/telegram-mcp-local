package service

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
)

// ResolveToken reads the daemon's persisted HTTP credential. create permits
// first-install generation; initial is an optional migration seed, never an
// override of an existing credential. A bridge must use create=false.
//
// The current secrets contract has only account credential slots. Use an
// isolated http-token namespace and its opaque Session slot, never the Telegram
// account's session. No token is stored in config.json or the service unit.
// Linux headless services default to the encrypted-file backend: auto-selecting
// based on the calling terminal's DBus environment would choose a different
// backend from systemd. An explicit TELEGRAM_MCP_SECRETS selects keyring instead.
// The selected backend is pinned in a non-secret http-token.backend marker.
func ResolveToken(dataDir, initial string, create bool) (string, error) {
	dir, err := filepath.Abs(dataDir)
	if err != nil || dataDir == "" {
		return "", secrets.ErrInvalid
	}
	if initial != "" && !validToken(initial) {
		return "", errors.New("HTTP token must be base64url encoding of 32 random bytes")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", secrets.ErrStorage
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return "", secrets.ErrPermission
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return "", secrets.ErrPermission
	}
	unlock, err := lockToken(filepath.Join(dir, "http-token.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	mode, err := tokenBackend(dir)
	if err != nil {
		return "", err
	}
	store, err := secrets.Open("http-token:"+dir, secrets.Options{Mode: mode, DataDir: filepath.Join(dir, "secrets"), KeyDir: tokenKeyDir(dir)})
	if err != nil {
		return "", err
	}
	raw, err := store.Get(secrets.Session)
	if err == nil {
		token := string(raw)
		if !validToken(token) {
			return "", secrets.ErrCorrupt
		}
		if initial != "" && subtle.ConstantTimeCompare([]byte(initial), raw) != 1 {
			return "", errors.New("environment token conflicts with persisted HTTP credential")
		}
		if err := persistTokenBackend(dir, string(store.Backend())); err != nil {
			return "", err
		}
		return token, nil
	}
	if !errors.Is(err, secrets.ErrNotFound) || !create {
		return "", err
	}
	token := initial
	if token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", errors.New("could not generate HTTP credential")
		}
		token = base64.RawURLEncoding.EncodeToString(b)
	}
	if err := persistTokenBackend(dir, string(store.Backend())); err != nil {
		return "", err
	}
	if err := store.Set(secrets.Session, []byte(token)); err != nil {
		return "", err
	}
	return token, nil
}

func validToken(token string) bool {
	b, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == token
}
