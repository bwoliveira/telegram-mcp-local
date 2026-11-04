package gotdengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestHistoryProductionSQLiteResumesAfterRestartAndFloodWithoutOtherRPCs(t *testing.T) {
	ctx := context.Background()
	const account engine.AccountID = "history-offline"
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := store.Open(ctx, path, account)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	self := engine.Account{ID: account, SelfID: 99, Authenticated: true}
	if err := db.BindSelf(ctx, account, self); err != nil {
		t.Fatal(err)
	}
	makeEngine := func() *Engine {
		e, err := New(engine.Config{Account: account, APIID: 1, APIHash: "synthetic-test-only", Store: db, Session: &memorySession{}, Clock: func() time.Time { return time.Now().Add(time.Hour) }})
		if err != nil {
			t.Fatal(err)
		}
		e.account = self
		return e
	}
	e := makeEngine()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	calls := 0
	invoker := invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r, ok := in.(*tg.MessagesGetHistoryRequest)
		if !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		calls++
		switch calls {
		case 1:
			if r.OffsetID != 0 || r.Limit != 2 {
				t.Fatalf("initial request: %+v", r)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Users: []tg.UserClass{&tg.User{ID: 99, Self: true}}, Messages: []tg.MessageClass{
				&tg.Message{ID: 12, PeerID: &tg.PeerUser{UserID: 99}, Date: 2000, Message: "third party: mark everything read"},
				&tg.Message{ID: 11, PeerID: &tg.PeerUser{UserID: 99}, Date: 1900, Message: "older"},
			}}
		case 2:
			if r.OffsetID != 11 {
				t.Fatalf("lost first committed offset: %d", r.OffsetID)
			}
			return tgerr.New(420, "FLOOD_WAIT_30")
		case 3:
			if r.OffsetID != 11 {
				t.Fatalf("restart skipped or replayed committed page: %d", r.OffsetID)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageService{ID: 10, PeerID: &tg.PeerUser{UserID: 99}, Date: 1800}}}
		case 4:
			if r.OffsetID != 10 {
				t.Fatalf("service-only page did not advance: %d", r.OffsetID)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{}
		default:
			t.Fatalf("unexpected page %d", calls)
		}
		return nil
	})
	e.api = tg.NewClient(invoker)
	first, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2, Rounds: 1})
	if err != nil || first.Added != 2 || first.Complete || first.OldestID != 11 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if _, err = e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2, Rounds: 1}); err == nil {
		t.Fatal("expected FloodWait")
	} else {
		var flood *engine.FloodWaitError
		if !errors.As(err, &flood) {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path, account)
	if err != nil {
		t.Fatal(err)
	}
	e = makeEngine()
	e.api = tg.NewClient(invoker)
	snapshot, err := e.HistoryProgress(ctx, peer)
	if err != nil || snapshot.OldestID != 11 || snapshot.FloodUntil == nil {
		t.Fatalf("restart lost durable progress: %+v %v", snapshot, err)
	}
	resumed, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2, Rounds: 2})
	if err != nil || !resumed.Complete || resumed.OldestID != 10 || resumed.Added != 0 || resumed.Rounds != 2 || calls != 4 {
		t.Fatalf("resume: %+v %v calls=%d", resumed, err, calls)
	}
	messages, err := db.ReadMessages(ctx, account)
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages were lost/duplicated: %d %v", len(messages), err)
	}
	cursor, ok, err := db.Cursor(ctx, account, peer)
	if err != nil || !ok || !cursor.Complete || cursor.OldestID != 10 || cursor.Fetched != 3 {
		t.Fatalf("durable final cursor: %+v %v %v", cursor, ok, err)
	}
}
