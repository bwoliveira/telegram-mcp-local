package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func lifecycleOpen(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), engine.AccountID("lifecycle"), WithClock(now))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func lifecycleExec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.writer.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func lifecycleCount(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := s.reader.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func lifecycleMessage(t *testing.T, s *Store, id int, expires any) engine.MessageKey {
	t.Helper()
	lifecycleExec(t, s, `INSERT OR IGNORE INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES('lifecycle','user',1,'user',0)`)
	lifecycleExec(t, s, `INSERT INTO messages(account,peer_type,peer_id,msg_id,date,text,source,first_seen_at,updated_at,expires_at) VALUES('lifecycle','user',1,?,0,?,'update',0,0,?)`, id, fmt.Sprintf("lifecyclemessage%d", id), expires)
	return engine.MessageKey{Account: "lifecycle", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1}, ID: id}
}

func TestLifecycleL01SweepBatchesAndIdempotence(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	for id := 1; id <= 1001; id++ {
		lifecycleMessage(t, s, id, now.Unix())
	}
	lifecycleMessage(t, s, 1002, now.Unix()+1)
	lifecycleMessage(t, s, 1003, nil)
	lifecycleExec(t, s, `INSERT INTO transcripts(row_id,text,source,created_at) SELECT row_id,'transcriptneedle','local',0 FROM messages WHERE msg_id=1`)
	lifecycleExec(t, s, `INSERT INTO reactions(row_id,counts_json,counts_hash,updated_at) SELECT row_id,'[]','hash',0 FROM messages WHERE msg_id=1`)
	lifecycleExec(t, s, `INSERT INTO mentions(row_id,account,peer_type,peer_id,msg_id,date) SELECT row_id,account,peer_type,peer_id,msg_id,date FROM messages WHERE msg_id=1`)
	applied, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Deleted) != 1001 {
		t.Fatalf("deleted=%d, want 1001", len(applied.Deleted))
	}
	for _, key := range applied.Deleted {
		if key.Account != "lifecycle" || key.Peer.Type != engine.PeerUser || key.Peer.ID != 1 || key.ID > 1001 {
			t.Fatalf("bad delete key: %+v", key)
		}
	}
	for _, table := range []string{"transcripts", "reactions", "mentions"} {
		if n := lifecycleCount(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s count=%d", table, n)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages`); n != 2 {
		t.Fatalf("messages=%d", n)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM message_tombstones WHERE reason='ttl'`); n != 1001 {
		t.Fatalf("tombstones=%d", n)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'transcriptneedle'`); n != 0 {
		t.Fatalf("FTS retained transcript: %d", n)
	}
	applied, err = s.Sweep(context.Background())
	if err != nil || len(applied.Deleted) != 0 {
		t.Fatalf("replay=%+v err=%v", applied, err)
	}
}

