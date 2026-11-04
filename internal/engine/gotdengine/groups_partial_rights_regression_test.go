package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestRefreshGroupPartialSelfRightsInvalidateParticipantPreview(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, mode := range []string{"hidden", "admin_required"} {
			if ref.Type == engine.PeerChat && mode == "admin_required" {
				continue
			}
			t.Run(fmt.Sprintf("%s:%d/%s", ref.Type, ref.ID, mode), func(t *testing.T) {
				ctx := context.Background()
				e, st, _ := fingerprintGroupFixture(t, ref)
				rights := tg.ChatAdminRights{ChangeInfo: true}
				partial, writes := false, 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					switch in.(type) {
					case *tg.MessagesGetFullChatRequest:
						var members tg.ChatParticipantsClass = &tg.ChatParticipants{ChatID: ref.ID, Participants: []tg.ChatParticipantClass{
							&tg.ChatParticipantAdmin{UserID: 99}, &tg.ChatParticipant{UserID: 10}, &tg.ChatParticipant{UserID: 11},
						}}
						if partial {
							members = &tg.ChatParticipantsForbidden{ChatID: ref.ID}
						}
						*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: ref.ID, About: "Complete metadata", Participants: members}, Chats: []tg.ChatClass{&tg.Chat{ID: ref.ID, Title: "Group", ParticipantsCount: 3, AdminRights: rights}}}
					case *tg.ChannelsGetFullChannelRequest:
						*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: ref.ID, About: "Complete metadata", ParticipantsCount: 3, ParticipantsHidden: partial && mode == "hidden"}, Chats: []tg.ChatClass{&tg.Channel{ID: ref.ID, Title: "Group", AccessHash: fingerprintGroupHash(ref), Megagroup: ref.ID == 30, Broadcast: ref.ID == 40, AdminRights: rights}}}
					case *tg.ChannelsGetParticipantsRequest:
						if partial && mode == "admin_required" {
							return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
						}
						members := []tg.ChannelParticipantClass{&tg.ChannelParticipantSelf{UserID: 99}, &tg.ChannelParticipant{UserID: 10}, &tg.ChannelParticipant{UserID: 11}}
						if partial {
							members = nil // Full metadata still explicitly reports self rights.
						}
						out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{Count: 3, Participants: members}
					default:
						writes++
						groupsTestReply(t, out)
					}
					return nil
				}))
				if _, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref}); err != nil {
					t.Fatal(err)
				}
				before, err := st.ReadGroup(ctx, "primary", ref)
				if err != nil || before.Partial || len(before.Participants) != 3 || !before.AmAdmin {
					t.Fatalf("complete cache: %+v err=%v", before, err)
				}
				req := engine.ParticipantsRequest{Group: ref, Action: engine.ParticipantsAdd, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}}, WriteOptions: engine.WriteOptions{DryRun: true}}
				preview, err := e.ManageParticipants(ctx, req)
				if err != nil || preview.Preview == nil || writes != 0 {
					t.Fatalf("preview=%+v err=%v writes=%d", preview, err, writes)
				}
				partial, rights.InviteUsers = true, true // Remain admin; only an individual right changes.
				live, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref})
				if err != nil || !live.Partial || !live.AmAdmin {
					t.Fatalf("partial refresh=%+v err=%v", live, err)
				}
				req.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
				got, err := e.ManageParticipants(ctx, req)
				if !errors.Is(err, engine.ErrPreviewMismatch) || got.Done || got.Affected != 0 || writes != 0 {
					t.Fatalf("partial refresh accepted stale participant approval: result=%+v err=%v writes=%d", got, err, writes)
				}
				after, err := st.ReadGroup(ctx, "primary", ref)
				if err != nil {
					t.Fatal(err)
				}
				rights.SetFlags()
				for i := range before.Participants {
					if before.Participants[i].Peer.ID == 99 {
						before.Participants[i].AdminRights = uint32(rights.Flags)
					}
				}
				before.Peer = after.Peer // Independent peer metadata may advance.
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("partial refresh must merge only explicit self permissions: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}
