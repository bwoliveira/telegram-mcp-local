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

type metadataAccessPath struct {
	name string
	peer engine.PeerRef
	run  func(context.Context, *Engine, engine.PeerRef) error
}

func metadataAccessPaths() []metadataAccessPath {
	channel := engine.PeerRef{Type: engine.PeerChannel, ID: 80}
	chat := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	refresh := func(ctx context.Context, e *Engine, p engine.PeerRef) error {
		_, err := e.RefreshGroup(ctx, engine.GroupRequest{Group: p})
		return err
	}
	invite := func(ctx context.Context, e *Engine, p engine.PeerRef) error {
		_, err := e.InviteLink(ctx, engine.InviteLinkRequest{Group: p})
		return err
	}
	return []metadataAccessPath{
		{"migration", channel, func(ctx context.Context, e *Engine, _ engine.PeerRef) error {
			return e.discoverLegacyMigrations(ctx, nil)
		}},
		{"refresh_channel", channel, refresh}, {"refresh_chat", chat, refresh},
		{"invite_channel", channel, invite}, {"invite_chat", chat, invite},
	}
}

func seedMetadataAccess(t *testing.T, path metadataAccessPath, fault func(string) error) (*Engine, *store.Store) {
	t.Helper()
	ctx := context.Background()
	e, _ := offlineEngine(t)
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "metadata.db"), "primary", store.FaultAt(fault))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.BindSelf(ctx, "primary", e.account); err != nil {
		t.Fatal(err)
	}
	e.cfg.Store = s
	e.cfg.Clock = func() time.Time { return time.Unix(3000, 0) }
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	next := engine.PeerRef{Type: engine.PeerChannel, ID: 80}
	cursor := engine.Cursor{Peer: path.peer, State: "running", OldestID: 7, NewestID: 9}
	seed := engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{
		&tg.Chat{ID: old.ID, Title: "Legacy"}, &tg.Channel{ID: next.ID, Title: "Successor", Megagroup: true, AccessHash: 98},
	}), Dialogs: []engine.Dialog{{Peer: old}, {Peer: next}}, Cursor: &cursor}
	for _, p := range []engine.PeerRef{old, next} {
		seed.Messages = append(seed.Messages, engine.Message{Key: engine.MessageKey{Account: "primary", Peer: p, ID: 7}, Text: "retained metadata history", Date: time.Unix(1000, 0)})
	}
	if _, err := s.Apply(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(ctx, "primary", 80, 41); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(ctx, "primary", 81, 55); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUpdatesState(ctx, "primary", engine.UpdatesState{Pts: 60, Date: 2000, Seq: 9}); err != nil {
		t.Fatal(err)
	}
	return e, s
}

func metadataErrorInvoker(t *testing.T, p engine.PeerRef, rpcErr error, calls *int) *tg.Client {
	t.Helper()
	return tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		*calls++
		switch r := in.(type) {
		case *tg.ChannelsGetFullChannelRequest:
			c, ok := r.Channel.(*tg.InputChannel)
			if p.Type != engine.PeerChannel || !ok || c.ChannelID != p.ID || c.AccessHash != 98 {
				t.Fatalf("wrong channel metadata request: %+v", r)
			}
		case *tg.MessagesGetFullChatRequest:
			if p.Type != engine.PeerChat || r.ChatID != p.ID {
				t.Fatalf("wrong basic-group metadata request: %+v", r)
			}
		default:
			t.Fatalf("unexpected RPC, including history/read receipts/writes: %T", in)
		}
		return rpcErr
	}))
}

