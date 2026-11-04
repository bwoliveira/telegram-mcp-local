package gotdengine

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func groupRefreshStoreEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "groups.db"), "primary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Store: st, Session: &memorySession{}})
	if err != nil {
		t.Fatal(err)
	}
	e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
	if err := st.BindSelf(ctx, "primary", e.account); err != nil {
		t.Fatal(err)
	}
	return e, st
}

func TestRefreshGroupChannelResponseLimitDoesNotBoundCollection(t *testing.T) {
	ctx := context.Background()
	e, st := groupRefreshStoreEngine(t)
	ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
	if _, err := st.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: engine.KindSupergroup}, AccessHash: 55}}}); err != nil {
		t.Fatal(err)
	}
	var offsets []int
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ChannelsGetFullChannelRequest:
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
				FullChat: &tg.ChannelFull{ID: ref.ID, ParticipantsCount: 3},
				Chats:    []tg.ChatClass{&tg.Channel{ID: ref.ID, Title: "Super", Megagroup: true, AccessHash: 55}},
			}
		case *tg.ChannelsGetParticipantsRequest:
			offsets = append(offsets, r.Offset)
			if _, ok := r.Filter.(*tg.ChannelParticipantsRecent); !ok {
				t.Fatalf("unexpected unfiltered request filter %T", r.Filter)
			}
			page := &tg.ChannelsChannelParticipants{Count: 3}
			end := min(3, r.Offset+min(2, r.Limit))
			for i := r.Offset; i < end; i++ {
				id := int64(10 + i)
				page.Participants = append(page.Participants, &tg.ChannelParticipant{UserID: id})
				page.Users = append(page.Users, &tg.User{ID: id, FirstName: "Member"})
			}
			out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = page
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))
	g, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Participants) != 1 || !g.Partial {
		t.Errorf("response limit not applied: %+v", g)
	}
	cached, err := st.ReadGroup(ctx, "primary", ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(cached.Participants) != 3 || cached.Partial || len(offsets) != 2 || offsets[1] != 2 {
		t.Errorf("response limit bounded collection: cached=%+v offsets=%v", cached, offsets)
	}
}

func TestRefreshGroupChannelCollectionHasIndependentCap(t *testing.T) {
	ctx := context.Background()
	e, st := groupRefreshStoreEngine(t)
	ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
	if _, err := st.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: engine.KindSupergroup}, AccessHash: 55}}}); err != nil {
		t.Fatal(err)
	}
	collected := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ChannelsGetFullChannelRequest:
			*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
				FullChat: &tg.ChannelFull{ID: ref.ID, ParticipantsCount: 10001},
				Chats:    []tg.ChatClass{&tg.Channel{ID: ref.ID, Megagroup: true, AccessHash: 55}},
			}
		case *tg.ChannelsGetParticipantsRequest:
			if r.Offset != collected || r.Limit <= 0 || r.Limit > 200 || r.Offset+r.Limit > 10000 {
				t.Fatalf("invalid bounded collection request: offset=%d limit=%d collected=%d", r.Offset, r.Limit, collected)
			}
			page := &tg.ChannelsChannelParticipants{Count: 10001}
			for i := 0; i < r.Limit; i++ {
				page.Participants = append(page.Participants, &tg.ChannelParticipant{UserID: int64(1000 + r.Offset + i)})
			}
			collected += r.Limit
			out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = page
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))
	g, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := st.ReadGroup(ctx, "primary", ref)
	if err != nil {
		t.Fatal(err)
	}
	if collected != 10000 || len(cached.Participants) != 10000 || !cached.Partial || cached.Size != 10001 || len(g.Participants) != 1 || !g.Partial {
		t.Fatalf("collection cap or response limit lost: collected=%d cached=%d cached_partial=%v size=%d response=%d response_partial=%v", collected, len(cached.Participants), cached.Partial, cached.Size, len(g.Participants), g.Partial)
	}
}

