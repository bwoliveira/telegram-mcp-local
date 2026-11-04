package mcp

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/account"
	"strings"
	"testing"
	"time"
)

// Every tool listed has a handler and a category, and every handler is listed.
func TestToolsAreComplete(t *testing.T) {
	handlers := (&Server{}).handlers()
	listed := map[string]bool{}
	for _, d := range toolDefinitions() {
		def := d.(map[string]any)
		name := def["name"].(string)
		if listed[name] {
			t.Errorf("%s is listed twice", name)
		}
		listed[name] = true
		if ToolCategory(name) == "" {
			t.Errorf("%s has no category", name)
		}
		if _, ok := def["annotations"].(map[string]any); !ok {
			t.Errorf("%s has no annotations", name)
		}
		if _, ok := handlers[name]; !ok {
			t.Errorf("%s is listed but has no handler", name)
		}
	}
	for name := range handlers {
		if !listed[name] {
			t.Errorf("%s has a handler but is not listed", name)
		}
	}
	for name := range toolCategories {
		if !listed[name] {
			t.Errorf("%s has a category but is not listed", name)
		}
	}
}

// An assistant learns at initialize that webhooks exist, and where.
func TestInstructionsMentionWebhooks(t *testing.T) {
	s := &Server{baseURL: "http://127.0.0.1:47831", clients: map[string]string{}, touched: map[string]time.Time{}}
	out := string(s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)))
	for _, want := range []string{"Webhooks", "http://127.0.0.1:47831/webhooks/documentacao"} {
		if !strings.Contains(out, want) {
			t.Errorf("initialize lacks %q", want)
		}
	}
}

// The account's label distinguishes connections; no engine is needed to
// initialize, and a second account must not be confused with the first.
func TestInitializeNamesTheAccount(t *testing.T) {
	count := 1
	s := &Server{baseURL: "http://127.0.0.1:47831", clients: map[string]string{}, touched: map[string]time.Time{},
		account: func(context.Context) account.Info {
			return account.Info{Label: "Trabalho", Phone: "+55 (11) 98765-4321"}
		},
		accounts: func() int { return count }}
	initialize := func() string {
		return string(s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)))
	}
	one := initialize()
	var response struct {
		Result struct {
			ServerInfo struct {
				Title string `json:"title"`
			} `json:"serverInfo"`
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(one), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Result.ServerInfo.Title, "Telegram") || !strings.Contains(response.Result.ServerInfo.Title, "Trabalho") {
		t.Errorf("no Telegram title from the label: %s", one)
	}
	if strings.Contains(response.Result.Instructions, "This connection is the Telegram account") {
		t.Errorf("one account, yet the instructions distinguish accounts: %s", one)
	}
	count = 2
	if two := initialize(); two == one || !strings.Contains(two, "Trabalho") {
		t.Errorf("two accounts, but initialize does not distinguish the labelled connection: %s", two)
	}
}

func TestInitializeWithoutAnAccount(t *testing.T) {
	s := &Server{baseURL: "http://127.0.0.1:47831", clients: map[string]string{}, touched: map[string]time.Time{}}
	if out := string(s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))); !strings.Contains(out, `"title":"Telegram MCP"`) {
		t.Errorf("title without a label: %s", out)
	}
}
