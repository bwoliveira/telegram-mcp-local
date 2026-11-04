package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func reactionRevisionState(key engine.MessageKey, n int) engine.ReactionUpdate {
	r := engine.ReactionUpdate{Key: key, Counts: []engine.ReactionCount{}, Recent: []engine.PeerReaction{}}
	if n > 0 {
		r.Counts = []engine.ReactionCount{{Emoji: "thumb", Count: n}}
	}
	return r
}

func reactionRevision(t *testing.T, s *Store, key engine.MessageKey) int64 {
	t.Helper()
	var v int64
	if err := s.reader.QueryRow(`SELECT r.version FROM reactions r JOIN messages m ON m.row_id=r.row_id WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=?`, key.Account, key.Peer.Type, key.Peer.ID, key.ID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReactionRevisionBatchReplayRollbackAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(ctx, path, confAccount, WithClock(func() time.Time { return confTime }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m := confMessage(engine.PeerRef{Type: engine.PeerUser, ID: 10}, 8, "synthetic revision")
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	// No previous state and an empty authoritative snapshot are the same state.
	a := confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{reactionRevisionState(m.Key, 0)}})
	if len(a.Reactions) != 0 {
		t.Fatalf("initial empty snapshot emitted a change: %+v", a)
	}
	var updates []engine.ReactionUpdate
	for _, n := range []int{1, 2, 1, 0} {
		updates = append(updates, reactionRevisionState(m.Key, n))
	}
	a = confApply(t, s, engine.Batch{Account: confAccount, Reactions: updates})
	if len(a.Reactions) != len(updates) {
		t.Fatalf("events=%+v", a.Reactions)
	}
	for i, ev := range a.Reactions {
		if ev.Version != int64(i+1) {
			t.Errorf("event %d version=%d", i, ev.Version)
		}
		data, err := json.Marshal(ev)
		if err != nil || strings.Contains(string(data), "version") {
			t.Fatalf("private revision escaped JSON: %s %v", data, err)
		}
	}
	if v := reactionRevision(t, s, m.Key); v != 4 {
		t.Fatalf("version=%d, want 4", v)
	}
	confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Reactions: updates[3:]}))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, confAccount)
	if err != nil {
		t.Fatal(err)
	}
	confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Reactions: updates[3:]}))
	if v := reactionRevision(t, s, m.Key); v != 4 {
		t.Fatalf("reopened empty version=%d", v)
	}
	interrupted := errors.New("synthetic apply interruption")
	s.fault = func(stage string) error {
		if stage == "apply_before_commit" {
			return interrupted
		}
		return nil
	}
	a, err = s.Apply(ctx, engine.Batch{Account: confAccount, Reactions: updates[:1]})
	if !errors.Is(err, interrupted) || len(a.Reactions) != 0 {
		t.Fatalf("rollback result=%+v error=%v", a, err)
	}
	if v := reactionRevision(t, s, m.Key); v != 4 {
		t.Fatalf("rolled-back revision=%d", v)
	}
	s.fault = nil
	a = confApply(t, s, engine.Batch{Account: confAccount, Reactions: updates[:1]})
	if len(a.Reactions) != 1 || a.Reactions[0].Version != 5 {
		t.Fatalf("retry reused/lost revision: %+v", a)
	}
}

func TestReactionRevisionHydrationAndCanonicalReplay(t *testing.T) {
	s := confOpen(t)
	m := confMessage(engine.PeerRef{Type: engine.PeerChannel, ID: 20}, 8, "synthetic hydration")
	m.Reactions = []engine.ReactionCount{{Emoji: "b", Count: 2}, {Emoji: "a", Count: 1}}
	a := confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	if len(a.Reactions) != 0 {
		t.Fatal("hydration emitted reaction events")
	}
	if v := reactionRevision(t, s, m.Key); v != 1 {
		t.Fatalf("hydration revision=%d", v)
	}
	r := engine.ReactionUpdate{Key: m.Key, Counts: []engine.ReactionCount{{Emoji: "a", Count: 1}, {Emoji: "b", Count: 2}}}
	confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{r}}))
	if v := reactionRevision(t, s, m.Key); v != 1 {
		t.Fatalf("canonical replay revision=%d", v)
	}
	r.Counts[0].Chosen = true
	a = confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{r}})
	if len(a.Reactions) != 1 || a.Reactions[0].Version != 2 {
		t.Fatalf("chosen state revision=%+v", a)
	}
	for _, actor := range []int64{11, 12, 11} {
		r.Recent = []engine.PeerReaction{{By: engine.PeerRef{Type: engine.PeerUser, ID: actor}, Emoji: "a"}}
		a = confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{r}})
		if len(a.Reactions) != 1 || a.Reactions[0].Version != reactionRevision(t, s, m.Key) {
			t.Fatalf("reactor revision=%+v", a)
		}
	}
	if v := reactionRevision(t, s, m.Key); v != 5 {
		t.Fatalf("reactor revisions=%d", v)
	}
}
