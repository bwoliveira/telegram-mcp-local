package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestGHIWriteToolsResolveConversationForms(t *testing.T) {
	s, f, _ := parityServer(t)
	upload := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(upload, []byte("synthetic upload"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, field string
		args        map[string]any
	}{
		{"send_text_message", "to", map[string]any{"text": "hello"}},
		{"send_media_message", "to", map[string]any{"type": "document", "url": upload}},
		{"send_location", "to", map[string]any{"latitude": -23.0, "longitude": -46.0}},
		{"send_poll", "to", map[string]any{"question": "Choice?", "options": []string{"A", "B"}}},
		{"edit_message", "chat_id", map[string]any{"message_id": "8", "text": "updated"}},
		{"delete_message", "chat_id", map[string]any{"message_id": "8"}},
		{"react_to_message", "chat_id", map[string]any{"message_id": "8"}},
		{"forward_message", "chat_id", map[string]any{"message_id": "8", "to": "user:1001"}},
		{"forward_message", "to", map[string]any{"message_id": "user:1001/1"}},
		{"mark_chat_read", "chat_id", nil},
		{"send_typing", "to", nil},
		{"organise_chat", "chat_id", map[string]any{"action": "archive"}},
	}
	forms := []string{"user:1000", "me", "SELF", "saved", "@FAKE_SELF", "https://t.me/fake_self/8", "t.me/s/fake_self", "Test Account", "1000"}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.field, func(t *testing.T) {
			var canonical map[string]any
			for _, input := range forms {
				t.Run(input, func(t *testing.T) {
					args := map[string]any{}
					for k, v := range tc.args {
						args[k] = v
					}
					args[tc.field] = input
					r := toolCall(t, s, tc.name, args)
					paritySuccess(t, r)
					_ = previewFingerprint(t, r)
					preview := toolBody(t, r)["preview"].(map[string]any)
					delete(preview, "fingerprint")
					if canonical == nil {
						canonical = preview
					} else if !reflect.DeepEqual(preview, canonical) {
						t.Fatalf("resolved preview differs from canonical ID: %+v != %+v", preview, canonical)
					}
				})
			}
		})
	}
	if len(f.Writes()) != 0 {
		t.Fatal("resolution/preview mutated Telegram", f.Writes())
	}
}

func TestGHIReadAndLocalToolsResolveConversationForms(t *testing.T) {
	s, f, _ := parityServer(t)
	for _, input := range []string{"user:1000", "me", "self", "saved", "@fake_self", "t.me/fake_self", "https://t.me/s/fake_self/8", "Test Account", "1000"} {
		for _, name := range []string{"get_profile_picture", "message_stats", "export_messages", "list_mentions", "mark_handled", "snooze_chat", "sync_history"} {
			t.Run(name+"/"+input, func(t *testing.T) {
				args := map[string]any{"chat_id": input}
				switch name {
				case "get_profile_picture":
					args = map[string]any{"to": input}
				case "snooze_chat":
					args["until"] = fake.Epoch().Add(time.Hour).Format(time.RFC3339)
				case "sync_history":
					args["rounds"] = 1
					args["count"] = 10
				}
				body := paritySuccess(t, toolCall(t, s, name, args))
				if name == "sync_history" {
					parityWaitHistory(t, s)
					if got := s.HistoryStatus().Chats; !reflect.DeepEqual(got, []string{"user:1000"}) {
						t.Fatal(got)
					}
				}
				if name == "mark_handled" || name == "snooze_chat" {
					if body["chat_id"] != "user:1000" {
						t.Fatal(body)
					}
				}
			})
		}
	}
	if len(f.Writes()) != 0 {
		t.Fatal("local tools wrote Telegram", f.Writes())
	}
}

