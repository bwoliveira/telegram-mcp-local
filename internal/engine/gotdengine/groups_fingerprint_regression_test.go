package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// These tests exercise the real Engine approval boundary and persisted offline
// snapshots. The only Telegram transport is the exact-request fake Invoker.
// No Telegram session, service, or non-fixture database is opened.
func fingerprintGroupFixture(t *testing.T, ref engine.PeerRef) (*Engine, *store.Store, engine.ParticipantsSnapshot) {
	t.Helper()
	e, s := groupRefreshStoreEngine(t)
	if err := e.SetMode(context.Background(), engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	kind := engine.KindGroup
	if ref.Type == engine.PeerChannel {
		kind = engine.KindSupergroup
		if ref.ID == 40 {
			kind = engine.KindChannel
		}
	}
	snapshot := engine.ParticipantsSnapshot{
		Group: ref, About: "Old about", Size: 3, AmAdmin: true, At: time.Unix(1000, 0).UTC(),
		Participants: []engine.Participant{
			{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, Name: "Self", Role: "admin"},
			{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Name: "Target", Role: "member"},
			{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 11}, Name: "Other", Role: "member"},
		},
	}
	_, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{
		{Peer: engine.Peer{Ref: ref, Kind: kind, Title: "Old title", Access: engine.AccessOK}, AccessHash: fingerprintGroupHash(ref)},
		{Peer: engine.Peer{Ref: snapshot.Participants[0].Peer, Kind: engine.KindSelf, FirstName: "Self", Access: engine.AccessOK}},
		{Peer: engine.Peer{Ref: snapshot.Participants[1].Peer, Kind: engine.KindUser, FirstName: "Target", Access: engine.AccessOK}, AccessHash: 1010},
		{Peer: engine.Peer{Ref: snapshot.Participants[2].Peer, Kind: engine.KindUser, FirstName: "Other", Access: engine.AccessOK}, AccessHash: 1111},
	}, Participants: []engine.ParticipantsSnapshot{snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	return e, s, snapshot
}

func fingerprintGroupHash(ref engine.PeerRef) int64 {
	if ref.Type == engine.PeerChat {
		return 0
	}
	return ref.ID * 101
}

func fingerprintSaveSnapshot(t *testing.T, s *store.Store, snapshot engine.ParticipantsSnapshot) {
	t.Helper()
	snapshot.At = snapshot.At.Add(time.Second)
	if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Participants: []engine.ParticipantsSnapshot{snapshot}}); err != nil {
		t.Fatal(err)
	}
}

func fingerprintChangePeer(t *testing.T, s *store.Store, ref engine.PeerRef, hash int64, change func(*engine.Peer)) {
	t.Helper()
	p, found, err := s.LookupPeer(context.Background(), "primary", ref)
	if err != nil || !found {
		t.Fatalf("lookup %v: found=%v err=%v", ref, found, err)
	}
	change(&p)
	if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: p, AccessHash: hash}}}); err != nil {
		t.Fatal(err)
	}
}

// Use reflection only to avoid coupling this regression to the representation
// of the new rights contract (a string set, bit mask, or Telegram-shaped struct).
// Missing rights are a hard failure, never a skip or an ignored JSON field.
func fingerprintChangeRight(t *testing.T, participant *engine.Participant, field string) {
	t.Helper()
	v := reflect.ValueOf(participant).Elem().FieldByName(field)
	if !v.IsValid() {
		t.Fatalf("Participant.%s missing: cached permission changes cannot be bound", field)
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		flag := "ChangeInfo"
		if field == "BannedRights" {
			flag = "ViewMessages"
		}
		f := v.FieldByName(flag)
		if !f.IsValid() || f.Kind() != reflect.Bool {
			t.Fatalf("unsupported %s rights struct %s", field, v.Type())
		}
		f.SetBool(!f.Bool())
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			t.Fatalf("unsupported rights slice %s", v.Type())
		}
		v.Set(reflect.Append(v, reflect.ValueOf("change_info").Convert(v.Type().Elem())))
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.Type().Elem().Kind() != reflect.Bool {
			t.Fatalf("unsupported rights map %s", v.Type())
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		v.SetMapIndex(reflect.ValueOf("change_info").Convert(v.Type().Key()), reflect.ValueOf(true).Convert(v.Type().Elem()))
	case reflect.String:
		v.SetString("change_info")
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() ^ 1)
	case reflect.Uint, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint() ^ 1)
	default:
		t.Fatalf("unsupported %s rights representation %s", field, v.Type())
	}
}

