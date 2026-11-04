package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// These tests cover the real Persister, not a manager or an event-bus fake.
// S01/S02/S15 process crashes live in crash_test.go. The manager's held gate,
// restart/getDifference and actual emitted events in S01/S03 belong to front A.
// Here, event-worthy changes mean the fields returned in engine.Applied.
const confAccount engine.AccountID = "store-conformance"

var confTime = time.Unix(1800000000, 0).UTC()

func confOpen(t *testing.T, opts ...Option) *Store {
	t.Helper()
	opts = append([]Option{WithClock(func() time.Time { return confTime })}, opts...)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "account", "store.db"), confAccount, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func confMessage(peer engine.PeerRef, id int, text string) engine.Message {
	return engine.Message{Key: engine.MessageKey{Account: confAccount, Peer: peer, ID: id}, Date: confTime.Add(-time.Hour), Text: text}
}

func confApply(t *testing.T, s *Store, b engine.Batch) engine.Applied {
	t.Helper()
	applied, err := s.Apply(context.Background(), b)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return applied
}

func confGet(t *testing.T, s *Store, key engine.MessageKey) engine.Message {
	t.Helper()
	m, found, err := s.Message(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("Message(%v): found=%v, err=%v", key, found, err)
	}
	return m
}

func confAbsent(t *testing.T, s *Store, key engine.MessageKey) {
	t.Helper()
	m, found, err := s.Message(context.Background(), key)
	if err != nil || found {
		t.Errorf("Message(%v): found=%v, message=%+v, err=%v; want absent", key, found, m, err)
	}
}

func confCount(t *testing.T, s *Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.reader.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func confKeys(t *testing.T, got, want []engine.MessageKey) {
	t.Helper()
	counts := make(map[engine.MessageKey]int)
	for _, k := range got {
		counts[k]++
	}
	for _, k := range want {
		counts[k]--
	}
	if len(got) != len(want) {
		t.Errorf("keys = %v; want %v", got, want)
		return
	}
	for _, n := range counts {
		if n != 0 {
			t.Errorf("keys = %v; want %v", got, want)
			return
		}
	}
}

func confNoEvents(t *testing.T, a engine.Applied) {
	t.Helper()
	// Skipped is diagnostic, not an event or a new durable version.
	if len(a.Inserted)+len(a.Edited)+len(a.Deleted)+len(a.UnresolvedDeletes)+len(a.Reactions)+len(a.Reads)+len(a.Access)+len(a.Gaps)+len(a.Versions) != 0 {
		t.Errorf("no-op exposed event-worthy Applied: %+v", a)
	}
}

func confCheckpoint(t *testing.T, s *Store, want engine.UpdatesState, channel int64, pts int) {
	t.Helper()
	got, found, err := s.LoadUpdatesState(context.Background(), confAccount)
	if err != nil || !found || got != want {
		t.Errorf("global checkpoint = %+v, found=%v, err=%v; want %+v", got, found, err, want)
	}
	gotPts, found, err := s.LoadChannelPts(context.Background(), confAccount, channel)
	if err != nil || !found || gotPts != pts {
		t.Errorf("channel checkpoint = %d, found=%v, err=%v; want %d", gotPts, found, err, pts)
	}
}

func TestS03ApplyErrorRollsBackBeforeSeparateCheckpoint(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("simulated SQLITE_FULL before Apply commit")
	armed, hits := false, 0
	s := confOpen(t, FaultAt(func(stage string) error {
		if armed && stage == "apply_before_commit" {
			hits++
			return injected
		}
		return nil
	}))
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 303}
	old := engine.UpdatesState{Pts: 20, Qts: 3, Date: 100, Seq: 7}
	if err := s.SaveUpdatesState(ctx, confAccount, old); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(ctx, confAccount, peer.ID, 50); err != nil {
		t.Fatal(err)
	}
	m := confMessage(peer, 1, "s03rollbackprobe")
	page := engine.Batch{Account: confAccount, Source: engine.SourceDifference, CatchUp: true,
		Peers:   []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Title: "rolled back peer"}}},
		Dialogs: []engine.Dialog{{Peer: peer, UnreadCount: 1}}, Messages: []engine.Message{m},
		Cursor: &engine.Cursor{Peer: peer, OldestID: 1, NewestID: 1, State: "idle", Fetched: 1},
	}
	armed = true
	applied, err := s.Apply(ctx, page)
	if hits != 1 || !errors.Is(err, injected) || !reflect.DeepEqual(applied, engine.Applied{}) {
		t.Fatalf("failed Apply: hits=%d, Applied=%+v, err=%v; want injected error and empty Applied", hits, applied, err)
	}
	for _, table := range []string{"peers", "dialogs", "messages", "messages_fts", "backfill_cursors"} {
		if n := confCount(t, s, "SELECT count(*) FROM "+table); n != 0 {
			t.Errorf("rollback left %d rows in %s", n, table)
		}
	}
	confAbsent(t, s, m.Key)
	confCheckpoint(t, s, old, peer.ID, 50)
	armed = false
	applied = confApply(t, s, page)
	confKeys(t, applied.Inserted, []engine.MessageKey{m.Key})
	confGet(t, s, m.Key)
	confCheckpoint(t, s, old, peer.ID, 50)
	confNoEvents(t, confApply(t, s, page))
	// Do not pretend to implement the manager's restart or held gate here.
	// The Persister proves that Apply itself never advances either checkpoint.
	newState := engine.UpdatesState{Pts: 21, Qts: 3, Date: 101, Seq: 8}
	if err := s.SaveUpdatesState(ctx, confAccount, newState); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(ctx, confAccount, peer.ID, 51); err != nil {
		t.Fatal(err)
	}
	confCheckpoint(t, s, newState, peer.ID, 51)
}