func TestGroupToolsResolveFormsAndRejectUserAliases(t *testing.T) {
	s, f, _ := parityServer(t)
	cases := []struct {
		name    string
		args    map[string]any
		preview bool
	}{
		{"get_group", map[string]any{"live": true}, false},
		{"manage_group_participants", map[string]any{"action": "add", "participants": []string{"@fake_ana"}}, true},
		{"update_group", map[string]any{"name": "Updated"}, true},
		{"get_group_invite_link", nil, false},
		{"get_group_invite_link", map[string]any{"reset": true}, true},
		{"leave_group", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var canonical map[string]any
			for _, input := range []string{"channel:2001", "@FAKE_SUPERGROUP", "t.me/fake_supergroup", "https://t.me/s/fake_supergroup/7", "Synthetic supergroup", "2001"} {
				t.Run(input, func(t *testing.T) {
					a := map[string]any{"group_id": input}
					for k, v := range tc.args {
						a[k] = v
					}
					r := toolCall(t, s, tc.name, a)
					paritySuccess(t, r)
					if tc.preview {
						_ = previewFingerprint(t, r)
						preview := toolBody(t, r)["preview"].(map[string]any)
						delete(preview, "fingerprint")
						if canonical == nil {
							canonical = preview
						} else if !reflect.DeepEqual(preview, canonical) {
							t.Fatal("group resolves differently", input)
						}
					}
				})
			}
			for _, input := range []string{"user:1000", "me", "self", "saved", "@fake_self", "t.me/fake_self", "Test Account", "1000"} {
				a := map[string]any{"group_id": input}
				for k, v := range tc.args {
					a[k] = v
				}
				r := toolCall(t, s, tc.name, a)
				if r["isError"] != true || !strings.Contains(toolBody(t, r)["error"].(string), "chat or channel") {
					t.Fatalf("accepted user as group %s: %+v", input, r)
				}
			}
		})
	}
	// Participant aliases also flow through the resolver, not only group_id.
	var canonical map[string]any
	for _, input := range []string{"user:1001", "@fake_ana", "t.me/fake_ana", "Ana Example"} {
		r := toolCall(t, s, "manage_group_participants", map[string]any{"group_id": "channel:2001", "action": "add", "participants": []string{input}})
		_ = previewFingerprint(t, r)
		preview := toolBody(t, r)["preview"].(map[string]any)
		delete(preview, "fingerprint")
		if canonical == nil {
			canonical = preview
		} else if !reflect.DeepEqual(preview, canonical) {
			t.Fatal("participant alias differs", input)
		}
	}
	if len(f.Writes()) != 0 {
		t.Fatal("group resolution/preview wrote Telegram", f.Writes())
	}
}

func TestPollResultsResolveConversationForms(t *testing.T) {
	s, f, st := parityServer(t)
	// Basic groups have no public username. A synthetic cached alias still
	// proves the shared resolver path without pretending Telegram assigned one.
	if _, err := st.Apply(context.Background(), engine.Batch{Account: "primary", Source: engine.SourceUpdate, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: fake.BasicGroupPeer, Kind: engine.KindGroup, Title: "Synthetic basic group", Username: "local_basic", Access: engine.AccessOK}}}}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"chat:1001", "@local_basic", "t.me/local_basic", "https://t.me/local_basic/7", "Synthetic basic group"} {
		t.Run(input, func(t *testing.T) {
			paritySuccess(t, toolCall(t, s, "get_poll_results", map[string]any{"chat_id": input, "message_id": "7"}))
		})
	}
	if len(f.Writes()) != 0 {
		t.Fatal(f.Writes())
	}
}

type keyLookupTrap struct {
	engine.Engine
	calls int
}

