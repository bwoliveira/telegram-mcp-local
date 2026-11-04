package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestBatchKeepsNewestMessageVersion(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "persister"}[withStore], func(t *testing.T) {
			cfg := engine.Config{Account: "test"}
			if withStore {
				cfg.Store = &persisterSpy{}
			}
			f := newFake(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := f.Subscribe(ctx, engine.SubscribeOptions{Kinds: []engine.EventKind{engine.EventEdit}, Buffer: 4})
			latestAt := fake.Epoch().Add(time.Minute)
			oldAt := fake.Epoch()
			latest := engine.Message{Key: key(fake.PrivatePeer, 1), Date: fake.Epoch(), EditDate: &latestAt, Text: "latest committed"}
			old := latest
			old.EditDate = &oldAt
			old.Text = "older rejected"
			if e := f.Publish(ctx, engine.Batch{Edits: []engine.Message{latest, old}}); e != nil {
				t.Fatal(e)
			}
			event := <-events
			if event.Message.Text != "latest committed" {
				t.Fatalf("wrong event version: %s", event.Message.Text)
			}
			select {
			case <-events:
				t.Fatal("duplicate event for one edited row")
			default:
			}
			ms, e := f.FetchWindow(ctx, engine.WindowRequest{Key: latest.Key})
			if e != nil || ms[0].Text != "latest committed" {
				t.Fatalf("message regressed: %+v %v", ms, e)
			}
		})
	}
}
func TestReopenedPersisterDoesNotReuseOutgoingIDs(t *testing.T) {
	store := &persisterSpy{}
	ctx := context.Background()
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite, Store: store})
	first := confirm(t, func(o engine.WriteOptions) (engine.WriteResult, error) {
		return f.SendText(ctx, engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "first persisted", WriteOptions: o})
	})
	f = newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite, Store: store})
	second := confirm(t, func(o engine.WriteOptions) (engine.WriteResult, error) {
		return f.SendText(ctx, engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "second persisted", WriteOptions: o})
	})
	if first.Messages[0] == second.Messages[0] || store.messages[second.Messages[0]].Text != "second persisted" {
		t.Fatalf("send ID reused: first %v second %v", first.Messages, second.Messages)
	}
}
func TestUnknownDeletionFactsAreUniqueAndReplayIsQuiet(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := f.Subscribe(ctx, engine.SubscribeOptions{Kinds: []engine.EventKind{engine.EventDelete}, Buffer: 10})
	ids := []int{50, 51}
	if e := f.Publish(ctx, engine.Batch{Deletes: []engine.Deletion{{IDs: ids}}}); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for {
		select {
		case e := <-events:
			if seen[e.ID] {
				t.Fatal("duplicate event identity")
			}
			seen[e.ID] = true
		default:
			goto drained
		}
	}
drained:
	if e := f.Publish(ctx, engine.Batch{Deletes: []engine.Deletion{{IDs: ids}}}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-events:
		t.Fatal("unknown deletion replay emitted again")
	default:
	}
	if e := f.Publish(ctx, engine.Batch{Deletes: []engine.Deletion{{IDs: []int{50, 52}}}}); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-events:
		if seen[e.ID] {
			t.Fatal("different unknown deletion reused identity")
		}
	case <-time.After(time.Second):
		t.Fatal("missing new deletion event")
	}
}
func TestBoundedBackfillDoesNotClaimBeginningOfHistory(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx := context.Background()
	until := fake.Epoch().Add(time.Hour)
	r, e := f.Backfill(ctx, engine.BackfillRequest{Peer: fake.PrivatePeer, Until: &until})
	if e != nil {
		t.Fatal(e)
	}
	if r.Complete {
		t.Fatal("bounded history incorrectly marked complete")
	}
	r, e = f.Backfill(ctx, engine.BackfillRequest{Peer: fake.PrivatePeer})
	if e != nil || r.Fetched == 0 || !r.Complete {
		t.Fatalf("later full backfill stuck: %+v %v", r, e)
	}
}
func (s *persisterSpy) ResolveMessageIDs(_ context.Context, _ engine.AccountID, ids []int) ([]engine.MessageKey, error) {
	var keys []engine.MessageKey
	for _, id := range ids {
		for k := range s.messages {
			if k.ID == id && k.Peer.Type != engine.PeerChannel {
				keys = append(keys, k)
			}
		}
	}
	return keys, nil
}
func TestForeignAccountBatchIsRejectedAtomically(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	e := f.Publish(context.Background(), engine.Batch{Account: "foreign", Messages: []engine.Message{{Key: key(fake.PrivatePeer, 99), Date: fake.Epoch()}}})
	if !errors.Is(e, engine.ErrAccountMismatch) {
		t.Fatal(e)
	}
}
