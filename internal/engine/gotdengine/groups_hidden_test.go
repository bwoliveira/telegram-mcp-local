package gotdengine

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"testing"
)

func TestRealGroupBasicHiddenParticipantsKeepAdvertisedSize(t *testing.T) {
	e, _ := offlineEngine(t)
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
		*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: 20, Participants: &tg.ChatParticipantsForbidden{ChatID: 20}}, Chats: []tg.ChatClass{&tg.Chat{ID: 20, Title: "Basic hidden", ParticipantsCount: 42}}}
		return nil
	}))
	g, err := e.RefreshGroup(context.Background(), engine.GroupRequest{Group: engine.PeerRef{Type: engine.PeerChat, ID: 20}})
	if err != nil || !g.Partial || g.Size != 42 {
		t.Fatalf("Telegram restriction misreported as empty group: size=%d partial=%v err=%v", g.Size, g.Partial, err)
	}
}
