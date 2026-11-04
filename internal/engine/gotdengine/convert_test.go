package gotdengine

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"testing"
	"time"
)

func TestConvertProductionPolicy(t *testing.T) {
	for _, media := range []tg.MessageMediaClass{
		&tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 1}, TTLSeconds: 10},
		&tg.MessageMediaDocument{Document: &tg.Document{ID: 1}, TTLSeconds: 2147483647},
	} {
		m := &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, TTLPeriod: 2147483647, Noforwards: true, Message: "private caption", Entities: []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 7, URL: "https://private.invalid"}}, Media: media}
		got, ok := convertMessage("primary", m)
		if !ok || !got.Protected || got.TTLPeriod != m.TTLPeriod || got.ExpiresAt == nil || !got.ExpiresAt.Equal(time.Unix(int64(m.Date)+int64(m.TTLPeriod), 0)) {
			t.Fatalf("lost auto-delete/protected policy: %+v", got)
		}
		if got.Text != "[ephemeral media]" || len(got.Entities) != 0 || got.Media == nil || got.Media.TTLSeconds <= 0 || got.Media.Downloadable || len(got.Media.Remote) != 0 || got.Media.Filename != "" {
			t.Fatalf("ephemeral payload leaked: %+v", got)
		}
	}
}

func TestConvertProtectedTextRemainsReadable(t *testing.T) {
	got, ok := convertMessage("primary", &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Message: "readable", Noforwards: true, Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 1}}})
	if !ok || got.Text != "readable" || !got.Protected || got.Media == nil || got.Media.Downloadable || len(got.Media.Remote) != 0 {
		t.Fatalf("wrong protected policy: %+v", got)
	}
}

func TestConvertPreservesUTF16AndCompositeIdentity(t *testing.T) {
	m := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 42}, Message: "😀 Bruno", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 3, Length: 5}}}
	got, ok := convertMessage("primary", m)
	if !ok || got.Key != (engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerChannel, ID: 42}, ID: 7}) {
		t.Fatalf("wrong identity: %#v", got.Key)
	}
	if len(got.Entities) != 1 || got.Entities[0].Offset != 3 || got.Entities[0].Length != 5 {
		t.Fatal("UTF-16 offsets changed")
	}
}
