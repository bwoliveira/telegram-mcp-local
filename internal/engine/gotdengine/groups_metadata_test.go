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
	"github.com/gotd/td/tgerr"
)

func TestGroupsUpdateRPCTypeTable(t *testing.T) {
	for _, ref := range groupsTestPeers {
		t.Run(fmt.Sprint(ref), func(t *testing.T) {
			e, s := writeFixture(t)
			var calls []string
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch r := in.(type) {
				case *tg.MessagesEditChatTitleRequest:
					if ref.Type != engine.PeerChat || r.ChatID != ref.ID || r.Title != "New 😀" {
						t.Fatalf("bad title: %#v", r)
					}
					calls = append(calls, "basic title")
				case *tg.ChannelsEditTitleRequest:
					wantHash := int64(3030)
					if ref.ID == 40 {
						wantHash = 4040
					}
					if ref.Type != engine.PeerChannel || !reflect.DeepEqual(r.Channel, &tg.InputChannel{ChannelID: ref.ID, AccessHash: wantHash}) || r.Title != "New 😀" {
						t.Fatalf("bad title: %#v", r)
					}
					calls = append(calls, "channel title")
				case *tg.MessagesEditChatAboutRequest:
					if r.About != "" {
						t.Fatal("empty about must clear the description")
					}
					if ref.Type == engine.PeerChat {
						if !reflect.DeepEqual(r.Peer, &tg.InputPeerChat{ChatID: ref.ID}) {
							t.Fatalf("bad about peer: %#v", r.Peer)
						}
					} else {
						wantHash := int64(3030)
						if ref.ID == 40 {
							wantHash = 4040
						}
						if !reflect.DeepEqual(r.Peer, &tg.InputPeerChannel{ChannelID: ref.ID, AccessHash: wantHash}) {
							t.Fatalf("bad about peer: %#v", r.Peer)
						}
					}
					calls = append(calls, "about")
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				groupsTestReply(t, out)
				return nil
			}))
			title, about := "New 😀", ""
			r := engine.UpdateGroupRequest{Group: ref, Title: &title, About: &about, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.UpdateGroup(context.Background(), r)
			if err != nil || len(calls) != 0 || p.Preview == nil || p.Preview.Text != "title: New 😀\nabout: " {
				t.Fatalf("preview=%+v err=%v", p, err)
			}
			r.DryRun, r.ExpectFingerprint = false, p.Preview.Fingerprint
			got, err := e.UpdateGroup(context.Background(), r)
			if err != nil || !got.Done || got.Affected != 2 || len(calls) != 2 || calls[1] != "about" {
				t.Fatalf("got=%+v err=%v calls=%v", got, err, calls)
			}
			peer, _, err := s.LookupPeer(context.Background(), "primary", ref)
			if err != nil || peer.Title == title {
				t.Fatal("RPC acceptance fabricated a local title instead of awaiting updates")
			}
		})
	}
}

func TestGroupsUpdatePartialFailure(t *testing.T) {
	e, _ := writeFixture(t)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		if _, ok := in.(*tg.MessagesEditChatAboutRequest); ok {
			return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
		}
		groupsTestReply(t, out)
		return nil
	}))
	title, about := "New", "About"
	r := engine.UpdateGroupRequest{Group: groupsTestPeers[0], Title: &title, About: &about, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.UpdateGroup(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun, r.ExpectFingerprint = false, p.Preview.Fingerprint
	got, err := e.UpdateGroup(context.Background(), r)
	var rpc *engine.RPCError
	if got.Done || got.Affected != 1 || calls != 2 || !errors.Is(err, engine.ErrPermission) || !errors.As(err, &rpc) || rpc.Method != "messages.editChatAbout" {
		t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
	}
}

func TestGroupsInviteReadNeverExports(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, missing := range []bool{false, true} {
			for _, dry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%v/missing=%v/dry=%v", ref, missing, dry), func(t *testing.T) {
					e, _ := writeFixture(t)
					if err := e.SetMode(context.Background(), engine.ModeReadOnly); err != nil {
						t.Fatal(err)
					}
					calls := 0
					e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
						calls++
						var invite tg.ExportedChatInviteClass = &tg.ChatInviteExported{Link: "https://telegram.invalid/+existing", RequestNeeded: true, ExpireDate: 12345}
						if missing {
							invite = nil
						}
						switch r := in.(type) {
						case *tg.MessagesGetFullChatRequest:
							if ref.Type != engine.PeerChat || r.ChatID != ref.ID {
								t.Fatalf("bad getFullChat: %#v", r)
							}
							*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: ref.ID, ExportedInvite: invite}}
						case *tg.ChannelsGetFullChannelRequest:
							wantHash := int64(3030)
							if ref.ID == 40 {
								wantHash = 4040
							}
							if ref.Type != engine.PeerChannel || !reflect.DeepEqual(r.Channel, &tg.InputChannel{ChannelID: ref.ID, AccessHash: wantHash}) {
								t.Fatalf("bad getFullChannel: %#v", r)
							}
							*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: ref.ID, ExportedInvite: invite}}
						default:
							t.Fatalf("forbidden RPC; reading must never export/revoke or send receipts: %T", in)
						}
						return nil
					}))
					got, err := e.InviteLink(context.Background(), engine.InviteLinkRequest{Group: ref, WriteOptions: engine.WriteOptions{DryRun: dry}})
					if err != nil || calls != 1 || got.DryRun || got.Done || got.Preview != nil {
						t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
					}
					if missing {
						if got.Link != "" || got.Note == "" {
							t.Fatalf("missing link: %+v", got)
						}
					} else if got.Link != "https://telegram.invalid/+existing" || !got.RequiresApproval || got.ExpiresAt == nil || !got.ExpiresAt.Equal(time.Unix(12345, 0)) {
						t.Fatalf("link metadata: %+v", got)
					}
				})
			}
		}
	}
}

