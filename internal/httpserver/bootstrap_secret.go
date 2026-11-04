package httpserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// PanelBootstrapHeader carries the local panel authority. It is deliberately
	// separate from the MCP bearer and is accepted only by the bootstrap route.
	PanelBootstrapHeader = "X-Telegram-MCP-Panel-Bootstrap"
	panelBootstrapFile   = "panel-bootstrap.key"
)

// PanelBootstrapSecretPath is the private bootstrap credential for the primary
// account. Client MCP configurations must never include this path or its value.
func PanelBootstrapSecretPath(dataDir string) string {
	return filepath.Join(dataDir, "accounts", "primary", panelBootstrapFile)
}

// EnsurePanelBootstrapSecret loads the panel bootstrap authority, creating it
// on first start in a private account directory.
func EnsurePanelBootstrapSecret(dataDir string) (string, error) {
	dir := filepath.Dir(PanelBootstrapSecretPath(dataDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create panel credential directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("protect panel credential directory: %w", err)
		}
	}
	secret, err := ReadPanelBootstrapSecret(dataDir)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	secret, err = newPanelBootstrapSecret()
	if err != nil {
		return "", err
	}
	path := PanelBootstrapSecretPath(dataDir)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ReadPanelBootstrapSecret(dataDir)
	}
	if err != nil {
		return "", fmt.Errorf("create panel bootstrap credential: %w", err)
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.WriteString(secret + "\n"); err != nil {
		return "", fmt.Errorf("write panel bootstrap credential: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("sync panel bootstrap credential: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close panel bootstrap credential: %w", err)
	}
	ok = true
	return secret, nil
}

// ReadPanelBootstrapSecret reads the local authority without creating it.
func ReadPanelBootstrapSecret(dataDir string) (string, error) {
	path := PanelBootstrapSecretPath(dataDir)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("panel bootstrap credential is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return "", errors.New("panel bootstrap credential must have mode 0600")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read panel bootstrap credential: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if len(secret) != 43 {
		return "", errors.New("panel bootstrap credential is invalid")
	}
	if _, err := base64.RawURLEncoding.DecodeString(secret); err != nil {
		return "", errors.New("panel bootstrap credential is invalid")
	}
	return secret, nil
}

// RotatePanelBootstrapSecret atomically replaces the local authority. A running
// daemon keeps its current value until restart; old values then stop working.
func RotatePanelBootstrapSecret(dataDir string) (string, error) {
	if _, err := EnsurePanelBootstrapSecret(dataDir); err != nil {
		return "", err
	}
	secret, err := newPanelBootstrapSecret()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(PanelBootstrapSecretPath(dataDir))
	f, err := os.CreateTemp(dir, ".panel-bootstrap-*")
	if err != nil {
		return "", fmt.Errorf("create rotated panel bootstrap credential: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.WriteString(secret + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, PanelBootstrapSecretPath(dataDir)); err != nil {
		return "", fmt.Errorf("replace panel bootstrap credential: %w", err)
	}
	return secret, nil
}

func newPanelBootstrapSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("cannot generate panel bootstrap credential")
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func validPanelBootstrapSecret(header http.Header, secret string) bool {
	values := header.Values(PanelBootstrapHeader)
	return secret != "" && len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte(secret)) == 1
}
