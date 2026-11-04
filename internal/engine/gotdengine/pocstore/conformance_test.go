package pocstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestPatchesChannelCallbacksAndReset(t *testing.T) {
	s := openStore(t)
	n := 20
	if err := s.PatchUpdatesState(ctx, "primary", engine.UpdatesPatch{Pts: &n}); err == nil {
		t.Fatal("patch of missing row succeeded")
	}
	initial := engine.UpdatesState{Pts: 1, Qts: 2, Date: 3, Seq: 4}
	if err := s.SaveUpdatesState(ctx, "primary", initial); err != nil {
		t.Fatal(err)
	}
	if err := s.PatchUpdatesState(ctx, "primary", engine.UpdatesPatch{Pts: &n, Seq: &n}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LoadUpdatesState(ctx, "primary")
	if err != nil || !ok || got != (engine.UpdatesState{Pts: 20, Qts: 2, Date: 3, Seq: 20}) {
		t.Fatalf("patch: %+v %v %v", got, ok, err)
	}
	for _, acc := range []engine.AccountID{"primary", "other"} {
		if err := s.SaveChannelPts(ctx, acc, 25, 70); err != nil {
			t.Fatal(err)
		}
	}
	p := engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel, ID: 25}}
	apply(t, s, engine.Batch{Peers: []engine.PeerUpsert{{Peer: p, AccessHash: 55}}})
	callbackError := errors.New("callback stopped")
	err = s.ForEachChannelPts(ctx, "primary", func(id int64, pts int) error {
		// Must not deadlock by calling back into this single-connection store.
		if _, ok, err := s.AccessHash(ctx, "primary", p.Ref); err != nil || !ok {
			t.Fatalf("callback hash: %v %v", ok, err)
		}
		if id != 25 || pts != 70 {
			t.Fatalf("callback values: %d %d", id, pts)
		}
		return callbackError
	})
	if !errors.Is(err, callbackError) {
		t.Fatal(err)
	}
	if err := s.ResetUpdates(ctx, "primary"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadUpdatesState(ctx, "primary"); err != nil || ok {
		t.Fatalf("reset global: %v %v", ok, err)
	}
	if _, ok, err := s.LoadChannelPts(ctx, "primary", 25); err != nil || ok {
		t.Fatalf("reset channel: %v %v", ok, err)
	}
	if _, ok, err := s.LoadChannelPts(ctx, "other", 25); err != nil || !ok {
		t.Fatalf("reset crossed accounts: %v %v", ok, err)
	}
	if err := s.DropChannelState(ctx, "other", 25); err != nil {
		t.Fatal(err)
	}
}

func TestReactionsReadsAccessAndGaps(t *testing.T) {
	s := openStore(t)
	m := message(1, "body")
	m.Out = true
	m.Media = &engine.Media{Type: engine.MediaDocument, Downloadable: true, Remote: []byte{1, 2, 3}}
	apply(t, s, engine.Batch{Messages: []engine.Message{m}})
	if got := mustMessage(t, s, m); !bytes.Equal(got.Media.Remote, m.Media.Remote) {
		t.Fatal("opaque media bytes lost")
	}
	reaction := engine.ReactionUpdate{Key: m.Key, Counts: []engine.ReactionCount{{Emoji: "z", Count: 2}, {Emoji: "a", Count: 1}}, Recent: []engine.PeerReaction{{By: peer, Emoji: "a"}}}
	b := engine.Batch{Reactions: []engine.ReactionUpdate{reaction}}
	r := apply(t, s, b)
	if len(r.Reactions) != 1 || len(r.Reactions[0].Added) != 1 {
		t.Fatalf("reactions: %+v", r)
	}
	if reaction.Counts[0].Emoji != "z" {
		t.Fatal("mutated caller's reaction slice")
	}
	noEvents(t, apply(t, s, b))
	reaction.Counts = []engine.ReactionCount{{Emoji: "a", Count: 1}}
	reaction.Recent = nil
	r = apply(t, s, engine.Batch{Reactions: []engine.ReactionUpdate{reaction}})
	if len(r.Reactions) != 1 || len(r.Reactions[0].Removed) != 1 {
		t.Fatalf("removed: %+v", r)
	}
	read := engine.ReadUpdate{Peer: peer, MaxID: 1, Outbox: true}
	r = apply(t, s, engine.Batch{Reads: []engine.ReadUpdate{read}})
	if len(r.Reads) != 1 || !reflect.DeepEqual(r.Reads[0].MessageIDs, []int{1}) {
		t.Fatalf("read: %+v", r)
	}
	noEvents(t, apply(t, s, engine.Batch{Reads: []engine.ReadUpdate{read}}))
	read.Outbox = false
	read.StillUnread = 4
	noEvents(t, apply(t, s, engine.Batch{Reads: []engine.ReadUpdate{read}}))
	dialogs, err := s.Dialogs(ctx, "primary")
	if err != nil || len(dialogs) != 1 || dialogs[0].UnreadCount != 4 || dialogs[0].ReadInboxMaxID != 1 || dialogs[0].ReadOutboxMaxID != 1 {
		t.Fatalf("dialogs: %+v %v", dialogs, err)
	}
	ch := engine.PeerRef{Type: engine.PeerChannel, ID: 9}
	m.Key.Peer = ch
	apply(t, s, engine.Batch{Messages: []engine.Message{m}})
	if err := s.SaveChannelPts(ctx, "primary", 9, 10); err != nil {
		t.Fatal(err)
	}
	access := engine.AccessChange{Peer: ch, State: engine.AccessLost, Reason: "CHANNEL_PRIVATE"}
	r = apply(t, s, engine.Batch{Access: []engine.AccessChange{access}})
	if len(r.Access) != 1 || len(r.Gaps) != 1 {
		t.Fatalf("access: %+v", r)
	}
	if _, ok, err := s.LoadChannelPts(ctx, "primary", 9); err != nil || ok {
		t.Fatalf("lost channel still enumerated: %v %v", ok, err)
	}
	mustMessage(t, s, m)
	c, ok, err := s.Cursor(ctx, "primary", ch)
	if err != nil || !ok || c.State != "no_access" {
		t.Fatalf("access cursor: %+v %v %v", c, ok, err)
	}
	noEvents(t, apply(t, s, engine.Batch{Access: []engine.AccessChange{access}}))
	at := time.Unix(100, 0).UTC()
	g := engine.GapMark{Peer: &ch, Reason: engine.GapChannelTooLong, Detected: at, FromID: 1, ToID: 8}
	r = apply(t, s, engine.Batch{Gaps: []engine.GapMark{g}})
	if len(r.Gaps) != 1 {
		t.Fatal(r)
	}
	noEvents(t, apply(t, s, engine.Batch{Gaps: []engine.GapMark{g}}))
	filled := at.Add(time.Hour)
	g.Filled = &filled
	if r = apply(t, s, engine.Batch{FilledGaps: []engine.GapMark{g}}); len(r.Gaps) != 1 {
		t.Fatal(r)
	}
	noEvents(t, apply(t, s, engine.Batch{FilledGaps: []engine.GapMark{g}}))
	g.Filled = nil
	noEvents(t, apply(t, s, engine.Batch{Gaps: []engine.GapMark{g}}))
}

