package gotdengine

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestRealGroupBasicFullChatPersistsParticipants(t *testing.T) {
	e, st := offlineEngine(t)
	ref := engine.PeerRef{Type: engine.PeerChat, ID: 20}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r, ok := in.(*tg.MessagesGetFullChatRequest)
		if !ok || r.ChatID != 20 {
			t.Fatalf("unexpected RPC %T", in)
		}
		calls++
		*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: 20, About: "Synthetic group", Participants: &tg.ChatParticipants{ChatID: 20, Participants: []tg.ChatParticipantClass{&tg.ChatParticipantCreator{UserID: 99}, &tg.ChatParticipantAdmin{UserID: 10, Date: 100}}}}, Chats: []tg.ChatClass{&tg.Chat{ID: 20, Title: "Basic", ParticipantsCount: 2, Date: 50, Creator: true}}, Users: []tg.UserClass{&tg.User{ID: 99, FirstName: "Self"}, &tg.User{ID: 10, FirstName: "Member", Username: "member"}}}
		return nil
	}))
	g, err := e.RefreshGroup(context.Background(), engine.GroupRequest{Group: ref, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || g.Partial || g.Size != 2 || len(g.Participants) != 2 || g.Owner == nil || g.Owner.ID != 99 || g.Participants[1].Role != "admin" || g.Participants[1].Name != "Member" || !g.AmAdmin {
		t.Fatalf("incorrect group snapshot: %+v", g)
	}
	_, ok, err := st.LookupPeer(context.Background(), "primary", ref)
	if err != nil || !ok {
		t.Fatalf("snapshot peer not persisted: %v", err)
	}
}

func TestRealGroupChannelPaginatesAndRestrictionsRemainPartial(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pages", true: "restricted"}[restricted], func(t *testing.T) {
			e, st := offlineEngine(t)
			ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
			_, err := st.Apply(context.Background(), engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: engine.KindSupergroup}, AccessHash: 55}}})
			if err != nil {
				t.Fatal(err)
			}
			var offsets []int
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch r := in.(type) {
				case *tg.ChannelsGetFullChannelRequest:
					*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 30, About: "Channel", CanViewParticipants: true, ParticipantsCount: 3}, Chats: []tg.ChatClass{&tg.Channel{ID: 30, Title: "Super", Megagroup: true, AccessHash: 55}}}
				case *tg.ChannelsGetParticipantsRequest:
					offsets = append(offsets, r.Offset)
					if restricted {
						return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
					}
					ids := []int64{10, 11}
					if r.Offset == 2 {
						ids = []int64{12}
					}
					page := &tg.ChannelsChannelParticipants{Count: 3}
					for _, id := range ids {
						page.Participants = append(page.Participants, &tg.ChannelParticipant{UserID: id, Date: 100})
						page.Users = append(page.Users, &tg.User{ID: id, FirstName: "Member"})
					}
					out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = page
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				return nil
			}))
			g, err := e.RefreshGroup(context.Background(), engine.GroupRequest{Group: ref, Limit: 3})
			if err != nil {
				t.Fatal(err)
			}
			if restricted {
				if !g.Partial || g.Size != 3 || len(g.Participants) != 0 {
					t.Fatalf("restriction hidden: %+v", g)
				}
			} else if g.Partial || len(g.Participants) != 3 || len(offsets) != 2 || offsets[1] != 2 {
				t.Fatalf("pagination failed: %+v offsets=%v", g, offsets)
			}
		})
	}
}

func TestRealGroupRejectsInvalidCanceledAndFloodWait(t *testing.T) {
	e, _ := offlineEngine(t)
	if _, err := e.RefreshGroup(context.Background(), engine.GroupRequest{Group: engine.PeerRef{Type: engine.PeerUser, ID: 10}}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("user accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: engine.PeerRef{Type: engine.PeerChat, ID: 20}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(420, "FLOOD_WAIT_5") }))
	if _, err := e.RefreshGroup(context.Background(), engine.GroupRequest{Group: engine.PeerRef{Type: engine.PeerChat, ID: 20}}); err == nil || errors.Is(err, engine.ErrNotSupported) {
		t.Fatalf("RPC failure lost: %v", err)
	}
}
