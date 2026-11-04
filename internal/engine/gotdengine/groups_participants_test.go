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

var groupsTestPeers = []engine.PeerRef{{Type: engine.PeerChat, ID: 20}, {Type: engine.PeerChannel, ID: 30}, {Type: engine.PeerChannel, ID: 40}}

func groupsTestReply(t *testing.T, out bin.Decoder) {
	t.Helper()
	switch v := out.(type) {
	case *tg.MessagesInvitedUsers:
		*v = tg.MessagesInvitedUsers{Updates: &tg.Updates{}}
	case *tg.UpdatesBox:
		v.Updates = &tg.Updates{}
	case *tg.BoolBox:
		v.Bool = &tg.BoolTrue{}
	case *tg.ExportedChatInviteBox:
		v.ExportedChatInvite = &tg.ChatInviteExported{Link: "https://telegram.invalid/+replacement"}
	default:
		t.Fatalf("unexpected response decoder %T", out)
	}
}

func TestGroupsParticipantsRPCTypeTable(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, action := range []engine.ParticipantsAction{engine.ParticipantsAdd, engine.ParticipantsRemove, engine.ParticipantsBan, engine.ParticipantsPromote, engine.ParticipantsDemote} {
			t.Run(fmt.Sprintf("%s:%d/%s", ref.Type, ref.ID, action), func(t *testing.T) {
				e, _ := writeFixture(t)
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					wantUser := &tg.InputUser{UserID: 10, AccessHash: 1010}
					wantPeer := &tg.InputPeerUser{UserID: 10, AccessHash: 1010}
					wantHash := int64(3030)
					if ref.ID == 40 {
						wantHash = 4040
					}
					wantChannel := &tg.InputChannel{ChannelID: ref.ID, AccessHash: wantHash}
					switch r := in.(type) {
					case *tg.MessagesAddChatUserRequest:
						if ref.Type != engine.PeerChat || action != engine.ParticipantsAdd || r.ChatID != 20 || r.FwdLimit != 0 || !reflect.DeepEqual(r.UserID, wantUser) {
							t.Fatalf("bad add: %#v", r)
						}
					case *tg.ChannelsInviteToChannelRequest:
						if ref.Type != engine.PeerChannel || action != engine.ParticipantsAdd || !reflect.DeepEqual(r.Channel, wantChannel) || !reflect.DeepEqual(r.Users, []tg.InputUserClass{wantUser}) {
							t.Fatalf("bad invite: %#v", r)
						}
					case *tg.MessagesDeleteChatUserRequest:
						if ref.Type != engine.PeerChat || (action != engine.ParticipantsRemove && action != engine.ParticipantsBan) || r.ChatID != 20 || r.RevokeHistory || !reflect.DeepEqual(r.UserID, wantUser) {
							t.Fatalf("bad remove: %#v", r)
						}
					case *tg.ChannelsEditBannedRequest:
						if ref.Type != engine.PeerChannel || (action != engine.ParticipantsRemove && action != engine.ParticipantsBan) || !reflect.DeepEqual(r.Channel, wantChannel) || !reflect.DeepEqual(r.Participant, wantPeer) || r.BannedRights.ViewMessages != (calls == 1) || r.BannedRights.UntilDate != 0 {
							t.Fatalf("bad ban/kick: %#v", r)
						}
					case *tg.MessagesEditChatAdminRequest:
						if ref.Type != engine.PeerChat || (action != engine.ParticipantsPromote && action != engine.ParticipantsDemote) || r.ChatID != 20 || r.IsAdmin != (action == engine.ParticipantsPromote) || !reflect.DeepEqual(r.UserID, wantUser) {
							t.Fatalf("bad basic admin: %#v", r)
						}
					case *tg.ChannelsEditAdminRequest:
						if ref.Type != engine.PeerChannel || (action != engine.ParticipantsPromote && action != engine.ParticipantsDemote) || !reflect.DeepEqual(r.Channel, wantChannel) || !reflect.DeepEqual(r.UserID, wantUser) || !reflect.DeepEqual(r.AdminRights, tg.ChatAdminRights{Other: action == engine.ParticipantsPromote}) {
							t.Fatalf("bad channel admin: %#v", r)
						}
					default:
						t.Fatalf("unexpected RPC %T", in)
					}
					groupsTestReply(t, out)
					return nil
				}))
				r := engine.ParticipantsRequest{Group: ref, Action: action, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}}, WriteOptions: engine.WriteOptions{DryRun: true}}
				p, err := e.ManageParticipants(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				if !p.DryRun || p.Preview == nil || p.Preview.Target.Ref != ref || p.Preview.Text != string(action) || len(p.Preview.Users) != 1 || calls != 0 {
					t.Fatalf("preview: %+v calls=%d", p, calls)
				}
				if action == engine.ParticipantsBan && p.Preview.Reversible {
					t.Fatal("ban marked reversible")
				}
				r.DryRun = false
				r.ExpectFingerprint = p.Preview.Fingerprint
				got, err := e.ManageParticipants(context.Background(), r)
				wantCalls := 1
				if ref.Type == engine.PeerChannel && action == engine.ParticipantsRemove {
					wantCalls = 2
				}
				if err != nil || !got.Done || got.Affected != 1 || got.PerUser["user:10"] != "ok" || calls != wantCalls {
					t.Fatalf("result=%+v err=%v calls=%d", got, err, calls)
				}
			})
		}
	}
}

