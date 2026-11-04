// Package pocstore provides a small, offline SQLite Persister for the gotd
// spike. It is NOT the production internal/store schema or search index.
// Records use explicit account-scoped keys and JSON payloads. All access shares
// one connection, so writes are serialized and no reader observes a partial
// Apply. Checkpoint holding/recovery belongs to the engine's gotd adapter.
package pocstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	_ "modernc.org/sqlite"
)

// Store owns a private PoC database. Do not open a production store with it.
type Store struct{ db *sql.DB }

// Open creates (or reopens) a PoC database at path. Its format is deliberately
// separate from the production store; incompatible databases are rejected.
func Open(path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("pocstore: a file path is required: %w", engine.ErrInvalid)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
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
	// No authority: url.URL{Scheme: "file"} makes a Windows path
	// "file://C:%5C...", which SQLite rejects (see internal/store.fileDSN).
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	var version, appID int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if err = db.QueryRow("PRAGMA application_id").Scan(&appID); err != nil {
		return fail(err)
	}
	const applicationID = 0x504f4331 // POC1, not a production store
	if version > 1 || (version != 0 && appID != applicationID) || (appID != 0 && appID != applicationID) {
		return fail(fmt.Errorf("pocstore: unsupported database format/version %d", version))
	}
	var tables int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return fail(err)
	}
	if version == 0 && tables != 0 {
		return fail(errors.New("pocstore: refusing an existing non-PoC database"))
	}
	for _, pragma := range []string{"busy_timeout=5000", "journal_mode=WAL", "synchronous=NORMAL", "foreign_keys=ON", "secure_delete=ON", "temp_store=MEMORY", "auto_vacuum=INCREMENTAL"} {
		if _, err = db.Exec("PRAGMA " + pragma); err != nil {
			return fail(err)
		}
	}
	if version == 0 {
		tx, err := db.Begin()
		if err != nil {
			return fail(err)
		}
		_, err = tx.Exec(`CREATE TABLE poc_records (
   account TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL,
   body BLOB NOT NULL, version INTEGER NOT NULL DEFAULT 1,
   PRIMARY KEY(account,kind,key)
  ) WITHOUT ROWID; PRAGMA application_id=1347371825; PRAGMA user_version=1;`)
		if err != nil {
			tx.Rollback()
			return fail(err)
		}
		if err = tx.Commit(); err != nil {
			return fail(err)
		}
	}
	return &Store{db: db}, nil
}

// Close releases the database; no session or network connection is owned here.
func (s *Store) Close() error { return s.db.Close() }

type database interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type record struct {
	key     string
	body    []byte
	version int64
}

func key(v any) string { b, _ := json.Marshal(v); return string(b) }
func load(ctx context.Context, db database, acc engine.AccountID, kind, k string, out any) (int64, bool, error) {
	var body []byte
	var version int64
	err := db.QueryRowContext(ctx, "SELECT body,version FROM poc_records WHERE account=? AND kind=? AND key=?", acc, kind, k).Scan(&body, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if err = json.Unmarshal(body, out); err != nil {
		return 0, false, fmt.Errorf("pocstore: invalid %s record: %w", kind, err)
	}
	return version, true, nil
}
func put(ctx context.Context, db database, acc engine.AccountID, kind, k string, v any) (bool, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return false, fmt.Errorf("pocstore: encoding %s: %w", kind, err)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO poc_records(account,kind,key,body) VALUES(?,?,?,?)
 ON CONFLICT(account,kind,key) DO UPDATE SET body=excluded.body,version=poc_records.version+1 WHERE poc_records.body<>excluded.body`, acc, kind, k, body)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
func remove(ctx context.Context, db database, acc engine.AccountID, kind, k string) (bool, error) {
	res, err := db.ExecContext(ctx, "DELETE FROM poc_records WHERE account=? AND kind=? AND key=?", acc, kind, k)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
func records(ctx context.Context, db database, acc engine.AccountID, kind string) ([]record, error) {
	rows, err := db.QueryContext(ctx, "SELECT key,body,version FROM poc_records WHERE account=? AND kind=? ORDER BY key", acc, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.key, &r.body, &r.version); err != nil {
			return nil, err
		}
		r.body = bytes.Clone(r.body)
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LoadUpdatesState(ctx context.Context, acc engine.AccountID) (engine.UpdatesState, bool, error) {
	var st engine.UpdatesState
	_, ok, err := load(ctx, s.db, acc, "updates", "", &st)
	return st, ok, err
}
func (s *Store) SaveUpdatesState(ctx context.Context, acc engine.AccountID, st engine.UpdatesState) error {
	_, err := put(ctx, s.db, acc, "updates", "", st)
	return err
}