func fingerprintParticipantInvoker(t *testing.T, e *Engine, ref engine.PeerRef, action engine.ParticipantsAction, reject bool) *int {
	t.Helper()
	calls := new(int)
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		*calls++
		if reject {
			t.Errorf("stale fingerprint reached RPC %T", in)
		}
		user := &tg.InputUser{UserID: 10, AccessHash: 1010}
		peer := &tg.InputPeerUser{UserID: 10, AccessHash: 1010}
		channel := &tg.InputChannel{ChannelID: ref.ID, AccessHash: fingerprintGroupHash(ref)}
		valid := false
		switch r := in.(type) {
		case *tg.MessagesAddChatUserRequest:
			valid = ref.Type == engine.PeerChat && action == engine.ParticipantsAdd && reflect.DeepEqual(r, &tg.MessagesAddChatUserRequest{ChatID: ref.ID, UserID: user})
		case *tg.ChannelsInviteToChannelRequest:
			valid = ref.Type == engine.PeerChannel && action == engine.ParticipantsAdd && reflect.DeepEqual(r, &tg.ChannelsInviteToChannelRequest{Channel: channel, Users: []tg.InputUserClass{user}})
		case *tg.MessagesDeleteChatUserRequest:
			valid = ref.Type == engine.PeerChat && (action == engine.ParticipantsRemove || action == engine.ParticipantsBan) && reflect.DeepEqual(r, &tg.MessagesDeleteChatUserRequest{ChatID: ref.ID, UserID: user})
		case *tg.ChannelsEditBannedRequest:
			valid = ref.Type == engine.PeerChannel && (action == engine.ParticipantsRemove || action == engine.ParticipantsBan) && reflect.DeepEqual(r, &tg.ChannelsEditBannedRequest{Channel: channel, Participant: peer, BannedRights: tg.ChatBannedRights{ViewMessages: *calls == 1}})
		case *tg.MessagesEditChatAdminRequest:
			valid = ref.Type == engine.PeerChat && (action == engine.ParticipantsPromote || action == engine.ParticipantsDemote) && reflect.DeepEqual(r, &tg.MessagesEditChatAdminRequest{ChatID: ref.ID, UserID: user, IsAdmin: action == engine.ParticipantsPromote})
		case *tg.ChannelsEditAdminRequest:
			valid = ref.Type == engine.PeerChannel && (action == engine.ParticipantsPromote || action == engine.ParticipantsDemote) && reflect.DeepEqual(r, &tg.ChannelsEditAdminRequest{Channel: channel, UserID: user, AdminRights: tg.ChatAdminRights{Other: action == engine.ParticipantsPromote}})
		}
		// Rejected cases need only prove zero RPCs. Do not obscure that defect
		// with argument mismatches caused by the deliberate identity mutation.
		if !valid && !reject {
			t.Fatalf("unexpected RPC or arguments: %T %#v", in, in)
		}
		groupsTestReply(t, out)
		return nil
	}))
	return calls
}

