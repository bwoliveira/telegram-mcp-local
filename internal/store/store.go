// Package store implements the account-scoped relational SQLite Persister.
// It owns no Telegram connection. Call JanitorOnce before exposing local reads.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	_ "modernc.org/sqlite"
)

//go:embed migrations/001.sql
var schemaV1 string

// Store owns one account file, a serialized writer and a separate read-only pool.
// Never copy a Store. Close only after stopping its consumers and janitor.
type Store struct {
	writer, reader    *sql.DB
	account           engine.AccountID
	dir               string
	now               func() time.Time
	writeMu           sync.Mutex
	fault             func(string) error
	retentionSettings RetentionSettings
}
type Option func(*Store)

func WithClock(clock func() time.Time) Option {
	return func(s *Store) {
		if clock != nil {
			s.now = clock
		}
	}
}

// FaultAt injects a per-instance fault. Stages: migration_before_commit,
// migration_v2_before_commit, migration_v3_before_commit, migration_v4_before_commit,
// migration_v5_before_commit, migration_v6_before_commit, migration_v7_before_commit,
// migration_v8_before_commit, migration_v9_before_commit,
// difference_checkpoint_before_commit, apply_before_commit, apply_after_commit.
// A crash hook may call os.Exit(137).
func FaultAt(hook func(stage string) error) Option { return func(s *Store) { s.fault = hook } }
func (s *Store) fail(stage string) error {
	if s.fault != nil {
		return s.fault(stage)
	}
	return nil
}

// Open creates or opens a store.db. Canonical placement is
// <data>/accounts/<slot>/store.db; the caller supplies that path explicitly.
func Open(ctx context.Context, path string, account engine.AccountID, opts ...Option) (*Store, error) {
	if path == "" || path == ":memory:" || account == "" {
		return nil, engine.ErrInvalid
	}
	migrations := storeMigrations()
	if err := validateMigrationSequence(migrations); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, engine.ErrInvalid
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	q := url.Values{}
	for _, p := range []string{"busy_timeout=5000", "foreign_keys=ON", "secure_delete=ON", "temp_store=MEMORY", "synchronous=NORMAL"} {
		q.Add("_pragma", p)
	}
	w, err := sql.Open("sqlite", fileDSN(path, q))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	s := &Store{writer: w, account: account, dir: dir, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	fail := func(e error) (*Store, error) { w.Close(); return nil, e }
	var v int
	if err = w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fail(err)
	}
	if v > currentSchemaVersion {
		return fail(fmt.Errorf("store: database version %d is newer than supported version %d", v, currentSchemaVersion))
	}
	if v == 0 {
		var tables int
		if err = w.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return fail(err)
		}
		if tables != 0 {
			return fail(errors.New("store: refusing unversioned nonempty database"))
		}
		if _, err = w.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return fail(err)
		}
	}
	if _, err = w.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fail(err)
	}
	for _, migration := range migrations {
		if v < migration.version {
			if err = migration.apply(s, ctx); err != nil {
				return fail(err)
			}
		}
	}
	// The account binding is a single writer transaction: two processes may
	// never claim an empty file for different slots at the same time.
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO accounts(account,updated_at) SELECT ?,? WHERE NOT EXISTS(SELECT 1 FROM accounts)`, account, s.now().Unix())
	if err != nil {
		tx.Rollback()
		return fail(err)
	}
	var bound string
	if err = tx.QueryRowContext(ctx, `SELECT account FROM accounts ORDER BY account<>? DESC LIMIT 1`, account).Scan(&bound); err != nil {
		tx.Rollback()
		return fail(err)
	}
	if bound != string(account) {
		tx.Rollback()
		return fail(engine.ErrAccountMismatch)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	q.Set("mode", "ro")
	r, err := sql.Open("sqlite", fileDSN(path, q))
	if err != nil {
		return fail(err)
	}
	r.SetMaxOpenConns(8)
	r.SetMaxIdleConns(8)
	s.reader = r
	if err = r.PingContext(ctx); err != nil {
		r.Close()
		return fail(err)
	}
	return s, nil
}
func (s *Store) migrateV1(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, schemaV1); err != nil {
		return fmt.Errorf("store: migration 1: %w", err)
	}
	if err = s.fail("migration_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Close() error { return errors.Join(s.reader.Close(), s.writer.Close()) }
func (s *Store) checkAccount(a engine.AccountID) error {
	if a == "" {
		return engine.ErrInvalid
	}
	if a != s.account {
		return engine.ErrAccountMismatch
	}
	return nil
}
func validPeer(p engine.PeerRef) bool {
	return p.ID > 0 && (p.Type == engine.PeerUser || p.Type == engine.PeerChat || p.Type == engine.PeerChannel)
}
func validKey(a engine.AccountID, k engine.MessageKey) error {
	if k.Account != a {
		return engine.ErrAccountMismatch
	}
	if !validPeer(k.Peer) || k.ID <= 0 {
		return engine.ErrInvalid
	}
	return nil
}
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}
func defaultKind(p engine.PeerRef) engine.PeerKind {
	switch p.Type {
	case engine.PeerChat:
		return engine.KindGroup
	case engine.PeerChannel:
		return engine.KindChannel
	default:
		return engine.KindUser
	}
}

// fileDSN is the SQLite URI for an absolute path. It has no authority
// ("file:/x", not "file:///x" or "file://x"): url.URL{Scheme: "file"} turns a
// Windows path into "file://C:%5C...", whose authority SQLite rejects
// ("invalid uri authority"), so the store could not open on Windows at all.
// internal/state and internal/index use the same form.
func fileDSN(path string, query url.Values) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + query.Encode()
}