func assertMetadataHistoryAndUnrelatedState(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, p := range []engine.PeerRef{{Type: engine.PeerChat, ID: 71}, {Type: engine.PeerChannel, ID: 80}} {
		m, ok, err := s.Message(ctx, engine.MessageKey{Account: "primary", Peer: p, ID: 7})
		if err != nil || !ok || m.Text != "retained metadata history" {
			t.Fatalf("history lost for %v: %+v %v %v", p, m, ok, err)
		}
	}
	pts, ok, err := s.LoadChannelPts(ctx, "primary", 81)
	if err != nil || !ok || pts != 55 {
		t.Fatalf("unrelated channel checkpoint changed: %d %v %v", pts, ok, err)
	}
	state, ok, err := s.LoadUpdatesState(ctx, "primary")
	if err != nil || !ok || state != (engine.UpdatesState{Pts: 60, Date: 2000, Seq: 9}) {
		t.Fatalf("global checkpoint changed: %+v %v %v", state, ok, err)
	}
}

func assertMetadataAccessState(t *testing.T, s *store.Store, p engine.PeerRef, lost bool, state ...engine.AccessState) {
	t.Helper()
	ctx := context.Background()
	wantAccess := engine.AccessOK
	if lost {
		wantAccess = engine.AccessLost
	}
	if len(state) > 0 {
		wantAccess = state[0]
	}
	wantCursor, wantGaps := "running", 0
	if lost {
		wantCursor, wantGaps = "no_access", 1
	}
	peer, ok, err := s.LookupPeer(ctx, "primary", p)
	if err != nil || !ok || peer.Access != wantAccess {
		t.Errorf("metadata access transition: %+v exists=%v err=%v want=%s", peer, ok, err, wantAccess)
	}
	cursor, ok, err := s.Cursor(ctx, "primary", p)
	if err != nil || !ok || cursor.State != wantCursor || cursor.Complete {
		t.Errorf("metadata cursor: %+v exists=%v err=%v want=%s", cursor, ok, err, wantCursor)
	}
	gaps, err := s.PendingGaps(ctx, "primary", &p)
	if err != nil || len(gaps) != wantGaps {
		t.Errorf("metadata access gaps: %+v err=%v want=%d", gaps, err, wantGaps)
	}
	if len(gaps) > 0 && gaps[0].Reason != engine.GapAccessLost {
		t.Errorf("wrong gap reason: %+v", gaps)
	}
	pts, exists, err := s.LoadChannelPts(ctx, "primary", 80)
	drop := lost && p.Type == engine.PeerChannel
	if err != nil || exists == drop || (!drop && pts != 41) {
		t.Errorf("metadata checkpoint: pts=%d exists=%v err=%v shouldDrop=%v", pts, exists, err, drop)
	}
	assertMetadataHistoryAndUnrelatedState(t, s)
}

func drainMetadataEvents(ch <-chan engine.Event) []engine.Event {
	var events []engine.Event
	for {
		select {
		case e := <-ch:
			events = append(events, e)
		default:
			return events
		}
	}
}

func TestMetadataDefinitiveAccessLossIsTransactional(t *testing.T) {
	for _, path := range metadataAccessPaths() {
		t.Run(path.name, func(t *testing.T) {
			for _, code := range []string{"CHANNEL_PRIVATE", "CHAT_FORBIDDEN", "USER_BANNED_IN_CHANNEL"} {
				t.Run(code, func(t *testing.T) {
					wantAccess := engine.AccessLost
					if code == "USER_BANNED_IN_CHANNEL" {
						wantAccess = engine.AccessBanned
					}
					e, s := seedMetadataAccess(t, path, func(string) error { return nil })
					calls := 0
					e.api = metadataErrorInvoker(t, path.peer, tgerr.New(400, code), &calls)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
					err := path.run(ctx, e, path.peer)
					if path.name == "migration" {
						if err != nil {
							t.Fatalf("migration must continue after committed loss: %v", err)
						}
					} else if !errors.Is(err, engine.ErrNoAccess) {
						t.Errorf("metadata error not classified no-access: %v", err)
					}
					assertMetadataAccessState(t, s, path.peer, true, wantAccess)
					first := drainMetadataEvents(events)
					counts := map[engine.EventKind]int{}
					for _, ev := range first {
						counts[ev.Kind]++
						if ev.ID == "" || ev.Peer == nil || *ev.Peer != path.peer || ev.Source != engine.SourceRefresh {
							t.Errorf("invalid metadata event: %+v", ev)
						}
						if ev.Kind == engine.EventAccess && (ev.Access == nil || ev.Access.State != wantAccess || ev.Access.Reason != code) {
							t.Errorf("invalid access event: %+v", ev)
						}
					}
					if !reflect.DeepEqual(counts, map[engine.EventKind]int{engine.EventAccess: 1, engine.EventCoverage: 1}) {
						t.Errorf("access/gap events missing or duplicated: %+v", first)
					}
					// A second request (and a fresh discovery generation) cannot create another
					// access episode, gap or event. Readable retained history is unchanged.
					_ = path.run(ctx, e, path.peer)
					assertMetadataAccessState(t, s, path.peer, true, wantAccess)
					if again := drainMetadataEvents(events); len(again) != 0 {
						t.Errorf("repeated access loss emitted duplicate events: %+v", again)
					}
					if path.name == "migration" && calls != 1 {
						t.Errorf("discovery re-probed terminal peer: calls=%d", calls)
					}
				})
			}
		})
	}
}

