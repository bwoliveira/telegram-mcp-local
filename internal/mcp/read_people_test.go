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

func peopleCall(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return s.call(context.Background(), callParams{Name: name, Arguments: raw})
}

func peopleArgs(tool string) map[string]any {
	if tool == "get_group" {
		return map[string]any{"group_id": "chat:1001"}
	}
	return map[string]any{}
}

func peopleFake(t *testing.T) *fake.Controller {
	t.Helper()
	c, err := fake.New(engine.Config{Account: "people", Mode: engine.ModeReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*fake.Controller)
}

type peopleTestStore struct {
	ReadStore
	fail     string
	override bool
	peers    []engine.Peer
	dialogs  []engine.Dialog
	messages []engine.Message
}

func (p *peopleTestStore) ReadPeers(ctx context.Context, acc engine.AccountID) ([]engine.Peer, error) {
	if p.fail == "peers" {
		return nil, errors.New("people peers unavailable")
	}
	if p.override {
		return p.peers, nil
	}
	return p.ReadStore.ReadPeers(ctx, acc)
}
func (p *peopleTestStore) ReadDialogs(ctx context.Context, acc engine.AccountID) ([]engine.Dialog, error) {
	if p.fail == "dialogs" {
		return nil, errors.New("people dialogs unavailable")
	}
	if p.override {
		return p.dialogs, nil
	}
	return p.ReadStore.ReadDialogs(ctx, acc)
}
func (p *peopleTestStore) ReadMessages(ctx context.Context, acc engine.AccountID) ([]engine.Message, error) {
	if p.fail == "messages" {
		return nil, errors.New("people messages unavailable")
	}
	if p.override {
		return p.messages, nil
	}
	return p.ReadStore.ReadMessages(ctx, acc)
}
func (p *peopleTestStore) ReadGroup(ctx context.Context, acc engine.AccountID, peer engine.PeerRef) (engine.Group, error) {
	if p.fail == "group" {
		return engine.Group{}, errors.New("people group unavailable")
	}
	return p.ReadStore.ReadGroup(ctx, acc, peer)
}
func (p *peopleTestStore) ReadCoverage(ctx context.Context, acc engine.AccountID, peer *engine.PeerRef) (bool, *time.Time, error) {
	if p.fail == "coverage" {
		return false, nil, errors.New("people coverage unavailable")
	}
	return p.ReadStore.ReadCoverage(ctx, acc, peer)
}

type peopleTestEngine struct {
	engine.Controller
	accountErr error
	groupErr   error
	groupCalls int
}

func (p *peopleTestEngine) Account(ctx context.Context) (engine.Account, error) {
	if p.accountErr != nil {
		return engine.Account{}, p.accountErr
	}
	return p.Controller.Account(ctx)
}
func (p *peopleTestEngine) RefreshGroup(ctx context.Context, req engine.GroupRequest) (engine.Group, error) {
	p.groupCalls++
	if p.groupErr != nil {
		return engine.Group{}, p.groupErr
	}
	return p.Controller.RefreshGroup(ctx, req)
}

func TestPeopleErrors(t *testing.T) {
	f := peopleFake(t)
	cases := []struct{ tool, fail string }{
		{"list_contacts", "peers"}, {"list_contacts", "coverage"},
		{"list_groups", "peers"}, {"list_groups", "coverage"},
		{"get_group", "group"}, {"get_group", "coverage"},
		{"list_unread", "dialogs"}, {"list_unread", "peers"}, {"list_unread", "messages"}, {"list_unread", "coverage"},
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.fail, func(t *testing.T) {
			s := New(Config{Engine: f, ReadStore: &peopleTestStore{ReadStore: f, fail: tc.fail}})
			r := peopleCall(t, s, tc.tool, peopleArgs(tc.tool))
			if r["isError"] != true || !strings.Contains(toolBody(t, r)["error"].(string), tc.fail+" unavailable") {
				t.Fatalf("store error not propagated: %v", r)
			}
		})
	}
	for _, tool := range []string{"list_contacts", "list_groups", "get_group", "list_unread"} {
		args := peopleArgs(tool)
		for _, tc := range []struct {
			name   string
			server *Server
			match  string
		}{
			{"engine", New(Config{ReadStore: f}), "engine is not configured"},
			{"store", New(Config{Engine: &peopleTestEngine{Controller: f}}), "not implemented yet"},
			{"account", New(Config{Engine: &peopleTestEngine{Controller: f, accountErr: engine.ErrAccountMismatch}, ReadStore: f}), "account"},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				r := peopleCall(t, tc.server, tool, args)
				if r["isError"] != true || !strings.Contains(strings.ToLower(toolBody(t, r)["error"].(string)), strings.ToLower(tc.match)) {
					t.Fatalf("%s error: %v", tc.name, r)
				}
			})
		}
	}
	for _, args := range []map[string]any{{}, {"group_id": "user:1001"}, {"group_id": "invalid"}, {"group_id": "chat:0"}, {"group_id": "chat:1001", "chat_id": "channel:1001"}} {
		r := peopleCall(t, New(Config{Engine: f, ReadStore: f}), "get_group", args)
		if r["isError"] != true {
			t.Fatalf("invalid group accepted: %v", args)
		}
	}
	e := &peopleTestEngine{Controller: f, groupErr: engine.ErrNoAccess}
	s := New(Config{Engine: e, ReadStore: f})
	r := peopleCall(t, s, "get_group", map[string]any{"group_id": "chat:1001", "live": true})
	if r["isError"] != true || e.groupCalls != 1 {
		t.Fatalf("live error: %v calls=%d", r, e.groupCalls)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("error paths wrote")
	}
}

