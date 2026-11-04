package mcp

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"testing"
)

type transcriptStore struct{ ReadStore }

func (s transcriptStore) ReadTranscript(ctx context.Context, k engine.MessageKey) (state.Transcript, bool, error) {
	if k.Peer == fake.PrivatePeer && k.ID == 5 {
		return state.Transcript{Text: "Distinctive spoken words from fake audio.", Raw: "Original distinctive transcript."}, true, nil
	}
	return state.Transcript{}, false, nil
}
func TestReadTranscriptSearchAndClipping(t *testing.T) {
	s, e, _, st := messagesSetup(t)
	s.readStore = transcriptStore{st}
	b := messagesBody(t, s, "search_messages", map[string]any{"query": "Distinctive spoken", "max_content_chars": 10, "fields": []string{"id", "transcript"}})
	if b["count"] != float64(1) {
		t.Fatal(b)
	}
	m := b["messages"].([]any)[0].(map[string]any)
	if m["id"] != "5" || m["text_truncated"] != true {
		t.Fatal(b)
	}
	tr := m["transcript"].(map[string]any)
	if tr["raw"] != nil && tr["raw"] != "" {
		t.Fatal(b)
	}
	b = messagesBody(t, s, "get_chat_messages", map[string]any{"chat_id": "user:1001", "since": "2026-01-02T11:00:05Z", "until": "2026-01-02T11:00:06Z"})
	if b["messages"].([]any)[0].(map[string]any)["transcript"] == nil {
		t.Fatal(b)
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
func TestReadRemoteMigrationAndMediaTranslation(t *testing.T) {
	s, e, spy, st := messagesSetup(t)
	st.peers = e.Snapshot().Peers
	for i := range st.peers {
		if st.peers[i].Ref == fake.BasicGroupPeer {
			p := fake.SupergroupPeer
			st.peers[i].MigratedTo = &p
		}
	}
	messagesBody(t, s, "search_messages", map[string]any{"query": "Hello", "chat_id": "channel:2001", "remote": true, "media_type": "image"})
	if len(spy.searchReq) != 2 {
		t.Fatal(spy.searchReq)
	}
	for _, r := range spy.searchReq {
		if r.Media != engine.MediaPhoto || r.Peer == nil {
			t.Fatal(r)
		}
	}
}
func TestReadHealthUsesStoreActivityAndCoverage(t *testing.T) {
	s, _, _, st := messagesSetup(t)
	b := messagesBody(t, s, "health", nil)
	activity := b["activity"].(map[string]any)
	if activity["messages_last_24h"].(float64) == 0 {
		t.Fatal(b)
	}
	checks := b["checks"].([]any)
	var coverage map[string]any
	for _, c := range checks {
		row := c.(map[string]any)
		if row["name"] == "coverage" {
			coverage = row
		}
	}
	if coverage == nil || coverage["status"] != "info" {
		t.Fatal(b)
	}
	st.complete = true
	b = messagesBody(t, s, "health", nil)
	for _, c := range b["checks"].([]any) {
		row := c.(map[string]any)
		if row["name"] == "coverage" && row["status"] != "ok" {
			t.Fatal(b)
		}
	}
	b = messagesBody(t, s, "telegram_status", nil)
	if b["index"].(map[string]any)["coverage_complete"] != true {
		t.Fatal(b)
	}
	if peopleCall(t, s, "health", map[string]any{"max_silence_hours": -1})["isError"] != true {
		t.Fatal("invalid silence")
	}
}
