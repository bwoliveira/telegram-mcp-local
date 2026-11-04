package gotdengine

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"testing"
)

func TestRealRemoteSearchAttachmentWithoutNativeFilterIsBoundedPartial(t *testing.T) {
	e, _ := offlineEngine(t)
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req := in.(*tg.MessagesSearchGlobalRequest)
		if _, ok := req.Filter.(*tg.InputMessagesFilterEmpty); !ok {
			t.Fatal("unexpected filter")
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "synthetic"}}}
		return nil
	}))
	result, err := e.SearchRemote(context.Background(), engine.SearchRequest{Query: "synthetic", Media: engine.MediaPoll, Limit: 1})
	if err != nil || !result.Partial || len(result.Messages) != 0 {
		t.Fatalf("valid attachment kind rejected or false completeness: %+v %v", result, err)
	}
}
