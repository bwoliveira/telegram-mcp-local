package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"modernc.org/sqlite"
)

// Sweep expires messages in transactions of at most 500 rows. It reports only
// committed deletions, including those committed before a later batch fails.
// The caller publishes these as delete events with source "ttl". Call Cleanup
// afterwards to remove queued files and erase FTS/WAL residue.
func (s *Store) Sweep(ctx context.Context) (engine.Applied, error) {
	var applied engine.Applied
	now := s.now().UTC().Unix()
	for {
		s.writeMu.Lock()
		keys, err := s.sweepBatch(ctx, now)
		s.writeMu.Unlock()
		applied.Deleted = append(applied.Deleted, keys...)
		if err != nil {
			return applied, err
		}
		if len(keys) < 500 {
			return applied, nil
		}
	}
}

// sweepBatch requires writeMu. Close rows before reusing the single writer
// connection. A rolled-back batch never appears in Applied.
func (s *Store) sweepBatch(ctx context.Context, now int64) ([]engine.MessageKey, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT peer_type,peer_id,msg_id FROM messages WHERE account=? AND expires_at<=? ORDER BY expires_at,row_id LIMIT 500`, s.account, now)
	if err != nil {
		return nil, err
	}
	var keys []engine.MessageKey
	for rows.Next() {
		key := engine.MessageKey{Account: s.account}
		if err := rows.Scan(&key.Peer.Type, &key.Peer.ID, &key.ID); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	var deleted []engine.MessageKey
	for _, key := range keys {
		changed, err := deleteMessage(ctx, tx, key, "ttl", now)
		if err != nil {
			return nil, fmt.Errorf("expire message: %w", err)
		}
		if changed {
			deleted = append(deleted, key)
		}
	}
	if len(deleted) > 0 {
		if err := indexMentions(ctx, tx, s.account, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}

// Transcript is the durable, message-bound result of a transcription. Only the
// contract v1 fields are stored; it has no retention independent of the message.
type Transcript struct {
	Text     string
	RawText  string
	Language string
	Model    string
	Source   string // local, manual, telegram
}

// SaveTranscript checks lifecycle restrictions in the same transaction that
// inserts the text. Replacing via DELETE + INSERT is deliberate: schema v1 has
// an INSERT trigger, not an UPDATE trigger, for refreshing the contentless FTS.
func (s *Store) SaveTranscript(ctx context.Context, key engine.MessageKey, transcript Transcript) error {
	if err := s.checkAccount(key.Account); err != nil {
		return err
	}
	if key.ID <= 0 || key.Peer.ID <= 0 || (key.Peer.Type != engine.PeerUser && key.Peer.Type != engine.PeerChat && key.Peer.Type != engine.PeerChannel) {
		return engine.ErrInvalid
	}
	if transcript.Source != "local" && transcript.Source != "manual" && transcript.Source != "telegram" {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rowID int64
	var mediaTTL, protected, peerProtected int
	var expiry sql.NullInt64
	var access string
	err = tx.QueryRowContext(ctx, `SELECT m.row_id,m.media_ttl,m.protected,m.expires_at,p.protected,p.access FROM messages m JOIN peers p ON p.account=m.account AND p.peer_type=m.peer_type AND p.peer_id=m.peer_id WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=?`, key.Account, key.Peer.Type, key.Peer.ID, key.ID).Scan(&rowID, &mediaTTL, &protected, &expiry, &peerProtected, &access)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.ErrMessageNotFound
	}
	if err != nil {
		return err
	}
	now := s.now().UTC().Unix()
	if mediaTTL > 0 || protected != 0 || peerProtected != 0 || (expiry.Valid && expiry.Int64 <= now) {
		return engine.ErrProtectedContent
	}
	if access != "ok" {
		return engine.ErrNoAccess
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM transcripts WHERE row_id=?`, rowID); err != nil {
		return err
	}
	var raw any
	if transcript.RawText != "" {
		raw = transcript.RawText
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO transcripts(row_id,text,raw_text,language,model,source,created_at) VALUES(?,?,?,?,?,?,?)`, rowID, transcript.Text, raw, transcript.Language, transcript.Model, transcript.Source, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Retention configures cached media and exports only. Zero disables expiration
// for that category. Without an explicit argument Cleanup reads the injected
// account settings, defaulting to 30/7 when unset. Messages, transcripts and
// tombstones are never aged out.
type Retention struct {
	MediaDays  int
	ExportDays int
}

// Cleanup applies retention and resumes the durable unlink queue. It does not
// call Sweep (or acquire writeMu recursively). Janitor runs Sweep then Cleanup.
// A failed unlink remains queued and is returned as an error; other queued
// files and the forensic SQLite cleanup are still attempted.
func (s *Store) Cleanup(ctx context.Context, policies ...Retention) error {
	if len(policies) > 1 {
		return engine.ErrInvalid
	}
	var policy Retention
	if len(policies) == 1 {
		policy = policies[0]
	} else {
		var err error
		policy, err = s.retentionPolicy(ctx)
		if err != nil {
			return err
		}
	}
	for _, days := range []int{policy.MediaDays, policy.ExportDays} {
		if days < 0 || int64(days) > math.MaxInt64/86400 {
			return engine.ErrInvalid
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := s.now().UTC().Unix()
	retentionErr := s.retainFiles(ctx, policy, now)
	unlinkErr := s.unlinkQueued(ctx)
	// Optimize is mandatory even with FTS5 secure-delete: some segment layouts
	// otherwise retain deleted tokens. Vacuum before the final checkpoint so it
	// cannot repopulate a WAL we just truncated.
	var cleanupErr error
	for _, query := range []string{
		`INSERT INTO messages_fts(messages_fts) VALUES('optimize')`,
		`PRAGMA incremental_vacuum`,
	} {
		if _, err := s.writer.ExecContext(ctx, query); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	var busy, logFrames, checkpointed int
	if err := s.writer.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	} else if busy != 0 {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("store cleanup: WAL checkpoint busy (%d frames, %d checkpointed)", logFrames, checkpointed))
	}
	return errors.Join(retentionErr, unlinkErr, cleanupErr)
}

// retainFiles requires writeMu. Queue and metadata changes commit together.
func (s *Store) retainFiles(ctx context.Context, policy Retention, now int64) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := func(days int) (int64, error) {
		seconds := int64(days) * 86400
		if now < math.MinInt64+seconds {
			return 0, engine.ErrInvalid
		}
		return now - seconds, nil
	}
	if policy.MediaDays > 0 {
		before, err := cutoff(policy.MediaDays)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM media_files WHERE row_id IN (SELECT row_id FROM messages WHERE account=?) AND downloaded_at<?`, s.account, before); err != nil {
			return err
		}
		// Unlike media_files and exports, schema v1 has no delete trigger for
		// profile_photos: enqueue explicitly before deleting its metadata.
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO files_to_unlink(path,queued_at,reason) SELECT path,?,'media_retention' FROM profile_photos WHERE account=? AND downloaded_at<?`, now, s.account, before); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM profile_photos WHERE account=? AND downloaded_at<?`, s.account, before); err != nil {
			return err
		}
	}
	if policy.ExportDays > 0 {
		before, err := cutoff(policy.ExportDays)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM exports WHERE account=? AND created_at<?`, s.account, before); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// unlinkQueued requires writeMu, including while unlinking: otherwise a fresh
// writer could reuse a path between our reference check and the unlink.
func (s *Store) unlinkQueued(ctx context.Context) error {
	rows, err := s.writer.QueryContext(ctx, `SELECT path FROM files_to_unlink ORDER BY queued_at,path`)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, path)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if len(paths) == 0 {
		return nil
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := filepath.Abs(s.dir)
	if err != nil {
		return err
	}
	protected, err := s.databasePaths(ctx)
	if err != nil {
		return err
	}
	// Canonicalize all current references once. Exact SQL string equality is
	// insufficient: ./file, file and /root/file can name the same live file.
	refs, err := s.writer.QueryContext(ctx, `SELECT path FROM media_files UNION SELECT path FROM profile_photos UNION SELECT path FROM exports`)
	if err != nil {
		return err
	}
	for refs.Next() {
		var path string
		if err := refs.Scan(&path); err != nil {
			refs.Close()
			return err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		protected[filepath.Clean(path)] = true
	}
	err = refs.Err()
	closeErr = refs.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// SameFile also protects hardlink aliases and references containing an
	// in-root symlink. Root.Stat cannot follow a reference outside the root.
	var protectedFiles []os.FileInfo
	for path := range protected {
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := root.Stat(rel)
		if err == nil {
			protectedFiles = append(protectedFiles, info)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// Open only the two canonical artifact roots, never the entire data dir.
	extraRoots := make(map[string]*os.Root)
	exportDir, err := s.StatsExportDir(ctx, s.account)
	if err != nil {
		return err
	}
	for _, artifactDir := range []string{exportDir, s.mediaRoot()} {
		if purgeBelow(dir, artifactDir) {
			continue
		}
		extraRoots[artifactDir] = nil
		info, e := os.Lstat(artifactDir)
		if e == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("store cleanup: invalid artifact root")
			}
			artifactRoot, e := os.OpenRoot(artifactDir)
			if e != nil {
				return e
			}
			defer artifactRoot.Close()
			extraRoots[artifactDir] = artifactRoot
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	for path := range protected {
		if info, e := os.Stat(path); e == nil {
			protectedFiles = append(protectedFiles, info)
		}
	}
	var result error
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		absolute := path
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(dir, absolute)
		}
		absolute = filepath.Clean(absolute)
		unlinkRoot := root
		relative, err := filepath.Rel(dir, absolute)
		missingRoot := false
		if err == nil && !purgeBelow(dir, absolute) {
			for artifactDir, artifactRoot := range extraRoots {
				if purgeBelow(artifactDir, absolute) {
					relative, err = filepath.Rel(artifactDir, absolute)
					unlinkRoot = artifactRoot
					missingRoot = artifactRoot == nil
					break
				}
			}
		}
		if missingRoot {
			if _, e := s.writer.ExecContext(ctx, `DELETE FROM files_to_unlink WHERE path=?`, path); e != nil {
				result = errors.Join(result, e)
			}
			continue
		}
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || protected[absolute] {
			result = errors.Join(result, errors.New("store cleanup: refusing unsafe or database unlink path"))
			continue
		}
		// Disallow symlinked parents even if they point back inside the root:
		// alias -> . must not turn alias/store.db into an unprotected database.
		if err := lifecycleUnlinkParents(unlinkRoot, relative); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if err := lifecycleRemoveFile(unlinkRoot, relative, protectedFiles); err != nil {
			result = errors.Join(result, err)
			continue
		}
		// A crash here is safe: ENOENT acknowledges the same queue item next boot.
		if _, err = s.writer.ExecContext(ctx, `DELETE FROM files_to_unlink WHERE path=?`, path); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

// Pin the parent directory so a rename or symlink swap cannot redirect Remove
// after the identity check. Remove never follows the final symlink itself.
func lifecycleRemoveFile(root *os.Root, relative string, protected []os.FileInfo) error {
	parent, err := root.OpenRoot(filepath.Dir(relative))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(relative)
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("store cleanup: refusing directory unlink")
	}
	if info.Mode()&os.ModeSymlink == 0 {
		for _, live := range protected {
			if os.SameFile(info, live) {
				return errors.New("store cleanup: refusing live-file alias")
			}
		}
	}
	err = parent.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// lifecycleUnlinkParents rejects symlink aliases, including aliases confined
// within the root. os.Root remains the boundary if a parent changes concurrently.
func lifecycleUnlinkParents(root *os.Root, relative string) error {
	parent := filepath.Dir(relative)
	if parent == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(parent, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("store cleanup: refusing symlinked parent")
		}
		if !info.IsDir() {
			return errors.New("store cleanup: unlink parent is not a directory")
		}
	}
	return nil
}

func (s *Store) databasePaths(ctx context.Context) (map[string]bool, error) {
	rows, err := s.writer.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make(map[string]bool)
	for rows.Next() {
		var seq int
		var name, path string
		if err := rows.Scan(&seq, &name, &path); err != nil {
			return nil, err
		}
		if path == "" {
			continue
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			paths[path+suffix] = true
		}
	}
	return paths, rows.Err()
}

// JanitorOnce is the synchronous startup barrier: call it before sync or HTTP
// routes are exposed, then run RunJanitor in the background. The callback is
// invoked only for committed deletions, after Cleanup and outside writeMu.
func (s *Store) JanitorOnce(ctx context.Context, onDeleted func(engine.Applied)) error {
	applied, sweepErr := s.Sweep(ctx)
	cleanupErr := s.Cleanup(ctx)
	if len(applied.Deleted) > 0 && onDeleted != nil {
		onDeleted(applied)
	}
	return errors.Join(sweepErr, cleanupErr)
}

var lifecycleJanitors sync.Map // *Store -> struct{}; removed on janitor exit

// RunJanitor blocks until cancellation, beginning with a synchronous cycle.
// Operational errors (including a busy checkpoint or a poisoned unlink queue)
// are retried after one second: they must not permanently disable TTL. On exit
// it returns ctx.Err(), joined with the last unresolved operational error.
// Callbacks carry message keys; the engine supplies source="ttl" when building
// delete events. Only one janitor should be started per store.
func (s *Store) RunJanitor(ctx context.Context, onDeleted func(engine.Applied)) error {
	if _, loaded := lifecycleJanitors.LoadOrStore(s, struct{}{}); loaded {
		return errors.New("store janitor: already running")
	}
	defer lifecycleJanitors.Delete(s)
	wake, stop, err := s.janitorCommitWake(ctx)
	if err != nil {
		return err
	}
	defer stop()
	var lastErr error
	for {
		cycleErr := s.JanitorOnce(ctx, onDeleted)
		if err := ctx.Err(); err != nil {
			return errors.Join(err, cycleErr, lastErr)
		}
		lastErr = cycleErr
		if cycleErr == nil {
			waitErr := s.waitJanitor(ctx, wake)
			if waitErr == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(err, waitErr, lastErr)
			}
			lastErr = waitErr
		}
		retry := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			retry.Stop()
			return errors.Join(ctx.Err(), lastErr)
		case <-retry.C:
		}
	}
}

// janitorCommitWake uses the verified modernc v1.60.1 HookRegisterer API on
// the single writer connection. The hook never executes SQL, blocks or aborts a
// commit; it only coalesces wakeups. Recomputing through the writer connection
// below waits for the actual commit, not merely the pre-commit notification.
func (s *Store) janitorCommitWake(ctx context.Context) (<-chan struct{}, func(), error) {
	wake := make(chan struct{}, 1)
	register := func(ctx context.Context, hook sqlite.CommitHookFn) error {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		conn, err := s.writer.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		return conn.Raw(func(raw any) error {
			hooks, ok := raw.(sqlite.HookRegisterer)
			if !ok {
				return errors.New("store janitor: SQLite commit hooks unavailable")
			}
			hooks.RegisterCommitHook(hook)
			return nil
		})
	}
	if err := register(ctx, func() int32 {
		select {
		case wake <- struct{}{}:
		default:
		}
		return 0
	}); err != nil {
		return nil, nil, err
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = register(ctx, nil)
	}
	return wake, stop, nil
}

// Every local commit wakes the exact-expiry timer. The 1s index-only probe is
// a fallback for an external writer or a replaced driver connection. Never
// postpone the existing 60s deadline when recalculating an earlier expiration.
func (s *Store) waitJanitor(ctx context.Context, wake <-chan struct{}) error {
	delay, err := s.nextJanitorDelay(ctx)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	probe := time.NewTicker(time.Second)
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-wake:
		case <-probe.C:
		}
		next, err := s.nextJanitorDelay(ctx)
		if err != nil {
			return err
		}
		if next < time.Until(deadline) {
			deadline = time.Now().Add(next)
			timer.Reset(next)
		}
	}
}

func (s *Store) nextJanitorDelay(ctx context.Context) (time.Duration, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var expiry sql.NullInt64
	if err := s.writer.QueryRowContext(ctx, `SELECT min(expires_at) FROM messages WHERE account=?`, s.account).Scan(&expiry); err != nil {
		return 0, err
	}
	delay := 60 * time.Second
	if expiry.Valid {
		until := time.Unix(expiry.Int64, 0).Sub(s.now())
		if until < 0 {
			until = 0
		}
		if until < delay {
			delay = until
		}
	}
	return delay, nil
}
