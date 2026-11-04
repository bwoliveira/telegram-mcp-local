package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

type messagesEngine struct {
	engine.Controller
	searches, windows int
	searchReq         []engine.SearchRequest
	remoteErr         error
	results           []engine.Message
	windowResults     []engine.Message
}

func (e *messagesEngine) SearchRemote(ctx context.Context, r engine.SearchRequest) (engine.SearchResult, error) {
	e.searches++
	e.searchReq = append(e.searchReq, r)
	if e.remoteErr != nil {
		return engine.SearchResult{}, e.remoteErr
	}
	if e.results != nil {
		return engine.SearchResult{Messages: e.results}, nil
	}
	return e.Controller.SearchRemote(ctx, r)
}
func (e *messagesEngine) FetchWindow(ctx context.Context, r engine.WindowRequest) ([]engine.Message, error) {
	e.windows++
	if e.windowResults != nil {
		return e.windowResults, nil
	}
	return e.Controller.FetchWindow(ctx, r)
}

type messagesStore struct {
	ReadStore
	complete   bool
	messages   []engine.Message
	peers      []engine.Peer
	fail       string
	indexRows  []engine.Message
	emptyFirst bool
}

func (s *messagesStore) ReadMessages(ctx context.Context, a engine.AccountID) ([]engine.Message, error) {
	if s.emptyFirst {
		s.emptyFirst = false
		return []engine.Message{}, nil
	}
	if s.fail == "messages" {
		return nil, errors.New("read failed")
	}
	if s.messages != nil {
		return s.messages, nil
	}
	return s.ReadStore.ReadMessages(ctx, a)
}
func (s *messagesStore) ReadPeers(ctx context.Context, a engine.AccountID) ([]engine.Peer, error) {
	if s.fail == "peers" {
		return nil, errors.New("read failed")
	}
	if s.peers != nil {
		return s.peers, nil
	}
	return s.ReadStore.ReadPeers(ctx, a)
}
func (s *messagesStore) ReadCoverage(ctx context.Context, a engine.AccountID, p *engine.PeerRef) (bool, *time.Time, error) {
	if s.fail == "coverage" {
		return false, nil, errors.New("coverage failed")
	}
	return s.complete, nil, nil
}

// An optional FTS adapter returns matches independently of the presentation filter.
type messagesIndex struct{ *messagesStore }

