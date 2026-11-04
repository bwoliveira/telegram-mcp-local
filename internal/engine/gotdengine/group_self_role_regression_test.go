package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestGroupSelfRoleReconcilesExplicitMetadata(t *testing.T) {
	self := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	for _, tc := range []struct {
		name, oldRole, wantRole string
		creator                 bool
		admin                   tg.ChatAdminRights
		banned                  tg.ChatBannedRights
	}{
		{name: "missing_restricted", wantRole: "restricted", banned: tg.ChatBannedRights{SendMessages: true, UntilDate: 12345}},
		{name: "missing_banned", wantRole: "banned", banned: tg.ChatBannedRights{ViewMessages: true, UntilDate: 12345}},
		{name: "admin_to_restricted", oldRole: "admin", wantRole: "restricted", banned: tg.ChatBannedRights{SendMessages: true, UntilDate: 12345}},
		{name: "admin_to_banned", oldRole: "admin", wantRole: "banned", banned: tg.ChatBannedRights{ViewMessages: true, UntilDate: 12345}},
		{name: "admin_to_member", oldRole: "admin", wantRole: "member"},
		{name: "clear_restriction", oldRole: "restricted", wantRole: "member"},
		{name: "clear_ban", oldRole: "banned", wantRole: "member"},
		{name: "promote_admin", oldRole: "restricted", wantRole: "admin", admin: tg.ChatAdminRights{InviteUsers: true}},
		{name: "creator_wins", oldRole: "restricted", wantRole: "creator", creator: true, admin: tg.ChatAdminRights{ChangeInfo: true}},
		// A creator participant constructor is positive ownership evidence; an
		// omitted Creator metadata flag must not silently demote that participant.
		{name: "creator_constructor_over_admin_metadata", oldRole: "creator", wantRole: "creator", admin: tg.ChatAdminRights{ChangeInfo: true}},
		{name: "creator_constructor_over_empty_metadata", oldRole: "creator", wantRole: "creator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := engine.Participant{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Role: "member", Name: "Other"}
			g := engine.Group{Participants: []engine.Participant{other}}
			joined := time.Unix(1000, 0).UTC()
			if tc.oldRole != "" {
				g.Participants = append(g.Participants, engine.Participant{Peer: self, Role: tc.oldRole, Name: "Self", Username: "synthetic_self", JoinedAt: &joined, CanEdit: true, AdminRights: 1, BannedRights: 2, BannedUntil: 9876})
			}
			known := groupSelfRights(&g, self.ID, tc.creator, tc.admin, tc.banned)
			tc.admin.SetFlags()
			tc.banned.SetFlags()
			want := groupMembership{Known: true, Present: true, Role: tc.wantRole, AdminRights: uint32(tc.admin.Flags), BannedRights: uint32(tc.banned.Flags), BannedUntil: tc.banned.UntilDate, CanEdit: tc.oldRole != ""}
			if got := groupMemberState(&g, self); got != want {
				t.Errorf("snapshot/fingerprint self state=%+v, want=%+v", got, want)
			}
			if known == nil || known.Role != tc.wantRole || known.AdminRights != want.AdminRights || known.BannedRights != want.BannedRights || known.BannedUntil != want.BannedUntil {
				t.Errorf("retained-cache explicit self=%+v, want=%+v", known, want)
			}
			if !reflect.DeepEqual(g.Participants[0], other) {
				t.Errorf("unrelated participant changed: %+v", g.Participants[0])
			}
			if tc.oldRole != "" {
				got := g.Participants[1]
				if got.Name != "Self" || got.Username != "synthetic_self" || !got.CanEdit || got.JoinedAt == nil || !got.JoinedAt.Equal(joined) {
					t.Errorf("role reconciliation replaced unrelated self metadata: %+v", got)
				}
			}
		})
	}
	t.Run("missing_empty_rights_do_not_prove_membership", func(t *testing.T) {
		g := engine.Group{}
		known := groupSelfRights(&g, self.ID, false, tg.ChatAdminRights{}, tg.ChatBannedRights{})
		if known == nil || known.Role != "member" || len(g.Participants) != 0 {
			t.Fatalf("empty metadata invented membership: explicit=%+v group=%+v", known, g)
		}
	})
	t.Run("unknown_self_is_not_inserted", func(t *testing.T) {
		g := engine.Group{}
		if known := groupSelfRights(&g, 0, true, tg.ChatAdminRights{}, tg.ChatBannedRights{}); known != nil || len(g.Participants) != 0 {
			t.Fatalf("unknown self inserted: explicit=%+v group=%+v", known, g)
		}
	})
}

