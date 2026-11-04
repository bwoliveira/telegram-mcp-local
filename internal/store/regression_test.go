package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

func TestReviewRegressions(t *testing.T) {
	ctx := context.Background()
	p := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	t.Run("TTLNotRetroactive", func(t *testing.T) {
		s := confOpen(t)
		m := confMessage(p, 1, "old")
		confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
		confApply(t, s, engine.Batch{Account: confAccount, Dialogs: []engine.Dialog{{Peer: p, TTLPeriod: 60}}})
		confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
		confGet(t, s, m.Key)
	})
	t.Run("DeletedAnswerReopensMention", func(t *testing.T) {
		s := confOpen(t)
		m := confMessage(p, 1, "mention")
		m.Mentioned = true
		r := confMessage(p, 2, "answer")
		r.Out = true
		confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m, r}})
		confApply(t, s, engine.Batch{Account: confAccount, Deletes: []engine.Deletion{{Peer: &p, IDs: []int{2}}}})
		var answered sql.NullInt64
		if err := s.reader.QueryRow("SELECT answered_by FROM mentions").Scan(&answered); err != nil {
			t.Fatal(err)
		}
		if answered.Valid {
			t.Fatalf("deleted response still answered: %+v", answered)
		}
	})
	t.Run("ExpiredAnswerReopensMention", func(t *testing.T) {
		s := confOpen(t)
		m := confMessage(p, 1, "mention")
		m.Mentioned = true
		r := confMessage(p, 2, "answer")
		r.Out = true
		expiry := confTime.Add(time.Hour)
		r.ExpiresAt = &expiry
		confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m, r}})
		s.now = func() time.Time { return expiry }
		if _, err := s.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		var answered sql.NullInt64
		if err := s.reader.QueryRow("SELECT answered_by FROM mentions").Scan(&answered); err != nil {
			t.Fatal(err)
		}
		if answered.Valid {
			t.Fatal("expired response still answered")
		}
	})
	t.Run("RecentSurvivesCountsOnlyEdit", func(t *testing.T) {
		s := confOpen(t)
		m := confMessage(p, 1, "reactions")
		confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
		r := engine.ReactionUpdate{Key: m.Key, Counts: []engine.ReactionCount{{Emoji: "a", Count: 1}}, Recent: []engine.PeerReaction{{By: p, Emoji: "a"}}}
		confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{r}})
		ed := confTime
		m.EditDate = &ed
		m.Reactions = r.Counts
		confApply(t, s, engine.Batch{Account: confAccount, Edits: []engine.Message{m}})
		a := confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{r}})
		if len(a.Reactions) != 0 {
			t.Fatal("duplicate reaction after edit")
		}
	})
	t.Run("PeerAccessRunsLifecycle", func(t *testing.T) {
		s := confOpen(t)
		ch := engine.PeerRef{Type: engine.PeerChannel, ID: 9}
		if err := s.SaveChannelPts(ctx, confAccount, 9, 10); err != nil {
			t.Fatal(err)
		}
		a := confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ch, Access: engine.AccessLost}}}, Access: []engine.AccessChange{{Peer: ch, State: engine.AccessLost}}})
		_, ok, e := s.LoadChannelPts(ctx, confAccount, 9)
		if e != nil || ok || len(a.Access) != 1 {
			t.Fatalf("lost skipped lifecycle: %+v %v %v", a, ok, e)
		}
	})
	t.Run("FullBatchReplayZeroWrites", func(t *testing.T) {
		s := confOpen(t)
		m := confMessage(p, 1, "mention")
		m.Mentioned = true
		b := engine.Batch{Account: confAccount, Messages: []engine.Message{m}, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, FirstName: "Éric", IsContact: true}}}, Dialogs: []engine.Dialog{{Peer: p, UnreadCount: 1}}}
		confApply(t, s, b)
		var before, after int64
		s.writer.QueryRow("SELECT total_changes()").Scan(&before)
		confApply(t, s, b)
		s.writer.QueryRow("SELECT total_changes()").Scan(&after)
		if before != after {
			t.Fatalf("replay changes=%d", after-before)
		}
	})
	t.Run("AccentAndBadFTS", func(t *testing.T) {
		s := confOpen(t)
		confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, FirstName: "Éric", IsContact: true}}}})
		contacts, e := s.ListContacts(ctx, confAccount, PeerOptions{Query: "Éric"})
		if e != nil || len(contacts) != 1 {
			t.Errorf("accent query: %v %v", contacts, e)
		}
		_, e = s.SearchMessages(ctx, confAccount, SearchOptions{Query: "NEAR(foo, bar)"})
		if !errors.Is(e, engine.ErrInvalid) {
			t.Errorf("FTS: %v", e)
		}
	})
}
