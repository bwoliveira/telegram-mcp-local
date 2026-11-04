package gotdengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func metadataCall(t *testing.T, s *mcp.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(s.Handle(context.Background(), req), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Error) > 0 || reply.Result.IsError || len(reply.Result.Content) != 1 {
		t.Fatalf("metadata tool failed: %s", name)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestMetadataAllReadPathsAfterUpgrade(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		name := "fresh"
		if upgrade {
			name = "legacy-v1"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(2000, 0)
			clock := func() time.Time { return now }
			path := filepath.Join(t.TempDir(), "metadata.db")
			db, err := store.Open(ctx, path, "primary", store.WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
			if err := db.BindSelf(ctx, "primary", e.account); err != nil {
				t.Fatal(err)
			}
			reaction := tg.ReactionCount{Reaction: &tg.ReactionCustomEmoji{DocumentID: 4321}, Count: 2}
			reaction.SetChosenOrder(0)
			raw := &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, Media: metadataPoll()}
			raw.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{reaction}})
			if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: raw}}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if upgrade {
				legacy, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := legacy.Exec(`UPDATE messages SET media_type='unsupported',media_json='{"type":"unsupported","downloadable":false}',poll_json=NULL,text=''; DELETE FROM reactions; PRAGMA user_version=1`); err != nil {
					t.Fatal(err)
				}
				if err := legacy.Close(); err != nil {
					t.Fatal(err)
				}
			}
			db, err = store.Open(ctx, path, "primary", store.WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}
			e.cfg.Store = db
			if upgrade {
				b := engine.Batch{Account: "primary", Source: engine.SourceRefresh}
				appendMessage(&b, raw, false, engine.PeerRef{})
				if _, err := db.Apply(ctx, b); err != nil {
					t.Fatal(err)
				}
			}
			rpcCalls := 0
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { rpcCalls++; return engine.ErrNotSupported }))
			exportDir, err := db.StatsExportDir(ctx, "primary")
			if err != nil {
				t.Fatal(err)
			}
			server := mcp.New(mcp.Config{Engine: e, ReadStore: db, Clock: clock, ExportDir: exportDir})
			check := func(v any) {
				t.Helper()
				row, ok := v.(map[string]any)
				if !ok {
					t.Fatal("missing message object")
				}
				if row["text"] != "Synthetic question" || row["media_type"] != "poll" || row["poll"] == nil || row["reactions"] == nil {
					t.Fatal("common read path lost poll/reactions")
				}
				r := row["reactions"].([]any)[0].(map[string]any)
				if r["emoji"] != "custom:4321" || r["chosen"] != true {
					t.Fatal("custom reaction/chosen state lost")
				}
			}
			check(metadataCall(t, server, "get_chat_messages", map[string]any{"chat_id": "user:10"})["messages"].([]any)[0])
			check(metadataCall(t, server, "search_messages", map[string]any{"chat_id": "user:10", "query": "Synthetic", "remote": false})["messages"].([]any)[0])
			check(metadataCall(t, server, "get_message_context", map[string]any{"chat_id": "user:10", "message_id": "8", "before": 0, "after": 0})["message"])
			selected := metadataCall(t, server, "get_chat_messages", map[string]any{"chat_id": "user:10", "fields": []string{"id", "reactions", "poll"}})["messages"].([]any)[0].(map[string]any)
			if selected["poll"] == nil || selected["reactions"] == nil || selected["text"] != nil {
				t.Fatal("explicit metadata field selection failed")
			}
			result := metadataCall(t, server, "export_messages", map[string]any{"chat_id": "user:10"})
			// The registry owns the local filename; enumerate only this test's export directory.
			entries, err := os.ReadDir(exportDir)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".ndjson") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(exportDir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var row map[string]any
					if err := json.Unmarshal([]byte(line), &row); err != nil {
						t.Fatal(err)
					}
					if row["id"] == "8" {
						check(row)
						found = true
					}
				}
			}
			if !found || result["retention_registered"] != true {
				t.Fatal("export omitted metadata or retention registration")
			}
			if rpcCalls != 0 {
				t.Fatal("local read issued RPC")
			}
		})
	}
}