func TestS04DifferenceReplayDoesNotWriteRowsOrVersions(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 404}
	m := confMessage(peer, 1, "s04replayprobe")
	b := engine.Batch{Account: confAccount, Source: engine.SourceDifference, CatchUp: true,
		Messages: []engine.Message{m}, Cursor: &engine.Cursor{Peer: peer, OldestID: 1, NewestID: 1, Fetched: 1, State: "idle"}}
	a := confApply(t, s, b)
	confKeys(t, a.Inserted, []engine.MessageKey{m.Key})
	version := confCount(t, s, "SELECT version FROM messages WHERE msg_id=1")
	var before, after, lastChanges int64
	if err := s.writer.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	a = confApply(t, s, b)
	if err := s.writer.QueryRow("SELECT total_changes(),changes()").Scan(&after, &lastChanges); err != nil {
		t.Fatal(err)
	}
	confNoEvents(t, a)
	if after != before || lastChanges != 0 {
		t.Errorf("S04 replay wrote rows: total_changes delta=%d, changes()=%d; contract I2 requires both zero", after-before, lastChanges)
	}
	if got := confCount(t, s, "SELECT version FROM messages WHERE msg_id=1"); got != version || got != 1 {
		t.Errorf("replay version=%d, previous=%d; want unchanged 1", got, version)
	}
	if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's04replayprobe'"); n != 1 {
		t.Errorf("replay FTS hits=%d; want 1", n)
	}
}

func TestS05OutOfOrderEditKeepsNewestVersion(t *testing.T) {
	for _, order := range []string{"edit_before_original", "newer_edit_before_older_edit"} {
		t.Run(order, func(t *testing.T) {
			s := confOpen(t)
			peer := engine.PeerRef{Type: engine.PeerUser, ID: 505}
			original := confMessage(peer, 9, "s05original")
			old, newest := original, original
			e1, e2 := confTime.Add(-time.Minute), confTime.Add(-time.Second)
			old.Text, old.EditDate = "s05olderedit", &e1
			newest.Text, newest.EditDate = "s05newestedit", &e2
			a := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{newest}})
			confKeys(t, a.Inserted, []engine.MessageKey{newest.Key})
			stale := original
			if order == "newer_edit_before_older_edit" {
				stale = old
			}
			confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceDifference, Messages: []engine.Message{stale}}))
			confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{newest}}))
			got := confGet(t, s, original.Key)
			if got.Text != newest.Text || got.EditDate == nil || !got.EditDate.Equal(e2) {
				t.Errorf("out-of-order copy regressed newest: %+v", got)
			}
			if n := confCount(t, s, "SELECT version FROM messages WHERE msg_id=9"); n != 1 {
				t.Errorf("stale copy changed version: %d", n)
			}
			if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's05original OR s05olderedit'"); n != 0 {
				t.Errorf("stale text indexed: %d", n)
			}
		})
	}
}

// Fetching a history page and committing it are separate operations. Hold the
// fetched old page in a goroutine while a live edit/delete commits, then release
// it: this reproduces the harmful interleaving without timing sleeps or network.
func confDelayedPage(s *Store, b engine.Batch) func() (engine.Applied, error) {
	release, ready := make(chan struct{}), make(chan struct{})
	type result struct {
		a   engine.Applied
		err error
	}
	done := make(chan result, 1)
	go func() {
		close(ready)
		<-release
		a, err := s.Apply(context.Background(), b)
		done <- result{a, err}
	}()
	<-ready
	return func() (engine.Applied, error) {
		close(release)
		r := <-done
		return r.a, r.err
	}
}