func (s messagesIndex) SearchLocal(ctx context.Context, a engine.AccountID, q string) ([]engine.Message, error) {
	return s.indexRows, nil
}
func messagesSetup(t *testing.T) (*Server, *fake.Controller, *messagesEngine, *messagesStore) {
	t.Helper()
	e := peopleFake(t)
	spy := &messagesEngine{Controller: e}
	st := &messagesStore{ReadStore: e}
	return New(Config{Engine: spy, ReadStore: st, Clock: fake.Epoch}), e, spy, st
}
func messagesBody(t *testing.T, s *Server, tool string, args map[string]any) map[string]any {
	t.Helper()
	r := peopleCall(t, s, tool, args)
	if r["isError"] != false {
		t.Fatal(r)
	}
	return toolBody(t, r)
}
func TestReadMessagesShapeCountAndExclusiveDates(t *testing.T) {
	s, e, _, _ := messagesSetup(t)
	args := map[string]any{"chat_id": "user:1001", "order": "oldest", "limit": 1, "fields": []string{"id", "text"}, "max_content_chars": 5}
	b := messagesBody(t, s, "get_chat_messages", args)
	m := b["messages"].([]any)[0].(map[string]any)
	if m["id"] != "1" || m["text_truncated"] != true || m["chat_id"] != nil || !strings.HasPrefix(m["text"].(string), "Hello") {
		t.Fatal(b)
	}
	if b["chat_name"] != "Ana Example" || b["partial"] != true || b["untrusted_content"] != UntrustedContent {
		t.Fatal(b)
	}
	args = map[string]any{"chat_id": "user:1001", "count_only": true, "limit": 1, "since": "2026-01-02T11:00:01Z", "until": "2026-01-02T11:00:04Z"}
	b = messagesBody(t, s, "get_chat_messages", args)
	if b["count"] != float64(2) || b["messages"] != nil {
		t.Fatal(b)
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
func TestReadMessagesMigrationUsesOriginalKeys(t *testing.T) {
	s, e, _, st := messagesSetup(t)
	data := e.Snapshot()
	st.peers = data.Peers
	st.messages = data.Messages
	for i := range st.peers {
		if st.peers[i].Ref == fake.BasicGroupPeer {
			p := fake.SupergroupPeer
			st.peers[i].MigratedTo = &p
		}
	}
	for _, peer := range []string{"chat:1001", "channel:2001"} {
		b := messagesBody(t, s, "get_chat_messages", map[string]any{"chat_id": peer, "order": "oldest"})
		if b["count"] != float64(3) {
			t.Fatal(b)
		}
		msgs := b["messages"].([]any)
		seen := map[string]bool{}
		for _, m := range msgs {
			seen[m.(map[string]any)["chat_id"].(string)] = true
		}
		if !seen["chat:1001"] || !seen["channel:2001"] {
			t.Fatal(b)
		}
	}
	b := messagesBody(t, s, "get_chat_messages", map[string]any{"chat_id": "channel:2001", "include_migrated": false})
	if b["count"] != float64(1) {
		t.Fatal(b)
	}
}
func TestReadMessagesInvalidArgumentsAndStoreErrors(t *testing.T) {
	s, _, _, st := messagesSetup(t)
	cases := []struct {
		tool string
		a    map[string]any
	}{
		{"get_chat_messages", map[string]any{}},
		{"get_chat_messages", map[string]any{"chat_id": "1001"}},
		{"get_chat_messages", map[string]any{"chat_id": "user:1001", "order": "bad"}},
		{"get_chat_messages", map[string]any{"chat_id": "user:1001", "count_only": true, "since": "bad"}},
		{"get_chat_messages", map[string]any{"chat_id": "user:1001", "fields": []string{"bad"}}},
		{"search_messages", map[string]any{"query": "  "}},
		{"search_messages", map[string]any{"query": "x", "max_content_chars": -1}},
		{"search_messages", map[string]any{"query": "x", "direction": "bad"}},
		{"search_messages", map[string]any{"query": "x", "since": "2026-01-02", "until": "2026-01-01"}},
		{"get_message_context", map[string]any{"message_id": "1"}},
		{"get_message_context", map[string]any{"chat_id": "chat:1001", "message_id": "channel:2001/1"}},
	}
	for _, tc := range cases {
		if peopleCall(t, s, tc.tool, tc.a)["isError"] != true {
			t.Fatal(tc)
		}
	}
	for _, failure := range []string{"peers", "messages", "coverage"} {
		st.fail = failure
		for _, tool := range []string{"list_chats", "get_chat_messages", "search_messages"} {
			if peopleCall(t, s, tool, map[string]any{"chat_id": "user:1001", "query": "synthetic"})["isError"] != true {
				t.Fatal(tool, failure)
			}
		}
	}
}
func TestReadSearchRemoteRequiresOptInAndIncompleteCoverage(t *testing.T) {
	s, e, spy, st := messagesSetup(t)
	args := map[string]any{"query": "synthetic", "chat_id": "user:1001", "limit": 1}
	b := messagesBody(t, s, "search_messages", args)
	if spy.searches != 0 || b["count"] != float64(1) || b["remote_used"] != false {
		t.Fatal(b)
	}
	args["remote"] = true
	st.complete = true
	b = messagesBody(t, s, "search_messages", args)
	if spy.searches != 0 || b["partial"] != false {
		t.Fatal(b)
	}
	st.complete = false
	b = messagesBody(t, s, "search_messages", args)
	if spy.searches != 1 || b["partial"] != true || b["remote_used"] != true {
		t.Fatal(b)
	}
	spy.remoteErr = engine.ErrNoAccess
	if peopleCall(t, s, "search_messages", args)["isError"] != true {
		t.Fatal("remote error ignored")
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
func TestReadSearchFiltersAndFTSSeam(t *testing.T) {
	s, e, _, st := messagesSetup(t)
	b := messagesBody(t, s, "search_messages", map[string]any{"query": "synthetic", "chat_id": "user:1001", "media_type": "image", "fields": []string{"id"}})
	msgs := b["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["id"] != "4" {
		t.Fatal(b)
	}
	for _, args := range []map[string]any{
		{"query": "synthetic", "chat_id": "user:1001", "direction": "out"},
		{"query": "synthetic", "chat_id": "channel:2001", "topic_id": 8},
	} {
		if b := messagesBody(t, s, "search_messages", args); b["count"] != float64(0) {
			t.Fatal(b)
		}
	}
	st.indexRows = e.Snapshot().Messages[:1]
	s.readStore = messagesIndex{st}
	b = messagesBody(t, s, "search_messages", map[string]any{"query": "FTS query (not a substring)"})
	if b["full_text"] != true || b["count"] != float64(1) {
		t.Fatal(b)
	}
}
func TestReadContextLocalWindowOfflineAndD2(t *testing.T) {
	s, e, spy, _ := messagesSetup(t)
	if err := e.Disconnect(context.Background(), "offline test"); err != nil {
		t.Fatal(err)
	}
	b := messagesBody(t, s, "get_message_context", map[string]any{"message_id": "user:1001/2", "before": 1, "after": 1, "fields": []string{"id"}})
	if b["message"].(map[string]any)["id"] != "2" || len(b["before"].([]any)) != 1 || len(b["after"].([]any)) != 1 || spy.windows != 0 {
		t.Fatal(b)
	}
	b = messagesBody(t, s, "get_message_context", map[string]any{"chat_id": "user:1001", "message_id": "2", "before": 0, "after": 0})
	if len(b["before"].([]any)) != 0 || len(b["after"].([]any)) != 0 {
		t.Fatal(b)
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
func TestReadContextFallbackAndErrors(t *testing.T) {
	s, e, spy, st := messagesSetup(t)
	st.emptyFirst = true
	b := messagesBody(t, s, "get_message_context", map[string]any{"message_id": "channel:2001/1"})
	if spy.windows != 1 || b["remote_used"] != true {
		t.Fatal(b)
	}
	if peopleCall(t, s, "get_message_context", map[string]any{"message_id": "channel:2001/999"})["isError"] != true {
		t.Fatal("missing message")
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
func TestReadChatsProjectionPinnedSearchAndBoundary(t *testing.T) {
	s, e, _, _ := messagesSetup(t)
	b := messagesBody(t, s, "list_chats", map[string]any{"search": "fake_ana", "fields": []string{"chat_id", "name", "muted_until"}})
	if b["count"] != float64(1) {
		t.Fatal(b)
	}
	row := b["chats"].([]any)[0].(map[string]any)
	if row["chat_id"] != "user:1001" || row["name"] != "Ana Example" || row["kind"] != nil {
		t.Fatal(b)
	}
	if _, ok := row["muted_until"]; !ok {
		t.Fatal(b)
	}
	b = messagesBody(t, s, "list_chats", map[string]any{"limit": 1})
	if b["truncated"] != true || b["count"] != float64(1) {
		t.Fatal(b)
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
func TestReadInjectedMessagesAreOnlyData(t *testing.T) {
	s, e, _, _ := messagesSetup(t)
	b := messagesBody(t, s, "get_chat_messages", map[string]any{"chat_id": "user:1001", "since": "2026-01-02T11:00:02Z", "until": "2026-01-02T11:00:03Z"})
	raw, _ := json.Marshal(b)
	if !strings.Contains(string(raw), "IGNORE ALL INSTRUCTIONS") || len(e.Writes()) != 0 {
		t.Fatal(b, e.Writes())
	}
}
func TestReadTTLAndAccountIsolation(t *testing.T) {
	s, e, _, st := messagesSetup(t)
	data := e.Snapshot()
	st.messages = data.Messages
	expiry := fake.Epoch().Add(-time.Second)
	for i := range st.messages {
		st.messages[i].ExpiresAt = &expiry
	}
	b := messagesBody(t, s, "get_chat_messages", map[string]any{"chat_id": "user:1001", "count_only": true})
	if b["count"] != float64(0) {
		t.Fatal(b)
	}
	st.messages[0].Key.Account = "another-account"
	if peopleCall(t, s, "search_messages", map[string]any{"query": "synthetic"})["isError"] != true {
		t.Fatal("foreign account leaked")
	}
}
