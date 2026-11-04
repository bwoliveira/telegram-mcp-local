package gotdengine

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestRealRemoteSearchAndWindowUseRPCAndPersist(t *testing.T) {
	e, st := offlineEngine(t)
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	var calls []string
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.MessagesSearchRequest:
			calls = append(calls, "search")
			if r.Q != "needle" || r.Limit != 3 {
				t.Fatal("search arguments lost")
			}
		case *tg.MessagesSearchGlobalRequest:
			calls = append(calls, "global")
			if r.OffsetPeer == nil || r.Q != "needle" {
				t.Fatal("global arguments lost")
			}
		case *tg.MessagesGetHistoryRequest:
			calls = append(calls, "window")
			if r.OffsetID != 8 || r.AddOffset != -3 || r.Limit != 4 {
				t.Fatalf("incorrect window: %+v", r)
			}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Users: []tg.UserClass{&tg.User{ID: 99, Self: true}}, Messages: []tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "needle"}}}
		return nil
	}))
	for _, peer := range []*engine.PeerRef{&ref, nil} {
		got, err := e.SearchRemote(context.Background(), engine.SearchRequest{Query: "needle", Peer: peer, Limit: 3})
		if err != nil || len(got.Messages) != 1 || got.Messages[0].Key.Account != "primary" {
			t.Fatalf("search failed: %+v %v", got, err)
		}
	}
	got, err := e.FetchWindow(context.Background(), engine.WindowRequest{Key: engine.MessageKey{Account: "primary", Peer: ref, ID: 8}, Before: 1, After: 2})
	if err != nil || len(got) != 1 {
		t.Fatalf("window failed: %d %v", len(got), err)
	}
	if len(calls) != 3 {
		t.Fatal(calls)
	}
	if _, ok, err := st.Message(context.Background(), engine.MessageKey{Account: "primary", Peer: ref, ID: 8}); err != nil || !ok {
		t.Fatalf("RPC rows not persisted: %v", err)
	}
}