func lifecycleArtifacts(t *testing.T, s *Store) []byte {
	t.Helper()
	var all []byte
	for _, name := range []string{"store.db", "store.db-wal", "store.db-shm"} {
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	return all
}

func TestLifecycleL03ForensicSweepAndCleanup(t *testing.T) {
	for _, padding := range []int{0, 128} {
		t.Run(fmt.Sprintf("segments-%d", padding), func(t *testing.T) { lifecycleForensicSweepAndCleanup(t, padding) })
	}
}

func lifecycleForensicSweepAndCleanup(t *testing.T, padding int) {
	t.Helper()
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	lifecycleMessage(t, s, 1, now.Unix())
	const message = "forensicmessageqzvuniq"
	const transcript = "forensictranscriptqzvuniq"
	lifecycleExec(t, s, `UPDATE messages SET text=? WHERE msg_id=1`, message)
	lifecycleExec(t, s, `INSERT INTO transcripts(row_id,text,source,created_at) SELECT row_id,?,'local',0 FROM messages WHERE msg_id=1`, transcript)
	for id := 2; id < padding+2; id++ {
		lifecycleMessage(t, s, id, nil)
	}
	if padding > 0 {
		lifecycleExec(t, s, `INSERT INTO messages_fts(messages_fts) VALUES('optimize')`)
		lifecycleExec(t, s, `UPDATE messages SET sender_name='newsegment' WHERE msg_id>1`)
	}
	media := filepath.Join(s.dir, message+"-"+transcript+".ogg")
	if err := os.WriteFile(media, []byte(message+" "+transcript), 0600); err != nil {
		t.Fatal(err)
	}
	lifecycleExec(t, s, `INSERT INTO media_files(row_id,variant,path,bytes,sha256,downloaded_at) SELECT row_id,'full',?,13,'hash',? FROM messages WHERE msg_id=1`, media, now.Unix())
	before := lifecycleArtifacts(t, s)
	for _, marker := range []string{message, transcript} {
		if !bytes.Contains(before, []byte(marker)) {
			t.Fatalf("invalid forensic fixture: %s absent before deletion", marker)
		}
	}
	if _, err := s.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media remains: %v", err)
	}
	after := lifecycleArtifacts(t, s)
	if err := filepath.Walk(s.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		after = append(after, []byte(path)...)
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			after = append(after, b...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{message, transcript} {
		if bytes.Contains(after, []byte(marker)) {
			t.Errorf("forensic marker survives in DB/WAL/SHM: %s", marker)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?`, message+" OR "+transcript); n != 0 {
		t.Fatalf("FTS retained deleted content: %d", n)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 0 {
		t.Fatalf("pending unlinks=%d", n)
	}
	var walSize int64
	if info, err := os.Stat(filepath.Join(s.dir, "store.db-wal")); err == nil {
		walSize = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if walSize != 0 {
		t.Fatalf("WAL not truncated: %d bytes", walSize)
	}
}

func TestLifecycleL04QueuedUnlinkSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	s, err := Open(ctx, path, "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "orphan.ogg")
	if err := os.WriteFile(media, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	lifecycleExec(t, s, `INSERT INTO files_to_unlink(path,queued_at,reason) VALUES(?,0,'test')`, media)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash orphan remains: %v", err)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 0 {
		t.Fatalf("queue=%d", n)
	}
}

func TestLifecycleL05TranscriptGuardsAndFTSReplacement(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, guard := range []string{"missing", "expired", "ttl-media", "protected-message", "protected-peer", "lost-access", "wrong-account"} {
		t.Run(guard, func(t *testing.T) {
			s := lifecycleOpen(t, func() time.Time { return now })
			key := lifecycleMessage(t, s, 1, nil)
			switch guard {
			case "missing":
				lifecycleExec(t, s, `DELETE FROM messages`)
			case "expired":
				lifecycleExec(t, s, `UPDATE messages SET expires_at=?`, now.Unix())
			case "ttl-media":
				lifecycleExec(t, s, `UPDATE messages SET media_ttl=10`)
			case "protected-message":
				lifecycleExec(t, s, `UPDATE messages SET protected=1`)
			case "protected-peer":
				lifecycleExec(t, s, `UPDATE peers SET protected=1`)
			case "lost-access":
				lifecycleExec(t, s, `UPDATE peers SET access='lost'`)
			case "wrong-account":
				key.Account = "other"
			}
			err := s.SaveTranscript(context.Background(), key, Transcript{Text: "forbiddentranscript", Source: "local"})
			if err == nil {
				t.Fatal("guard accepted forbidden transcript")
			}
			if n := lifecycleCount(t, s, `SELECT count(*) FROM transcripts`); n != 0 {
				t.Fatalf("forbidden transcript persisted: %d", n)
			}
			if n := lifecycleCount(t, s, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'forbiddentranscript'`); n != 0 {
				t.Fatalf("forbidden FTS=%d", n)
			}
		})
	}
	s := lifecycleOpen(t, func() time.Time { return now })
	key := lifecycleMessage(t, s, 1, nil)
	for _, text := range []string{"firsttranscriptneedle", "secondtranscriptneedle"} {
		if err := s.SaveTranscript(context.Background(), key, Transcript{Text: text, Source: "local", Language: "pt", Model: "tiny"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM transcripts`); n != 1 {
		t.Fatalf("transcripts=%d", n)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'firsttranscriptneedle'`); n != 0 {
		t.Fatalf("old transcript still indexed: %d", n)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'secondtranscriptneedle'`); n != 1 {
		t.Fatalf("replacement not indexed: %d", n)
	}
}

func TestLifecycleL06L11RetentionKeepsMessagesAndEvidence(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	key := lifecycleMessage(t, s, 1, nil)
	if err := s.SaveTranscript(context.Background(), key, Transcript{Text: "keeptranscript", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	oldMedia := filepath.Join(s.dir, "old.ogg")
	newMedia := filepath.Join(s.dir, "new.ogg")
	oldExport := filepath.Join(s.dir, "old.json")
	newExport := filepath.Join(s.dir, "new.json")
	for _, path := range []string{oldMedia, newMedia, oldExport, newExport} {
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleExec(t, s, `INSERT INTO media_files(row_id,variant,path,bytes,sha256,downloaded_at) SELECT row_id,'full',?,4,'old',? FROM messages`, oldMedia, now.Add(-31*24*time.Hour).Unix())
	lifecycleExec(t, s, `INSERT INTO media_files(row_id,variant,path,bytes,sha256,downloaded_at) SELECT row_id,'thumb',?,4,'new',? FROM messages`, newMedia, now.Unix())
	lifecycleExec(t, s, `INSERT INTO exports(id,account,path,filter_json,count,bytes,created_at) VALUES(1,'lifecycle',?,'{}',1,4,?),(2,'lifecycle',?,'{}',1,4,?)`, oldExport, now.Add(-8*24*time.Hour).Unix(), newExport, now.Unix())
	lifecycleExec(t, s, `INSERT INTO pending_deletes(account,msg_id,received_at) VALUES('lifecycle',998,1)`)
	lifecycleExec(t, s, `INSERT INTO message_tombstones(account,peer_type,peer_id,msg_id,deleted_at,reason) VALUES('lifecycle','user',1,999,1,'deleted')`)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{oldMedia, oldExport} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old file retained: %s (%v)", path, err)
		}
	}
	for _, path := range []string{newMedia, newExport} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("new file removed: %s (%v)", path, err)
		}
	}
	for _, table := range []string{"messages", "transcripts", "pending_deletes", "message_tombstones", "media_files", "exports"} {
		if n := lifecycleCount(t, s, `SELECT count(*) FROM `+table); n != 1 {
			t.Errorf("%s=%d", table, n)
		}
	}
}

func TestLifecycleCleanupConfinesUnlinksAndProtectsDatabase(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, kind := range []string{"absolute-outside", "relative-traversal", "symlink-parent", "inroot-database-alias", "hardlink-database-alias", "database", "wal", "directory", "referenced", "normalized-reference", "hardlink-reference"} {
		t.Run(kind, func(t *testing.T) {
			s := lifecycleOpen(t, func() time.Time { return now })
			outside := filepath.Join(t.TempDir(), "valuable.txt")
			if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			queued := outside
			switch kind {
			case "relative-traversal":
				queued = "../" + filepath.Base(filepath.Dir(outside)) + "/valuable.txt"
			case "symlink-parent":
				link := filepath.Join(s.dir, "escape")
				if err := os.Symlink(filepath.Dir(outside), link); err != nil {
					t.Fatal(err)
				}
				queued = filepath.Join(link, "valuable.txt")
			case "inroot-database-alias":
				link := filepath.Join(s.dir, "alias")
				if err := os.Symlink(".", link); err != nil {
					t.Fatal(err)
				}
				queued = filepath.Join(link, "store.db")
			case "hardlink-database-alias":
				queued = filepath.Join(s.dir, "database-alias")
				if err := os.Link(filepath.Join(s.dir, "store.db"), queued); err != nil {
					t.Fatal(err)
				}
			case "database":
				queued = filepath.Join(s.dir, "store.db")
			case "wal":
				queued = filepath.Join(s.dir, "store.db-wal")
			case "directory":
				queued = s.dir
			case "referenced", "normalized-reference", "hardlink-reference":
				lifecycleMessage(t, s, 1, nil)
				queued = filepath.Join(s.dir, "active.ogg")
				if err := os.WriteFile(queued, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				reference := queued
				if kind == "normalized-reference" {
					reference = "./active.ogg"
				}
				lifecycleExec(t, s, `INSERT INTO media_files(row_id,variant,path,bytes,sha256,downloaded_at) SELECT row_id,'full',?,4,'hash',? FROM messages`, reference, now.Unix())
				if kind == "hardlink-reference" {
					queued = filepath.Join(s.dir, "active-alias.ogg")
					if err := os.Link(reference, queued); err != nil {
						t.Fatal(err)
					}
				}
			}
			lifecycleExec(t, s, `INSERT INTO files_to_unlink(path,queued_at,reason) VALUES(?,0,'test')`, queued)
			if err := s.Cleanup(context.Background()); err == nil {
				t.Fatal("unsafe queue must produce an error")
			}
			if _, err := os.Stat(outside); err != nil {
				t.Fatalf("outside file changed: %v", err)
			}
			if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 1 {
				t.Fatalf("unsafe queue dropped: %d", n)
			}
			if n := lifecycleCount(t, s, `SELECT count(*) FROM accounts`); n != 1 {
				t.Fatalf("database changed: %d", n)
			}
		})
	}
}

func TestLifecycleSweepCommittedBatchesOnly(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	for id := 1; id <= 501; id++ {
		lifecycleMessage(t, s, id, now.Unix())
	}
	lifecycleExec(t, s, `CREATE TRIGGER lifecycle_injected_error BEFORE DELETE ON messages WHEN old.msg_id=501 BEGIN SELECT RAISE(ABORT,'injected delete failure'); END`)
	applied, err := s.Sweep(context.Background())
	if err == nil {
		t.Fatal("expected second batch failure")
	}
	if len(applied.Deleted) != 500 {
		t.Fatalf("committed deletes=%d, want 500", len(applied.Deleted))
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages`); n != 1 {
		t.Fatalf("remaining messages=%d", n)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM message_tombstones`); n != 500 {
		t.Fatalf("rolled-back tombstone escaped: %d", n)
	}
	lifecycleExec(t, s, `DROP TRIGGER lifecycle_injected_error`)
	applied, err = s.Sweep(context.Background())
	if err != nil || len(applied.Deleted) != 1 {
		t.Fatalf("retry=%+v err=%v", applied, err)
	}
}

func TestLifecycleL06RetentionZeroAndBoundary(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	lifecycleMessage(t, s, 1, nil)
	media := filepath.Join(s.dir, "ancient.ogg")
	photo := filepath.Join(s.dir, "ancient.jpg")
	export := filepath.Join(s.dir, "ancient.ndjson")
	for _, path := range []string{media, photo, export} {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleExec(t, s, `INSERT INTO media_files(row_id,variant,path,bytes,sha256,downloaded_at) SELECT row_id,'full',?,4,'hash',? FROM messages`, media, now.Add(-30*24*time.Hour).Unix())
	lifecycleExec(t, s, `INSERT INTO profile_photos(account,peer_type,peer_id,variant,path,bytes,downloaded_at) VALUES('lifecycle','user',1,'full',?,4,0)`, photo)
	lifecycleExec(t, s, `INSERT INTO exports(id,account,path,filter_json,count,bytes,created_at) VALUES(1,'lifecycle',?,'{}',1,4,0)`, export)
	if err := s.Cleanup(context.Background(), Retention{MediaDays: 0, ExportDays: 0}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{media, photo, export} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("retention disabled removed %s: %v", path, err)
		}
	}
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media); err != nil {
		t.Fatalf("exact boundary must remain: %v", err)
	}
	for _, path := range []string{photo, export} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expired artifact survives: %s (%v)", path, err)
		}
	}
	now = now.Add(time.Second)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("boundary+1 should expire: %v", err)
	}
}

func TestLifecycleCleanupCheckpointBusyIsNotSuccess(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	lifecycleMessage(t, s, 1, nil)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	tx, err := s.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	lifecycleExec(t, s, `UPDATE messages SET text='checkpointbusyneedle' WHERE msg_id=1`)
	lifecycleExec(t, s, `PRAGMA busy_timeout=10`)
	if err := s.Cleanup(context.Background()); err == nil {
		t.Fatal("pinned reader: falsely reported successful truncation")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(s.dir, "store.db-wal")); err == nil && info.Size() != 0 {
		t.Fatalf("retry did not truncate WAL: %d", info.Size())
	}
}

func TestLifecycleUnlinkFinalSymlinkDoesNotFollowTarget(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	target := filepath.Join(t.TempDir(), "valuable.txt")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.dir, "old-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	lifecycleExec(t, s, `INSERT INTO files_to_unlink(path,queued_at,reason) VALUES(?,0,'test')`, link)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink not removed: %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatalf("symlink target changed: %q (%v)", b, err)
	}
}

func TestLifecycleD6DelayBounds(t *testing.T) {
	now := time.Unix(1800000000, 250000000)
	s := lifecycleOpen(t, func() time.Time { return now })
	lifecycleMessage(t, s, 1, nil)
	for _, test := range []struct {
		expiry any
		want   time.Duration
	}{
		{nil, 60 * time.Second},
		{now.Unix() + 120, 60 * time.Second},
		{now.Unix() + 60, 59750 * time.Millisecond},
		{now.Unix() + 1, 750 * time.Millisecond},
		{now.Unix(), 0},
	} {
		lifecycleExec(t, s, `UPDATE messages SET expires_at=?`, test.expiry)
		got, err := s.nextJanitorDelay(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("expiry=%v delay=%v want=%v", test.expiry, got, test.want)
		}
	}
}

func TestLifecycleRetentionFailureDoesNotSkipForensicWork(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	lifecycleMessage(t, s, 1, now.Unix())
	lifecycleMessage(t, s, 2, nil)
	const marker = "forensicretentionfailneedle"
	lifecycleExec(t, s, `UPDATE messages SET text=? WHERE msg_id=1`, marker)
	orphan := filepath.Join(s.dir, "orphan.ogg")
	retained := filepath.Join(s.dir, "retained.ogg")
	for _, path := range []string{orphan, retained} {
		if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleExec(t, s, `INSERT INTO files_to_unlink(path,queued_at,reason) VALUES(?,0,'test')`, orphan)
	lifecycleExec(t, s, `INSERT INTO media_files(row_id,variant,path,bytes,sha256,downloaded_at) SELECT row_id,'full',?,4,'hash',0 FROM messages WHERE msg_id=2`, retained)
	if _, err := s.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	lifecycleExec(t, s, `CREATE TRIGGER lifecycle_retention_failure BEFORE DELETE ON media_files BEGIN SELECT RAISE(ABORT,'injected retention failure'); END`)
	if err := s.Cleanup(context.Background()); err == nil {
		t.Fatal("retention failure not reported")
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("independent unlink skipped: %v", err)
	}
	if bytes.Contains(lifecycleArtifacts(t, s), []byte(marker)) {
		t.Fatal("retention failure skipped forensic cleanup")
	}
}

func TestLifecycleJanitorContinuesAfterPoisonedUnlink(t *testing.T) {
	s := lifecycleOpen(t, time.Now)
	lifecycleMessage(t, s, 1, time.Now().Add(time.Second).Unix())
	lifecycleExec(t, s, `INSERT INTO files_to_unlink(path,queued_at,reason) VALUES(?,0,'test')`, filepath.Join(s.dir, "store.db"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	calls := 0
	err := s.RunJanitor(ctx, func(a engine.Applied) { calls++; cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup disabled future TTL sweeps: %v", err)
	}
	if calls != 1 {
		t.Fatalf("callbacks=%d", calls)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages`); n != 0 {
		t.Fatalf("expired rows=%d", n)
	}
}

func TestLifecycleCommitWakeAndDetach(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := lifecycleOpen(t, func() time.Time { return now })
	wake, stop, err := s.janitorCommitWake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	lifecycleMessage(t, s, 1, now.Add(time.Second).Unix())
	select {
	case <-wake:
	default:
		t.Fatal("writer commit did not synchronously notify expiry timer")
	}
	stop()
	lifecycleExec(t, s, `UPDATE messages SET expires_at=?`, now.Unix())
	select {
	case <-wake:
		t.Fatal("stopped janitor still receives commits")
	default:
	}
}

func TestLifecycleJanitorNoticesEarlierInsertedExpiry(t *testing.T) {
	s := lifecycleOpen(t, time.Now)
	lifecycleMessage(t, s, 1, time.Now().Add(2*time.Minute).Unix())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	events := make(chan engine.Applied, 1)
	done := make(chan error, 1)
	go func() { done <- s.RunJanitor(ctx, func(a engine.Applied) { events <- a; cancel() }) }()
	// Allow the first cycle to choose its 60s timer, then introduce an expiry
	// earlier than the one it originally saw. No engine-specific wake API exists.
	time.Sleep(100 * time.Millisecond)
	lifecycleMessage(t, s, 2, time.Now().Add(time.Second).Unix())
	select {
	case a := <-events:
		if len(a.Deleted) != 1 || a.Deleted[0].ID != 2 {
			t.Fatalf("deletes=%+v", a.Deleted)
		}
	case <-ctx.Done():
		t.Error("janitor slept through a newly inserted short expiry")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("janitor exit=%v", err)
	}
}

func TestLifecycleL02JanitorBootAndD6ExactExpiry(t *testing.T) {
	t.Run("boot", func(t *testing.T) {
		now := time.Unix(1800000000, 0)
		s := lifecycleOpen(t, func() time.Time { return now })
		lifecycleMessage(t, s, 1, now.Unix())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		err := s.RunJanitor(ctx, func(a engine.Applied) {
			calls++
			if len(a.Deleted) != 1 {
				t.Errorf("boot deletes=%d", len(a.Deleted))
			}
			cancel()
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("janitor cancellation=%v", err)
		}
		if calls != 1 {
			t.Fatalf("boot callbacks=%d", calls)
		}
	})
	t.Run("exact-expiry", func(t *testing.T) {
		s := lifecycleOpen(t, time.Now)
		lifecycleMessage(t, s, 1, time.Now().Add(time.Second).Unix())
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		calls := 0
		err := s.RunJanitor(ctx, func(a engine.Applied) {
			calls++
			if len(a.Deleted) != 1 {
				t.Errorf("timer deletes=%d", len(a.Deleted))
			}
			cancel()
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("did not use sub-60-second expiry timer: %v", err)
		}
		if calls != 1 {
			t.Fatalf("callbacks=%d", calls)
		}
	})
}
