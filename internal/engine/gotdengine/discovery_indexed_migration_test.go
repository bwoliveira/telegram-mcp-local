package gotdengine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestDiscoveryIndexedMigrationOutsideRecentPage(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx := context.Background()
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	next := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
	channel := &tg.Channel{ID: next.ID, Title: "After", Megagroup: true, AccessHash: 99}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID, Title: "Before"}, channel}), Dialogs: []engine.Dialog{{Peer: old}, {Peer: next}}}); err != nil {
		t.Fatal(err)
	}
	var fullCalls atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.MessagesGetDialogsRequest:
			if r.Limit != 100 || r.OffsetID != 0 {
				t.Errorf("worker must retain bounded recent-page refresh: %+v", r)
			}
			// All 100 recent dialogs exclude the already-indexed successor.
			page := &tg.MessagesDialogsSlice{Count: 102}
			for i := int64(1); i <= 100; i++ {
				page.Dialogs = append(page.Dialogs, &tg.Dialog{Peer: &tg.PeerUser{UserID: i}})
			}
			out.(*tg.MessagesDialogsBox).Dialogs = page
		case *tg.ChannelsGetFullChannelRequest:
			if r.Channel.(*tg.InputChannel).ChannelID != next.ID {
				t.Errorf("wrong candidate: %+v", r)
			}
			fullCalls.Add(1)
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: next.ID, MigratedFromChatID: old.ID}, Chats: []tg.ChatClass{channel}}
		default:
			t.Errorf("unexpected RPC: %T", in)
		}
		return nil
	}))
	runMigrationWorkerRefreshes(t, e, 2)
	p, _, err := db.LookupPeer(ctx, "primary", old)
	if err != nil || p.MigratedTo == nil || *p.MigratedTo != next || fullCalls.Load() != 1 {
		t.Fatalf("indexed successor outside recent page was not discovered: peer=%+v calls=%d err=%v", p, fullCalls.Load(), err)
	}
	chats, err := db.ListChats(ctx, "primary", store.ChatOptions{PeerOptions: store.PeerOptions{Query: "Before"}})
	if err != nil || len(chats) != 0 {
		t.Fatalf("predecessor remains visible: %+v %v", chats, err)
	}
}

func runMigrationWorkerRefreshes(t *testing.T, e *Engine, count int) {
	t.Helper()
	e.dialogRefreshEvery = time.Hour
	e.dialogRefresh = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stop := e.startDialogRefresh(ctx)
	defer stop()
	// The extra unbuffered handoff waits for the final requested refresh.
	for i := 0; i <= count; i++ {
		select {
		case e.dialogRefresh <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("worker did not complete refresh")
		}
	}
	stop()
}

func TestDiscoveryPeersOnlyLegacyMigration(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx := context.Background()
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	next := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
	channel := &tg.Channel{ID: next.ID, Title: "After", Megagroup: true, AccessHash: 99}
	// Imported/older indexes can retain peers and messages without a dialog row.
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID, Title: "Before"}, channel}), Dialogs: []engine.Dialog{{Peer: next}}, Messages: []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: old, ID: 7}, Text: "retained legacy history", Date: time.Unix(1000, 0)}}}); err != nil {
		t.Fatal(err)
	}
	dialogs, err := db.ReadDialogs(ctx, "primary")
	if err != nil || len(dialogs) != 1 || dialogs[0].Peer != next {
		t.Fatalf("invalid peers-only fixture: %+v %v", dialogs, err)
	}
	chats, err := db.ListChats(ctx, "primary", store.ChatOptions{})
	if err != nil || len(chats) != 2 {
		t.Fatalf("peers-only predecessor not initially visible: %+v %v", chats, err)
	}
	var fullCalls atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogsRequest:
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Chats: []tg.ChatClass{channel}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: next.ID}}}}
		case *tg.ChannelsGetFullChannelRequest:
			fullCalls.Add(1)
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: next.ID, MigratedFromChatID: old.ID}, Chats: []tg.ChatClass{channel}}
		default:
			t.Errorf("unexpected RPC: %T", in)
		}
		return nil
	}))
	runMigrationWorkerRefreshes(t, e, 2)
	if fullCalls.Load() != 1 {
		t.Errorf("peers-only predecessor not discovered: full calls=%d", fullCalls.Load())
	}
	assertLegacyMigration(t, db, old, next)
}

func TestDiscoveryIndexedMigrationProbeBudgetRotates(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx := context.WithValue(context.Background(), migrationDiscoveryCacheKey{}, &migrationDiscoveryState{checked: make(map[engine.PeerRef]bool)})
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	seed := engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID}})}
	for id := int64(1); id <= 205; id++ {
		seed.Peers = append(seed.Peers, peerUpserts(nil, []tg.ChatClass{&tg.Channel{ID: id, Megagroup: true, AccessHash: id + 1000}})...)
		seed.Dialogs = append(seed.Dialogs, engine.Dialog{Peer: engine.PeerRef{Type: engine.PeerChannel, ID: id}})
	}
	if _, err := db.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	calls := 0
	seen := map[int64]int{}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.MessagesGetDialogsRequest:
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{}
		case *tg.ChannelsGetFullChannelRequest:
			id := r.Channel.(*tg.InputChannel).ChannelID
			calls++
			seen[id]++
			if id <= 100 {
				return tgerr.New(400, "CHANNEL_PRIVATE")
			}
			full := &tg.ChannelFull{ID: id}
			if id == 205 {
				full.MigratedFromChatID = old.ID
			}
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: full}
		default:
			t.Errorf("unexpected RPC: %T", in)
		}
		return nil
	}))
	for tick, want := range []int{100, 100, 5, 0} {
		calls = 0
		if _, err := e.RefreshDialogs(ctx, engine.RefreshRequest{Limit: 100}); err != nil {
			t.Fatal(err)
		}
		if calls != want {
			t.Fatalf("tick %d: probes=%d want=%d (bounded rotating discovery)", tick, calls, want)
		}
	}
	for id := int64(1); id <= 205; id++ {
		if seen[id] != 1 {
			t.Errorf("candidate %d probed %d times", id, seen[id])
		}
	}
	p, _, err := db.LookupPeer(ctx, "primary", old)
	if err != nil || p.MigratedTo == nil || p.MigratedTo.ID != 205 {
		t.Fatalf("late candidate starved: %+v %v", p, err)
	}
}