func TestGroupsParticipantsPartialRPCFailures(t *testing.T) {
	e, _ := writeFixture(t)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		r, ok := in.(*tg.MessagesEditChatAdminRequest)
		if !ok {
			t.Fatalf("unexpected RPC %T", in)
		}
		if r.UserID.(*tg.InputUser).UserID == 11 {
			return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
		}
		groupsTestReply(t, out)
		return nil
	}))
	r := engine.ParticipantsRequest{Group: groupsTestPeers[0], Action: engine.ParticipantsPromote, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}, {Type: engine.PeerUser, ID: 11}}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.ManageParticipants(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun, r.ExpectFingerprint = false, p.Preview.Fingerprint
	got, err := e.ManageParticipants(context.Background(), r)
	var rpc *engine.RPCError
	if got.Done || got.Affected != 1 || calls != 2 || got.PerUser["user:10"] != "ok" || got.PerUser["user:11"] != "telegram: CHAT_ADMIN_REQUIRED on messages.editChatAdmin" || !errors.Is(err, engine.ErrPermission) || !errors.As(err, &rpc) || rpc.Type != "CHAT_ADMIN_REQUIRED" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestGroupsParticipantsMissingInvitees(t *testing.T) {
	for _, ref := range groupsTestPeers {
		t.Run(fmt.Sprint(ref), func(t *testing.T) {
			e, _ := writeFixture(t)
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				var id int64
				switch r := in.(type) {
				case *tg.MessagesAddChatUserRequest:
					id = r.UserID.(*tg.InputUser).UserID
				case *tg.ChannelsInviteToChannelRequest:
					id = r.Users[0].(*tg.InputUser).UserID
				default:
					t.Fatalf("unexpected RPC (must not export or send a link): %T", in)
				}
				groupsTestReply(t, out)
				if id == 11 {
					out.(*tg.MessagesInvitedUsers).MissingInvitees = []tg.MissingInvitee{{UserID: 11}}
				}
				return nil
			}))
			r := engine.ParticipantsRequest{Group: ref, Action: engine.ParticipantsAdd, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}, {Type: engine.PeerUser, ID: 11}}, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.ManageParticipants(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			r.DryRun, r.ExpectFingerprint = false, p.Preview.Fingerprint
			got, err := e.ManageParticipants(context.Background(), r)
			if got.Done || got.Affected != 1 || calls != 2 || got.PerUser["user:10"] != "ok" || got.PerUser["user:11"] == "ok" || !errors.Is(err, engine.ErrPermission) {
				t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestGroupsParticipantsKickUnbanFailureIsPartial(t *testing.T) {
	e, _ := writeFixture(t)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		if _, ok := in.(*tg.ChannelsEditBannedRequest); !ok {
			t.Fatalf("unexpected %T", in)
		}
		if calls == 2 {
			return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
		}
		groupsTestReply(t, out)
		return nil
	}))
	r := engine.ParticipantsRequest{Group: groupsTestPeers[1], Action: engine.ParticipantsRemove, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.ManageParticipants(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun, r.ExpectFingerprint = false, p.Preview.Fingerprint
	got, err := e.ManageParticipants(context.Background(), r)
	if !errors.Is(err, engine.ErrPermission) || got.Done || got.Affected != 1 || got.PerUser["user:10"] == "ok" || calls != 2 {
		t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
	}
}