func TestS06aInFlightBackfillCannotOverwriteLiveEdit(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 606}
	old := confMessage(peer, 20, "s06oldcopy")
	confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{old}})
	release := confDelayedPage(s, engine.Batch{Account: confAccount, Source: engine.SourceBackfill, Messages: []engine.Message{old},
		Cursor: &engine.Cursor{Peer: peer, OldestID: 20, NewestID: 20, State: "idle"}})
	newest := old
	edit := confTime.Add(-time.Second)
	newest.Text, newest.EditDate = "s06liveedit", &edit
	a := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{newest}})
	confKeys(t, a.Edited, []engine.MessageKey{old.Key})
	a, err := release()
	if err != nil {
		t.Fatal(err)
	}
	confNoEvents(t, a)
	if got := confGet(t, s, old.Key); got.Text != newest.Text || got.EditDate == nil || !got.EditDate.Equal(edit) {
		t.Errorf("backfill overwrote live edit: %+v", got)
	}
	if n := confCount(t, s, "SELECT version FROM messages WHERE msg_id=20"); n != 2 {
		t.Errorf("version=%d; want 2", n)
	}
}

func TestS06bInFlightBackfillCannotResurrectDeletedMessage(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 607}
	m := confMessage(peer, 21, "s06deletedcopy")
	confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{m}})
	release := confDelayedPage(s, engine.Batch{Account: confAccount, Source: engine.SourceBackfill, Messages: []engine.Message{m}})
	deletion := engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Deletes: []engine.Deletion{{Peer: &peer, IDs: []int{21}}}}
	confKeys(t, confApply(t, s, deletion).Deleted, []engine.MessageKey{m.Key})
	a, err := release()
	if err != nil {
		t.Fatal(err)
	}
	confNoEvents(t, a)
	for _, source := range []engine.Source{engine.SourceBackfill, engine.SourceDifference, engine.SourceSearch} {
		confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Source: source, Messages: []engine.Message{m}}))
	}
	confNoEvents(t, confApply(t, s, deletion))
	confAbsent(t, s, m.Key)
	if n := confCount(t, s, "SELECT count(*) FROM message_tombstones WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?", confAccount, peer.Type, peer.ID, m.Key.ID); n != 1 {
		t.Errorf("tombstones=%d; want 1", n)
	}
	if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's06deletedcopy'"); n != 0 {
		t.Errorf("deleted copy remains in FTS: %d", n)
	}
}

func TestS06cUnknownNonChannelDeletionBlocksLaterBackfill(t *testing.T) {
	s := confOpen(t)
	unknown := engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Deletes: []engine.Deletion{{IDs: []int{44}}}}
	a := confApply(t, s, unknown)
	if !reflect.DeepEqual(a.UnresolvedDeletes, []int{44}) {
		t.Errorf("unresolved=%v; want [44]", a.UnresolvedDeletes)
	}
	confNoEvents(t, confApply(t, s, unknown))
	if n := confCount(t, s, "SELECT count(*) FROM pending_deletes WHERE account=? AND msg_id=44", confAccount); n != 1 {
		t.Errorf("pending deletes=%d; want 1", n)
	}
	user := confMessage(engine.PeerRef{Type: engine.PeerUser, ID: 608}, 44, "s06pendinguser")
	channel := confMessage(engine.PeerRef{Type: engine.PeerChannel, ID: 608}, 44, "s06unrelatedchannel")
	a = confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceBackfill, Messages: []engine.Message{user, channel}})
	confKeys(t, a.Inserted, []engine.MessageKey{channel.Key})
	if a.Skipped != 1 {
		t.Errorf("skipped=%d; want 1", a.Skipped)
	}
	confAbsent(t, s, user.Key)
	confGet(t, s, channel.Key)
	if n := confCount(t, s, "SELECT count(*) FROM message_tombstones WHERE account=? AND peer_type='user' AND peer_id=? AND msg_id=44", confAccount, user.Key.Peer.ID); n != 1 {
		t.Errorf("pending deletion did not become tombstone: %d", n)
	}
}