func TestRefreshGroupChannelPartialCollectionPreservesCompleteCache(t *testing.T) {
	for _, mode := range []string{"collection_cap", "hidden", "admin_required"} {
		for _, existing := range []bool{false, true} {
			state := "initial_snapshot"
			if existing {
				state = "complete_snapshot"
			}
			t.Run(mode+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				e, st := groupRefreshStoreEngine(t)
				ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
				size := 2
				if mode == "collection_cap" {
					size = 10001
				}
				seed := engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: engine.KindSupergroup}, AccessHash: 55}}}
				if existing {
					owner := engine.PeerRef{Type: engine.PeerUser, ID: 1000}
					linked := engine.PeerRef{Type: engine.PeerChannel, ID: 31}
					created := time.Unix(100, 0).UTC()
					snapshot := engine.ParticipantsSnapshot{Group: ref, About: "Retained about", Size: size, At: time.Unix(200, 0).UTC(), Owner: &owner, CreatedAt: &created, LinkedChat: &linked, AmAdmin: true}
					for i := 0; i < size; i++ {
						snapshot.Participants = append(snapshot.Participants, groupMember(int64(1000+i), "admin", 150))
					}
					seed.Participants = []engine.ParticipantsSnapshot{snapshot}
				}
				if _, err := st.Apply(ctx, seed); err != nil {
					t.Fatal(err)
				}
				before, beforeErr := st.ReadGroup(ctx, "primary", ref)
				if existing && (beforeErr != nil || before.Partial || len(before.Participants) != size) {
					t.Fatalf("invalid complete seed: count=%d partial=%v err=%v", len(before.Participants), before.Partial, beforeErr)
				}
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					switch r := in.(type) {
					case *tg.ChannelsGetFullChannelRequest:
						*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
							FullChat: &tg.ChannelFull{ID: ref.ID, About: "Live about", ParticipantsCount: size, ParticipantsHidden: mode == "hidden"},
							Chats:    []tg.ChatClass{&tg.Channel{ID: ref.ID, Megagroup: true, AccessHash: 55}},
						}
					case *tg.ChannelsGetParticipantsRequest:
						calls++
						if _, ok := r.Filter.(*tg.ChannelParticipantsRecent); !ok {
							t.Fatalf("unexpected unfiltered request filter %T", r.Filter)
						}
						if mode == "admin_required" {
							return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
						}
						page := &tg.ChannelsChannelParticipants{Count: size}
						end := min(size, r.Offset+r.Limit)
						if mode == "hidden" {
							end = min(1, end)
						}
						for i := r.Offset; i < end; i++ {
							page.Participants = append(page.Participants, &tg.ChannelParticipant{UserID: int64(1000 + i)})
						}
						out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = page
					default:
						t.Fatalf("unexpected RPC %T", in)
					}
					return nil
				}))
				g, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref, Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				if !g.Partial || g.About != "Live about" || g.Size != size || len(g.Participants) > 1 || calls == 0 {
					t.Fatalf("live response must remain partial: count=%d partial=%v about=%q size=%d calls=%d", len(g.Participants), g.Partial, g.About, g.Size, calls)
				}
				after, err := st.ReadGroup(ctx, "primary", ref)
				if err != nil {
					t.Fatal(err)
				}
				if existing {
					// Peer discovery and explicitly known actor permissions can advance
					// independently; collection metadata and members stay complete.
					before.Peer = after.Peer
					before.AmAdmin = false // Full channel metadata explicitly has no self admin rights.
					if !reflect.DeepEqual(before, after) {
						t.Fatalf("partial unfiltered RPC replaced complete snapshot: before_count=%d after_count=%d before_partial=%v after_partial=%v before_at=%v after_at=%v before_about=%q after_about=%q", len(before.Participants), len(after.Participants), before.Partial, after.Partial, before.SnapshotAt, after.SnapshotAt, before.About, after.About)
					}
					return
				}
				wantCount := 1
				if mode == "collection_cap" {
					wantCount = 10000
				} else if mode == "admin_required" {
					wantCount = 0
				}
				if !after.Partial || after.Size != size || len(after.Participants) != wantCount || after.About != "Live about" {
					t.Fatalf("initial partial snapshot not retained: count=%d want=%d partial=%v size=%d about=%q", len(after.Participants), wantCount, after.Partial, after.Size, after.About)
				}
			})
		}
	}
}

