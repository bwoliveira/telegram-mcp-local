package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
)

// Only the backend name is stored here, never credentials. Pinning the backend
// lets a terminal and a login service with different environments share one
// credential rather than silently creating a second one in another backend.
func tokenBackend(dir string) (string, error) {
	requested := os.Getenv("TELEGRAM_MCP_SECRETS")
	switch requested {
	case "", "auto":
		requested = ""
	case "linux-file":
		requested = "file"
	case "file", "keyring":
	default:
		return "", secrets.ErrInvalid
	}
	mode, err := readTokenBackend(filepath.Join(dir, "http-token.backend"))
	if err == nil {
		if requested != "" && requested != mode {
			return "", errors.New("secrets backend conflicts with persisted HTTP credential backend")
		}
		return mode, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if requested != "" {
		return requested, nil
	}
	if runtime.GOOS == "linux" {
		return "file", nil
	}
	return "keyring", nil
}

func readTokenBackend(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", secrets.ErrStorage
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return "", secrets.ErrPermission
	}
	if info.Size() > 16 {
		return "", secrets.ErrCorrupt
	}
	f, err := os.Open(path)
	if err != nil {
		return "", secrets.ErrStorage
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 17))
	if err != nil {
		return "", secrets.ErrStorage
	}
	mode := string(b)
	if mode != "file" && mode != "keyring" {
		return "", secrets.ErrCorrupt
	}
	return mode, nil
}

// Caller holds http-token.lock. Persist before creating a credential so a crash
// cannot leave a keyring credential whose backend is unknown to the next daemon.
func persistTokenBackend(dir, mode string) error {
	path := filepath.Join(dir, "http-token.backend")
	existing, err := readTokenBackend(path)
	if err == nil {
		if existing != mode {
			return secrets.ErrCorrupt
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return secrets.ErrStorage
	}
	_, writeErr := io.WriteString(f, mode)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return secrets.ErrStorage
	}
	return nil
}