func TestS06dConcurrentBackfillsKeepCursorMonotonicAndUnique(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 609}
	oldest := confTime.Add(-100 * time.Hour)
	seed := confMessage(peer, 120, "s06live")
	confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{seed},
		Cursor: &engine.Cursor{Peer: peer, OldestID: 100, NewestID: 120, OldestDate: &oldest, State: "running", Fetched: 1}})
	start := make(chan struct{})
	type result struct {
		cursor engine.Cursor
		err    error
	}
	done := make(chan result, 2)
	for _, low := range []int{90, 80} {
		low := low
		go func() {
			<-start
			date := oldest.Add(time.Duration(low-100) * time.Hour)
			b := engine.Batch{Account: confAccount, Source: engine.SourceBackfill,
				Messages: []engine.Message{confMessage(peer, 95, "s06overlap"), confMessage(peer, low, "s06page")},
				Cursor:   &engine.Cursor{Peer: peer, OldestID: low, NewestID: 100, OldestDate: &date, State: "idle", Fetched: int64(121 - low)}}
			_, err := s.Apply(context.Background(), b)
			if err != nil {
				done <- result{err: err}
				return
			}
			cur, found, err := s.Cursor(context.Background(), confAccount, peer)
			if !found && err == nil {
				err = errors.New("committed page has no cursor")
			}
			done <- result{cursor: cur, err: err}
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		r := <-done
		if r.err != nil || r.cursor.OldestID > 90 || r.cursor.NewestID != 120 {
			t.Errorf("concurrent cursor=%+v, err=%v; want oldest <=90, newest=120", r.cursor, r.err)
		}
	}
	cur, found, err := s.Cursor(context.Background(), confAccount, peer)
	wantDate := oldest.Add(-20 * time.Hour)
	if err != nil || !found || cur.OldestID != 80 || cur.NewestID != 120 || cur.OldestDate == nil || !cur.OldestDate.Equal(wantDate) {
		t.Errorf("final cursor=%+v, found=%v, err=%v; want [80..120] and date %v", cur, found, err, wantDate)
	}
	// A later-arriving shallower page is another deterministic regression probe.
	confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceBackfill,
		Messages: []engine.Message{confMessage(peer, 95, "s06overlap")}, Cursor: &engine.Cursor{Peer: peer, OldestID: 90, NewestID: 100, OldestDate: &oldest}}))
	cur, _, err = s.Cursor(context.Background(), confAccount, peer)
	if err != nil || cur.OldestID != 80 || cur.NewestID != 120 || cur.OldestDate == nil || !cur.OldestDate.Equal(wantDate) {
		t.Errorf("shallower replay regressed cursor: %+v, err=%v", cur, err)
	}
	for _, table := range []string{"messages", "messages_fts"} {
		if n := confCount(t, s, "SELECT count(*) FROM "+table); n != 4 {
			t.Errorf("%s rows=%d; want 4 distinct messages", table, n)
		}
	}
}

func TestS07ChannelGapPersistsAndClosesWithBackfill(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 707}
	g := engine.GapMark{Peer: &peer, Reason: engine.GapChannelTooLong, FromID: 10, ToID: 20, Detected: confTime}
	a := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Gaps: []engine.GapMark{g},
		Cursor: &engine.Cursor{Peer: peer, OldestID: 10, NewestID: 20, State: "running"}})
	if len(a.Gaps) != 1 || !reflect.DeepEqual(a.Gaps[0], g) {
		t.Errorf("Applied gaps=%+v; want %+v", a.Gaps, g)
	}
	confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{g}}))
	page, err := s.SearchMessages(context.Background(), confAccount, SearchOptions{Query: "s07gap", Peer: &peer})
	if err != nil || page.Coverage.Complete || len(page.Coverage.Gaps) != 1 || page.Coverage.Gaps[0].Reason != engine.GapChannelTooLong {
		t.Fatalf("open-gap coverage=%+v, err=%v", page.Coverage, err)
	}
	m := confMessage(peer, 15, "s07gap")
	filled := confTime.Add(time.Minute)
	g.Filled = &filled
	pageBatch := engine.Batch{Account: confAccount, Source: engine.SourceBackfill, Messages: []engine.Message{m}, FilledGaps: []engine.GapMark{g},
		Cursor: &engine.Cursor{Peer: peer, OldestID: 1, NewestID: 20, Complete: true, State: "idle"}}
	a = confApply(t, s, pageBatch)
	if len(a.Gaps) != 1 || a.Gaps[0].Filled == nil || !a.Gaps[0].Filled.Equal(filled) {
		t.Errorf("filled Applied gap=%+v", a.Gaps)
	}
	page, err = s.SearchMessages(context.Background(), confAccount, SearchOptions{Query: "s07gap", Peer: &peer})
	if err != nil || !page.Coverage.Complete || len(page.Coverage.Gaps) != 0 || len(page.Messages) != 1 {
		t.Errorf("filled-gap coverage=%+v, hits=%d, err=%v", page.Coverage, len(page.Messages), err)
	}
	if n := confCount(t, s, "SELECT count(*) FROM gaps WHERE reason='channel_too_long' AND filled_at=?", filled.Unix()); n != 1 {
		t.Errorf("durable filled gap count=%d; want 1", n)
	}
	confNoEvents(t, confApply(t, s, pageBatch))
}

