package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

// StatsExportRecord is lifecycle metadata only, never a second copy of content.
type StatsExportRecord struct {
	Path       string
	FilterJSON string
	Count      int
	Bytes      int64
	CreatedAt  time.Time
}

// StatsExportDir matches Cleanup's canonical export root. Noncanonical stores
// (including temporary test stores) keep exports below their own account root.
func (s *Store) StatsExportDir(ctx context.Context, account engine.AccountID) (string, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return "", err
	}
	if filepath.Base(filepath.Dir(s.dir)) == "accounts" {
		return filepath.Join(filepath.Dir(filepath.Dir(s.dir)), "exports"), nil
	}
	return filepath.Join(s.dir, "exports"), nil
}

// RegisterStatsExport registers a published private NDJSON for Cleanup's export
// retention/purge. Reject files outside that root, symlinks, loose permissions,
// incorrect sizes and invalid metadata. It never overwrites another registration.
func (s *Store) RegisterStatsExport(ctx context.Context, account engine.AccountID, path, filterJSON string, count int, bytes int64) error {
	dir, err := s.StatsExportDir(ctx, account)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if filepath.Dir(absolute) != dir || filepath.Ext(absolute) != ".ndjson" || strings.HasPrefix(filepath.Base(absolute), ".") || count < 0 || bytes < 0 || !json.Valid([]byte(filterJSON)) {
		return engine.ErrInvalid
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || service.CheckPrivateDataPath(dir, true) != nil {
		return engine.ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err = root.Lstat(filepath.Base(absolute))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || service.CheckPrivateDataPath(absolute, false) != nil || info.Size() != bytes {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.writer.ExecContext(ctx, `INSERT INTO exports(account,path,filter_json,count,bytes,created_at) VALUES(?,?,?,?,?,?)`, account, absolute, filterJSON, count, bytes, s.now().Unix())
	return err
}

// ReadStatsExport lets consumers verify the exact account-scoped registration.
func (s *Store) ReadStatsExport(ctx context.Context, account engine.AccountID, path string) (StatsExportRecord, error) {
	var out StatsExportRecord
	if err := s.checkReadAccount(ctx, account); err != nil {
		return out, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return out, err
	}
	var created int64
	err = s.reader.QueryRowContext(ctx, `SELECT path,filter_json,count,bytes,created_at FROM exports WHERE account=? AND path=?`, account, absolute).Scan(&out.Path, &out.FilterJSON, &out.Count, &out.Bytes, &created)
	out.CreatedAt = time.Unix(created, 0).UTC()
	return out, err
}
