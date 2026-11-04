package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

const crashAccount engine.AccountID = "crash-test-account"

// These tests exercise only the Persister boundary. The real manager's
// getDifference, held-checkpoint gate and event bus are front A's responsibility.
func TestS01CrashAfterCommitReplayCheckpoint(t *testing.T) {
	crashApplyReplay(t, "apply_after_commit", true)
}

func TestS02CrashBeforeCommitReplayCheckpoint(t *testing.T) {
	crashApplyReplay(t, "apply_before_commit", false)
}

func crashApplyReplay(t *testing.T, stage string, committed bool) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s := crashOpen(t, path)
	seed, page := crashBatches()
	if _, err := s.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	oldState, newState := crashStates()
	if err := s.SaveUpdatesState(ctx, crashAccount, oldState); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(ctx, crashAccount, page.Cursor.Peer.ID, 200); err != nil {
		t.Fatal(err)
	}
	crashAssertData(t, s, false)
	crashClose(t, s)

	crashRunChild(t, path, stage)
	s = crashOpen(t, path)
	// Inspect recovery before replay can repair anything. In particular, the
	// cursor, every message and its FTS posting must agree about the commit.
	crashAssertData(t, s, committed)
	crashAssertCheckpoint(t, s, oldState, 200)
	applied, err := s.Apply(ctx, page)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if committed {
		crashAssertNoChanges(t, applied)
	} else {
		want := []engine.MessageKey{page.Messages[0].Key, page.Messages[1].Key}
		if !reflect.DeepEqual(applied.Inserted, want) || len(applied.Versions) != len(want) {
			t.Fatalf("first successful replay Applied = %+v; want inserted %v with versions", applied, want)
		}
		for _, version := range applied.Versions {
			if version != 1 {
				t.Fatalf("first successful replay version = %d; want 1", version)
			}
		}
		applied.Inserted, applied.Versions = nil, nil
		crashAssertNoChanges(t, applied)
	}
	crashAssertData(t, s, true)
	crashAssertCheckpoint(t, s, oldState, 200)
	// A second replay must expose no event-worthy Applied changes, including
	// after the interrupted transaction has been replayed successfully.
	applied, err = s.Apply(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	crashAssertNoChanges(t, applied)
	crashAssertData(t, s, true)
	crashAssertCheckpoint(t, s, oldState, 200)

	// Checkpoints are deliberately separate writes, made only after data is
	// known committed. No Apply call above is allowed to advance either one.
	if err := s.SaveUpdatesState(ctx, crashAccount, newState); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(ctx, crashAccount, page.Cursor.Peer.ID, 202); err != nil {
		t.Fatal(err)
	}
	crashClose(t, s)
	s = crashOpen(t, path)
	crashAssertCheckpoint(t, s, newState, 202)
	crashAssertData(t, s, true)
	applied, err = s.Apply(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	crashAssertNoChanges(t, applied)
	crashAssertData(t, s, true)
}

func crashBatches() (engine.Batch, engine.Batch) {
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 701}
	date := time.Unix(1700000000, 0).UTC()
	oldest := date.Add(-2 * time.Second)
	message := func(id int, text string, at time.Time) engine.Message {
		return engine.Message{Key: engine.MessageKey{Account: crashAccount, Peer: peer, ID: id}, Date: at, Text: text}
	}
	seed := engine.Batch{
		Account: crashAccount, Source: engine.SourceUpdate,
		Peers:    []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Title: "before crash"}}},
		Messages: []engine.Message{message(90, "crashbaseline", date)},
		Cursor:   &engine.Cursor{Peer: peer, NewestID: 90, OldestID: 90, OldestDate: &date, State: "running", Fetched: 1},
	}
	page := engine.Batch{
		Account: crashAccount, Source: engine.SourceDifference, CatchUp: true,
		Peers:    []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Title: "after crash"}}},
		Messages: []engine.Message{message(89, "crashpagealpha", date.Add(-time.Second)), message(88, "crashpagebeta", oldest)},
		Cursor:   &engine.Cursor{Peer: peer, NewestID: 90, OldestID: 88, OldestDate: &oldest, State: "idle", Complete: true, Fetched: 3},
	}
	return seed, page
}

func crashStates() (engine.UpdatesState, engine.UpdatesState) {
	return engine.UpdatesState{Pts: 100, Qts: 7, Date: 1700000000, Seq: 11},
		engine.UpdatesState{Pts: 102, Qts: 8, Date: 1700000002, Seq: 12}
}

func crashAssertCheckpoint(t *testing.T, s *Store, want engine.UpdatesState, channelPts int) {
	t.Helper()
	state, found, err := s.LoadUpdatesState(context.Background(), crashAccount)
	if err != nil || !found || state != want {
		t.Fatalf("global checkpoint = %+v, found=%v, err=%v; want %+v", state, found, err, want)
	}
	_, page := crashBatches()
	pts, found, err := s.LoadChannelPts(context.Background(), crashAccount, page.Cursor.Peer.ID)
	if err != nil || !found || pts != channelPts {
		t.Fatalf("channel checkpoint = %d, found=%v, err=%v; want %d", pts, found, err, channelPts)
	}
}

