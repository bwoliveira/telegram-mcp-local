package gotdengine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestMigrationFloodDoesNotBlockStartupManager(t *testing.T) {
	e, db := discoveryEngine(t)
	e.dialogRefreshEvery = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: 71}})}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveUpdatesState(ctx, "primary", engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	var probes, dialogs atomic.Int32
	diff := make(chan struct{}, 1)
	flood := make(chan struct{}, 8)
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		case *tg.MessagesGetDialogsRequest:
			dialogs.Add(1)
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Chats: []tg.ChatClass{&tg.Channel{ID: 81, Megagroup: true, AccessHash: 99}}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}}
		case *tg.ChannelsGetFullChannelRequest:
			probes.Add(1)
			flood <- struct{}{}
			return tgerr.New(420, "FLOOD_WAIT_3600")
		case *tg.UpdatesGetStateRequest:
			*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 1, Date: 1000}
		case *tg.UpdatesGetDifferenceRequest:
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1000}
			select {
			case diff <- struct{}{}:
			default:
			}
		default:
			return errors.New("unexpected synthetic startup RPC")
		}
		return nil
	}))
	e.runClient = func(c context.Context, fn func(context.Context) error) error { return fn(c) }
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("startup did not join")
		}
	}()
	select {
	case <-diff:
	case <-ctx.Done():
		t.Fatalf("migration flood blocked manager startup: probes=%d dialogs=%d", probes.Load(), dialogs.Load())
	}
	select {
	case <-flood:
	case <-ctx.Done():
		t.Fatal("worker never attempted deferred discovery")
	}
	for i := 0; i < 5; i++ {
		select {
		case e.dialogRefresh <- struct{}{}:
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if probes.Load() != 1 {
		t.Fatalf("retried migration before FloodWait.Until: %d", probes.Load())
	}
	p, _, err := db.LookupPeer(ctx, "primary", old)
	if err != nil || p.MigratedTo != nil {
		t.Fatalf("flood invented migration %+v %v", p, err)
	}
}
