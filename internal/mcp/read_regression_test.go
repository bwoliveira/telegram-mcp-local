package mcp

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"testing"
)

type firstWindowStore struct {
	ReadStore
	first []engine.Message
}

func (s *firstWindowStore) ReadMessages(ctx context.Context, a engine.AccountID) ([]engine.Message, error) {
	if s.first != nil {
		rows := s.first
		s.first = nil
		return rows, nil
	}
	return s.ReadStore.ReadMessages(ctx, a)
}
func TestReadContextComplementsMissingNeighbors(t *testing.T) {
	s, e, spy, st := messagesSetup(t)
	rows := e.Snapshot().Messages
	var center engine.Message
	for _, m := range rows {
		if m.Key.Peer == fake.PrivatePeer && m.Key.ID == 2 {
			center = m
		}
	}
	s.readStore = &firstWindowStore{ReadStore: st, first: []engine.Message{center}}
	b := messagesBody(t, s, "get_message_context", map[string]any{"message_id": "user:1001/2", "before": 1, "after": 1})
	if spy.windows != 1 || len(b["before"].([]any)) != 1 || len(b["after"].([]any)) != 1 || b["partial"] != true {
		t.Fatal(b, spy.windows)
	}
}
func TestReadSearchRevalidatesQueryAfterConcurrentEdit(t *testing.T) {
	s, e, spy, st := messagesSetup(t)
	old := e.Snapshot().Messages[0]
	old.Text = "matching-review-token"
	edited := old
	edited.Text = "edited-unrelated-content"
	st.messages = []engine.Message{edited}
	s.readStore = &firstWindowStore{ReadStore: st, first: []engine.Message{old}}
	spy.results = []engine.Message{}
	b := messagesBody(t, s, "search_messages", map[string]any{"query": "matching-review-token", "remote": true})
	if b["count"] != float64(0) || spy.searches != 1 {
		t.Fatal(b)
	}
}

func TestReadRemoteCannotReintroduceInvisibleRows(t *testing.T) {
	s, e, spy, st := messagesSetup(t)
	var old engine.Message
	for _, m := range e.Snapshot().Messages {
		if m.Key.Peer == fake.PrivatePeer && m.Key.ID == 1 {
			old = m
		}
	}
	st.messages = []engine.Message{}
	spy.results = []engine.Message{old}
	b := messagesBody(t, s, "search_messages", map[string]any{"query": "Hello", "chat_id": "user:1001", "remote": true})
	if b["count"] != float64(0) || spy.searches != 1 {
		t.Fatal(b)
	}
	spy.windowResults = []engine.Message{old}
	r := peopleCall(t, s, "get_message_context", map[string]any{"message_id": "user:1001/1"})
	if r["isError"] != true || spy.windows != 1 {
		t.Fatal(r)
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
