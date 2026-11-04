package gotdengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/tg"
)

func TestConvertForumTopicIdentity(t *testing.T) {
	for _, tt := range []struct {
		name  string
		reply *tg.MessageReplyHeader
		topic int
	}{
		{"explicit_top", &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 101, ReplyToTopID: 100}, 100},
		{"root_without_top", &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 200}, 200},
		{"general_header", &tg.MessageReplyHeader{ForumTopic: true}, 1},
		{"ordinary_reply", &tg.MessageReplyHeader{ReplyToMsgID: 100}, 0},
		{"discussion_not_forum", &tg.MessageReplyHeader{ReplyToMsgID: 101, ReplyToTopID: 100}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := &tg.Message{ID: 300, PeerID: &tg.PeerChannel{ChannelID: 42}, ReplyTo: tt.reply}
			got, ok := convertMessage("primary", raw)
			if !ok || got.TopicID != tt.topic {
				t.Fatalf("raw reply %+v converted topic=%d; want %d", tt.reply, got.TopicID, tt.topic)
			}
			if got.ReplyTo == nil || got.ReplyTo.ID != tt.reply.ReplyToMsgID || got.ReplyTo.TopID != tt.reply.ReplyToTopID {
				t.Fatalf("reply identity changed: %+v", got.ReplyTo)
			}
		})
	}
}

// Exercise real raw Telegram conversion, durable mention indexing, and public MCP
// triage. No model.Message fixtures or Telegram connection participate.
func TestConvertForumStoreTriageIsolation(t *testing.T) {
	for _, name := range []string{"fresh", "legacy_v1_restart", "late_forum_metadata"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1700001000, 0)
			clock := func() time.Time { return now }
			path := filepath.Join(t.TempDir(), "store.db")
			db, err := store.Open(ctx, path, "primary", store.WithClock(clock))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if db != nil {
					db.Close()
				}
			}()
			e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Store: db, Session: &memorySession{}, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
			if err = db.BindSelf(ctx, "primary", e.account); err != nil {
				t.Fatal(err)
			}
			peer := engine.PeerRef{Type: engine.PeerChannel, ID: 42}
			raw := func(id int, out, mention bool, reply *tg.MessageReplyHeader) *tg.Message {
				m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 42}, Date: 1700000000 + id, Message: "forum fixture", Out: out, Mentioned: mention}
				if reply != nil {
					m.ReplyTo = reply
				}
				return m
			}
			forum := name != "late_forum_metadata"
			apply := func(messages ...*tg.Message) {
				t.Helper()
				b := engine.Batch{Account: "primary", Source: engine.SourceBackfill, Peers: peerUpserts(nil, []tg.ChatClass{&tg.Channel{ID: 42, Megagroup: true, Forum: forum}})}
				for _, m := range messages {
					appendMessage(&b, m, false, peer)
				}
				if _, err := db.Apply(ctx, b); err != nil {
					t.Fatal(err)
				}
			}
			apply(
				raw(101, false, true, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 100}),
				raw(201, true, false, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 200}),
				raw(301, false, true, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 302, ReplyToTopID: 300}),
				raw(401, true, false, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 402, ReplyToTopID: 400}),
				raw(501, false, true, nil),
				raw(601, true, false, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 600}),
			)
			if name == "legacy_v1_restart" {
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				db = nil
				old, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				// Reproduce v1's converter: every topic was zero, while raw reply IDs
				// survived. Persist a stale cross-topic answer, as the old index did.
				_, err = old.Exec(`UPDATE messages SET topic_id=0; UPDATE mentions SET topic_id=0,answered_at=1700000201,answered_by=201; PRAGMA user_version=1`)
				old.Close()
				if err != nil {
					t.Fatal(err)
				}
				db, err = store.Open(ctx, path, "primary", store.WithClock(clock))
				if err != nil {
					t.Fatal(err)
				}
				e.cfg.Store = db
			}
			if name == "late_forum_metadata" {
				forum = true
				apply()                           // Metadata alone must repair existing General identities.
				apply(raw(501, false, true, nil)) // Equal edit date is not a content edit.
			}
			server := mcp.New(mcp.Config{Engine: e, ReadStore: db, Clock: clock})
			assert := func(wantAnswered bool) {
				t.Helper()
				page, err := db.ListMentions(ctx, "primary", store.MentionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Mentions) != 3 {
					t.Fatalf("durable mentions=%d; want 3", len(page.Mentions))
				}
				topics := map[int]int{101: 100, 301: 300, 501: 1}
				for _, m := range page.Mentions {
					if m.TopicID != topics[m.Key.ID] || (m.AnsweredBy != nil) != wantAnswered {
						t.Errorf("durable mention id=%d topic=%d answered=%v; want topic=%d answered=%v", m.Key.ID, m.TopicID, m.AnsweredBy, topics[m.Key.ID], wantAnswered)
					}
				}
				call := func(tool string, args map[string]any) map[string]any {
					t.Helper()
					request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}
					wire, _ := json.Marshal(request)
					var response struct {
						Result struct {
							IsError bool `json:"isError"`
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"result"`
						Error json.RawMessage `json:"error"`
					}
					out := server.Handle(ctx, wire)
					if err := json.Unmarshal(out, &response); err != nil {
						t.Fatal(err)
					}
					if len(response.Error) > 0 || response.Result.IsError || len(response.Result.Content) != 1 {
						t.Fatalf("MCP %s: %s", tool, out)
					}
					var body map[string]any
					if err := json.Unmarshal([]byte(response.Result.Content[0].Text), &body); err != nil {
						t.Fatal(err)
					}
					return body
				}
				body := call("list_mentions", map[string]any{"chat_id": "channel:42"})
				mentions, ok := body["mentions"].([]any)
				if !ok || len(mentions) != 3 {
					t.Fatalf("MCP mentions: %v", body)
				}
				for _, v := range mentions {
					row := v.(map[string]any)
					if row["answered"] != wantAnswered {
						t.Errorf("MCP cross-topic answer: %v; want answered=%v", row, wantAnswered)
					}
				}
				body = call("list_unanswered", map[string]any{"include_group_mentions": true})
				chats, ok := body["chats"].([]any)
				if !ok {
					t.Fatalf("MCP triage: %v", body)
				}
				if (!wantAnswered && len(chats) != 1) || (wantAnswered && len(chats) != 0) {
					t.Errorf("MCP unanswered chats=%v; want answered=%v", chats, wantAnswered)
				}
			}
			assert(false)
			// An explicit reply cannot answer a mention in a different topic either.
			apply(raw(701, true, false, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 101, ReplyToTopID: 700}))
			assert(false)
			apply(raw(801, true, false, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 101, ReplyToTopID: 100}), raw(802, true, false, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 300}), raw(803, true, false, nil))
			assert(true)
		})
	}
}
