package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestWideIDJSONWebhookDeliveries(t *testing.T) {
	m, _ := manager(t)
	ctx := context.Background()
	srv, deliveries := wireServer(t, nil)
	_, secret, err := m.Create(ctx, Settings{URL: ptr(srv.URL), Events: ptr([]string{"message", "edit"})})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []int64{9007199254740993, 9223372036854775807, -9223372036854775808} {
		for _, kind := range []engine.EventKind{engine.EventMessage, engine.EventEdit} {
			t.Run(string(kind)+"/"+strconv.FormatInt(id, 10), func(t *testing.T) {
				message := engine.Message{Key: engine.MessageKey{Account: "test", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1001}, ID: i + 1}, Date: fake.Epoch(),
					Poll: &engine.Poll{ID: id, Question: "wide identifier"}, GroupedID: id,
					Media:     &engine.Media{Type: engine.MediaDocument, Remote: []byte(`{"document_id":9223372036854775807,"photo_id":9223372036854775807,"access_hash":-9223372036854775808,"random_id":9223372036854775807}`)},
					Reactions: []engine.ReactionCount{{Emoji: "custom:" + strconv.FormatInt(id, 10), Count: 1}}}
				ev, ok := project(engine.Event{ID: string(kind) + strconv.Itoa(i), Kind: kind, Account: "test", Source: engine.SourceUpdate, Message: &message})
				if !ok {
					t.Fatal("event projection rejected fixture")
				}
				m.Dispatch(ctx, ev)
				got := receive(t, deliveries)
				if got.header.Get("X-Telegram-MCP-Signature") != Sign(secret, got.body) {
					t.Fatal("signature differs from exact delivered bytes")
				}
				var body map[string]any
				if err := json.Unmarshal(got.body, &body); err != nil {
					t.Fatal(err)
				}
				wireMessage := body["message"].(map[string]any)
				pollID := wireMessage["poll"].(map[string]any)["id"]
				if pollID != strconv.FormatInt(id, 10) {
					t.Errorf("webhook poll.id = %#v (%T); want exact decimal string %q", pollID, pollID, strconv.FormatInt(id, 10))
				}
				for _, forbidden := range []string{"document_id", "photo_id", "access_hash", "random_id", "remote"} {
					if bytes.Contains(got.body, []byte(`"`+forbidden+`"`)) {
						t.Errorf("opaque media field %s leaked: %s", forbidden, got.body)
					}
				}
			})
		}
	}
}