func TestPeopleUnreadFiltersLimitsAndMissingPreviews(t *testing.T) {
	f := peopleFake(t)
	now := fake.Epoch()
	until := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	store := &peopleTestStore{ReadStore: f, override: true}
	for i := 1; i <= 105; i++ {
		ref := engine.PeerRef{Type: engine.PeerUser, ID: int64(i)}
		store.peers = append(store.peers, engine.Peer{Ref: ref, Kind: engine.KindUser, FirstName: "Contact"})
		d := engine.Dialog{Peer: ref, UnreadCount: 30}
		if i == 1 {
			d.MuteUntil = &until
		}
		if i == 2 {
			d.FolderID = 1
		}
		if i == 3 {
			d.UnreadCount = 0
			d.UnreadMark = true
		}
		if i == 4 {
			d.MuteUntil = &past
		}
		store.dialogs = append(store.dialogs, d)
		if i == 5 {
			continue
		}
		for j := 1; j <= 30; j++ {
			store.messages = append(store.messages, engine.Message{Key: engine.MessageKey{Account: "people", Peer: ref, ID: j}, Date: now.Add(time.Duration(j) * time.Second), Text: "Preview"})
		}
	}
	// Invalid scope and outgoing/expired rows must never become previews.
	store.messages = append(store.messages,
		engine.Message{Key: engine.MessageKey{Account: "other", Peer: store.peers[0].Ref, ID: 999}, Text: "other account"},
		engine.Message{Key: engine.MessageKey{Account: "people", Peer: store.peers[0].Ref, ID: 998}, Out: true, Text: "outgoing"},
		engine.Message{Key: engine.MessageKey{Account: "people", Peer: store.peers[0].Ref, ID: 997}, ExpiresAt: &past, Text: "expired"},
	)
	s := New(Config{Engine: f, ReadStore: store, Clock: fake.Epoch})
	b := toolBody(t, peopleCall(t, s, "list_unread", map[string]any{"limit": 999, "per_chat": 999}))
	rows := b["chats"].([]any)
	if len(rows) != 100 || b["truncated"] != true {
		t.Fatalf("chat limit: %v", b["count"])
	}
	for _, v := range rows {
		row := v.(map[string]any)
		if row["chat_id"] == "user:2" {
			t.Fatal("archived included by default")
		}
		for _, m := range row["messages"].([]any) {
			if m.(map[string]any)["text"] != "Preview" {
				t.Fatalf("invalid preview: %v", m)
			}
		}
		if len(row["messages"].([]any)) > 20 {
			t.Fatal("per_chat maximum exceeded")
		}
	}
	b = toolBody(t, peopleCall(t, s, "list_unread", map[string]any{"limit": 100, "include_muted": false, "include_archived": true}))
	rows = b["chats"].([]any)
	for _, v := range rows {
		row := v.(map[string]any)
		if row["chat_id"] == "user:1" {
			t.Fatal("muted included")
		}
		if len(row["messages"].([]any)) > 5 {
			t.Fatal("per_chat default exceeded")
		}
	}
	// Narrow the dataset so missing previews, marks, defaults, expired mute and
	// archived inclusion are all verified independently from the global cap.
	store.dialogs = store.dialogs[:5]
	b = toolBody(t, peopleCall(t, s, "list_unread", map[string]any{}))
	rows = b["chats"].([]any)
	if len(rows) != 4 {
		t.Fatalf("default filters: %v", b)
	}
	found := map[string]map[string]any{}
	for _, v := range rows {
		row := v.(map[string]any)
		found[row["chat_id"].(string)] = row
	}
	if found["user:1"] == nil || found["user:3"]["marked_unread"] != true || found["user:4"]["muted"] != false || len(found["user:5"]["messages"].([]any)) != 0 {
		t.Fatalf("default filters/missing previews: %v", b)
	}
	if b["unread_messages"] != float64(90) {
		t.Fatalf("counter sum must not be preview count: %v", b)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("unread filters wrote")
	}
}

