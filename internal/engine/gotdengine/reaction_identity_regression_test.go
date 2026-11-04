package gotdengine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// The fixed clocks deliberately make receipt/server time unusable as an event
// version. Every database is synthetic, and no Telegram client is started.
func reactionIdentityEngine(t *testing.T, ctx context.Context, path string, at time.Time) (*Engine, *store.Store) {
	t.Helper()
	db, err := store.Open(ctx, path, "primary", store.WithClock(func() time.Time { return at }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
	if err := db.BindSelf(ctx, "primary", e.account); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		return fmt.Errorf("unexpected RPC in offline reaction identity test: %T", in)
	}))
	return e, db
}

func reactionIdentityState(count int, actor int64, chosen bool) tg.MessageReactions {
	r := tg.MessageReactions{}
	if count == 0 {
		return r
	}
	c := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: count}
	if chosen {
		c.SetChosenOrder(0)
	}
	r.Results = []tg.ReactionCount{c}
	if actor != 0 {
		r.SetRecentReactions([]tg.MessagePeerReaction{{PeerID: &tg.PeerUser{UserID: actor}, Date: 1200, Reaction: &tg.ReactionEmoji{Emoticon: "👍"}}})
	}
	return r
}

func reactionIdentitySeed(t *testing.T, ctx context.Context, e *Engine, peer tg.PeerClass) {
	t.Helper()
	m := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "Synthetic unchanged content"}
	var u tg.UpdateClass = &tg.UpdateNewMessage{Message: m}
	if _, ok := peer.(*tg.PeerChannel); ok {
		u = &tg.UpdateNewChannelMessage{Message: m}
	}
	if err := e.handle(ctx, &tg.UpdateShort{Update: u}); err != nil {
		t.Fatal(err)
	}
}

func reactionIdentityUpdate(peer tg.PeerClass, r tg.MessageReactions) tg.UpdateClass {
	return &tg.UpdateMessageReactions{Peer: peer, MsgID: 8, Reactions: r}
}

func reactionIdentityEvents(t *testing.T, events <-chan engine.Event, want int) []engine.Event {
	t.Helper()
	var got []engine.Event
	for {
		select {
		case ev := <-events:
			if ev.Kind != engine.EventReaction || ev.Reaction == nil {
				t.Fatalf("unexpected event: %+v", ev)
			}
			got = append(got, ev)
		default:
			if len(got) != want {
				t.Fatalf("reaction events = %d, want %d: %+v", len(got), want, got)
			}
			return got
		}
	}
}

func reactionIdentityUnique(t *testing.T, events []engine.Event) {
	t.Helper()
	seen := map[string]int{}
	for i, ev := range events {
		if ev.ID == "" {
			t.Errorf("reaction %d has empty event ID", i)
		}
		if previous, ok := seen[ev.ID]; ok {
			t.Errorf("distinct committed reactions %d and %d reuse event_id %s", previous, i, ev.ID)
		}
		seen[ev.ID] = i
		t.Logf("reaction[%d] id=%s counts=%+v added=%+v removed=%+v", i, ev.ID, ev.Reaction.Counts, ev.Reaction.Added, ev.Reaction.Removed)
	}
}

