package gotdengine

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestRealWindowIncludesAnchorWhenNoNewerRowsRequested(t *testing.T) {
	e, _ := offlineEngine(t)
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req := in.(*tg.MessagesGetHistoryRequest)
		// getHistory's offset_id is exclusive; add_offset=-1 includes the anchor.
		if req.AddOffset != -1 {
			t.Fatalf("zero-after window excludes its anchor: add_offset=%d", req.AddOffset)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000}}}
		return nil
	}))
	_, err := e.FetchWindow(context.Background(), engine.WindowRequest{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 8}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRealScopedReadRejectsUnexpectedConversation(t *testing.T) {
	e, _ := offlineEngine(t)
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerChat{ChatID: 123}, Date: 1000}}}
		return nil
	}))
	_, err := e.FetchWindow(context.Background(), engine.WindowRequest{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 8}})
	if !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("unrelated message accepted: %v", err)
	}
}
