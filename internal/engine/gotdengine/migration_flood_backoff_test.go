package gotdengine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestMigrationFloodDeadlineRetriesAndRecovers(t *testing.T) {
	e, db := discoveryEngine(t)
	// Reuse the worker's serial per-generation cache without sleeping or sharing
	// a mutable fake clock with another goroutine.
	state := &migrationDiscoveryState{checked: make(map[engine.PeerRef]bool)}
	ctx := context.WithValue(context.Background(), migrationDiscoveryCacheKey{}, state)
	now := time.Now()
	e.cfg.Clock = func() time.Time { return now }
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	next := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
	if _, err := db.Apply(ctx, engine.Batch{
		Account:  "primary",
		Peers:    peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID, Title: "Before"}}),
		Dialogs:  []engine.Dialog{{Peer: old, TopMessageID: 7, UnreadCount: 3, Pinned: true}},
		Messages: []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: old, ID: 7}, Text: "retained legacy history", Date: time.Unix(1000, 0)}},
	}); err != nil {
		t.Fatal(err)
	}
	pages, probes := 0, 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogsRequest:
			pages++
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Chats: []tg.ChatClass{&tg.Channel{ID: next.ID, Megagroup: true, AccessHash: 99}}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: next.ID}}}}
		case *tg.ChannelsGetFullChannelRequest:
			probes++
			switch probes {
			case 1:
				return tgerr.New(420, "FLOOD_WAIT_3600")
			case 2:
				return tgerr.New(420, "FLOOD_WAIT_7200")
			default:
				*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: next.ID, MigratedFromChatID: old.ID}}
			}
		default:
			t.Fatalf("unexpected synthetic RPC: %T", in)
		}
		return nil
	}))
	refresh := func(wantPages, wantProbes int, wantFlood bool) time.Time {
		t.Helper()
		result, err := e.RefreshDialogs(ctx, engine.RefreshRequest{Limit: 100})
		var flood *engine.FloodWaitError
		if wantFlood {
			if !errors.As(err, &flood) {
				t.Fatalf("direct refresh lost auxiliary FloodWait details: %v", err)
			}
		} else if err != nil {
			t.Fatalf("refresh during metadata backoff failed: %v", err)
		}
		if pages != wantPages || probes != wantProbes || result.Dialogs != 1 {
			t.Fatalf("pages=%d probes=%d dialogs=%d, want %d/%d/1", pages, probes, result.Dialogs, wantPages, wantProbes)
		}
		if flood != nil {
			return flood.Until
		}
		return time.Time{}
	}
	firstUntil := refresh(1, 1, true)
	refresh(2, 1, false)
	now = firstUntil.Add(-time.Nanosecond)
	refresh(3, 1, false)
	now = firstUntil
	secondUntil := refresh(4, 2, true)
	if !secondUntil.After(firstUntil) {
		t.Fatalf("fixture did not extend the flood deadline: first=%v second=%v", firstUntil, secondUntil)
	}
	now = secondUntil.Add(-time.Nanosecond)
	refresh(5, 2, false)
	now = secondUntil
	refresh(6, 3, false)
	// Success must cache the immutable origin, not restart full probes.
	refresh(7, 3, false)
	assertLegacyMigration(t, db, old, next)
}

func TestMigrationFloodDoesNotBypassDialogsFlood(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: 71}})}); err != nil {
		t.Fatal(err)
	}
	var pages, probes atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogsRequest:
			if pages.Add(1) > 1 {
				return tgerr.New(420, "FLOOD_WAIT_3600")
			}
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Chats: []tg.ChatClass{&tg.Channel{ID: 81, Megagroup: true, AccessHash: 99}}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}}
		case *tg.ChannelsGetFullChannelRequest:
			probes.Add(1)
			return tgerr.New(420, "FLOOD_WAIT_3600")
		default:
			t.Fatalf("unexpected synthetic RPC: %T", in)
		}
		return nil
	}))
	e.dialogRefreshEvery = time.Minute
	e.dialogRefresh = make(chan struct{})
	stop := e.startDialogRefresh(ctx)
	defer stop()
	for i := 0; i < 5; i++ {
		select {
		case e.dialogRefresh <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("worker stopped accepting signals during FloodWait")
		}
	}
	stop()
	if pages.Load() != 2 || probes.Load() != 1 {
		t.Fatalf("independent RPC gates not respected: dialog pages=%d full probes=%d, want 2/1", pages.Load(), probes.Load())
	}
}
