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

// The successful full-channel result and any earlier pages precede stronger,
// definitive access evidence. None of that stale metadata may be committed.
func TestParticipantsAccessLossDiscardsFullSnapshot(t *testing.T) {
	for _, code := range []string{"CHANNEL_PRIVATE", "CHAT_FORBIDDEN", "USER_BANNED_IN_CHANNEL"} {
		for _, pageBeforeError := range []bool{false, true} {
			for _, failCommit := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/earlier_page=%v/fail_commit=%v", code, pageBeforeError, failCommit), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					path := metadataAccessPaths()[1] // refresh_channel
					armed := false
					diskErr := errors.New("synthetic participants lifecycle commit failure")
					e, st := seedMetadataAccess(t, path, func(stage string) error {
						if armed && failCommit && stage == "apply_before_commit" {
							return diskErr
						}
						return nil
					})
					if _, err := st.Apply(ctx, engine.Batch{Account: "primary", Participants: []engine.ParticipantsSnapshot{{
						Group: path.peer, About: "retained about", Size: 1, At: time.Unix(2000, 0),
						Participants: []engine.Participant{groupMember(12, "member", 1000)},
					}}}); err != nil {
						t.Fatal(err)
					}
					before, err := st.ReadGroup(ctx, "primary", path.peer)
					if err != nil {
						t.Fatal(err)
					}
					fullCalls, memberCalls := 0, 0
					e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
						switch r := in.(type) {
						case *tg.ChannelsGetFullChannelRequest:
							fullCalls++
							*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
								FullChat: &tg.ChannelFull{ID: path.peer.ID, About: "stale about", ParticipantsCount: 3},
								Chats:    []tg.ChatClass{&tg.Channel{ID: path.peer.ID, Title: "stale title", Megagroup: true, AccessHash: 999, Creator: true}},
								Users:    []tg.UserClass{&tg.User{ID: 998, FirstName: "stale full user"}},
							}
							return nil
						case *tg.ChannelsGetParticipantsRequest:
							memberCalls++
							if pageBeforeError && r.Offset == 0 {
								out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{
									Count: 3, Participants: []tg.ChannelParticipantClass{&tg.ChannelParticipant{UserID: 997}},
									Users: []tg.UserClass{&tg.User{ID: 997, FirstName: "stale page user"}},
								}
								return nil
							}
							return tgerr.New(400, code)
						default:
							t.Fatalf("unexpected RPC: %T", in)
							return nil
						}
					}))
					events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
					armed = true
					got, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: path.peer})
					wantAccess := engine.AccessLost
					if code == "USER_BANNED_IN_CHANNEL" {
						wantAccess = engine.AccessBanned
					}
					if failCommit {
						if !errors.Is(err, diskErr) || !e.held.Load() {
							t.Errorf("persistence failure not propagated/fenced: err=%v held=%v", err, e.held.Load())
						}
						wantAccess = engine.AccessOK
					} else {
						var rpc *engine.RPCError
						if !errors.Is(err, engine.ErrNoAccess) || !errors.As(err, &rpc) || rpc.Type != code || rpc.Method != "channels.getParticipants" {
							t.Errorf("definitive participant failure not returned: %v", err)
						}
					}
					if !reflect.DeepEqual(got, engine.Group{}) {
						t.Errorf("stale successful full-channel response escaped: %+v", got)
					}
					wantCalls := 1
					if pageBeforeError {
						wantCalls++
					}
					if fullCalls != 1 || memberCalls != wantCalls {
						t.Errorf("unexpected calls: full=%d members=%d wantMembers=%d", fullCalls, memberCalls, wantCalls)
					}
					assertMetadataAccessState(t, st, path.peer, !failCommit, wantAccess)
					after, err := st.ReadGroup(ctx, "primary", path.peer)
					if err != nil {
						t.Fatal(err)
					}
					before.Peer.Access = wantAccess
					if !reflect.DeepEqual(before, after) {
						t.Errorf("stale snapshot mutated retained group: before=%+v after=%+v", before, after)
					}
					for _, id := range []int64{997, 998} {
						if _, found, err := st.LookupPeer(ctx, "primary", engine.PeerRef{Type: engine.PeerUser, ID: id}); err != nil || found {
							t.Errorf("stale full/page peer persisted: id=%d found=%v err=%v", id, found, err)
						}
					}
					committed := drainMetadataEvents(events)
					counts := map[engine.EventKind]int{}
					for _, event := range committed {
						counts[event.Kind]++
						if event.Kind == engine.EventAccess && (event.Access == nil || event.Access.State != wantAccess || event.Access.Reason != code) {
							t.Errorf("invalid access event: %+v", event)
						}
					}
					if failCommit {
						if len(committed) != 0 {
							t.Errorf("events escaped rollback: %+v", committed)
						}
					} else if !reflect.DeepEqual(counts, map[engine.EventKind]int{engine.EventAccess: 1, engine.EventCoverage: 1}) {
						t.Errorf("missing or extra lifecycle events: %+v", committed)
					}
				})
			}
		}
	}
}

func TestAccessEventPropagatesBatchCatchUp(t *testing.T) {
	for _, catchUp := range []bool{false, true} {
		t.Run(fmt.Sprint(catchUp), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := metadataAccessPaths()[1]
			e, st := seedMetadataAccess(t, path, func(string) error { return nil })
			events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 4, Kinds: []engine.EventKind{engine.EventAccess}})
			if _, err := e.apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceDifference, CatchUp: catchUp,
				Access: []engine.AccessChange{{Peer: path.peer, State: engine.AccessLost, Reason: "CHANNEL_PRIVATE"}},
			}); err != nil {
				t.Fatal(err)
			}
			assertMetadataAccessState(t, st, path.peer, true)
			got := drainMetadataEvents(events)
			if len(got) != 1 {
				t.Fatalf("expected one committed access event: %+v", got)
			}
			if got[0].CatchUp != catchUp || got[0].Source != engine.SourceDifference {
				t.Fatalf("access event lost batch provenance: %+v wantCatchUp=%v", got[0], catchUp)
			}
		})
	}
}
