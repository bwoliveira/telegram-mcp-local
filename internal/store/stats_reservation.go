package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

// statsExportTarget validates the private root shared with Cleanup. Export
// basenames are flat so symlinked intermediate directories cannot be accepted.
func (s *Store) statsExportTarget(ctx context.Context, account engine.AccountID, path string) (string, error) {
	dir, err := s.StatsExportDir(ctx, account)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if filepath.Dir(absolute) != dir || filepath.Ext(absolute) != ".ndjson" || strings.HasPrefix(filepath.Base(absolute), ".") {
		return "", engine.ErrInvalid
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || service.CheckPrivateDataPath(dir, true) != nil {
		return "", engine.ErrInvalid
	}
	return absolute, nil
}

// ReserveStatsExport durably tracks BOTH possible file names before the writer
// creates content. A process crash during streaming or after rename therefore
// cannot leave an export outside retention. Zero-byte pending entries use the
// normal seven-day export retention, not a separate indefinite staging policy.
func (s *Store) ReserveStatsExport(ctx context.Context, account engine.AccountID, path, filterJSON string) error {
	absolute, err := s.statsExportTarget(ctx, account, path)
	if err != nil {
		return err
	}
	if !json.Valid([]byte(filterJSON)) {
		return engine.ErrInvalid
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{filepath.Base(absolute), filepath.Base(absolute) + ".partial"} {
		if _, err := root.Lstat(name); err == nil {
			return engine.ErrInvalid
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, target := range []string{absolute, absolute + ".partial"} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO exports(account,path,filter_json,count,bytes,created_at) VALUES(?,?,?,0,0,?)`, account, target, filterJSON, s.now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CommitStatsExport replaces pending metadata only after private-file validation.
// Removing the partial registration also queues that name for idempotent cleanup.
func (s *Store) CommitStatsExport(ctx context.Context, account engine.AccountID, path string, count int, bytes int64) error {
	absolute, err := s.statsExportTarget(ctx, account, path)
	if err != nil {
		return err
	}
	if count < 0 || bytes < 0 {
		return engine.ErrInvalid
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || service.CheckPrivateDataPath(absolute, false) != nil || info.Size() != bytes {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE exports SET count=?,bytes=? WHERE account=? AND path=? AND EXISTS(SELECT 1 FROM exports pending WHERE pending.account=? AND pending.path=?)`, count, bytes, account, absolute, account, absolute+".partial")
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return engine.ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM exports WHERE account=? AND path=?`, account, absolute+".partial"); err != nil {
		return err
	}
	return tx.Commit()
}

// AbortStatsExport queues both names on normal failure. Even if cancellation
// prevents this cleanup, the reserved records still guarantee eventual retention.
func (s *Store) AbortStatsExport(ctx context.Context, account engine.AccountID, path string) error {
	absolute, err := s.statsExportTarget(ctx, account, path)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.writer.ExecContext(ctx, `DELETE FROM exports WHERE account=? AND path IN (?,?)`, account, absolute, absolute+".partial")
	return err
}