func TestGroupsInviteRevokeAndLeaveRPCTypeTable(t *testing.T) {
	for _, ref := range groupsTestPeers {
		for _, action := range []string{"invite_link", "leave"} {
			t.Run(fmt.Sprintf("%v/%s", ref, action), func(t *testing.T) {
				e, _ := writeFixture(t)
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					switch r := in.(type) {
					case *tg.MessagesExportChatInviteRequest:
						if action != "invite_link" || !r.LegacyRevokePermanent || r.RequestNeeded || r.ExpireDate != 0 || r.UsageLimit != 0 {
							t.Fatalf("bad export/revoke: %#v", r)
						}
						if ref.Type == engine.PeerChat {
							if !reflect.DeepEqual(r.Peer, &tg.InputPeerChat{ChatID: ref.ID}) {
								t.Fatalf("wrong peer %#v", r.Peer)
							}
						} else {
							wantHash := int64(3030)
							if ref.ID == 40 {
								wantHash = 4040
							}
							if !reflect.DeepEqual(r.Peer, &tg.InputPeerChannel{ChannelID: ref.ID, AccessHash: wantHash}) {
								t.Fatalf("wrong peer %#v", r.Peer)
							}
						}
					case *tg.MessagesDeleteChatUserRequest:
						if action != "leave" || ref.Type != engine.PeerChat || r.ChatID != ref.ID || r.RevokeHistory {
							t.Fatalf("bad leave: %#v", r)
						}
						if _, ok := r.UserID.(*tg.InputUserSelf); !ok {
							t.Fatalf("not self: %#v", r.UserID)
						}
					case *tg.ChannelsLeaveChannelRequest:
						wantHash := int64(3030)
						if ref.ID == 40 {
							wantHash = 4040
						}
						if action != "leave" || ref.Type != engine.PeerChannel || !reflect.DeepEqual(r.Channel, &tg.InputChannel{ChannelID: ref.ID, AccessHash: wantHash}) {
							t.Fatalf("bad channel leave: %#v", r)
						}
					default:
						t.Fatalf("unexpected RPC %T", in)
					}
					groupsTestReply(t, out)
					return nil
				}))
				call := func(opts engine.WriteOptions) (engine.WriteResult, error) {
					if action == "leave" {
						return e.Leave(context.Background(), engine.LeaveRequest{Group: ref, WriteOptions: opts})
					}
					got, err := e.InviteLink(context.Background(), engine.InviteLinkRequest{Group: ref, Revoke: true, WriteOptions: opts})
					if err == nil && !opts.DryRun && (!got.Revoked || got.Link != "https://telegram.invalid/+replacement") {
						t.Fatalf("revoke result=%+v", got)
					}
					return got.WriteResult, err
				}
				p, err := call(engine.WriteOptions{DryRun: true})
				if err != nil || p.Preview == nil || p.Preview.Reversible || calls != 0 {
					t.Fatalf("preview=%+v err=%v calls=%d", p, err, calls)
				}
				got, err := call(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
				if err != nil || !got.Done || got.Affected != 1 || calls != 1 {
					t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
				}
			})
		}
	}
}