func TestMetadataAccessLossPersistenceFailurePropagatesAndRollsBack(t *testing.T) {
	for _, path := range metadataAccessPaths() {
		t.Run(path.name, func(t *testing.T) {
			armed := false
			diskErr := errors.New("synthetic metadata commit failure")
			e, s := seedMetadataAccess(t, path, func(stage string) error {
				if armed && stage == "apply_before_commit" {
					return diskErr
				}
				return nil
			})
			calls := 0
			e.api = metadataErrorInvoker(t, path.peer, tgerr.New(400, "CHANNEL_PRIVATE"), &calls)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
			armed = true
			err := path.run(ctx, e, path.peer)
			if !errors.Is(err, diskErr) {
				t.Errorf("access persistence failure swallowed: %v", err)
			}
			if !e.held.Load() {
				t.Error("failed lifecycle commit did not fence checkpoints")
			}
			assertMetadataAccessState(t, s, path.peer, false)
			if got := drainMetadataEvents(events); len(got) != 0 {
				t.Errorf("event escaped rolled-back access loss: %+v", got)
			}
			adapter := stateAdapter{p: s, acc: "primary", e: e}
			if err := adapter.SetChannelPts(ctx, 99, 80, 99); !errors.Is(err, engine.ErrCheckpointHeld) {
				t.Errorf("failed lifecycle checkpoint not held: %v", err)
			}
		})
	}
}

func TestMetadataNonDefinitiveErrorsPreserveAccess(t *testing.T) {
	for _, path := range metadataAccessPaths() {
		t.Run(path.name, func(t *testing.T) {
			for _, code := range []string{"CHANNEL_INVALID", "CHAT_ADMIN_REQUIRED", "INTERNAL", "FLOOD_WAIT_30"} {
				t.Run(code, func(t *testing.T) {
					e, s := seedMetadataAccess(t, path, func(string) error { return nil })
					calls := 0
					e.api = metadataErrorInvoker(t, path.peer, tgerr.New(400, code), &calls)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
					err := path.run(ctx, e, path.peer)
					if path.name != "migration" || code != "CHANNEL_INVALID" {
						if err == nil {
							t.Errorf("non-definitive metadata failure swallowed: %s", code)
						}
					}
					assertMetadataAccessState(t, s, path.peer, false)
					if path.name == "migration" && code == "CHANNEL_INVALID" {
						if err := path.run(ctx, e, path.peer); err != nil || calls != 2 {
							t.Errorf("invalid channel was treated as terminal: err=%v calls=%d", err, calls)
						}
					}
					if e.held.Load() {
						t.Error("non-persistence error held checkpoint")
					}
					if got := drainMetadataEvents(events); len(got) != 0 {
						t.Errorf("non-definitive error emitted lifecycle event: %+v", got)
					}
				})
			}
		})
	}
}
