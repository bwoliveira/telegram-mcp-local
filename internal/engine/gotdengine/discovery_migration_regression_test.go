package gotdengine

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestDiscoveryChannelFullMigratedFrom(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Channel{ID: 81, Title: "After", Megagroup: true, AccessHash: 99}})}); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.ChannelsGetFullChannelRequest:
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 81, MigratedFromChatID: 71}, Chats: []tg.ChatClass{&tg.Channel{ID: 81, Title: "After", Megagroup: true, AccessHash: 99}}}
		case *tg.ChannelsGetParticipantsRequest:
			out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))
	if _, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref}); err != nil {
		t.Fatal(err)
	}
	p, ok, err := db.LookupPeer(ctx, "primary", engine.PeerRef{Type: engine.PeerChat, ID: 71})
	if err != nil || !ok || p.MigratedTo == nil || *p.MigratedTo != ref {
		t.Fatalf("full metadata migration not persisted: %+v %v", p, err)
	}
}

func TestDiscoveryMigrationKeepsHistoryHidesOldChat(t *testing.T) {
	for _, source := range []string{"chat_service", "channel_service", "chat_metadata"} {
		t.Run(source, func(t *testing.T) {
			e, db := discoveryEngine(t)
			ctx := context.Background()
			old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
			next := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
			if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: old, Kind: engine.KindGroup, Title: "Before", Access: engine.AccessOK}}}, Dialogs: []engine.Dialog{{Peer: old, TopMessageID: 7}}, Messages: []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: old, ID: 7}, Text: "retained history", Date: time.Unix(1000, 0)}}}); err != nil {
				t.Fatal(err)
			}
			raw := &tg.Updates{Chats: []tg.ChatClass{&tg.Channel{ID: 81, Title: "After", Megagroup: true, AccessHash: 99}}}
			switch source {
			case "chat_service":
				raw.Updates = []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.MessageService{ID: 8, PeerID: &tg.PeerChat{ChatID: 71}, Action: &tg.MessageActionChatMigrateTo{ChannelID: 81}}}}
			case "channel_service":
				raw.Updates = []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: &tg.MessageService{ID: 1, PeerID: &tg.PeerChannel{ChannelID: 81}, Action: &tg.MessageActionChannelMigrateFrom{ChatID: 71, Title: "Before"}}}}
			case "chat_metadata":
				raw.Chats = append(raw.Chats, &tg.Chat{ID: 71, Title: "Before", MigratedTo: &tg.InputChannel{ChannelID: 81, AccessHash: 99}})
			}
			for n := 0; n < 2; n++ {
				if err := e.handle(ctx, raw); err != nil {
					t.Fatal(err)
				}
			}
			p, ok, err := db.LookupPeer(ctx, "primary", old)
			if err != nil || !ok || p.MigratedTo == nil || *p.MigratedTo != next {
				t.Fatalf("migration not persisted: %+v %v", p, err)
			}
			chats, err := db.ListChats(ctx, "primary", store.ChatOptions{})
			if err != nil || len(chats) != 1 || chats[0].Peer.Ref != next {
				t.Fatalf("old group still listed: %+v %v", chats, err)
			}
			history, err := db.ListMessages(ctx, "primary", next, store.MessageOptions{})
			if err != nil || len(history.Messages) != 1 || history.Messages[0].Key.Peer != old {
				t.Fatalf("migration discarded history: %+v %v", history, err)
			}
		})
	}
}