func TestGroupsFingerprintParticipantsRegression(t *testing.T) {
	actions := []engine.ParticipantsAction{engine.ParticipantsAdd, engine.ParticipantsRemove, engine.ParticipantsBan, engine.ParticipantsPromote, engine.ParticipantsDemote}
	for _, ref := range groupsTestPeers {
		for _, action := range actions {
			for _, mutation := range []string{
				"none", "group_title", "group_about", "group_count", "other_role", "other_display", "other_admin_rights", "other_banned_rights", "target_display", "target_membership_display",
				"target_role", "target_admin_rights", "target_banned_rights", "target_identity", "target_hash", "target_access", "self_role", "self_admin_rights", "self_banned_rights", "self_permission", "self_identity", "group_identity", "group_access", "group_hash",
			} {
				if mutation == "group_hash" && ref.Type == engine.PeerChat {
					continue // basic chat inputs have no access hash
				}
				t.Run(fmt.Sprintf("%s:%d/%s/%s", ref.Type, ref.ID, action, mutation), func(t *testing.T) {
					e, s, snapshot := fingerprintGroupFixture(t, ref)
					reject := false
					switch mutation {
					case "target_role", "target_admin_rights", "target_banned_rights", "target_identity", "target_hash", "target_access", "self_role", "self_admin_rights", "self_banned_rights", "self_permission", "self_identity", "group_identity", "group_access", "group_hash":
						reject = true
					}
					calls := fingerprintParticipantInvoker(t, e, ref, action, reject)
					r := engine.ParticipantsRequest{Group: ref, Action: action, Users: []engine.PeerRef{{Type: engine.PeerUser, ID: 10}}, WriteOptions: engine.WriteOptions{DryRun: true}}
					p, err := e.ManageParticipants(context.Background(), r)
					if err != nil || p.Preview == nil || *calls != 0 {
						t.Fatalf("local preview=%+v err=%v calls=%d", p, err, *calls)
					}
					switch mutation {
					case "group_title", "group_count", "group_access", "group_hash":
						hash := fingerprintGroupHash(ref)
						if mutation == "group_hash" {
							hash++
						}
						fingerprintChangePeer(t, s, ref, hash, func(p *engine.Peer) {
							switch mutation {
							case "group_title":
								p.Title = "Unrelated concurrent title"
							case "group_count":
								p.Participants = 20
							case "group_access":
								p.Access = engine.AccessLeft
							}
						})
					case "target_display", "target_hash", "target_access":
						hash := int64(1010)
						if mutation == "target_hash" {
							hash++
						}
						fingerprintChangePeer(t, s, r.Users[0], hash, func(p *engine.Peer) {
							if mutation == "target_display" {
								p.FirstName, p.LastName, p.Username, p.Phone = "Renamed", "Display", "new_username", "123"
							}
							if mutation == "target_access" {
								p.Access = engine.AccessDeleted
							}
						})
					case "target_identity":
						r.Users[0].ID = 11
					case "self_identity":
						e.account.SelfID = 100
					case "group_identity":
						// An independently persisted, otherwise identical group.
						other := ref
						other.ID += 100
						peer, _, err := s.LookupPeer(context.Background(), "primary", ref)
						if err != nil {
							t.Fatal(err)
						}
						peer.Ref = other
						snapshot.Group = other
						if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: peer, AccessHash: fingerprintGroupHash(ref)}}, Participants: []engine.ParticipantsSnapshot{snapshot}}); err != nil {
							t.Fatal(err)
						}
						r.Group = other
					case "group_about":
						snapshot.About = "Unrelated concurrent about"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "other_role":
						snapshot.Participants[2].Role = "admin"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "other_display":
						snapshot.Participants[2].Name, snapshot.Participants[2].Username = "Other renamed", "other_new"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "target_membership_display":
						snapshot.Participants[1].Name, snapshot.Participants[1].Username = "Target renamed", "target_new"
						joined := time.Unix(123, 0)
						snapshot.Participants[1].JoinedAt = &joined
						fingerprintSaveSnapshot(t, s, snapshot)
					case "target_role":
						snapshot.Participants[1].Role = "admin"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "self_role":
						snapshot.Participants[0].Role = "member"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "self_permission":
						snapshot.AmAdmin = false
						fingerprintSaveSnapshot(t, s, snapshot)
					case "target_admin_rights", "target_banned_rights", "other_admin_rights", "other_banned_rights", "self_admin_rights", "self_banned_rights":
						index := 1
						if mutation == "other_admin_rights" || mutation == "other_banned_rights" {
							index = 2
						}
						if mutation == "self_admin_rights" || mutation == "self_banned_rights" {
							index = 0
						}
						field := "AdminRights"
						if mutation == "target_banned_rights" || mutation == "other_banned_rights" || mutation == "self_banned_rights" {
							field = "BannedRights"
						}
						fingerprintChangeRight(t, &snapshot.Participants[index], field)
						fingerprintSaveSnapshot(t, s, snapshot)
					}
					r.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
					got, err := e.ManageParticipants(context.Background(), r)
					if reject {
						if err == nil || *calls != 0 || got.Done || got.Affected != 0 {
							t.Fatalf("relevant %s accepted stale approval: result=%+v err=%v calls=%d", mutation, got, err, *calls)
						}
						if mutation != "target_access" && mutation != "group_access" && !errors.Is(err, engine.ErrPreviewMismatch) {
							t.Fatalf("relevant change must invalidate approval: %v", err)
						}
						return
					}
					wantCalls := 1
					if ref.Type == engine.PeerChannel && action == engine.ParticipantsRemove {
						wantCalls = 2
					}
					if err != nil || !got.Done || got.Affected != 1 || got.PerUser["user:10"] != "ok" || *calls != wantCalls {
						t.Fatalf("unrelated %s invalidated approval: result=%+v err=%v calls=%d want=%d", mutation, got, err, *calls, wantCalls)
					}
				})
			}
		}
	}
}

