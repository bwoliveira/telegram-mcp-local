package mcp

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"testing"
)

func TestTelegramLocalMessages(t *testing.T) {
	e, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: e, ReadStore: e.(*fake.Controller)})
	r := toolCall(t, s, "get_chat_messages", map[string]any{"chat_id": "user:1001", "limit": 1, "fields": []string{"id", "text"}, "max_content_chars": 3})
	b := toolBody(t, r)
	if r["isError"] != false || b["count"] != float64(1) {
		t.Fatal(r)
	}
	if _, ok := b["migration_warning"]; ok {
		t.Fatal(b)
	}
}
