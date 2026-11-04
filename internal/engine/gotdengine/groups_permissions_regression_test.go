package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestGroupsFingerprintRetainsTelegramParticipantRights(t *testing.T) {
	admin := tg.ChatAdminRights{ChangeInfo: true, BanUsers: true, AddAdmins: true, Other: true}
	admin.SetFlags()
	banned := tg.ChatBannedRights{ViewMessages: true, SendMessages: true, UntilDate: 12345}
	banned.SetFlags()
	for _, tc := range []struct {
		name          string
		raw           tg.ChannelParticipantClass
		admin, banned uint32
		until         int
		edit          bool
	}{
		{"creator", &tg.ChannelParticipantCreator{UserID: 10, AdminRights: admin}, uint32(admin.Flags), 0, 0, false},
		{"admin", &tg.ChannelParticipantAdmin{UserID: 10, AdminRights: admin, CanEdit: true}, uint32(admin.Flags), 0, 0, true},
		{"banned", &tg.ChannelParticipantBanned{Peer: &tg.PeerUser{UserID: 10}, BannedRights: banned}, 0, uint32(banned.Flags), banned.UntilDate, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := channelGroupMember(tc.raw)
			if !ok || got.AdminRights != tc.admin || got.BannedRights != tc.banned || got.BannedUntil != tc.until || got.CanEdit != tc.edit {
				t.Fatalf("participant rights lost: %+v", got)
			}
		})
	}
}

func TestGroupsFingerprintSelfRightsFromRefresh(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, hidden := range []bool{false, true} {
			if hidden && ref.Type == engine.PeerChat {
				continue
			}
			t.Run(fmt.Sprintf("%s:%d/hidden_%t", ref.Type, ref.ID, hidden), func(t *testing.T) {
				e, s, snapshot := fingerprintGroupFixture(t, ref)
				rights := tg.ChatAdminRights{ChangeInfo: true}
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					switch in.(type) {
					case *tg.MessagesGetFullChatRequest:
						*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: ref.ID, Participants: &tg.ChatParticipants{ChatID: ref.ID, Participants: []tg.ChatParticipantClass{&tg.ChatParticipantAdmin{UserID: 99}}}}, Chats: []tg.ChatClass{&tg.Chat{ID: ref.ID, Title: "Group", ParticipantsCount: 1, AdminRights: rights}}}
					case *tg.ChannelsGetFullChannelRequest:
						*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: ref.ID, ParticipantsCount: 1}, Chats: []tg.ChatClass{&tg.Channel{ID: ref.ID, Title: "Group", AccessHash: fingerprintGroupHash(ref), Megagroup: ref.ID == 30, Broadcast: ref.ID == 40, AdminRights: rights}}}
					case *tg.ChannelsGetParticipantsRequest:
						participants := []tg.ChannelParticipantClass{&tg.ChannelParticipantSelf{UserID: 99}}
						if hidden {
							participants = nil
						}
						out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{Count: len(participants), Participants: participants}
					default:
						t.Fatalf("unexpected write RPC %T", in)
					}
					return nil
				}))
				// No retained full snapshot: allow the hidden recent-members view to initialize.
				if hidden {
					snapshot.Partial = true
					fingerprintSaveSnapshot(t, s, snapshot)
				}
				if _, err := e.RefreshGroup(context.Background(), engine.GroupRequest{Group: ref}); err != nil {
					t.Fatal(err)
				}
				p, err := e.InviteLink(context.Background(), engine.InviteLinkRequest{Group: ref, Revoke: true, WriteOptions: engine.WriteOptions{DryRun: true}})
				if err != nil {
					t.Fatal(err)
				}
				rights.InviteUsers = true
				if _, err := e.RefreshGroup(context.Background(), engine.GroupRequest{Group: ref}); err != nil {
					t.Fatal(err)
				}
				before := calls
				got, err := e.InviteLink(context.Background(), engine.InviteLinkRequest{Group: ref, Revoke: true, WriteOptions: engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}})
				if !errors.Is(err, engine.ErrPreviewMismatch) || got.Done || calls != before {
					t.Fatalf("refreshed self permissions did not invalidate: %+v err=%v calls=%d before=%d", got, err, calls, before)
				}
			})
		}
	}
}