func TestS08AccountGapAndRefreshedDialogsRemainVisible(t *testing.T) {
	s := confOpen(t)
	p1, p2 := engine.PeerRef{Type: engine.PeerUser, ID: 808}, engine.PeerRef{Type: engine.PeerChat, ID: 808}
	for _, p := range []engine.PeerRef{p1, p2} {
		confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceBackfill,
			Cursor: &engine.Cursor{Peer: p, OldestID: 1, NewestID: 10, Complete: true, State: "idle"}})
	}
	g := engine.GapMark{Reason: engine.GapDifferenceTooLong, Detected: confTime}
	a := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceDifference,
		Gaps: []engine.GapMark{g}, Dialogs: []engine.Dialog{{Peer: p1, TopMessageID: 30, UnreadCount: 2}, {Peer: p2, TopMessageID: 40, UnreadCount: 3}}})
	if len(a.Gaps) != 1 || a.Gaps[0].Peer != nil || a.Gaps[0].Reason != engine.GapDifferenceTooLong {
		t.Errorf("account gap=%+v", a.Gaps)
	}
	if n := confCount(t, s, "SELECT count(*) FROM dialogs WHERE (peer_type='user' AND top_message_id=30 AND unread_count=2) OR (peer_type='chat' AND top_message_id=40 AND unread_count=3)"); n != 2 {
		t.Errorf("refreshed dialogs=%d; want 2", n)
	}
	// A global gap affects both conversations, even with complete cursors.
	for _, peer := range []*engine.PeerRef{nil, &p1, &p2} {
		page, err := s.SearchMessages(context.Background(), confAccount, SearchOptions{Query: "s08missing", Peer: peer})
		if err != nil || page.Coverage.Complete || len(page.Coverage.Gaps) != 1 || page.Coverage.Gaps[0].Peer != nil {
			t.Errorf("account-gap coverage for %v: %+v, err=%v", peer, page.Coverage, err)
		}
	}
	confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{g}}))
	for _, p := range []engine.PeerRef{p1, p2} {
		cur, found, err := s.Cursor(context.Background(), confAccount, p)
		if err != nil || !found || cur.NewestID != 10 || cur.OldestID != 1 {
			t.Errorf("dialog refresh silently moved backfill checkpoint: %+v, found=%v, err=%v", cur, found, err)
		}
	}
	// Scheduling actual network backfills is the engine's responsibility.
}

func TestS09EditReplacesFTSAndRecalculatesMentions(t *testing.T) {
	s := confOpen(t)
	if err := s.BindSelf(context.Background(), confAccount, engine.Account{ID: confAccount, SelfID: 900, Username: "myself"}); err != nil {
		t.Fatal(err)
	}
	peer := engine.PeerRef{Type: engine.PeerChat, ID: 909}
	m := confMessage(peer, 1, "s09oldtoken")
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	if n := confCount(t, s, "SELECT count(*) FROM mentions"); n != 0 {
		t.Fatalf("unmentioned message indexed: %d", n)
	}
	for i, text := range []string{"s09newtoken 😀 @MySelf", "s09finaltoken"} {
		edit := confTime.Add(time.Duration(i) * time.Second)
		m.Text, m.EditDate = text, &edit
		m.Entities = nil
		if i == 0 {
			// Offset 15 = 11 ASCII runes + space + 2 UTF-16 emoji units + space.
			m.Entities = []engine.Entity{{Type: engine.EntityMention, Offset: 15, Length: 7}}
		}
		a := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{m}})
		confKeys(t, a.Edited, []engine.MessageKey{m.Key})
		if len(a.Inserted) != 0 || a.Versions[VersionKey(m.Key)] != int64(i+2) {
			t.Errorf("edit Applied=%+v; want only edit with version %d", a, i+2)
		}
		term := "s09newtoken"
		wantMentions := int64(1)
		if i == 1 {
			term, wantMentions = "s09finaltoken", 0
		}
		if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?", term); n != 1 {
			t.Errorf("edited token %q hits=%d; want 1", term, n)
		}
		if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's09oldtoken'"); n != 0 {
			t.Errorf("old token still searchable: %d", n)
		}
		if n := confCount(t, s, "SELECT count(*) FROM mentions"); n != wantMentions {
			t.Errorf("edit %d mentions=%d; want %d", i+1, n, wantMentions)
		}
		if i == 0 && confCount(t, s, "SELECT count(*) FROM mentions WHERE by_username=1 AND by_flag=0") != 1 {
			t.Error("UTF-16 username mention was not derived from entity")
		}
		confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceDifference, Edits: []engine.Message{m}}))
	}
	if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's09newtoken'"); n != 0 {
		t.Errorf("intermediate edit remains searchable: %d", n)
	}
}

