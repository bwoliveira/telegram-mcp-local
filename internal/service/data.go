package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// tokenKeyDir is deliberately a sibling of, never a child of, the data dir.
// A backup/copy of TELEGRAM_MCP_DATA alone must not contain its decryption key.
// Copying both locations, same-user malware and root remain out of scope.
func tokenKeyDir(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(filepath.Dir(dataDir), ".telegram-mcp-keys", hex.EncodeToString(sum[:]))
}

// PrepareData establishes private SQLite files BEFORE opening SQLite. Otherwise
// WAL/SHM inherit the initial database mode (0644), even if a store chmods its
// main database after opening it. Do not change the process-global umask.
func PrepareData(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dataDir)
	if err != nil {
		return err
	}
	unlock, err := protectDataDirectory(dataDir, info)
	if err != nil {
		return err
	}
	defer unlock()
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		path := filepath.Join(dataDir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if name != "state.db" {
				continue
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if err != nil {
				return err
			}
			if err = f.Close(); err != nil {
				return err
			}
			if err := prepareDataFile(path); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return os.ErrPermission
		}
		if err = prepareDataFile(path); err != nil {
			return err
		}
	}
	return nil
}