func TestPeopleOfflineAndSearchEmpty(t *testing.T) {
	f := peopleFake(t)
	if err := f.Disconnect(context.Background(), "people offline read test"); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: f, ReadStore: f})
	for _, tool := range []string{"list_contacts", "list_groups", "list_unread", "get_group"} {
		r := peopleCall(t, s, tool, peopleArgs(tool))
		if r["isError"] != false {
			t.Fatalf("offline %s: %v", tool, toolBody(t, r))
		}
	}
	for _, tool := range []string{"list_contacts", "list_groups"} {
		b := toolBody(t, peopleCall(t, s, tool, map[string]any{"search": "no-match"}))
		key := "contacts"
		if tool == "list_groups" {
			key = "groups"
		}
		if b["count"] != float64(0) || len(b[key].([]any)) != 0 {
			t.Fatalf("empty %s: %v", tool, b)
		}
	}
	if r := peopleCall(t, s, "get_group", map[string]any{"group_id": "chat:1001", "live": true}); r["isError"] != true {
		t.Fatalf("offline live refresh succeeded: %v", r)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("offline reads wrote")
	}
}

func TestPeopleGroupsAndSnapshot(t *testing.T) {
	f := peopleFake(t)
	s := New(Config{Engine: f, ReadStore: f})
	b := toolBody(t, peopleCall(t, s, "list_groups", map[string]any{}))
	rows, ok := b["groups"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("groups: %v", b)
	}
	kinds := map[string]bool{}
	for _, v := range rows {
		row := v.(map[string]any)
		kinds[row["kind"].(string)] = true
	}
	if !kinds["group"] || !kinds["supergroup"] || !kinds["channel"] {
		t.Fatalf("kinds: %v", kinds)
	}
	for _, live := range []bool{false, true} {
		b = toolBody(t, peopleCall(t, s, "get_group", map[string]any{"group_id": "channel:2002", "live": live, "search": "ANA"}))
		if b["group_id"] != "channel:2002" || b["name"] != "Synthetic channel" || b["kind"] != "channel" || b["owner"] != "user:1000" || b["size"] != float64(42) || b["partial"] != true {
			t.Fatalf("group shape live=%v: %v", live, b)
		}
		participants := b["participants"].([]any)
		if len(participants) != 1 || participants[0].(map[string]any)["chat_id"] != "user:1001" {
			t.Fatalf("participants: %v", b)
		}
		if b["created_at"] == nil || b["snapshot_at"] == nil {
			t.Fatalf("timestamps: %v", b)
		}
	}
	if len(f.Writes()) != 0 {
		t.Fatal("group reads wrote to Telegram")
	}
}

