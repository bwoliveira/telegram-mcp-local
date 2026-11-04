package fake_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

var _ localReadStore = (*fake.Controller)(nil)

type localReadStore interface {
	ReadPeers(context.Context, engine.AccountID) ([]engine.Peer, error)
	ReadDialogs(context.Context, engine.AccountID) ([]engine.Dialog, error)
	ReadMessages(context.Context, engine.AccountID) ([]engine.Message, error)
	ReadGroup(context.Context, engine.AccountID, engine.PeerRef) (engine.Group, error)
	ReadCoverage(context.Context, engine.AccountID, *engine.PeerRef) (bool, *time.Time, error)
}

func TestLocalReadsAreOfflineDetachedSnapshots(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	r, ok := any(f).(localReadStore)
	if !ok {
		t.Fatal("fake does not implement the local read seam")
	}
	ctx := context.Background()
	if err := f.Disconnect(ctx, "offline test"); err != nil {
		t.Fatal(err)
	}
	if err := f.LogOut(ctx); err != nil {
		t.Fatal(err)
	}
	before := f.Snapshot()
	peers, err := r.ReadPeers(ctx, "test")
	if err != nil || !reflect.DeepEqual(peers, before.Peers) {
		t.Fatalf("peers = %+v, %v", peers, err)
	}
	dialogs, err := r.ReadDialogs(ctx, "test")
	if err != nil || !reflect.DeepEqual(dialogs, before.Dialogs) {
		t.Fatalf("dialogs = %+v, %v", dialogs, err)
	}
	messages, err := r.ReadMessages(ctx, "test")
	if err != nil || !reflect.DeepEqual(messages, before.Messages) {
		t.Fatalf("messages = %+v, %v", messages, err)
	}
	for _, g := range before.Groups {
		got, err := r.ReadGroup(ctx, "test", g.Peer.Ref)
		if err != nil || !reflect.DeepEqual(got, g) {
			t.Fatalf("group = %+v, %v", got, err)
		}
		got.Participants[0].Name = "mutated"
		*got.Owner = engine.PeerRef{}
	}
	peers[0].FirstName = "mutated"
	messages[0].Text = "mutated"
	*messages[0].Sender = engine.PeerRef{}
	dialogs[0].UnreadCount = -100
	if !reflect.DeepEqual(f.Snapshot(), before) {
		t.Fatal("read snapshot aliases fake state")
	}
	if len(f.Writes()) != 0 {
		t.Fatal("local reads performed writes")
	}
	for _, call := range []func(context.Context, engine.AccountID) error{
		func(ctx context.Context, a engine.AccountID) error { _, e := r.ReadPeers(ctx, a); return e },
		func(ctx context.Context, a engine.AccountID) error { _, e := r.ReadDialogs(ctx, a); return e },
		func(ctx context.Context, a engine.AccountID) error { _, e := r.ReadMessages(ctx, a); return e },
		func(ctx context.Context, a engine.AccountID) error {
			_, e := r.ReadGroup(ctx, a, before.Groups[0].Peer.Ref)
			return e
		},
		func(ctx context.Context, a engine.AccountID) error { _, _, e := r.ReadCoverage(ctx, a, nil); return e },
	} {
		for _, account := range []engine.AccountID{"", "other"} {
			if e := call(ctx, account); !errors.Is(e, engine.ErrAccountMismatch) {
				t.Fatalf("account %q: %v", account, e)
			}
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if e := call(cancelled, "test"); !errors.Is(e, context.Canceled) {
			t.Fatalf("cancel: %v", e)
		}
	}
}

func TestLocalReadsTTLDeletesAndLostAccess(t *testing.T) {
	now := fake.Epoch()
	f := newFake(t, engine.Config{Account: "test", Clock: func() time.Time { return now }})
	ctx := context.Background()
	until := now.Add(time.Second)
	peer := fake.PrivatePeer
	messages := []engine.Message{
		{Key: key(peer, 100), Date: now, Text: "expires explicitly", ExpiresAt: &until},
		{Key: key(peer, 101), Date: now, Text: "expires by period", TTLPeriod: 1},
		{Key: key(peer, 102), Date: now, Text: "deleted"},
		{Key: key(fake.ChannelPeer, 102), Date: now, Text: "same id in another namespace"},
	}
	if err := f.Publish(ctx, engine.Batch{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if err := f.Publish(ctx, engine.Batch{Deletes: []engine.Deletion{{Peer: &peer, IDs: []int{102}}}}); err != nil {
		t.Fatal(err)
	}
	now = until // Expiration is inclusive; no janitor or Publish after advancing.
	out, err := f.ReadMessages(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	channelFound := false
	for _, m := range out {
		if m.Key.Account != "test" {
			t.Fatalf("cross-account message: %+v", m.Key)
		}
		if m.Key.Peer == peer && m.Key.ID >= 100 {
			t.Fatalf("expired/deleted content: %+v", m)
		}
		if m.Key == key(fake.ChannelPeer, 102) {
			channelFound = true
		}
	}
	if !channelFound {
		t.Fatal("channel namespace was affected by private delete")
	}
	// All seen peers, including a noncontact with no dialog, must remain visible.
	seen := engine.PeerRef{Type: engine.PeerUser, ID: 9999}
	if err := f.Publish(ctx, engine.Batch{
		Peers:  []engine.PeerUpsert{{Peer: engine.Peer{Ref: seen, Kind: engine.KindUser, FirstName: "Only seen"}}},
		Access: []engine.AccessChange{{Peer: fake.BasicGroupPeer, State: engine.AccessLost}},
	}); err != nil {
		t.Fatal(err)
	}
	peers, err := f.ReadPeers(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range peers {
		if p.Ref == seen {
			found = true
		}
	}
	if !found {
		t.Fatal("seen noncontact missing")
	}
	g, err := f.ReadGroup(ctx, "test", fake.BasicGroupPeer)
	if err != nil || g.Peer.Access != engine.AccessLost || !g.SnapshotAt.Equal(fake.Epoch()) {
		t.Fatalf("retained group: %+v, %v", g, err)
	}
	if _, err := f.ReadGroup(ctx, "test", fake.PrivatePeer); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("user as group: %v", err)
	}
	if _, err := f.ReadGroup(ctx, "test", engine.PeerRef{Type: engine.PeerChat, ID: 9999}); !errors.Is(err, engine.ErrPeerNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if _, err := f.ReadGroup(ctx, "test", engine.PeerRef{Type: "bogus", ID: 1}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("invalid group: %v", err)
	}
}

func TestLocalCoverageNeverInventsCompleteness(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx := context.Background()
	peer := fake.PrivatePeer
	complete, oldest, err := f.ReadCoverage(ctx, "test", &peer)
	if err != nil || complete || oldest != nil {
		t.Fatalf("missing cursor: %v, %v, %v", complete, oldest, err)
	}
	since := fake.Epoch().Add(-time.Hour)
	cursor := engine.Cursor{Peer: peer, Complete: true, State: "idle", OldestDate: &since}
	if err := f.Publish(ctx, engine.Batch{Cursor: &cursor}); err != nil {
		t.Fatal(err)
	}
	complete, oldest, err = f.ReadCoverage(ctx, "test", &peer)
	if err != nil || complete || oldest == nil || !oldest.Equal(since) {
		t.Fatalf("unproven cursor coverage: %v, %v, %v", complete, oldest, err)
	}
	*oldest = time.Time{}
	_, oldest, _ = f.ReadCoverage(ctx, "test", &peer)
	if oldest == nil || !oldest.Equal(since) {
		t.Fatal("cursor aliases returned boundary")
	}
	for _, scope := range []*engine.PeerRef{nil, &peer} {
		if err := f.Publish(ctx, engine.Batch{Gaps: []engine.GapMark{{Peer: scope, Reason: engine.GapStateLost}}}); err != nil {
			t.Fatal(err)
		}
		complete, _, err := f.ReadCoverage(ctx, "test", scope)
		if err != nil || complete {
			t.Fatalf("gap coverage: %v, %v", complete, err)
		}
	}
	complete, oldest, err = f.ReadCoverage(ctx, "test", nil)
	if err != nil || complete || oldest != nil {
		t.Fatalf("global missing cursor: %v, %v, %v", complete, oldest, err)
	}
}

func TestLocalReadsConcurrentPublish(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 20; j++ {
				if _, e := f.ReadPeers(ctx, "test"); e != nil {
					t.Error(e)
				}
				if _, e := f.ReadDialogs(ctx, "test"); e != nil {
					t.Error(e)
				}
				if _, e := f.ReadMessages(ctx, "test"); e != nil {
					t.Error(e)
				}
				if _, e := f.ReadGroup(ctx, "test", fake.BasicGroupPeer); e != nil {
					t.Error(e)
				}
				if _, _, e := f.ReadCoverage(ctx, "test", nil); e != nil {
					t.Error(e)
				}
			}
		})
	}
	wg.Go(func() {
		for i := 0; i < 20; i++ {
			if e := f.Publish(ctx, engine.Batch{Messages: []engine.Message{{Key: key(fake.PrivatePeer, 500+i), Date: fake.Epoch(), Text: "concurrent"}}}); e != nil {
				t.Error(e)
			}
		}
	})
	wg.Wait()
}