func crashAssertData(t *testing.T, s *Store, committed bool) {
	t.Helper()
	ctx := context.Background()
	seed, page := crashBatches()
	wantCursor, wantTitle, wantCount := seed.Cursor, "before crash", 1
	if committed {
		wantCursor, wantTitle, wantCount = page.Cursor, "after crash", 3
	}
	cursor, found, err := s.Cursor(ctx, crashAccount, page.Cursor.Peer)
	if err != nil || !found || !reflect.DeepEqual(cursor, *wantCursor) {
		t.Fatalf("cursor = %+v, found=%v, err=%v; want %+v", cursor, found, err, *wantCursor)
	}
	peer, found, err := s.LookupPeer(ctx, crashAccount, page.Cursor.Peer)
	if err != nil || !found || peer.Title != wantTitle {
		t.Fatalf("peer = %+v, found=%v, err=%v; want title %q", peer, found, err, wantTitle)
	}
	for _, query := range []string{"SELECT count(*) FROM messages", "SELECT count(*) FROM messages_fts"} {
		var count int
		if err := s.reader.QueryRowContext(ctx, query).Scan(&count); err != nil || count != wantCount {
			t.Fatalf("%s = %d, err=%v; want %d", query, count, err, wantCount)
		}
	}
	messages := append(append([]engine.Message{}, seed.Messages...), page.Messages...)
	for i, want := range messages {
		present := i == 0 || committed
		got, found, err := s.Message(ctx, want.Key)
		if err != nil || found != present || (present && (got.Key != want.Key || got.Text != want.Text || !got.Date.Equal(want.Date))) {
			t.Fatalf("message %v = %+v, found=%v, err=%v; want present=%v, text=%q", want.Key, got, found, err, present, want.Text)
		}
		wantHits := 0
		if present {
			wantHits = 1
			var version int
			if err := s.reader.QueryRowContext(ctx, "SELECT version FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?", want.Key.Account, want.Key.Peer.Type, want.Key.Peer.ID, want.Key.ID).Scan(&version); err != nil || version != 1 {
				t.Fatalf("message %v version=%d, err=%v; want 1", want.Key, version, err)
			}
		}
		var hits int
		if err := s.reader.QueryRowContext(ctx, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?", want.Text).Scan(&hits); err != nil || hits != wantHits {
			t.Fatalf("FTS MATCH %q = %d, err=%v; want %d", want.Text, hits, err, wantHits)
		}
		var joined int
		if err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM messages_fts JOIN messages m ON m.row_id=messages_fts.rowid WHERE messages_fts MATCH ? AND m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=?`, want.Text, want.Key.Account, want.Key.Peer.Type, want.Key.Peer.ID, want.Key.ID).Scan(&joined); err != nil || joined != wantHits {
			t.Fatalf("FTS row identity for %v = %d, err=%v; want %d", want.Key, joined, err, wantHits)
		}
	}
}

func crashAssertNoChanges(t *testing.T, applied engine.Applied) {
	t.Helper()
	// Skipped is diagnostic, not an event. Every change-bearing field,
	// including the version map, must otherwise be empty.
	if len(applied.Inserted)+len(applied.Edited)+len(applied.Deleted)+len(applied.UnresolvedDeletes)+len(applied.Reactions)+len(applied.Reads)+len(applied.Access)+len(applied.Gaps)+len(applied.Versions) != 0 {
		t.Fatalf("replay exposed duplicate/event-worthy Applied changes: %+v", applied)
	}
}

// TestCrashStoreSubprocess is invoked in a fresh copy of this test binary.
// Only the selected FaultAt hook exits 137; normal returns and setup failures
// must not masquerade as a crash at the intended transaction boundary.
func TestCrashStoreSubprocess(t *testing.T) {
	stage := os.Getenv("STORE_CRASH_TEST_STAGE")
	if stage == "" {
		t.Skip("subprocess helper")
	}
	if stage != "apply_before_commit" && stage != "apply_after_commit" && stage != "migration_before_commit" {
		t.Fatalf("unknown crash stage %q", stage)
	}
	path := os.Getenv("STORE_CRASH_TEST_PATH")
	if path == "" {
		t.Fatal("missing subprocess database path")
	}
	s, err := Open(context.Background(), path, crashAccount, FaultAt(func(at string) error {
		if at == stage {
			fmt.Fprintf(os.Stdout, "STORE_CRASH_HIT:%s\n", at)
			os.Exit(137)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if stage != "migration_before_commit" {
		_, page := crashBatches()
		if _, err := s.Apply(context.Background(), page); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("selected crash hook %q was never reached", stage)
}

func crashRunChild(t *testing.T, path, stage string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestCrashStoreSubprocess$", "-test.v")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "STORE_CRASH_TEST_STAGE=") && !strings.HasPrefix(entry, "STORE_CRASH_TEST_PATH=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "STORE_CRASH_TEST_STAGE="+stage, "STORE_CRASH_TEST_PATH="+path)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 137 || !bytes.Contains(out, []byte("STORE_CRASH_HIT:"+stage+"\n")) {
		t.Fatalf("crash subprocess at %q: err=%v, context=%v, want exit 137 and exact hook marker; output:\n%s", stage, err, ctx.Err(), out)
	}
}

func crashOpen(t *testing.T, path string, opts ...Option) *Store {
	t.Helper()
	s, err := Open(context.Background(), path, crashAccount, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { crashClose(t, s) })
	return s
}

func crashClose(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("close store: %v", err)
	}
}

// Version 1's N-1 fixture is an empty, real SQLite database at user_version=0.
// No older application schema or migration fixture exists in this contract.
func TestS15CrashMigrationFromZero(t *testing.T) {
	for _, mode := range []string{"normal", "process_crash", "error_rollback"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			db := crashRawDB(t, path, false)
			if _, err := db.Exec("PRAGMA user_version=0"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			crashAssertEmptyV0(t, path)
			switch mode {
			case "process_crash":
				crashRunChild(t, path, "migration_before_commit")
				crashAssertEmptyV0(t, path)
			case "error_rollback":
				injected := errors.New("crash test migration failure")
				hit := false
				s, err := Open(context.Background(), path, crashAccount, FaultAt(func(stage string) error {
					if stage == "migration_before_commit" {
						hit = true
						return injected
					}
					return nil
				}))
				if s != nil {
					crashClose(t, s)
				}
				if !hit || !errors.Is(err, injected) || s != nil {
					t.Fatalf("migration fault: hit=%v, store=%v, err=%v", hit, s, err)
				}
				crashAssertEmptyV0(t, path)
			}
			s := crashOpen(t, path)
			var version int
			if err := s.reader.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentSchemaVersion {
				t.Fatalf("recovered migration version=%d, err=%v; want %d", version, err, currentSchemaVersion)
			}
			for _, table := range []string{"accounts", "peers", "messages", "messages_fts", "updates_state", "channel_state", "backfill_cursors", "message_tombstones", "pending_deletes"} {
				var count int
				if err := s.reader.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("migration table %q: count=%d, err=%v", table, count, err)
				}
			}
			seed, page := crashBatches()
			for _, batch := range []engine.Batch{seed, page} {
				if _, err := s.Apply(context.Background(), batch); err != nil {
					t.Fatalf("migrated database is not usable: %v", err)
				}
			}
			crashAssertData(t, s, true)
			crashClose(t, s)
			s = crashOpen(t, path)
			crashAssertData(t, s, true)
		})
	}
}

func crashRawDB(t *testing.T, path string, readOnly bool) *sql.DB {
	t.Helper()
	q := url.Values{}
	if readOnly {
		q.Set("mode", "ro")
	}
	db, err := sql.Open("sqlite", fileDSN(path, q))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close raw database: %v", err)
		}
	})
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

func crashAssertEmptyV0(t *testing.T, path string) {
	t.Helper()
	db := crashRawDB(t, path, true)
	defer db.Close()
	var version, objects int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatalf("interrupted migration version=%d, err=%v; want 0", version, err)
	}
	// Include indexes, triggers and FTS shadow tables: no partial schema is
	// allowed to survive, not merely the absence of a single main table.
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("version-0 schema objects=%d, err=%v; want no partial schema", objects, err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("version-0 integrity=%q, err=%v", integrity, err)
	}
}

func TestS15CrashFutureVersionRejectedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := crashRawDB(t, path, false)
	futureVersion := currentSchemaVersion + 1
	if _, err := db.Exec(fmt.Sprintf("CREATE TABLE future_payload(value TEXT NOT NULL); INSERT INTO future_payload VALUES('preserve future data'); PRAGMA user_version=%d", futureVersion)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	s, err := Open(context.Background(), path, crashAccount, FaultAt(func(string) error {
		hit = true
		return errors.New("future database must not run migrations")
	}))
	if s != nil {
		crashClose(t, s)
	}
	if err == nil || s != nil || hit || !strings.Contains(err.Error(), fmt.Sprintf("version %d", futureVersion)) || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("future version rejection: store=%v, hook=%v, err=%v; want clear version error before migration", s, hit, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("future-version rejection changed database file contents")
	}
	db = crashRawDB(t, path, true)
	var version int
	var value string
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != futureVersion {
		t.Fatalf("future version=%d, err=%v; want unchanged %d", version, err, futureVersion)
	}
	if err := db.QueryRow("SELECT value FROM future_payload").Scan(&value); err != nil || value != "preserve future data" {
		t.Fatalf("future payload=%q, err=%v", value, err)
	}
	var objects int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil || objects != 1 {
		t.Fatalf("future database schema changed: objects=%d, err=%v; want original table only", objects, err)
	}
}