func TestReactionIdentitySameCountsDifferentReactorsAndChosen(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(fmt.Sprintf("channel_%v", channel), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			e, db := reactionIdentityEngine(t, ctx, filepath.Join(t.TempDir(), "reactions.db"), time.Unix(3000, 0))
			var peer tg.PeerClass = &tg.PeerUser{UserID: 10}
			if channel {
				peer = &tg.PeerChannel{ChannelID: 20}
			}
			reactionIdentitySeed(t, ctx, e, peer)
			events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
			var got []engine.Event
			for _, r := range []tg.MessageReactions{
				reactionIdentityState(1, 11, false),
				reactionIdentityState(1, 12, false),
				reactionIdentityState(1, 12, true),
				reactionIdentityState(1, 12, false),
				reactionIdentityState(1, 11, false),
			} {
				u := &tg.UpdateShort{Update: reactionIdentityUpdate(peer, r)}
				if err := e.handle(ctx, u); err != nil {
					t.Fatal(err)
				}
				got = append(got, reactionIdentityEvents(t, events, 1)...)
				if err := e.handle(ctx, u); err != nil {
					t.Fatal(err)
				}
				reactionIdentityEvents(t, events, 0)
			}
			reactionIdentityUnique(t, got)
			if len(got[1].Reaction.Added) != 1 || got[1].Reaction.Added[0].By.ID != 12 || len(got[1].Reaction.Removed) != 1 || got[1].Reaction.Removed[0].By.ID != 11 {
				t.Errorf("reactor replacement lost committed delta: %+v", got[1].Reaction)
			}
			if !got[2].Reaction.Counts[0].Chosen || got[3].Reaction.Counts[0].Chosen {
				t.Error("chosen-state transitions lost")
			}
			m, ok, err := db.Message(ctx, engine.MessageKey{Account: "primary", Peer: peerRef(peer), ID: 8})
			if err != nil || !ok || m.Text != "Synthetic unchanged content" || m.EditDate != nil {
				t.Fatalf("reaction transitions changed message content: %+v, %v", m, err)
			}
		})
	}
}

func TestReactionIdentityAnonymousABAReopenAndDeterminism(t *testing.T) {
	// Repeat identical committed histories in independent DBs with different
	// wall clocks; durable fact IDs must match without a random/in-memory seed.
	var firstIDs []string
	for run := 0; run < 2; run++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		t.Cleanup(cancel)
		path := filepath.Join(t.TempDir(), "reactions.db")
		e, db := reactionIdentityEngine(t, ctx, path, time.Unix(3000+int64(run)*1000, 0))
		peer := &tg.PeerUser{UserID: 10}
		reactionIdentitySeed(t, ctx, e, peer)
		events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
		var got []engine.Event
		for i, count := range []int{1, 2, 1, 0, 1, 2, 1} {
			u := &tg.UpdateShort{Update: reactionIdentityUpdate(peer, reactionIdentityState(count, 0, false))}
			if err := e.handle(ctx, u); err != nil {
				t.Fatal(err)
			}
			got = append(got, reactionIdentityEvents(t, events, 1)...)
			if err := e.handle(ctx, u); err != nil {
				t.Fatal(err)
			}
			reactionIdentityEvents(t, events, 0)
			if i == 2 || i == 3 {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				e, db = reactionIdentityEngine(t, ctx, path, time.Unix(9000+int64(run)*1000, 0))
				events = e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
				if err := e.handle(ctx, u); err != nil {
					t.Fatal(err)
				}
				reactionIdentityEvents(t, events, 0)
			}
		}
		reactionIdentityUnique(t, got)
		ids := make([]string, len(got))
		for i, ev := range got {
			ids[i] = ev.ID
		}
		if run == 0 {
			firstIDs = ids
		} else if !reflect.DeepEqual(ids, firstIDs) {
			t.Errorf("same committed history changed IDs with independent DB/clock: first=%v second=%v", firstIDs, ids)
		}
	}
}

func TestReactionIdentityRepeatedTransitionsWithinBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e, _ := reactionIdentityEngine(t, ctx, filepath.Join(t.TempDir(), "reactions.db"), time.Unix(3000, 0))
	peer := &tg.PeerUser{UserID: 10}
	reactionIdentitySeed(t, ctx, e, peer)
	events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
	var updates []tg.UpdateClass
	for _, count := range []int{1, 2, 1, 2, 1} {
		updates = append(updates, reactionIdentityUpdate(peer, reactionIdentityState(count, 0, false)))
	}
	if err := e.handle(ctx, &tg.Updates{Updates: updates}); err != nil {
		t.Fatal(err)
	}
	reactionIdentityUnique(t, reactionIdentityEvents(t, events, len(updates)))
}
