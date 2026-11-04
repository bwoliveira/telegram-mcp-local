package gotdengine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestDiscoveryLegacyMigrationMetadataFailureRetainsPage(t *testing.T) {
	for _, failure := range []string{"flood", "mismatched_full", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			e, db := discoveryEngine(t)
			ctx := context.Background()
			old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
			if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID, Title: "Before"}}), Dialogs: []engine.Dialog{{Peer: old}}}); err != nil {
				t.Fatal(err)
			}
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetDialogsRequest:
					out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Chats: []tg.ChatClass{&tg.Channel{ID: 81, Megagroup: true, AccessHash: 99}}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}, UnreadCount: 2}}}
				case *tg.ChannelsGetFullChannelRequest:
					switch failure {
					case "flood":
						return tgerr.New(420, "FLOOD_WAIT_3600")
					case "cancel":
						return context.Canceled
					default:
						*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 999, MigratedFromChatID: old.ID}}
					}
				default:
					t.Fatalf("unexpected RPC: %T", in)
				}
				return nil
			}))
			result, err := e.RefreshDialogs(ctx, engine.RefreshRequest{})
			if err == nil {
				t.Fatal("metadata failure was swallowed")
			}
			if failure == "flood" {
				var flood *engine.FloodWaitError
				if !errors.As(err, &flood) {
					t.Fatalf("FloodWait cannot reach the discovery backoff: %v", err)
				}
			}
			if result.Dialogs != 1 {
				t.Errorf("committed dialog page not reflected in result: %+v", result)
			}
			chats, readErr := db.ListChats(ctx, "primary", store.ChatOptions{})
			if readErr != nil || len(chats) != 2 {
				t.Fatalf("full-metadata failure discarded dialog page: %+v %v", chats, readErr)
			}
			p, _, readErr := db.LookupPeer(ctx, "primary", old)
			if readErr != nil || p.MigratedTo != nil {
				t.Fatalf("failed metadata created a migration: %+v %v", p, readErr)
			}
		})
	}
}

func TestDiscoveryLegacyMigrationSkipsInaccessibleChannel(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx := context.Background()
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID}}), Dialogs: []engine.Dialog{{Peer: old}}}); err != nil {
		t.Fatal(err)
	}
	fullCalls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.MessagesGetDialogsRequest:
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Chats: []tg.ChatClass{&tg.Channel{ID: 80, Megagroup: true, AccessHash: 98}, &tg.Channel{ID: 81, Megagroup: true, AccessHash: 99}}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 80}}, &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}}
		case *tg.ChannelsGetFullChannelRequest:
			fullCalls++
			if r.Channel.(*tg.InputChannel).ChannelID == 80 {
				return tgerr.New(400, "CHANNEL_PRIVATE")
			}
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 81, MigratedFromChatID: old.ID}}
		default:
			t.Fatalf("unexpected RPC: %T", in)
		}
		return nil
	}))
	if _, err := e.RefreshDialogs(ctx, engine.RefreshRequest{}); err != nil {
		t.Fatal(err)
	}
	p, _, err := db.LookupPeer(ctx, "primary", old)
	if err != nil || p.MigratedTo == nil || p.MigratedTo.ID != 81 || fullCalls != 2 {
		t.Fatalf("inaccessible channel starved remaining migration discovery: %+v calls=%d %v", p, fullCalls, err)
	}
}

func TestDiscoveryLegacyMigrationNegativeFullMetadataIsNotRepeatedByWorker(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx := context.Background()
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID, Title: "Ordinary basic group"}}), Dialogs: []engine.Dialog{{Peer: old}}}); err != nil {
		t.Fatal(err)
	}
	var fullCalls atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogsRequest:
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Chats: []tg.ChatClass{&tg.Channel{ID: 81, Megagroup: true, AccessHash: 99}}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}}
		case *tg.ChannelsGetFullChannelRequest:
			fullCalls.Add(1)
			// A directly created supergroup has no migration predecessor.
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 81}}
		default:
			t.Fatalf("unexpected RPC: %T", in)
		}
		return nil
	}))
	e.dialogRefreshEvery = time.Hour
	e.dialogRefresh = make(chan struct{})
	workerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := e.startDialogRefresh(workerCtx)
	defer stop()
	// Each unbuffered handoff proves the previous refresh completed, including
	// its full-channel metadata read. No timing sleeps or goroutine races.
	for i := 0; i < 3; i++ {
		select {
		case e.dialogRefresh <- struct{}{}:
		case <-workerCtx.Done():
			t.Fatal("worker did not finish refresh")
		}
	}
	stop()
	if calls := fullCalls.Load(); calls != 1 {
		t.Fatalf("ordinary groups caused repeated full-channel probes: %d", calls)
	}
	p, _, err := db.LookupPeer(ctx, "primary", old)
	if err != nil || p.MigratedTo != nil {
		t.Fatalf("ordinary group was incorrectly hidden: %+v %v", p, err)
	}
}

func TestDiscoveryLegacyMigrationDoesNotProbeUnrelatedPeers(t *testing.T) {
	for _, scenario := range []string{"no_legacy", "broadcast", "left", "min", "incidental"} {
		t.Run(scenario, func(t *testing.T) {
			e, db := discoveryEngine(t)
			ctx := context.Background()
			if scenario != "no_legacy" {
				old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
				if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID}}), Dialogs: []engine.Dialog{{Peer: old}}}); err != nil {
					t.Fatal(err)
				}
			}
			channel := &tg.Channel{ID: 81, Megagroup: true, AccessHash: 99}
			page := &tg.MessagesDialogs{Chats: []tg.ChatClass{channel}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}}
			switch scenario {
			case "broadcast":
				channel.Megagroup, channel.Broadcast = false, true
			case "left":
				channel.Left = true
			case "min":
				channel.Min = true
			case "incidental":
				page.Dialogs = nil
			}
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.MessagesGetDialogsRequest); !ok {
					t.Fatalf("unnecessary or unsafe full-channel RPC: %T", in)
				}
				out.(*tg.MessagesDialogsBox).Dialogs = page
				return nil
			}))
			if _, err := e.RefreshDialogs(ctx, engine.RefreshRequest{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
