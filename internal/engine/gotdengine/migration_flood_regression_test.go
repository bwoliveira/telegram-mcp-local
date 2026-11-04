package gotdengine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestMigrationFloodDoesNotBlockIndependentDialogRefresh(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{&tg.Chat{ID: 71, Title: "Ordinary basic group"}})}); err != nil {
		t.Fatal(err)
	}
	var pages, probes atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogsRequest:
			n := pages.Add(1)
			page := &tg.MessagesDialogs{Chats: []tg.ChatClass{&tg.Channel{ID: 81, Megagroup: true, AccessHash: 99}}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}}
			if n > 1 {
				page.Chats = append(page.Chats, &tg.Channel{ID: 82, Broadcast: true, AccessHash: 100, Title: "New empty broadcast"})
				page.Dialogs = append(page.Dialogs, &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 82}})
			}
			out.(*tg.MessagesDialogsBox).Dialogs = page
		case *tg.ChannelsGetFullChannelRequest:
			probes.Add(1)
			return tgerr.New(420, "FLOOD_WAIT_3600")
		default:
			t.Fatalf("unexpected synthetic RPC: %T", in)
		}
		return nil
	}))
	e.dialogRefreshEvery = time.Minute
	e.dialogRefresh = make(chan struct{})
	stop := e.startDialogRefresh(ctx)
	defer stop()
	// Each unbuffered handoff confirms that the previous event completed.
	// Event 1 hits metadata FloodWait; event 2 is an independent channel signal.
	for i := 0; i < 3; i++ {
		select {
		case e.dialogRefresh <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("worker timed out")
		}
	}
	stop()
	chats, err := db.ListChats(ctx, "primary", store.ChatOptions{PeerOptions: store.PeerOptions{Query: "New empty broadcast"}})
	if err != nil {
		t.Fatal(err)
	}
	if pages.Load() < 2 || len(chats) != 1 || probes.Load() != 1 {
		t.Fatalf("auxiliary migration FloodWait blocked independent dialog discovery: dialog pages=%d full probes=%d new broadcasts listed=%d; want >=2 pages, 1 probe, 1 broadcast", pages.Load(), probes.Load(), len(chats))
	}
}