func TestInjectedCursorWriteFailureRollsBackPage(t *testing.T) {
	s := openStore(t)
	_, err := s.db.Exec(`CREATE TRIGGER fail_cursor BEFORE INSERT ON poc_records WHEN new.kind='cursor' BEGIN SELECT RAISE(ABORT,'injected cursor failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	m := message(2, "transaction must roll back")
	c := engine.Cursor{Peer: peer, OldestID: 2}
	result, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}, Cursor: &c})
	if err == nil || !reflect.DeepEqual(result, engine.Applied{}) {
		t.Fatalf("failure returned partial events: %+v %v", result, err)
	}
	missing(t, s, m.Key)
	if _, err = s.db.Exec("DROP TRIGGER fail_cursor"); err != nil {
		t.Fatal(err)
	}
	apply(t, s, engine.Batch{Messages: []engine.Message{m}, Cursor: &c})
	mustMessage(t, s, m)
}

func TestConcurrentReadersWritersAndCancellation(t *testing.T) {
	s := openStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for worker := 0; worker < 8; worker++ {
		wg.Add(2)
		go func(worker int) {
			defer wg.Done()
			for i := 1; i <= 20; i++ {
				m := message(worker*100+i, "concurrent")
				if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
					errs <- err
					return
				}
			}
		}(worker)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				if _, _, err := s.Message(ctx, message(1, "").Key); err != nil {
					errs <- err
					return
				}
				if _, err := s.Dialogs(ctx, "primary"); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM poc_records WHERE kind='message'").Scan(&count); err != nil || count != 160 {
		t.Fatalf("lost rows: %d %v", count, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Apply(canceled, engine.Batch{Account: "primary", Messages: []engine.Message{message(9999, "not committed")}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Apply: %v", err)
	}
	missing(t, s, message(9999, "").Key)
}

func TestPrivateFilesReopenTombstonesAndForensicDeletion(t *testing.T) {
	name := "escaped?#.db"
	if runtime.GOOS == "windows" {
		// '?' is forbidden on Windows; '#' and spaces still exercise URI escaping.
		name = "escaped #.db"
	}
	path := filepath.Join(t.TempDir(), "private", name)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	st, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0600) {
		t.Fatalf("database permissions: %v %v", st, err)
	}
	st, err = os.Stat(filepath.Dir(path))
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0700) {
		t.Fatalf("directory permissions: %v %v", st, err)
	}
	marker := "unique-forensic-body-marker-do-not-retain"
	m := message(7, marker)
	apply(t, s, engine.Batch{Messages: []engine.Message{m}})
	apply(t, s, engine.Batch{Deletes: []engine.Deletion{{Peer: &peer, IDs: []int{7}}}})
	if _, err = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Fatalf("deleted body remains in %s", suffix)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	noEvents(t, apply(t, s, engine.Batch{Messages: []engine.Message{m}}))
	missing(t, s, m.Key)
}

func TestRejectForeignOrFutureDatabases(t *testing.T) {
	for _, future := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "other.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		statement := "CREATE TABLE production_store(content TEXT)"
		if future {
			statement = "PRAGMA user_version=99"
		}
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if s, err := Open(path); err == nil {
			s.Close()
			t.Fatal("opened incompatible database")
		}
	}
}

func TestAccountMismatchAndUnsupportedDownloadsLeaveNothing(t *testing.T) {
	s := openStore(t)
	m := message(1, "data")
	m.Key.Account = "other"
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Downloads: []engine.DownloadRecord{{Key: message(1, "").Key}}}); !errors.Is(err, engine.ErrNotSupported) {
		t.Fatalf("download: %v", err)
	}
	if err := s.SetAuthState(ctx, "primary", engine.AuthRevoked, "test revocation"); err != nil {
		t.Fatal(err)
	}
	var record accountRecord
	_, ok, err := load(ctx, s.db, "primary", "account", "", &record)
	if err != nil || !ok || record.Auth != engine.AuthRevoked {
		t.Fatalf("auth: %+v %v %v", record, ok, err)
	}
}
