package gotdengine

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMediaNativeTranscriptionExplicitPending(t *testing.T) {
	e, _ := offlineEngine(t)
	key := mediaTestMessage(t, e, engine.Message{Media: &engine.Media{Type: engine.MediaVoice}})
	var calls int
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		switch r := in.(type) {
		case *tg.MessagesGetMessagesRequest:
			raw := mediaRawDocument(e)
			raw.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
		case *tg.MessagesTranscribeAudioRequest:
			if r.MsgID != 7 {
				t.Fatalf("wrong id %d", r.MsgID)
			}
			if _, ok := r.Peer.(*tg.InputPeerSelf); !ok {
				t.Fatalf("wrong peer %T", r.Peer)
			}
			*out.(*tg.MessagesTranscribedAudio) = tg.MessagesTranscribedAudio{Text: "partial", TranscriptionID: 42, Pending: true}
		default:
			t.Fatalf("unexpected %T", in)
		}
		return nil
	}))
	if calls != 0 {
		t.Fatal("automatic transcription")
	}
	got, err := e.TranscribeNative(context.Background(), key)
	if err != nil || got.Text != "partial" || got.ID != 42 || !got.Pending || calls != 2 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
}