func TestS10DeleteCascadesAndErasesPhysicalContent(t *testing.T) {
	for _, layout := range []string{"single_segment", "multiple_segments"} {
		t.Run(layout, func(t *testing.T) {
			s := confOpen(t)
			peer := engine.PeerRef{Type: engine.PeerChat, ID: 1010}
			marker := "s10forensicuniquemarkerforcontentremoval"
			m := confMessage(peer, 1, marker)
			m.Mentioned = true
			m.Media = &engine.Media{Type: engine.MediaDocument, Downloadable: true, Remote: []byte("synthetic opaque location")}
			confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{m},
				Reactions: []engine.ReactionUpdate{{Key: m.Key, Counts: []engine.ReactionCount{{Emoji: "👍", Count: 1}}}}})
			if err := s.SaveTranscript(context.Background(), m.Key, Transcript{Text: marker + " transcript", RawText: marker + " raw", Source: "manual"}); err != nil {
				t.Fatal(err)
			}
			mediaPath := filepath.Join(s.dir, "media", "synthetic.bin")
			if err := os.MkdirAll(filepath.Dir(mediaPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mediaPath, []byte(marker), 0600); err != nil {
				t.Fatal(err)
			}
			confApply(t, s, engine.Batch{Account: confAccount, Downloads: []engine.DownloadRecord{{Key: m.Key, Path: mediaPath, Bytes: int64(len(marker)), At: confTime}}})
			if layout == "multiple_segments" {
				for id := 2; id <= 20; id++ {
					other := confMessage(peer, id, fmt.Sprintf("unrelated segment filler %d", id))
					confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{other}})
				}
			}
			if layout == "single_segment" {
				if _, err := s.writer.Exec("INSERT INTO messages_fts(messages_fts) VALUES('optimize')"); err != nil {
					t.Fatal(err)
				}
			}
			segments := confCount(t, s, "SELECT count(DISTINCT segid) FROM messages_fts_idx")
			if (layout == "single_segment" && segments != 1) || (layout == "multiple_segments" && segments < 2) {
				t.Fatalf("FTS fixture %s has %d segments; must exercise distinct physical layouts", layout, segments)
			}
			for _, table := range []string{"messages", "mentions", "reactions", "transcripts", "media_files"} {
				if n := confCount(t, s, "SELECT count(*) FROM "+table); n < 1 {
					t.Fatalf("fixture missing %s", table)
				}
			}
			if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?", marker); n != 1 {
				t.Fatalf("fixture FTS hits=%d; want 1", n)
			}
			// Establish that the physical marker really existed before testing erasure.
			var busy, log, checkpointed int
			if err := s.writer.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil || busy != 0 {
				t.Fatalf("fixture checkpoint busy=%d, err=%v", busy, err)
			}
			before, err := os.ReadFile(filepath.Join(s.dir, "store.db"))
			if err != nil || !bytes.Contains(before, []byte(marker)) {
				t.Fatalf("physical fixture marker missing, err=%v", err)
			}
			deletion := engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Deletes: []engine.Deletion{{Peer: &peer, IDs: []int{1}}}}
			confKeys(t, confApply(t, s, deletion).Deleted, []engine.MessageKey{m.Key})
			confAbsent(t, s, m.Key)
			for _, table := range []string{"mentions", "reactions", "transcripts", "media_files"} {
				if n := confCount(t, s, "SELECT count(*) FROM "+table); n != 0 {
					t.Errorf("delete left %s rows=%d", table, n)
				}
			}
			if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?", marker); n != 0 {
				t.Errorf("cascade reinserted FTS content: %d", n)
			}
			if n := confCount(t, s, "SELECT count(*) FROM message_tombstones WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=1", confAccount, peer.Type, peer.ID); n != 1 {
				t.Errorf("tombstone count=%d; want 1", n)
			}
			if n := confCount(t, s, "SELECT count(*) FROM files_to_unlink WHERE path=?", mediaPath); n != 1 {
				t.Errorf("durable unlink queue=%d; want 1", n)
			}
			confNoEvents(t, confApply(t, s, deletion))
			if err := s.Cleanup(context.Background(), Retention{}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(mediaPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("media retained after committed cleanup: %v", err)
			}
			for _, suffix := range []string{"", "-wal"} {
				data, err := os.ReadFile(filepath.Join(s.dir, "store.db") + suffix)
				if errors.Is(err, os.ErrNotExist) && suffix == "-wal" {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte(marker)) {
					t.Errorf("deleted content remains physically in store.db%s", suffix)
				}
			}
		})
	}
}