func TestRefreshGroupChannelSearchUsesServerFilterAndPreservesCache(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "without_snapshot"
		if existing {
			name = "complete_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e, st := groupRefreshStoreEngine(t)
			ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
			seed := engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: engine.KindSupergroup}, AccessHash: 55}}}
			if existing {
				seed.Participants = []engine.ParticipantsSnapshot{{Group: ref, About: "Retained about", Size: 2, At: e.now(), Participants: []engine.Participant{
					{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Name: "Member", Role: "member"},
					{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 11}, Name: "Other", Role: "member"},
				}}}
			}
			if _, err := st.Apply(ctx, seed); err != nil {
				t.Fatal(err)
			}
			before, beforeErr := st.ReadGroup(ctx, "primary", ref)
			if beforeErr != nil && (existing || !errors.Is(beforeErr, engine.ErrPeerNotFound)) {
				t.Fatal(beforeErr)
			}
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch r := in.(type) {
				case *tg.ChannelsGetFullChannelRequest:
					*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
						FullChat: &tg.ChannelFull{ID: ref.ID, About: "Live about", ParticipantsCount: 2},
						Chats:    []tg.ChatClass{&tg.Channel{ID: ref.ID, Megagroup: true, AccessHash: 55}},
					}
				case *tg.ChannelsGetParticipantsRequest:
					filter, ok := r.Filter.(*tg.ChannelParticipantsSearch)
					if !ok || filter.Q != "member" {
						t.Errorf("search requires server-side search filter: %#v", r.Filter)
					}
					if r.Offset != 0 {
						t.Fatalf("unexpected search offset %d", r.Offset)
					}
					out.(*tg.ChannelsChannelParticipantsBox).ChannelParticipants = &tg.ChannelsChannelParticipants{
						Count: 1, Participants: []tg.ChannelParticipantClass{&tg.ChannelParticipant{UserID: 10}},
						Users: []tg.UserClass{&tg.User{ID: 10, FirstName: "Member"}},
					}
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				return nil
			}))
			g, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref, Search: "member", Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			if !g.Partial || len(g.Participants) != 1 || g.Participants[0].Peer.ID != 10 || g.Size != 2 {
				t.Errorf("wrong filtered response: %+v", g)
			}
			after, err := st.ReadGroup(ctx, "primary", ref)
			if beforeErr != nil {
				if !errors.Is(err, beforeErr) {
					t.Errorf("search created membership snapshot: after=%+v err=%v", after, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Participants, after.Participants) || before.Partial != after.Partial || !before.SnapshotAt.Equal(after.SnapshotAt) || before.About != after.About {
				t.Errorf("search replaced membership snapshot: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestRefreshGroupBasicSearchLimitPreservesCompleteCache(t *testing.T) {
	ctx := context.Background()
	e, st := groupRefreshStoreEngine(t)
	ref := engine.PeerRef{Type: engine.PeerChat, ID: 20}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetFullChatRequest); !ok {
			t.Fatalf("unexpected RPC %T", in)
		}
		*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{
			FullChat: &tg.ChatFull{ID: ref.ID, Participants: &tg.ChatParticipants{ChatID: ref.ID, Participants: []tg.ChatParticipantClass{
				&tg.ChatParticipantCreator{UserID: 99}, &tg.ChatParticipant{UserID: 10}, &tg.ChatParticipant{UserID: 11},
			}}},
			Chats: []tg.ChatClass{&tg.Chat{ID: ref.ID, Title: "Basic", ParticipantsCount: 3}},
			Users: []tg.UserClass{&tg.User{ID: 99, FirstName: "Owner"}, &tg.User{ID: 10, FirstName: "Match first"}, &tg.User{ID: 11, FirstName: "Match second"}},
		}
		return nil
	}))
	g, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: ref, Search: "match", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Participants) != 1 || g.Participants[0].Peer.ID != 10 || !g.Partial {
		t.Errorf("search must precede response limit: %+v", g)
	}
	cached, err := st.ReadGroup(ctx, "primary", ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(cached.Participants) != 3 || cached.Partial || cached.Size != 3 || cached.Owner == nil || cached.Owner.ID != 99 {
		t.Errorf("response search/limit damaged complete cache: %+v", cached)
	}
}
