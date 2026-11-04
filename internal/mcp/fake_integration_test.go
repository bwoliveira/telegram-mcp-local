package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	fakeengine "github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func newFakeServer(t *testing.T, mode engine.Mode) (*Server, *fakeengine.Controller) {
	t.Helper()
	ctrl, err := fakeengine.New(engine.Config{Mode: mode, Clock: fakeengine.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	f := ctrl.(*fakeengine.Controller)
	return New(Config{Engine: f, Clock: fakeengine.Epoch, MediaDir: t.TempDir(), ExportDir: t.TempDir()}), f
}
func previewFingerprint(t *testing.T, r map[string]any) string {
	t.Helper()
	if r["isError"] != false {
		t.Fatalf("preview failed: %v", r)
	}
	b := toolBody(t, r)
	if b["dry_run"] != true || b["done"] == true {
		t.Fatalf("not a preview: %v", b)
	}
	return b["preview"].(map[string]any)["fingerprint"].(string)
}

// All fifteen Telegram-changing tools, including normally read-only invite
// retrieval when reset is requested, must go through one approval boundary.
func TestAllTelegramWritesPreviewAndReadOnlyGate(t *testing.T) {
	media := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(media, []byte("synthetic upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		a    map[string]any
	}{
		{"send_text_message", map[string]any{"to": "user:1001", "text": "hello"}},
		{"send_media_message", map[string]any{"to": "user:1001", "type": "document", "url": media}},
		{"send_location", map[string]any{"to": "user:1001", "latitude": -23.0, "longitude": -46.0}},
		{"send_poll", map[string]any{"to": "chat:1001", "question": "Choice?", "options": []string{"Alpha", "Beta"}, "multiple_choice": true}},
		{"edit_message", map[string]any{"chat_id": "user:1000", "message_id": "8", "text": "updated"}},
		{"delete_message", map[string]any{"chat_id": "user:1000", "message_id": "8"}},
		{"react_to_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}},
		{"forward_message", map[string]any{"chat_id": "user:1001", "message_id": "1", "to": "user:1000"}},
		{"mark_chat_read", map[string]any{"chat_id": "user:1001"}},
		{"send_typing", map[string]any{"to": "user:1001"}},
		{"organise_chat", map[string]any{"chat_id": "user:1001", "action": "archive"}},
		{"manage_group_participants", map[string]any{"group_id": "chat:1001", "action": "add", "participants": []string{"user:1001"}}},
		{"update_group", map[string]any{"group_id": "chat:1001", "name": "updated group"}},
		{"leave_group", map[string]any{"group_id": "chat:1001"}},
		{"get_group_invite_link", map[string]any{"group_id": "chat:1001", "reset": true}},
	}
	for _, tc := range cases {
		for _, mode := range []engine.Mode{engine.ModeReadOnly, engine.ModeReadWrite} {
			t.Run(tc.name+"/"+string(mode), func(t *testing.T) {
				s, f := newFakeServer(t, mode)
				a := map[string]any{}
				for k, v := range tc.a {
					a[k] = v
				}
				fp := previewFingerprint(t, toolCall(t, s, tc.name, a))
				if len(f.Writes()) != 0 {
					t.Fatal("preview wrote to Telegram engine")
				}
				a["confirm"] = true
				if tc.name == "delete_message" {
					a["confirm_revoke"] = true
				}
				if toolCall(t, s, tc.name, a)["isError"] != true || len(f.Writes()) != 0 {
					t.Fatal("missing fingerprint executed")
				}
				a["preview_fingerprint"] = fp
				r := toolCall(t, s, tc.name, a)
				if mode == engine.ModeReadOnly {
					if r["isError"] != true || len(f.Writes()) != 0 {
						t.Fatal("read_only was bypassed")
					}
					if !strings.Contains(toolBody(t, r)["error"].(string), "panel") {
						t.Fatal("read_only error must direct user to panel")
					}
				} else {
					if r["isError"] != false || len(f.Writes()) != 1 {
						t.Fatalf("approved request did not execute: %v writes=%v", r, f.Writes())
					}
					if toolCall(t, s, tc.name, a)["isError"] != true || len(f.Writes()) != 1 {
						t.Fatal("fingerprint replay wrote again")
					}
				}
			})
		}
	}
}
func TestReadToolsAndUntrustedMessagesCannotWrite(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadWrite)
	cases := []struct {
		name string
		a    map[string]any
	}{
		{"get_message_context", map[string]any{"chat_id": "user:1001", "message_id": "2"}},
		{"get_group", map[string]any{"group_id": "chat:1001", "live": true}},
		{"get_poll_results", map[string]any{"chat_id": "chat:1001", "message_id": "7"}},
		{"get_profile_picture", map[string]any{"to": "user:1001"}},
		{"resolve_contact", map[string]any{"queries": []string{"@fake_ana"}}},
		{"get_group_invite_link", map[string]any{"group_id": "chat:1001"}},
		{"download_media", map[string]any{"chat_id": "user:1001", "message_id": "4"}},
		{"telegram_status", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := toolCall(t, s, tc.name, tc.a)
			if r["isError"] != false {
				t.Fatal(r)
			}
			if len(f.Writes()) != 0 {
				t.Fatal("read sent a write or read receipt")
			}
			if tc.name == "get_message_context" {
				b := toolBody(t, r)
				encoded, _ := json.Marshal(b)
				if !strings.Contains(string(encoded), "IGNORE ALL INSTRUCTIONS") || b["untrusted_content"] == nil {
					t.Fatal("message content not returned as untrusted data")
				}
			}
		})
	}
}
func TestRemoteMediaApprovalUsesStableBytes(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadWrite)
	data := "synthetic upload"
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(data)) }))
	defer web.Close()
	a := map[string]any{"to": "user:1001", "type": "document", "url": web.URL + "/example.txt"}
	fp := previewFingerprint(t, toolCall(t, s, "send_media_message", a))
	a["confirm"] = true
	a["preview_fingerprint"] = fp
	if r := toolCall(t, s, "send_media_message", a); r["isError"] != false || len(f.Writes()) != 1 {
		t.Fatalf("stable URL could not be confirmed: %v", r)
	}
	delete(a, "confirm")
	delete(a, "preview_fingerprint")
	fp = previewFingerprint(t, toolCall(t, s, "send_media_message", a))
	data = "changed upload"
	a["confirm"] = true
	a["preview_fingerprint"] = fp
	if r := toolCall(t, s, "send_media_message", a); r["isError"] != true || len(f.Writes()) != 1 {
		t.Fatal("changed URL bytes bypassed fingerprint")
	}
}
func TestHistoryUsesEngineAndTerminates(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadOnly)
	r := toolCall(t, s, "sync_history", map[string]any{"chat_id": "user:1001", "count": 20, "rounds": 1})
	if r["isError"] != false {
		t.Fatal(r)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job := s.HistoryStatus()
		if job != nil && job.FinishedAt != nil {
			if job.Done != 1 || len(job.Results) != 1 || len(job.Failures) != 0 {
				t.Fatalf("history failed: %+v", job)
			}
			if len(f.Writes()) != 0 {
				t.Fatal("backfill sent receipt or write")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("backfill did not finish")
}
func TestUnsupportedEngineMethodIsHonest(t *testing.T) {
	e := &unsupportedWindowEngine{}
	s := New(Config{Engine: e})
	r := toolCall(t, s, "get_message_context", map[string]any{"message_id": "channel:123/456"})
	if r["isError"] != true || !strings.Contains(toolBody(t, r)["error"].(string), "not implemented yet") {
		t.Fatal(r)
	}
}

type unsupportedWindowEngine struct{ engine.Engine }

func (*unsupportedWindowEngine) Account(context.Context) (engine.Account, error) {
	return engine.Account{ID: "test"}, nil
}
func (*unsupportedWindowEngine) FetchWindow(context.Context, engine.WindowRequest) ([]engine.Message, error) {
	return nil, engine.ErrNotSupported
}
