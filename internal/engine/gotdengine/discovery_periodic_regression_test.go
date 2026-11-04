package gotdengine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestDiscoveryPeriodicDialogsRefreshRetriesAndJoins(t *testing.T) {
	e, db := discoveryEngine(t)
	e.dialogRefreshEvery = 10 * time.Millisecond
	if err := db.SaveUpdatesState(context.Background(), "primary", engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	blocked := make(chan struct{})
	e.api = tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		case *tg.MessagesGetDialogsRequest:
			if r.Hash != 0 || r.OffsetID != 0 || r.OffsetDate != 0 {
				t.Errorf("refresh did not start from current dialogs: %+v", r)
			}
			n := calls.Add(1)
			if n == 2 {
				return errors.New("synthetic transient refresh failure")
			}
			if n >= 4 {
				close(blocked)
				<-ctx.Done()
				return ctx.Err()
			}
			result := &tg.MessagesDialogs{}
			if n == 3 {
				result.Chats = []tg.ChatClass{&tg.Channel{ID: 81, Title: "Discovered empty broadcast", Broadcast: true, AccessHash: 99}}
				result.Dialogs = []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}, UnreadCount: 2}}
			}
			out.(*tg.MessagesDialogsBox).Dialogs = result
		case *tg.UpdatesGetStateRequest:
			*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 1, Date: 1000}
		case *tg.UpdatesGetDifferenceRequest:
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1000}
		default:
			t.Errorf("forbidden RPC %T", in)
			return errors.New("forbidden RPC")
		}
		return nil
	}))
	e.runClient = func(ctx context.Context, f func(context.Context) error) error { return f(ctx) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-blocked:
	case <-ctx.Done():
		<-done
		t.Fatalf("Run never performed periodic discovery after startup/error: calls=%d", calls.Load())
	}
	chats, err := db.ListChats(context.Background(), "primary", store.ChatOptions{})
	if err != nil || len(chats) != 1 || chats[0].Peer.Ref.ID != 81 || chats[0].Dialog.UnreadCount != 2 {
		t.Errorf("periodic refresh did not persist empty broadcast: %+v %v", chats, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not cancel and join in-flight dialog refresh")
	}
	if calls.Load() != 4 {
		t.Fatalf("unexpected refresh requests: %d", calls.Load())
	}
}