func TestS11AccountWideDeleteExcludesChannels(t *testing.T) {
	s := confOpen(t)
	var messages []engine.Message
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		messages = append(messages, confMessage(engine.PeerRef{Type: typ, ID: 1111}, 55, "s11namespace"))
	}
	confApply(t, s, engine.Batch{Account: confAccount, Messages: messages})
	keys, err := s.ResolveMessageIDs(context.Background(), confAccount, []int{55, 55})
	if err != nil {
		t.Fatal(err)
	}
	wantDeleted := []engine.MessageKey{messages[0].Key, messages[1].Key}
	confKeys(t, keys, wantDeleted)
	deletion := engine.Batch{Account: confAccount, Deletes: []engine.Deletion{{IDs: []int{55}}}}
	confKeys(t, confApply(t, s, deletion).Deleted, wantDeleted)
	for _, key := range wantDeleted {
		confAbsent(t, s, key)
	}
	confGet(t, s, messages[2].Key)
	if n := confCount(t, s, "SELECT count(*) FROM message_tombstones WHERE peer_type='channel'"); n != 0 {
		t.Errorf("account-wide delete touched channel tombstones: %d", n)
	}
	confNoEvents(t, confApply(t, s, deletion))
	if n := confCount(t, s, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's11namespace'"); n != 1 {
		t.Errorf("namespace deletion FTS hits=%d; want surviving channel only", n)
	}
}

func TestS12PeerNamespacesAndAccountIsolation(t *testing.T) {
	s := confOpen(t)
	var messages []engine.Message
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		messages = append(messages, confMessage(engine.PeerRef{Type: typ, ID: 5}, 7, "namespace "+string(typ)))
	}
	confKeys(t, confApply(t, s, engine.Batch{Account: confAccount, Messages: messages}).Inserted,
		[]engine.MessageKey{messages[0].Key, messages[1].Key, messages[2].Key})
	for _, m := range messages {
		if got := confGet(t, s, m.Key); got.Text != m.Text || got.Key != m.Key {
			t.Errorf("namespace collision: got %+v, want %+v", got, m)
		}
	}
	if n := confCount(t, s, "SELECT count(DISTINCT row_id) FROM messages WHERE peer_id=5 AND msg_id=7"); n != 3 {
		t.Errorf("physical namespace rows=%d; want 3", n)
	}
	foreign := messages[0]
	foreign.Key.Account = "other-account"
	if _, _, err := s.Message(context.Background(), foreign.Key); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Errorf("cross-account read: %v", err)
	}
	if a, err := s.Apply(context.Background(), engine.Batch{Account: "other-account", Messages: []engine.Message{foreign}}); !errors.Is(err, engine.ErrAccountMismatch) || !reflect.DeepEqual(a, engine.Applied{}) {
		t.Errorf("cross-account batch: Applied=%+v, err=%v", a, err)
	}
	if a, err := s.Apply(context.Background(), engine.Batch{Account: confAccount, Messages: []engine.Message{foreign}}); !errors.Is(err, engine.ErrAccountMismatch) || !reflect.DeepEqual(a, engine.Applied{}) {
		t.Errorf("foreign message in local batch: Applied=%+v, err=%v", a, err)
	}
	if n := confCount(t, s, "SELECT count(*) FROM messages"); n != 3 {
		t.Errorf("account mismatch mutated messages: %d", n)
	}
}

func TestS13MinHashNeverReplacesFullHash(t *testing.T) {
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChannel} {
		t.Run(string(typ), func(t *testing.T) {
			s := confOpen(t)
			peer := engine.PeerRef{Type: typ, ID: 1313}
			for i, step := range []struct {
				hash     int64
				min      bool
				wantHash int64
				found    bool
				title    string
			}{
				{111, true, 0, false, "known title"},
				{222, false, 222, true, ""},
				{333, true, 222, true, ""},
				{0, false, 222, true, ""},
				{444, false, 444, true, ""},
			} {
				confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Title: step.title}, AccessHash: step.hash, HashMin: step.min}}})
				hash, found, err := s.AccessHash(context.Background(), confAccount, peer)
				if err != nil || hash != step.wantHash || found != step.found {
					t.Errorf("step %d full AccessHash=%d, found=%v, err=%v; want %d/%v", i, hash, found, err, step.wantHash, step.found)
				}
				got, found, err := s.LookupPeer(context.Background(), confAccount, peer)
				if err != nil || !found || got.Title != "known title" {
					t.Errorf("empty peer field erased known title: %+v, found=%v, err=%v", got, found, err)
				}
			}
		})
	}
}

