package mcp

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"testing"
)

func TestReadToolsResolveConversationForms(t *testing.T) {
	e, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: e, ReadStore: e.(*fake.Controller), Clock: fake.Epoch})
	for _, input := range []string{"me", "self", "saved", "@fake_self", "t.me/fake_self", "Test Account", "user:1000", "1000"} {
		for _, name := range []string{"get_chat_messages", "search_messages", "get_message_context"} {
			t.Run(name+"/"+input, func(t *testing.T) {
				args := map[string]any{"chat_id": input}
				switch name {
				case "search_messages":
					args["query"] = "synthetic"
				case "get_message_context":
					args["message_id"] = "8"
				}
				r := toolCall(t, s, name, args)
				if r["isError"] != false {
					t.Fatalf("%v", r)
				}
				if name == "get_chat_messages" && toolBody(t, r)["chat_id"] != "user:1000" {
					t.Fatal(r)
				}
			})
		}
	}
	for _, input := range []string{"@fake_supergroup", "https://t.me/fake_supergroup", "Synthetic supergroup", "channel:2001", "2001"} {
		t.Run("get_group/"+input, func(t *testing.T) {
			r := toolCall(t, s, "get_group", map[string]any{"group_id": input})
			if r["isError"] != false || toolBody(t, r)["group_id"] != "channel:2001" {
				t.Fatal(r)
			}
		})
	}
	if writes := e.(*fake.Controller).Writes(); len(writes) != 0 {
		t.Fatalf("reads wrote Telegram: %v", writes)
	}
}
