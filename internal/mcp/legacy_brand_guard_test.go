// Legacy literals are isolated here to verify that inherited branding and APIs stay absent.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
)

func TestCompatibilitySourcesExcludePredecessorDomains(t *testing.T) {
	// Negative source guard: these domains must never reappear in runtime code.
	for _, root := range []string{"../index", "."} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || entry.Name() == "legacy_brand_guard_test.go" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, domain := range []string{"@g.us", "@s.whatsapp.net", "@lid", "@broadcast", "@newsletter"} {
				if strings.Contains(string(data), domain) {
					t.Errorf("%s contains predecessor domain %q", path, domain)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMissingEngineAndLegacyAliasesAreErrors(t *testing.T) {
	s := New(Config{})
	for _, name := range []string{"send_text_message", "get_chat_messages", "whatsapp_status", "check_numbers"} {
		r := toolCall(t, s, name, map[string]any{"to": "user:1", "text": "hello", "chat_id": "user:1"})
		if r["isError"] != true {
			t.Errorf("%s unexpectedly succeeded: %v", name, r)
		}
	}
	r := toolCall(t, s, "list_chats", nil)
	if !strings.Contains(toolBody(t, r)["error"].(string), "not implemented yet") {
		t.Fatal(r)
	}
}

func TestHealthVerdicts(t *testing.T) {
	now := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	hoursAgo := func(h time.Duration) *time.Time {
		t := now.Add(-h * time.Hour)
		return &t
	}
	cases := []struct {
		name      string
		mutate    func(*healthInputs)
		want      string
		failing   string
		checkWant string
	}{
		{name: "connected does not verify inherited index", mutate: func(*healthInputs) {}, want: "warn", failing: "coverage", checkWant: "warn"},
		{name: "not authorized", mutate: func(in *healthInputs) {
			in.Diagnostics.Authorized = false
			in.Sync.State = engine.StateNotAuthorized
		}, want: "fail", failing: "paired", checkWant: "fail"},
		{name: "revoked state overrides stale diagnostics", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateRevoked
		}, want: "fail", failing: "paired", checkWant: "fail"},
		{name: "logged out state overrides stale diagnostics", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateLoggedOut
		}, want: "fail", failing: "paired", checkWant: "fail"},
		{name: "diagnostics unavailable", mutate: func(in *healthInputs) {
			in.DiagnosticsErr = errors.New("private backend failure")
		}, want: "fail", failing: "engine", checkWant: "fail"},
		{name: "revoked diagnostics error", mutate: func(in *healthInputs) {
			in.DiagnosticsErr = engine.ErrRevoked
		}, want: "fail", failing: "paired", checkWant: "fail"},
		{name: "sync error", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateError
		}, want: "fail", failing: "sync", checkWant: "fail"},
		{name: "sync stopped", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateStopped
		}, want: "fail", failing: "sync", checkWant: "fail"},
		{name: "paused is intentional not auto restart", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StatePaused
			in.DiagnosticsErr = engine.ErrPaused
		}, want: "warn", failing: "sync", checkWant: "warn"},
		{name: "starting", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateStarting
		}, want: "warn", failing: "sync", checkWant: "warn"},
		{name: "connecting", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateConnecting
		}, want: "warn", failing: "sync", checkWant: "warn"},
		{name: "reconnecting", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateReconnecting
		}, want: "warn", failing: "sync", checkWant: "warn"},
		{name: "resyncing", mutate: func(in *healthInputs) {
			in.Sync.State = engine.StateResyncing
		}, want: "warn", failing: "sync", checkWant: "warn"},
		{name: "unknown connection state", mutate: func(in *healthInputs) {
			in.Sync.State = "unexpected"
		}, want: "fail", failing: "sync", checkWant: "fail"},
		{name: "quiet inherited index", mutate: func(in *healthInputs) {
			in.Activity.NewestIncoming = hoursAgo(8)
		}, want: "warn", failing: "receiving", checkWant: "warn"},
		{name: "higher tolerance still does not verify Telegram", mutate: func(in *healthInputs) {
			in.Activity.NewestIncoming = hoursAgo(8)
			in.MaxSilence = 12 * time.Hour
		}, want: "warn", failing: "receiving", checkWant: "warn"},
		{name: "old inherited messages do not prove engine failure", mutate: func(in *healthInputs) {
			in.Activity.NewestIncoming = hoursAgo(30)
		}, want: "warn", failing: "receiving", checkWant: "warn"},
		{name: "empty inherited index", mutate: func(in *healthInputs) {
			in.Activity = index.Activity{}
		}, want: "warn", failing: "receiving", checkWant: "warn"},
		{name: "unavailable inherited index", mutate: func(in *healthInputs) {
			in.ActivityErr = errors.New("private index failure")
		}, want: "warn", failing: "receiving", checkWant: "warn"},
		{name: "silent window is not a Telegram gap", mutate: func(in *healthInputs) {
			in.Gaps = []index.Gap{{From: now.Add(-50 * time.Hour), Until: now.Add(-30 * time.Hour), Hours: 20}}
		}, want: "warn", failing: "coverage", checkWant: "warn"},
		{name: "checkpoint held", mutate: func(in *healthInputs) {
			in.Sync.Updates.Held = true
		}, want: "warn", failing: "updates", checkWant: "warn"},
		{name: "engine gaps", mutate: func(in *healthInputs) {
			in.Sync.Updates.OpenGaps = 2
		}, want: "warn", failing: "updates", checkWant: "warn"},
		{name: "diagnostics checkpoint held", mutate: func(in *healthInputs) {
			in.Diagnostics.LocalUpdates.Held = true
		}, want: "warn", failing: "updates", checkWant: "warn"},
		{name: "diagnostics engine gaps", mutate: func(in *healthInputs) {
			in.Diagnostics.LocalUpdates.OpenGaps = 2
		}, want: "warn", failing: "updates", checkWant: "warn"},
		{name: "history failures", mutate: func(in *healthInputs) {
			in.History = &HistoryJob{Chats: []string{"user:123"}, Failures: map[string]any{"user:123": "unavailable"}}
		}, want: "warn", failing: "history_request", checkWant: "warn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := healthy(now)
			tc.mutate(&in)
			h := evaluate(in)
			if h.Status != tc.want {
				t.Fatalf("status %q, want %q; checks %+v", h.Status, tc.want, h.Checks)
			}
			c := check(t, h, tc.failing)
			if c.Status != tc.checkWant || c.Fix == "" {
				t.Fatalf("%s = %+v, want %s with actionable fix", tc.failing, c, tc.checkWant)
			}
			body, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"wacli", "WhatsApp", "phone must be online", "private backend failure", "private index failure"} {
				if strings.Contains(string(body), forbidden) {
					t.Fatalf("health contains %q: %s", forbidden, body)
				}
			}
		})
	}
}