func TestGroupsFingerprintMetadataRegression(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, requested := range []string{"title", "about", "both"} {
			for _, changed := range []string{"title", "about", "other_role", "self_permission"} {
				t.Run(fmt.Sprintf("%s:%d/%s/change_%s", ref.Type, ref.ID, requested, changed), func(t *testing.T) {
					e, s, snapshot := fingerprintGroupFixture(t, ref)
					title, about := "Approved title", "Approved about"
					r := engine.UpdateGroupRequest{Group: ref, WriteOptions: engine.WriteOptions{DryRun: true}}
					if requested != "about" {
						r.Title = &title
					}
					if requested != "title" {
						r.About = &about
					}
					reject := changed == "self_permission" || changed == "title" && r.Title != nil || changed == "about" && r.About != nil
					calls := 0
					e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
						calls++
						if reject {
							t.Errorf("changed requested field/permission reached RPC %T", in)
						}
						valid := false
						switch v := in.(type) {
						case *tg.MessagesEditChatTitleRequest:
							valid = r.Title != nil && ref.Type == engine.PeerChat && reflect.DeepEqual(v, &tg.MessagesEditChatTitleRequest{ChatID: ref.ID, Title: title})
						case *tg.ChannelsEditTitleRequest:
							valid = r.Title != nil && ref.Type == engine.PeerChannel && reflect.DeepEqual(v, &tg.ChannelsEditTitleRequest{Channel: &tg.InputChannel{ChannelID: ref.ID, AccessHash: fingerprintGroupHash(ref)}, Title: title})
						case *tg.MessagesEditChatAboutRequest:
							var input tg.InputPeerClass = &tg.InputPeerChat{ChatID: ref.ID}
							if ref.Type == engine.PeerChannel {
								input = &tg.InputPeerChannel{ChannelID: ref.ID, AccessHash: fingerprintGroupHash(ref)}
							}
							valid = r.About != nil && reflect.DeepEqual(v, &tg.MessagesEditChatAboutRequest{Peer: input, About: about})
						}
						if !valid {
							t.Fatalf("unexpected metadata RPC: %T %#v", in, in)
						}
						groupsTestReply(t, out)
						return nil
					}))
					p, err := e.UpdateGroup(context.Background(), r)
					if err != nil || p.Preview == nil || calls != 0 {
						t.Fatalf("preview=%+v err=%v calls=%d", p, err, calls)
					}
					switch changed {
					case "title":
						fingerprintChangePeer(t, s, ref, fingerprintGroupHash(ref), func(p *engine.Peer) { p.Title = "Concurrent title" })
					case "about":
						snapshot.About = "Concurrent about"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "other_role":
						snapshot.Participants[2].Role = "admin"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "self_permission":
						snapshot.AmAdmin = false
						fingerprintSaveSnapshot(t, s, snapshot)
					}
					r.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
					got, err := e.UpdateGroup(context.Background(), r)
					if reject {
						if !errors.Is(err, engine.ErrPreviewMismatch) || calls != 0 || got.Done || got.Affected != 0 {
							t.Fatalf("stale metadata approval: result=%+v err=%v calls=%d", got, err, calls)
						}
					} else {
						want := 1
						if requested == "both" {
							want = 2
						}
						if err != nil || !got.Done || got.Affected != want || calls != want {
							t.Fatalf("unrelated metadata invalidated approval: result=%+v err=%v calls=%d", got, err, calls)
						}
					}
				})
			}
		}
	}
}