func (e *keyLookupTrap) Account(context.Context) (engine.Account, error) {
	e.calls++
	return engine.Account{}, errors.New("lookup forbidden")
}
func TestResolvedMessageKeysKeepCompoundIdentityStrict(t *testing.T) {
	s, _, _ := parityServer(t)
	for _, a := range []arguments{
		{ChatJID: "me", MessageID: "user:1000/8"},
		{ChatJID: "https://t.me/fake_self/8", MessageID: "user:1000/8"},
		{ChatJID: "Test Account", MessageID: "8"},
		{MessageID: "user:1000/8"},
	} {
		k, fail := s.key(context.Background(), a)
		if fail != nil || k.Peer != fake.SelfPeer || k.ID != 8 || k.Account != "primary" {
			t.Fatalf("key %+v: %+v %v", a, k, fail)
		}
	}
	trap := &keyLookupTrap{}
	s = New(Config{Engine: trap})
	for _, a := range []arguments{
		{ChatJID: "me", MessageID: "08"}, {ChatJID: "me", MessageID: "0"},
		{ChatJID: "me", MessageID: "2147483648"}, {MessageID: "me/8"},
		{MessageID: "https://t.me/fake_self/8"}, {MessageID: "user:01000/8"},
		{ChatJID: "me", MessageID: "user:1000/08"},
	} {
		if _, fail := s.key(context.Background(), a); fail == nil {
			t.Fatalf("accepted malformed message key: %+v", a)
		}
	}
	if trap.calls != 0 {
		t.Fatalf("malformed identity looked up external state %d times", trap.calls)
	}
	s, _, _ = parityServer(t)
	for _, a := range []arguments{{ChatJID: "me", MessageID: "user:1001/8"}, {ChatJID: "user:01000", MessageID: "8"}} {
		if _, fail := s.key(context.Background(), a); fail == nil {
			t.Fatalf("accepted mismatch/malformed typed peer: %+v", a)
		}
	}
}

func TestCanonicalPeersAndLocalMarksWorkOffline(t *testing.T) {
	// Canonical tokens are parsed without requiring a configured engine at all.
	empty := New(Config{})
	for _, input := range []string{"user:1", "chat:2", "channel:3"} {
		if _, err := empty.resolvePeer(context.Background(), input); err != nil {
			t.Fatal(input, err)
		}
	}
	for _, state := range []string{"disconnected", "logged_out"} {
		t.Run(state, func(t *testing.T) {
			s, f, st := parityServer(t)
			if state == "disconnected" {
				if err := f.Disconnect(context.Background(), "offline test"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.LogOut(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			paritySuccess(t, toolCall(t, s, "mark_handled", map[string]any{"chat_id": "user:1001"}))
			marks, err := st.ReadTriageMarks(context.Background(), "primary")
			if err != nil || marks[fake.PrivatePeer].HandledAt == nil {
				t.Fatal(marks, err)
			}
			paritySuccess(t, toolCall(t, s, "snooze_chat", map[string]any{"chat_id": "user:1001", "until": fake.Epoch().Add(time.Hour).Format(time.RFC3339)}))
			marks, err = st.ReadTriageMarks(context.Background(), "primary")
			if err != nil || marks[fake.PrivatePeer].SnoozeUntil == nil {
				t.Fatal(marks, err)
			}
			if len(f.Writes()) != 0 {
				t.Fatal(f.Writes())
			}
		})
	}
}

func TestLegacyHistoryRequestResolvesConversationForms(t *testing.T) {
	ctrl, err := fake.New(engine.Config{Mode: engine.ModeReadWrite, Clock: fake.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: ctrl, Clock: fake.Epoch})
	for _, input := range []string{"user:1000", "me", "@fake_self", "t.me/fake_self", "Test Account", "1000"} {
		job, _, err := s.RequestHistory(context.Background(), input, 1, 10, 1)
		if err != nil || !reflect.DeepEqual(job.Chats, []string{"user:1000"}) {
			t.Fatalf("%s: %+v %v", input, job, err)
		}
		parityWaitHistory(t, s)
	}
	if len(ctrl.(*fake.Controller).Writes()) != 0 {
		t.Fatal("history sent Telegram writes")
	}
}

// Keep the private store's registration capability visible in this fixture;
// passing a shallow ReadStore wrapper can accidentally turn exports into stubs.
var _ StatsExportRegistry = (*store.Store)(nil)