func TestGroupSelfRoleSnapshotAndRetainedCacheFingerprints(t *testing.T) {
	for _, ref := range []engine.PeerRef{{Type: engine.PeerChannel, ID: 30}, {Type: engine.PeerChannel, ID: 40}} {
		for _, tc := range []struct {
			name, oldRole, wantRole string
			creator                 bool
			admin                   tg.ChatAdminRights
			banned                  tg.ChatBannedRights
		}{
			{name: "missing_restricted", wantRole: "restricted", banned: tg.ChatBannedRights{SendMessages: true, UntilDate: 12345}},
			{name: "missing_banned", wantRole: "banned", banned: tg.ChatBannedRights{ViewMessages: true, UntilDate: 12345}},
			{name: "admin_to_restricted", oldRole: "admin", wantRole: "restricted", banned: tg.ChatBannedRights{SendMessages: true, UntilDate: 12345}},
			{name: "admin_to_banned", oldRole: "admin", wantRole: "banned", banned: tg.ChatBannedRights{ViewMessages: true, UntilDate: 12345}},
			{name: "admin_to_member", oldRole: "admin", wantRole: "member"},
			{name: "clear_restriction", oldRole: "restricted", wantRole: "member"},
			{name: "clear_ban", oldRole: "banned", wantRole: "member"},
			{name: "creator", oldRole: "member", creator: true, wantRole: "creator"},
			{name: "creator_constructor", oldRole: "creator", wantRole: "creator"},
		} {
			t.Run(fmt.Sprintf("%s:%d/%s", ref.Type, ref.ID, tc.name), func(t *testing.T) {
				fingerprints := map[string]string{}
				for _, mode := range []string{"snapshot", "retained_complete"} {
					t.Run(mode, func(t *testing.T) {
						ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
						defer cancel()
						e, st, snapshot := fingerprintGroupFixture(t, ref)
						snapshot.Partial = mode == "snapshot"
						if tc.creator || tc.oldRole == "creator" {
							owner := engine.PeerRef{Type: engine.PeerUser, ID: 99}
							snapshot.Owner = &owner
						}
						if tc.oldRole == "" {
							snapshot.Participants = snapshot.Participants[1:]
						} else {
							snapshot.Participants[0].Role = tc.oldRole
						}
						fingerprintSaveSnapshot(t, st, snapshot)
						before, err := st.ReadGroup(ctx, "primary", ref)
						if err != nil {
							t.Fatal(err)
						}
						writes := 0
						e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
							switch in.(type) {
							case *tg.ChannelsGetFullChannelRequest:
								*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: ref.ID, About: "Fresh metadata", ParticipantsCount: 3, ParticipantsHidden: mode == "retained_complete"}, Chats: []tg.ChatClass{&tg.Channel{ID: ref.ID, Title: "Group", AccessHash: fingerprintGroupHash(ref), Megagroup: ref.ID == 30, Broadcast: ref.ID == 40, Creator: tc.creator, AdminRights: tc.admin, BannedRights: tc.banned}}}
							case *tg.ChannelsGetParticipantsRequest:
								members := []tg.ChannelParticipantClass{&tg.ChannelParticipant{UserID: 10}, &tg.ChannelParticipant{UserID: 11}}
								switch tc.oldRole {
								case "admin":
									members = append(members, &tg.ChannelParticipantAdmin{UserID: 99})
								case "restricted", "banned":
									members = append(members, &tg.ChannelParticipantBanned{Peer: &tg.PeerUser{UserID: 99}, BannedRights: tg.ChatBannedRights{SendMessages: true, ViewMessages: tc.oldRole == "banned", UntilDate: 9876}})
								case "creator":
									members = append(members, &tg.ChannelParticipantCreator{UserID: 99})
								case "member":
									members = append(members, &tg.ChannelParticipantSelf{UserID: 99})
								}
								out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{Count: len(members), Participants: members}
							default:
								writes++
								t.Errorf("unexpected write RPC %T", in)
								return engine.ErrInvalid
							}
							return nil
						}))
						req := engine.ParticipantsRequest{Group: ref, Action: engine.ParticipantsAdd, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}}, WriteOptions: engine.WriteOptions{DryRun: true}}
						oldPreview, err := e.ManageParticipants(ctx, req)
						if err != nil || oldPreview.Preview == nil {
							t.Fatalf("preview=%+v err=%v", oldPreview, err)
						}
						live, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref})
						if err != nil {
							t.Fatal(err)
						}
						cached, err := st.ReadGroup(ctx, "primary", ref)
						if err != nil {
							t.Fatal(err)
						}
						tc.admin.SetFlags()
						tc.banned.SetFlags()
						want := groupMembership{Known: true, Present: true, Role: tc.wantRole, AdminRights: uint32(tc.admin.Flags), BannedRights: uint32(tc.banned.Flags), BannedUntil: tc.banned.UntilDate}
						self := engine.PeerRef{Type: engine.PeerUser, ID: 99}
						for name, group := range map[string]*engine.Group{"live": &live, "cached": &cached} {
							if got := groupMemberState(group, self); got != want {
								t.Errorf("%s self state=%+v, want=%+v", name, got, want)
							}
						}
						if cached.AmAdmin != tc.creator || cached.Partial {
							t.Errorf("cached admin/completeness inconsistent: %+v", cached)
						}
						if mode == "retained_complete" {
							if !live.Partial || !cached.SnapshotAt.Equal(before.SnapshotAt) || cached.About != before.About || cached.Size != before.Size {
								t.Errorf("partial refresh replaced retained metadata: before=%+v after=%+v live=%+v", before, cached, live)
							}
							for _, member := range before.Participants {
								if member.Peer == self {
									continue
								}
								found := false
								for _, got := range cached.Participants {
									if got.Peer == member.Peer {
										found = reflect.DeepEqual(got, member)
									}
								}
								if !found {
									t.Errorf("retained unrelated member changed: %+v", member)
								}
							}
						}
						req.WriteOptions = engine.WriteOptions{ExpectFingerprint: oldPreview.Preview.Fingerprint}
						result, err := e.ManageParticipants(ctx, req)
						if !errors.Is(err, engine.ErrPreviewMismatch) || result.Done || writes != 0 {
							t.Errorf("stale approval was not rejected before RPC: result=%+v err=%v writes=%d", result, err, writes)
						}
						req.WriteOptions = engine.WriteOptions{DryRun: true}
						fresh, err := e.ManageParticipants(ctx, req)
						if err != nil || fresh.Preview == nil {
							t.Fatalf("fresh preview=%+v err=%v", fresh, err)
						}
						fingerprints[mode] = fresh.Preview.Fingerprint
						if _, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref}); err != nil {
							t.Fatal(err)
						}
						repeated, err := e.ManageParticipants(ctx, req)
						if err != nil || repeated.Preview == nil || repeated.Preview.Fingerprint != fresh.Preview.Fingerprint || writes != 0 {
							t.Errorf("identical explicit rights changed fingerprint: preview=%+v err=%v writes=%d", repeated, err, writes)
						}
					})
				}
				if fingerprints["snapshot"] != fingerprints["retained_complete"] {
					t.Errorf("equivalent self state differs by cache path: %v", fingerprints)
				}
			})
		}
	}
}