func TestS14LostChannelAccessDropsCheckpointButKeepsHistory(t *testing.T) {
	s := confOpen(t)
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 1414}
	other := int64(1415)
	m := confMessage(peer, 10, "s14retainedhistory")
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	for _, id := range []int64{peer.ID, other} {
		if err := s.SaveChannelPts(ctx, confAccount, id, 100); err != nil {
			t.Fatal(err)
		}
	}
	b := engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLost, Reason: "CHANNEL_PRIVATE"}}}
	a := confApply(t, s, b)
	if !reflect.DeepEqual(a.Access, b.Access) || len(a.Gaps) != 1 || a.Gaps[0].Reason != engine.GapAccessLost {
		t.Errorf("lost-access Applied=%+v", a)
	}
	if _, found, err := s.LoadChannelPts(ctx, confAccount, peer.ID); err != nil || found {
		t.Errorf("lost channel checkpoint retained: found=%v, err=%v", found, err)
	}
	seen := make(map[int64]int)
	if err := s.ForEachChannelPts(ctx, confAccount, func(id int64, pts int) error { seen[id] = pts; return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, map[int64]int{other: 100}) {
		t.Errorf("channel enumeration=%v; want unaffected channel only", seen)
	}
	gotPeer, found, err := s.LookupPeer(ctx, confAccount, peer)
	if err != nil || !found || gotPeer.Access != engine.AccessLost {
		t.Errorf("lost peer access=%+v, found=%v, err=%v", gotPeer, found, err)
	}
	confGet(t, s, m.Key)
	cur, found, err := s.Cursor(ctx, confAccount, peer)
	if err != nil || !found || cur.State != "no_access" {
		t.Errorf("lost-access cursor=%+v, found=%v, err=%v", cur, found, err)
	}
	confNoEvents(t, confApply(t, s, b))
}

func TestS16EightWritersEightReadersTenSeconds(t *testing.T) {
	s := confOpen(t)
	const workers, ring = 8, 16
	var seed []engine.Message
	for worker := 0; worker < workers; worker++ {
		for id := 1; id <= ring; id++ {
			m := confMessage(engine.PeerRef{Type: engine.PeerChannel, ID: int64(1600 + worker)}, id, "s16loadprobe")
			m.Out = true
			seed = append(seed, m)
		}
	}
	confApply(t, s, engine.Batch{Account: confAccount, Messages: seed})
	if max := s.writer.Stats().MaxOpenConnections; max != 1 {
		t.Fatalf("writer pool maximum=%d; want 1", max)
	}
	if _, err := s.reader.ExecContext(context.Background(), "UPDATE accounts SET updated_at=0"); err == nil {
		t.Fatal("reader pool permits writes; expected mode=ro")
	}
	start := make(chan struct{})
	// Stop admitting work at 10s, then drain all admitted work under a separate
	// bounded context. Every error is a failure, including SQLITE_INTERRUPT.
	ctx, sqlContext, _, cancel := s16Contexts(context.Background(), 10*time.Second, 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var writes, reads [workers]atomic.Int64
	failures := make(chan error, workers*2)
	report := func(role string, err error) {
		if err == nil {
			return
		}
		failures <- fmt.Errorf("%s: %w", role, err)
	}
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for iteration := 0; ctx.Err() == nil; iteration++ {
				m := confMessage(engine.PeerRef{Type: engine.PeerChannel, ID: int64(1600 + worker)}, iteration%ring+1, "s16loadprobe")
				m.Out = true
				edit := confTime.Add(time.Duration(iteration+1) * time.Second)
				m.EditDate = &edit
				if _, err := s.Apply(sqlContext, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{m}}); err != nil {
					report(fmt.Sprintf("writer %d", worker), err)
					return
				}
				writes[worker].Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			for ctx.Err() == nil {
				// One read transaction observes messages and FTS at the same
				// commit boundary; no intermediate trigger state may leak.
				tx, err := s.reader.BeginTx(sqlContext, &sql.TxOptions{ReadOnly: true})
				if err != nil {
					report(fmt.Sprintf("reader %d begin", worker), err)
					return
				}
				var n, hits int
				err = tx.QueryRowContext(sqlContext, "SELECT count(*) FROM messages").Scan(&n)
				if err == nil {
					err = tx.QueryRowContext(sqlContext, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's16loadprobe'").Scan(&hits)
				}
				rollbackErr := tx.Rollback()
				if err != nil {
					report(fmt.Sprintf("reader %d query", worker), err)
					return
				}
				if rollbackErr != nil {
					report(fmt.Sprintf("reader %d rollback", worker), rollbackErr)
					return
				}
				if n != workers*ring || hits != n {
					failures <- fmt.Errorf("reader %d saw non-atomic messages/FTS: rows=%d hits=%d", worker, n, hits)
					return
				}
				reads[worker].Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	for i := 0; i < workers; i++ {
		if writes[i].Load() == 0 || reads[i].Load() == 0 {
			t.Errorf("worker %d did not exercise both roles: writes=%d reads=%d", i, writes[i].Load(), reads[i].Load())
		}
		t.Logf("worker %d commits=%d read snapshots=%d", i, writes[i].Load(), reads[i].Load())
	}
	rows, err := s.reader.QueryContext(context.Background(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, parent string
		var row sql.NullInt64
		var fk int
		if err := rows.Scan(&table, &row, &parent, &fk); err != nil {
			t.Fatal(err)
		}
		t.Errorf("foreign key violation: table=%s row=%v parent=%s fk=%d", table, row, parent, fk)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
