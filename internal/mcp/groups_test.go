package mcp

import (
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestGroupsActionsApprovalAndMutationBinding(t *testing.T) {
	for _, group := range []string{"chat:1001", "channel:2001", "channel:2002"} {
		for _, action := range []string{"add", "remove", "ban", "promote", "demote"} {
			t.Run(group+"/"+action, func(t *testing.T) {
				s, f := newFakeServer(t, engine.ModeReadWrite)
				a := map[string]any{"group_id": group, "action": action, "participants": []string{"user:1001"}}
				p := toolCall(t, s, "manage_group_participants", a)
				fp := previewFingerprint(t, p)
				preview := toolBody(t, p)["preview"].(map[string]any)
				if preview["text"] != action {
					t.Fatalf("missing action in preview: %v", preview)
				}
				if action == "ban" && (preview["reversible"] != false || len(preview["warnings"].([]any)) == 0) {
					t.Fatalf("missing destructive warning: %v", preview)
				}
				a["confirm"] = true
				a["preview_fingerprint"] = fp
				a["participants"] = []string{"user:1000"}
				if toolCall(t, s, "manage_group_participants", a)["isError"] != true || len(f.Writes()) != 0 {
					t.Fatal("changed participants were approved")
				}
				a["participants"] = []string{"user:1001"}
				if r := toolCall(t, s, "manage_group_participants", a); r["isError"] != false || len(f.Writes()) != 1 {
					t.Fatalf("confirmation: %v", r)
				}
			})
		}
	}
}

func TestGroupsMetadataPreviewAndApproval(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadWrite)
	a := map[string]any{"group_id": "chat:1001", "name": "New title", "description": ""}
	p := toolCall(t, s, "update_group", a)
	fp := previewFingerprint(t, p)
	text := toolBody(t, p)["preview"].(map[string]any)["text"].(string)
	if !strings.Contains(text, "New title") || !strings.Contains(text, "about:") {
		t.Fatalf("payload invisible: %v", p)
	}
	a["confirm"] = true
	a["preview_fingerprint"] = fp
	if r := toolCall(t, s, "update_group", a); r["isError"] != false || len(f.Writes()) != 1 {
		t.Fatal(r)
	}
}

func TestGroupsInviteReadNeverResets(t *testing.T) {
	for _, mode := range []engine.Mode{engine.ModeReadOnly, engine.ModeReadWrite} {
		s, f := newFakeServer(t, mode)
		for _, confirm := range []bool{false, true} {
			r := toolCall(t, s, "get_group_invite_link", map[string]any{"group_id": "chat:1001", "confirm": confirm})
			if r["isError"] != false || len(f.Writes()) != 0 {
				t.Fatal(r)
			}
			if toolBody(t, r)["link"] == "" {
				t.Fatal("missing existing synthetic link")
			}
		}
	}
}

func TestGroupsLeaveWarningAndInvalidInputs(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadWrite)
	r := toolCall(t, s, "leave_group", map[string]any{"group_id": "chat:1001"})
	previewFingerprint(t, r)
	p := toolBody(t, r)["preview"].(map[string]any)
	if p["reversible"] != false || len(p["warnings"].([]any)) == 0 {
		t.Fatal("leave requires warning")
	}
	for _, a := range []map[string]any{
		{"group_id": "user:1001", "action": "add", "participants": []string{"user:1001"}},
		{"group_id": "chat:1001", "action": "unknown", "participants": []string{"user:1001"}},
		{"group_id": "chat:1001", "action": "add", "participants": []string{"user:1001", "user:1001"}},
		{"group_id": "chat:1001", "action": "add", "participants": []string{"chat:1001"}},
	} {
		if toolCall(t, s, "manage_group_participants", a)["isError"] != true {
			t.Fatalf("accepted invalid input: %v", a)
		}
	}
	if len(f.Writes()) != 0 {
		t.Fatal("validation wrote")
	}
}
