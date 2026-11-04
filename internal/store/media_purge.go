package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// PurgeMediaPaths removes lifecycle metadata and durably queues the selected
// files in one transaction. It never unlinks; callers must use
// UnlinkQueuedMedia afterwards so new references are checked under writeMu.
// Cleanup resumes the queue after a crash. Unknown media files are allowed
// under the private account or canonical media root (interrupted downloads).
func (s *Store) PurgeMediaPaths(ctx context.Context, account engine.AccountID, paths []string) error {
	if err := s.checkAccount(account); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	protected, err := s.databasePaths(ctx)
	if err != nil {
		return err
	}
	var databaseFiles []os.FileInfo
	for path := range protected {
		if info, e := os.Stat(path); e == nil {
			databaseFiles = append(databaseFiles, info)
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	exportDir, err := s.StatsExportDir(ctx, account)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(paths))
	shared := make(map[string]bool)
	for _, path := range paths {
		if path == "" || strings.IndexByte(path, 0) >= 0 {
			return engine.ErrInvalid
		}
		absolute := s.purgeAbsolute(path)
		if protected[absolute] {
			return engine.ErrInvalid
		}
		root := s.dir
		if !purgeBelow(root, absolute) {
			if purgeBelow(s.mediaRoot(), absolute) {
				root = s.mediaRoot()
			} else if purgeBelow(exportDir, absolute) {
				root = exportDir
				shared[absolute] = true
			} else {
				return engine.ErrInvalid
			}
		}
		if err := purgeValidateFile(root, absolute, databaseFiles); err != nil {
			return err
		}
		selected[absolute] = true
	}
	if len(selected) == 0 {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Match lexical aliases as Cleanup does. A path in the shared exports root
	// needs ownership evidence; it must never delete another account's export.
	rows, err := tx.QueryContext(ctx, `SELECT 'media',m.account,f.path FROM media_files f JOIN messages m ON m.row_id=f.row_id UNION ALL SELECT 'export',account,path FROM exports UNION ALL SELECT 'photo',account,path FROM profile_photos`)
	if err != nil {
		return err
	}
	type record struct{ kind, path string }
	var records []record
	owned := make(map[string]bool)
	for rows.Next() {
		var kind, owner, path string
		if err := rows.Scan(&kind, &owner, &path); err != nil {
			rows.Close()
			return err
		}
		absolute := s.purgeAbsolute(path)
		if !selected[absolute] {
			continue
		}
		if owner != string(account) {
			rows.Close()
			return engine.ErrAccountMismatch
		}
		// This contract does not purge profile-photo metadata. Refuse to leave a
		// live reference for a caller that unlinks immediately after our commit.
		if kind == "photo" {
			rows.Close()
			return engine.ErrInvalid
		}
		owned[absolute] = true
		records = append(records, record{kind, path})
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for path := range shared {
		if owned[path] {
			continue
		}
		// Replaying a committed purge is safe even after its metadata is gone.
		var queued int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM files_to_unlink WHERE path=?`, path).Scan(&queued); err != nil {
			return err
		}
		if queued == 0 {
			return engine.ErrInvalid
		}
	}
	for _, r := range records {
		query := `DELETE FROM media_files WHERE path=? AND row_id IN (SELECT row_id FROM messages WHERE account=?)`
		if r.kind == "export" {
			query = `DELETE FROM exports WHERE path=? AND account=?`
		}
		if _, err := tx.ExecContext(ctx, query, r.path, account); err != nil {
			return err
		}
	}
	for path := range selected {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO files_to_unlink(path,queued_at,reason) VALUES(?,?,'media_purge')`, path, s.now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UnlinkQueuedMedia drains committed queue entries while holding the same
// writer lock as Apply/RegisterStatsExport. New references protect reused paths;
// failures stay queued for the next cleanup, including process restart.
func (s *Store) UnlinkQueuedMedia(ctx context.Context, account engine.AccountID) error {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.unlinkQueued(ctx)
}

func (s *Store) purgeAbsolute(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.dir, path)
	}
	return filepath.Clean(path)
}

// mediaRoot confines canonical media without granting the entire data directory.
func (s *Store) mediaRoot() string {
	if filepath.Base(filepath.Dir(s.dir)) == "accounts" && filepath.Base(s.dir) == string(s.account) {
		return filepath.Join(filepath.Dir(filepath.Dir(s.dir)), "media")
	}
	return filepath.Join(s.dir, "media")
}

func purgeBelow(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Validation shares Cleanup's confined parent traversal. Missing files/parents
// are valid for replay, but symlinks, directories and database hardlinks are not.
func purgeValidateFile(dir, path string, protected []os.FileInfo) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return engine.ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return err
	}
	if err := lifecycleUnlinkParents(root, rel); err != nil {
		return err
	}
	info, err = root.Lstat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return engine.ErrInvalid
	}
	for _, database := range protected {
		if os.SameFile(info, database) {
			return engine.ErrInvalid
		}
	}
	return nil
}
