package gotdengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestDiscoveryLegacyMigrationRefresh(t *testing.T) {
	for _, source := range []string{"channel_full", "chat_metadata", "chat_service", "channel_service", "group_full"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			e, _ := offlineEngine(t)
			path := filepath.Join(t.TempDir(), "legacy.db")
			db, err := store.Open(ctx, path, "primary")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.BindSelf(ctx, "primary", e.account); err != nil {
				t.Fatal(err)
			}
			old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
			next := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
			channel := &tg.Channel{ID: next.ID, Title: "After", Megagroup: true, AccessHash: 99}
			seed := engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: old.ID, Title: "Before"}, channel}), Dialogs: []engine.Dialog{{Peer: old, TopMessageID: 7, UnreadCount: 3, Pinned: true}, {Peer: next}}, Messages: []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: old, ID: 7}, Text: "retained legacy history", Date: time.Unix(1000, 0)}}}
			if _, err := db.Apply(ctx, seed); err != nil {
				t.Fatal(err)
			}
			// Reopen an existing index without replaying any migration update.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(ctx, path, "primary")
			if err != nil {
				t.Fatal(err)
			}
			e.cfg.Store = db
			fullCalls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch r := in.(type) {
				case *tg.MessagesGetDialogsRequest:
					page := &tg.MessagesDialogs{Chats: []tg.ChatClass{channel}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: next.ID}, TopMessage: 9, UnreadCount: 2}}}
					switch source {
					case "chat_metadata":
						page.Chats = append(page.Chats, &tg.Chat{ID: old.ID, Title: "Before", MigratedTo: &tg.InputChannel{ChannelID: next.ID, AccessHash: 99}})
					case "chat_service":
						page.Messages = []tg.MessageClass{&tg.MessageService{ID: 8, PeerID: &tg.PeerChat{ChatID: old.ID}, Action: &tg.MessageActionChatMigrateTo{ChannelID: next.ID}}}
					case "channel_service":
						page.Messages = []tg.MessageClass{&tg.MessageService{ID: 1, PeerID: &tg.PeerChannel{ChannelID: next.ID}, Action: &tg.MessageActionChannelMigrateFrom{ChatID: old.ID}}}
					}
					out.(*tg.MessagesDialogsBox).Dialogs = page
				case *tg.ChannelsGetFullChannelRequest:
					if r.Channel.(*tg.InputChannel).ChannelID != next.ID {
						t.Fatalf("wrong channel: %+v", r)
					}
					fullCalls++
					full := &tg.ChannelFull{ID: next.ID}
					if source == "channel_full" || source == "group_full" {
						full.MigratedFromChatID = old.ID
					}
					*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: full, Chats: []tg.ChatClass{channel}}
				case *tg.ChannelsGetParticipantsRequest:
					if source != "group_full" {
						t.Fatal("dialog discovery must not enumerate participants")
					}
					out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{}
				default:
					t.Fatalf("unexpected RPC (including writes or receipts): %T", in)
				}
				return nil
			}))
			for n := 0; n < 2; n++ {
				if source == "group_full" {
					_, err = e.RefreshGroup(ctx, engine.GroupRequest{Group: next})
				} else {
					_, err = e.RefreshDialogs(ctx, engine.RefreshRequest{Limit: 100})
				}
				if err != nil {
					t.Fatal(err)
				}
				assertLegacyMigration(t, db, old, next)
			}
			if source == "channel_full" && fullCalls != 1 {
				t.Fatalf("resolved migration fetched full metadata again: %d calls", fullCalls)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(ctx, path, "primary")
			if err != nil {
				t.Fatal(err)
			}
			assertLegacyMigration(t, db, old, next)
		})
	}
}

func assertLegacyMigration(t *testing.T, db *store.Store, old, next engine.PeerRef) {
	t.Helper()
	ctx := context.Background()
	p, ok, err := db.LookupPeer(ctx, "primary", old)
	if err != nil || !ok || p.MigratedTo == nil || *p.MigratedTo != next || p.Title != "Before" {
		t.Errorf("legacy basic group migration not persisted: %+v %v", p, err)
	}
	chats, err := db.ListChats(ctx, "primary", store.ChatOptions{})
	if err != nil || len(chats) != 1 || chats[0].Peer.Ref != next {
		t.Errorf("legacy predecessor still listed: %+v %v", chats, err)
	}
	history, err := db.ListMessages(ctx, "primary", next, store.MessageOptions{})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Key.Peer != old || history.Messages[0].Text != "retained legacy history" {
		t.Errorf("linked history lost: %+v %v", history, err)
	}
	message, ok, err := db.Message(ctx, engine.MessageKey{Account: "primary", Peer: old, ID: 7})
	if err != nil || !ok || message.Text != "retained legacy history" {
		t.Errorf("original message key no longer addressable: %+v %v", message, err)
	}
	dialogs, err := db.ReadDialogs(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dialogs {
		if d.Peer == old && (d.TopMessageID != 7 || d.UnreadCount != 3 || !d.Pinned) {
			t.Errorf("migration reset original dialog metadata: %+v", d)
		}
	}
}
