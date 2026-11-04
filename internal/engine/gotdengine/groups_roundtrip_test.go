package gotdengine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestGroupMetadataLiveSQLiteCacheRoundTrip(t *testing.T) {
	for _, kind := range []engine.PeerKind{engine.KindGroup, engine.KindSupergroup, engine.KindChannel} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(2000, 0).UTC()
			clock := func() time.Time { return now }
			path := filepath.Join(t.TempDir(), "groups.db")
			db, err := store.Open(ctx, path, "primary", store.WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Store: db, Session: &memorySession{}, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
			if err := db.BindSelf(ctx, "primary", e.account); err != nil {
				t.Fatal(err)
			}
			ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
			if kind == engine.KindGroup {
				ref.Type = engine.PeerChat
			}
			if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: kind, Title: "Synthetic group"}, AccessHash: 55}}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				users := []tg.UserClass{&tg.User{ID: 99, FirstName: "Owner"}, &tg.User{ID: 10, FirstName: "Member", Username: "member"}}
				switch r := in.(type) {
				case *tg.MessagesGetFullChatRequest:
					if ref.Type != engine.PeerChat || r.ChatID != ref.ID {
						t.Fatalf("wrong basic group request: %+v", r)
					}
					*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
						FullChat: &tg.ChatFull{ID: ref.ID, About: "Synthetic description", Participants: &tg.ChatParticipants{ChatID: ref.ID, Participants: []tg.ChatParticipantClass{&tg.ChatParticipant{UserID: 10, Date: 100}, &tg.ChatParticipantCreator{UserID: 99}}}},
						Chats:    []tg.ChatClass{&tg.Chat{ID: ref.ID, Title: "Synthetic group", ParticipantsCount: 2, Date: 50, Creator: true}}, Users: users,
					}
				case *tg.ChannelsGetFullChannelRequest:
					if ref.Type != engine.PeerChannel || !reflect.DeepEqual(r.Channel, &tg.InputChannel{ChannelID: ref.ID, AccessHash: 55}) {
						t.Fatalf("wrong channel request: %+v", r)
					}
					*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
						FullChat: &tg.ChannelFull{ID: ref.ID, About: "Synthetic description", ParticipantsCount: 2, LinkedChatID: 40},
						Chats:    []tg.ChatClass{&tg.Channel{ID: ref.ID, Title: "Synthetic group", AccessHash: 55, Megagroup: kind == engine.KindSupergroup, Broadcast: kind == engine.KindChannel, Date: 50, AdminRights: tg.ChatAdminRights{ChangeInfo: true}}}, Users: users,
					}
				case *tg.ChannelsGetParticipantsRequest:
					if ref.Type != engine.PeerChannel || r.Offset != 0 {
						t.Fatalf("wrong participant request: %+v", r)
					}
					out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{Count: 2, Participants: []tg.ChannelParticipantClass{&tg.ChannelParticipant{UserID: 10, Date: 100}, &tg.ChannelParticipantCreator{UserID: 99}}, Users: users}
				default:
					t.Fatalf("forbidden RPC during group read: %T", in)
				}
				return nil
			}))
			server := mcp.New(mcp.Config{Engine: e, ReadStore: db, Clock: clock})
			id := fmt.Sprintf("%s:%d", ref.Type, ref.ID)
			live := metadataCall(t, server, "get_group", map[string]any{"group_id": id, "live": true})
			if live["owner"] != "user:99" || live["created_at"] != time.Unix(50, 0).UTC().Format(time.RFC3339) || live["am_admin"] != true || live["snapshot_available"] != true || live["partial"] != false {
				t.Fatalf("live metadata incomplete: %+v", live)
			}
			if kind != engine.KindGroup && live["linked_chat_id"] != "channel:40" {
				t.Fatalf("live linked chat missing: %+v", live)
			}
			wantCalls := 2
			if kind == engine.KindGroup {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("RPC calls=%d, want %d", calls, wantCalls)
			}
			checkCache := func(server *mcp.Server) {
				t.Helper()
				cached := metadataCall(t, server, "get_group", map[string]any{"group_id": id, "live": false})
				for _, field := range []string{"owner", "created_at", "linked_chat_id", "am_admin", "about", "size", "partial", "snapshot_at", "snapshot_available", "participants"} {
					if !reflect.DeepEqual(cached[field], live[field]) {
						t.Errorf("cached %s=%v, live=%v", field, cached[field], live[field])
					}
				}
				filtered := metadataCall(t, server, "get_group", map[string]any{"group_id": id, "live": false, "search": "Member"})
				if filtered["owner"] != "user:99" || len(filtered["participants"].([]any)) != 1 || filtered["partial"] != true {
					t.Errorf("cached filtering lost group metadata: %+v", filtered)
				}
				if calls != wantCalls {
					t.Fatal("cached group read called Telegram")
				}
			}
			checkCache(server)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(ctx, path, "primary", store.WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}
			e.cfg.Store = db
			e.account.Authenticated = false
			checkCache(mcp.New(mcp.Config{Engine: e, ReadStore: db, Clock: clock}))
		})
	}
}