func TestPeopleUnreadReadOnly(t *testing.T) {
	f := peopleFake(t)
	s := New(Config{Engine: f, ReadStore: f})
	before := f.Snapshot()
	b := toolBody(t, peopleCall(t, s, "list_unread", map[string]any{"per_chat": 1}))
	rows, ok := b["chats"].([]any)
	if !ok || len(rows) != 4 {
		t.Fatalf("unread: %v", b)
	}
	if b["unread_messages"] != float64(10) {
		t.Fatalf("server counters: %v", b)
	}
	for _, v := range rows {
		row := v.(map[string]any)
		msgs := row["messages"].([]any)
		if len(msgs) > 1 {
			t.Fatalf("per_chat: %v", row)
		}
		for _, m := range msgs {
			if m.(map[string]any)["from_me"] != false {
				t.Fatalf("outgoing preview: %v", m)
			}
		}
	}
	after := f.Snapshot()
	for i, d := range before.Dialogs {
		if d.UnreadCount != after.Dialogs[i].UnreadCount || d.ReadInboxMaxID != after.Dialogs[i].ReadInboxMaxID {
			t.Fatal("read counters changed")
		}
	}
	if len(f.Writes()) != 0 {
		t.Fatal("unread wrote to Telegram")
	}
}

func TestPeopleContactsIncludeNamelessNoncontacts(t *testing.T) {
	f := peopleFake(t)
	store := &peopleTestStore{ReadStore: f, override: true, peers: []engine.Peer{
		{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 1}, Kind: engine.KindBot, Username: "nameless", IsContact: false},
		{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 2}, Kind: engine.KindUser, FirstName: "Named", Phone: "55110000", IsContact: true},
		{Ref: engine.PeerRef{Type: engine.PeerChat, ID: 1}, Kind: engine.KindGroup, Title: "Not a contact"},
	}}
	s := New(Config{Engine: f, ReadStore: store})
	b := toolBody(t, peopleCall(t, s, "list_contacts", map[string]any{}))
	rows := b["contacts"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["chat_id"] != "user:1" || rows[0].(map[string]any)["is_contact"] != false {
		t.Fatalf("nameless noncontact: %v", b)
	}
	b = toolBody(t, peopleCall(t, s, "list_contacts", map[string]any{"search": "55110000", "limit": 1}))
	if b["count"] != float64(1) || b["contacts"].([]any)[0].(map[string]any)["chat_id"] != "user:2" {
		t.Fatalf("phone search: %v", b)
	}
	b = toolBody(t, peopleCall(t, s, "list_contacts", map[string]any{"limit": 1}))
	if b["count"] != float64(1) || b["truncated"] != true {
		t.Fatalf("contact limit: %v", b)
	}
	b = toolBody(t, peopleCall(t, New(Config{Engine: f, ReadStore: f}), "list_groups", map[string]any{"search": "FAKE_CHANNEL", "limit": 1}))
	if b["count"] != float64(1) || b["groups"].([]any)[0].(map[string]any)["group_id"] != "channel:2002" {
		t.Fatalf("group search: %v", b)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("contact search wrote")
	}
}

func TestPeopleContactsReadStore(t *testing.T) {
	f := peopleFake(t)
	s := New(Config{Engine: f, ReadStore: f})
	r := peopleCall(t, s, "list_contacts", map[string]any{"search": "FAKE_ANA"})
	if r["isError"] != false {
		t.Fatalf("local contacts: %v", toolBody(t, r))
	}
	b := toolBody(t, r)
	rows := b["contacts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("contacts: %v", b)
	}
	row := rows[0].(map[string]any)
	if row["chat_id"] != "user:1001" || row["name"] != "Ana Example" || row["kind"] != "user" || row["is_contact"] != true {
		t.Fatalf("contact shape: %v", row)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("contacts wrote to Telegram")
	}
}
