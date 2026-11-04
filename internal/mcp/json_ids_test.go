package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

// Decode with UseNumber: a float64 test oracle could silently round the same
// identifier it is supposed to protect.
func precisionToolBody(t *testing.T, result map[string]any) any {
	t.Helper()
	if result["isError"] != false {
		t.Fatalf("tool failed: %v", result)
	}
	content := result["content"].([]any)
	var body any
	decoder := json.NewDecoder(strings.NewReader(content[0].(map[string]any)["text"].(string)))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func scanPrecisionToolJSON(t *testing.T, value any, path string) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			scanPrecisionToolJSON(t, item, path+"."+key)
		}
	case []any:
		for i, item := range v {
			scanPrecisionToolJSON(t, item, fmt.Sprintf("%s[%d]", path, i))
		}
	case json.Number:
		if n, ok := new(big.Int).SetString(string(v), 10); ok && n.Abs(n).Cmp(big.NewInt(9007199254740991)) > 0 {
			t.Errorf("unsafe JSON number at %s: %s; opaque int64 ids must be decimal strings", path, v)
		}
	}
}

type precisionPollEngine struct {
	engine.Engine
	poll engine.Poll
}

func (e precisionPollEngine) PollResults(context.Context, engine.PollRequest) (engine.PollResults, error) {
	return engine.PollResults{Poll: e.poll}, nil
}

func TestWideIDJSONStoreExport(t *testing.T) {
	s, st, _, now := statsReal(t)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1001}
	statsApply(t, st, engine.Batch{
		Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Kind: engine.KindUser, Access: engine.AccessOK}}},
		Messages: []engine.Message{{
			Key: engine.MessageKey{Account: "people", Peer: peer, ID: 42}, Date: *now,
			Poll: &engine.Poll{ID: 9007199254740993, Question: "wide identifier"}, GroupedID: 9223372036854775807,
			Media: &engine.Media{Type: engine.MediaWebPage, WebPage: &engine.WebPageInfo{ID: -9223372036854775808}, Remote: []byte("opaque-media-credentials")},
		}},
	})
	body := precisionToolBody(t, toolCall(t, s, "export_messages", map[string]any{"chat_id": "user:1001"})).(map[string]any)
	scanPrecisionToolJSON(t, body, "export_messages")
	raw, err := os.ReadFile(body["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected header and message, got %d lines", len(lines))
	}
	for i, line := range lines {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		scanPrecisionToolJSON(t, value, fmt.Sprintf("export[%d]", i))
	}
	for _, want := range []string{`"id":"9007199254740993"`, `"id":"-9223372036854775808"`} {
		if !bytes.Contains(lines[1], []byte(want)) {
			t.Errorf("export lost exact decimal identifier %s: %s", want, lines[1])
		}
	}
	if bytes.Contains(raw, []byte("opaque-media-credentials")) {
		t.Fatal("export leaked opaque media credentials")
	}
}

func TestWideIDJSONToolResponses(t *testing.T) {
	for _, id := range []int64{9007199254740993, 9223372036854775807, -9223372036854775808} {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			s, base, remote, st := messagesSetup(t)
			peer := engine.PeerRef{Type: engine.PeerUser, ID: 1001}
			poll := engine.Poll{ID: id, Question: "wide identifier", Options: []engine.PollOption{{Key: "YQ==", Text: "yes"}}}
			m := engine.Message{Key: engine.MessageKey{Account: "people", Peer: peer, ID: 42}, Date: fake.Epoch(), Text: "wide identifier", GroupedID: id,
				Poll: &poll, Media: &engine.Media{Type: engine.MediaWebPage, WebPage: &engine.WebPageInfo{ID: id, URL: "https://example.invalid"}, Remote: []byte(`{"document_id":9223372036854775807,"photo_id":9223372036854775807,"access_hash":-9223372036854775808,"random_id":9223372036854775807}`)},
				Reactions: []engine.ReactionCount{{Emoji: "custom:" + strconv.FormatInt(id, 10), Count: 1}}}
			st.messages = []engine.Message{m}
			remote.results, remote.windowResults = []engine.Message{m}, []engine.Message{m}
			pollServer := New(Config{Engine: precisionPollEngine{Engine: base, poll: poll}})
			for _, tc := range []struct {
				name string
				s    *Server
				args map[string]any
			}{
				{"get_chat_messages", s, map[string]any{"chat_id": "user:1001"}},
				{"get_chat_messages", s, map[string]any{"chat_id": "user:1001", "fields": []string{"poll", "media", "reactions"}}},
				{"search_messages", s, map[string]any{"query": "wide"}},
				{"search_messages", s, map[string]any{"query": "wide", "fields": []string{"poll", "media"}}},
				{"search_messages", s, map[string]any{"query": "wide", "remote": true, "chat_id": "user:1001"}},
				{"get_message_context", s, map[string]any{"chat_id": "user:1001", "message_id": "42"}},
				{"get_message_context", s, map[string]any{"chat_id": "user:1001", "message_id": "42", "fields": []string{"poll", "media"}}},
				{"get_poll_results", pollServer, map[string]any{"chat_id": "user:1001", "message_id": "42"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					// Exercise the complete JSON-RPC tools/call transport, not just DTOs.
					body := precisionToolBody(t, toolCall(t, tc.s, tc.name, tc.args))
					scanPrecisionToolJSON(t, body, tc.name)
					raw, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Contains(raw, []byte(`"id":"`+strconv.FormatInt(id, 10)+`"`)) {
						t.Errorf("exact decimal id missing from response: %s", raw)
					}
					for _, forbidden := range []string{"document_id", "photo_id", "access_hash", "random_id", "remote"} {
						// remote_used is safe provenance, unlike opaque Media.Remote.
						if bytes.Contains(raw, []byte(`"`+forbidden+`"`)) {
							t.Errorf("opaque media field %s leaked: %s", forbidden, raw)
						}
					}
				})
			}
		})
	}
}