func TestGroupsFingerprintInviteLeaveRegression(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, action := range []string{"invite_link", "leave"} {
			for _, mutation := range []string{"none", "group_title", "group_about", "other_role", "self_permission", "self_role", "self_admin_rights", "self_banned_rights", "self_identity", "group_identity", "group_hash", "group_access"} {
				if mutation == "group_hash" && ref.Type == engine.PeerChat {
					continue
				}
				t.Run(fmt.Sprintf("%s:%d/%s/%s", ref.Type, ref.ID, action, mutation), func(t *testing.T) {
					e, s, snapshot := fingerprintGroupFixture(t, ref)
					reject := mutation == "self_permission" || mutation == "self_role" || mutation == "self_admin_rights" || mutation == "self_banned_rights" || mutation == "self_identity" || mutation == "group_identity" || mutation == "group_hash" || mutation == "group_access"
					calls := 0
					e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
						calls++
						if reject {
							t.Errorf("stale invite/leave approval reached RPC %T", in)
						}
						valid := false
						switch v := in.(type) {
						case *tg.MessagesExportChatInviteRequest:
							var input tg.InputPeerClass = &tg.InputPeerChat{ChatID: ref.ID}
							if ref.Type == engine.PeerChannel {
								input = &tg.InputPeerChannel{ChannelID: ref.ID, AccessHash: fingerprintGroupHash(ref)}
							}
							valid = action == "invite_link" && reflect.DeepEqual(v, &tg.MessagesExportChatInviteRequest{Peer: input, LegacyRevokePermanent: true})
						case *tg.MessagesDeleteChatUserRequest:
							valid = action == "leave" && ref.Type == engine.PeerChat && reflect.DeepEqual(v, &tg.MessagesDeleteChatUserRequest{ChatID: ref.ID, UserID: &tg.InputUserSelf{}})
						case *tg.ChannelsLeaveChannelRequest:
							valid = action == "leave" && ref.Type == engine.PeerChannel && reflect.DeepEqual(v, &tg.ChannelsLeaveChannelRequest{Channel: &tg.InputChannel{ChannelID: ref.ID, AccessHash: fingerprintGroupHash(ref)}})
						}
						if !valid && !reject {
							t.Fatalf("unexpected invite/leave RPC: %T %#v", in, in)
						}
						groupsTestReply(t, out)
						return nil
					}))
					requestRef := ref
					call := func(opts engine.WriteOptions) (engine.WriteResult, error) {
						if action == "leave" {
							return e.Leave(context.Background(), engine.LeaveRequest{Group: requestRef, WriteOptions: opts})
						}
						got, err := e.InviteLink(context.Background(), engine.InviteLinkRequest{Group: requestRef, Revoke: true, WriteOptions: opts})
						if err == nil && !opts.DryRun && (got.Link != "https://telegram.invalid/+replacement" || !got.Revoked) {
							t.Fatalf("invite result=%+v", got)
						}
						return got.WriteResult, err
					}
					p, err := call(engine.WriteOptions{DryRun: true})
					if err != nil || p.Preview == nil || calls != 0 {
						t.Fatalf("preview=%+v err=%v calls=%d", p, err, calls)
					}
					switch mutation {
					case "group_title", "group_hash", "group_access":
						hash := fingerprintGroupHash(ref)
						if mutation == "group_hash" {
							hash++
						}
						fingerprintChangePeer(t, s, ref, hash, func(p *engine.Peer) {
							if mutation == "group_title" {
								p.Title = "Unrelated title"
							}
							if mutation == "group_access" {
								p.Access = engine.AccessLeft
							}
						})
					case "group_about":
						snapshot.About = "Unrelated about"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "other_role":
						snapshot.Participants[2].Role = "admin"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "self_permission":
						snapshot.AmAdmin = false
						fingerprintSaveSnapshot(t, s, snapshot)
					case "self_role":
						snapshot.Participants[0].Role = "member"
						fingerprintSaveSnapshot(t, s, snapshot)
					case "self_admin_rights", "self_banned_rights":
						field := "AdminRights"
						if mutation == "self_banned_rights" {
							field = "BannedRights"
						}
						fingerprintChangeRight(t, &snapshot.Participants[0], field)
						fingerprintSaveSnapshot(t, s, snapshot)
					case "self_identity":
						e.account.SelfID = 100
					case "group_identity":
						other := ref
						other.ID += 100
						peer, _, err := s.LookupPeer(context.Background(), "primary", ref)
						if err != nil {
							t.Fatal(err)
						}
						peer.Ref = other
						snapshot.Group = other
						if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: peer, AccessHash: fingerprintGroupHash(ref)}}, Participants: []engine.ParticipantsSnapshot{snapshot}}); err != nil {
							t.Fatal(err)
						}
						requestRef = other
					}
					got, err := call(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
					if reject {
						if err == nil || calls != 0 || got.Done || got.Affected != 0 {
							t.Fatalf("stale invite/leave approval: result=%+v err=%v calls=%d", got, err, calls)
						}
						if mutation != "group_access" && !errors.Is(err, engine.ErrPreviewMismatch) {
							t.Fatalf("relevant change must invalidate approval: %v", err)
						}
					} else if err != nil || !got.Done || got.Affected != 1 || calls != 1 {
						t.Fatalf("unrelated invite/leave change rejected: result=%+v err=%v calls=%d", got, err, calls)
					}
				})
			}
		}
	}
}