func statusReport(t *testing.T, s *Server, ctx context.Context) map[string]any {
	t.Helper()
	result := s.status(ctx, arguments{})
	if result["isError"] != false {
		t.Fatalf("status tool failed: %+v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var report map[string]any
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"wacli", "WhatsApp", "private diagnostics failure"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status contains %q: %s", forbidden, text)
		}
	}
	return report
}

// A translated panel summary cannot change the protocol schema or omit its
// canonical English security conditions. Translation is explicit per request,
// not a global setting that could leak into MCP answers.
func TestCatalogueLanguagesKeepTelegramContract(t *testing.T) {
	canonical := Catalogue()
	before, err := json.Marshal(toolDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 40 || len(catalogueSummaries) != 40 {
		t.Fatalf("want 40 tools and 40 multilingual summaries; got %d and %d", len(canonical), len(catalogueSummaries))
	}
	mutations := 0
	for _, tool := range canonical {
		if !telegramMutation(tool.Name) {
			continue
		}
		mutations++
		controls := map[string]string{"dry_run": "boolean", "confirm": "boolean", "preview_fingerprint": "string", "idempotency_key": "string"}
		for _, argument := range tool.Arguments {
			if kind, ok := controls[argument.Name]; ok {
				if argument.Type != kind || argument.Required {
					t.Errorf("%s.%s prevents default preview", tool.Name, argument.Name)
				}
				delete(controls, argument.Name)
			}
		}
		if len(controls) != 0 {
			t.Errorf("%s lacks write controls: %v", tool.Name, controls)
		}
	}
	if mutations != 15 {
		t.Errorf("want 15 Telegram-changing tools, got %d", mutations)
	}
	for _, lang := range []string{"en", "pt-BR", "pt_BR", "es", "es-MX", "unknown", ""} {
		t.Run(lang, func(t *testing.T) {
			translated := CatalogueForLanguage(lang)
			if len(translated) != len(canonical) {
				t.Fatalf("%s changed tool count", lang)
			}
			for i, tool := range translated {
				original := canonical[i]
				if tool.Name != original.Name {
					t.Fatalf("%s changed tool name %q", lang, original.Name)
				}
				a, _ := json.Marshal(tool.Arguments)
				b, _ := json.Marshal(original.Arguments)
				if string(a) != string(b) {
					t.Errorf("%s changed arguments for %s", lang, tool.Name)
				}
				if !strings.Contains(tool.Description, original.Description) {
					t.Errorf("%s dropped the technical contract for %s", lang, tool.Name)
				}
				summary, ok := catalogueSummaries[tool.Name]
				if !ok || summary[0] == "" || summary[1] == "" {
					t.Errorf("%s lacks complete translations", tool.Name)
				}
				if strings.HasPrefix(lang, "pt") && !strings.HasPrefix(tool.Description, summary[0]) {
					t.Errorf("%s lacks Portuguese summary", tool.Name)
				}
				if strings.HasPrefix(lang, "es") && !strings.HasPrefix(tool.Description, summary[1]) {
					t.Errorf("%s lacks Spanish summary", tool.Name)
				}
				if tool.Name == "resolve_contact" {
					found := false
					for _, arg := range tool.Arguments {
						if arg.Name == "queries" && arg.Type == "string[]" && arg.Required {
							found = true
						}
					}
					if !found {
						t.Error("resolve_contact must require queries:string[] in every language")
					}
				}
				for _, legacy := range []string{"whatsapp", "wacli", "check_numbers", "chat_jid", "group_jid", "max_answers"} {
					raw, _ := json.Marshal(tool)
					if strings.Contains(strings.ToLower(string(raw)), legacy) {
						t.Errorf("%s exposes %q in %s", lang, legacy, tool.Name)
					}
				}
			}
		})
	}
	after, err := json.Marshal(toolDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("panel translation mutated the MCP definitions")
	}
}

// The catalogue is a contract, not evidence of a live Telegram connection.
func TestTelegramCatalogueContract(t *testing.T) {
	defs := toolDefinitions()
	if len(defs) != 40 {
		t.Fatalf("want exactly 40 tools, got %d", len(defs))
	}
	mutations := map[string]bool{}
	for _, name := range []string{"send_text_message", "send_media_message", "send_location", "send_poll", "edit_message", "delete_message", "react_to_message", "forward_message", "mark_chat_read", "send_typing", "organise_chat", "manage_group_participants", "update_group", "get_group_invite_link", "leave_group"} {
		mutations[name] = true
	}
	seen := map[string]bool{}
	mutationCount := 0
	for _, raw := range defs {
		def := raw.(map[string]any)
		name := def["name"].(string)
		seen[name] = true
		schema := def["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		encoded, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		for _, legacy := range []string{"whatsapp", "wacli", "check_numbers", "chat_jid", "group_jid", "sender_jid", "@g.us", "max_answers"} {
			if strings.Contains(strings.ToLower(string(encoded)), legacy) {
				t.Errorf("%s still exposes %q", name, legacy)
			}
		}
		if mutations[name] {
			mutationCount++
			for _, required := range schema["required"].([]string) {
				if required == "confirm" || required == "dry_run" || required == "preview_fingerprint" || required == "idempotency_key" {
					t.Errorf("%s must allow the initial preview without %s", name, required)
				}
			}
			for field, kind := range map[string]string{"dry_run": "boolean", "confirm": "boolean", "preview_fingerprint": "string", "idempotency_key": "string"} {
				p, ok := props[field].(map[string]any)
				if !ok || p["type"] != kind {
					t.Errorf("%s lacks %s:%s", name, field, kind)
				}
			}
			for _, note := range []string{"preview", "read_only", "human", "10 minutes", "single-use"} {
				if !strings.Contains(def["description"].(string), note) {
					t.Errorf("%s lacks security note %q", name, note)
				}
			}
			if def["annotations"].(map[string]any)["readOnlyHint"] != false {
				t.Errorf("%s is marked read-only", name)
			}
		} else if name != "purge_media" {
			if _, ok := props["preview_fingerprint"]; ok {
				t.Errorf("%s is not a Telegram mutation", name)
			}
		}
		if name == "resolve_contact" {
			p, ok := props["queries"].(map[string]any)
			if !ok || p["type"] != "array" || p["items"].(map[string]any)["type"] != "string" {
				t.Errorf("resolve_contact must accept queries:string[]")
			}
			if _, ok := props["numbers"]; ok {
				t.Error("resolve_contact exposes numbers")
			}
		}
		if name == "send_poll" {
			p, ok := props["multiple_choice"].(map[string]any)
			if !ok || p["type"] != "boolean" {
				t.Error("send_poll must accept multiple_choice:boolean")
			}
		}
		for _, field := range []string{"chat_id", "group_id"} {
			if p, ok := props[field].(map[string]any); ok && !strings.Contains(p["description"].(string), "channel:") {
				t.Errorf("%s.%s does not describe typed peers", name, field)
			}
		}
		if p, ok := props["message_id"].(map[string]any); ok {
			for _, note := range []string{"chat_id", "channel:123/456", "alone"} {
				if !strings.Contains(p["description"].(string), note) {
					t.Errorf("%s.message_id lacks %q", name, note)
				}
			}
		}
	}
	if mutationCount != 15 {
		t.Errorf("want exactly 15 Telegram-changing tools, got %d", mutationCount)
	}
	for name := range mutations {
		if !seen[name] {
			t.Errorf("missing Telegram-changing tool %s", name)
		}
	}
	for _, name := range []string{"telegram_status", "resolve_contact"} {
		if !seen[name] {
			t.Errorf("missing %s", name)
		}
	}
}

func TestInitializeTelegramInstructions(t *testing.T) {
	s := &Server{baseURL: "http://127.0.0.1:47831", clients: map[string]string{}, touched: map[string]time.Time{}}
	out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	var response struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatal(err)
	}
	instructions := response.Result.Instructions
	for _, want := range []string{"Telegram", "third parties", "instructions", "read_only", "preview", "confirm", "preview_fingerprint"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("initialize instructions lack %q: %s", want, instructions)
		}
	}
	for _, legacy := range []string{"WhatsApp", "wacli", "whatsapp_status", "check_numbers", "Configurações"} {
		if strings.Contains(instructions, legacy) {
			t.Errorf("initialize instructions still contain %q", legacy)
		}
	}
}
