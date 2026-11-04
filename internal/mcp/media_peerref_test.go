package mcp

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestMediaToolsResolveConversationForms(t *testing.T) {
	s, f, st := parityServer(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1000}, ID: 50}
	err := f.Publish(ctx, engine.Batch{Account: key.Account, Source: engine.SourceRefresh, Messages: []engine.Message{{Key: key, Date: fake.Epoch(), Media: &engine.Media{Type: engine.MediaVoice, Downloadable: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTranscript(ctx, key, store.Transcript{Text: "cached", RawText: "original", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{"user:1000", "me", "@fake_self", "t.me/fake_self", "Test Account"} {
		for _, tool := range []string{"download_media", "transcribe_audio", "save_transcript", "purge_media"} {
			t.Run(tool+"/"+form, func(t *testing.T) {
				var args map[string]any
				switch tool {
				case "download_media":
					args = map[string]any{"chat_id": form, "message_id": "50", "link": true}
				case "transcribe_audio":
					args = map[string]any{"chat_id": form, "message_id": "50"}
				case "save_transcript":
					args = map[string]any{"chat_id": form, "message_id": "50", "text": "corrected"}
				case "purge_media":
					args = map[string]any{"chat_id": form}
				}
				result := toolCall(t, s, tool, args)
				paritySuccess(t, result)
			})
		}
	}
	if len(f.Writes()) != 0 {
		t.Fatal("local media operations wrote to Telegram", f.Writes())
	}
}
