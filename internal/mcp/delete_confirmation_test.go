package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func toolProperties(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, raw := range toolDefinitions() {
		def := raw.(map[string]any)
		if def["name"] == name {
			return def["inputSchema"].(map[string]any)["properties"].(map[string]any)
		}
	}
	t.Fatalf("tool %s is not registered", name)
	return nil
}

func TestDeleteForEveryoneRequiresExplicitRevokeConfirmation(t *testing.T) {
	props := toolProperties(t, "delete_message")
	confirmRevoke, ok := props["confirm_revoke"].(map[string]any)
	if !ok || confirmRevoke["type"] != "boolean" {
		t.Fatal("delete_message must expose confirm_revoke:boolean")
	}

	s, f := newFakeServer(t, engine.ModeReadWrite)
	args := map[string]any{"chat_id": "user:1000", "message_id": "8"}
	preview := toolCall(t, s, "delete_message", args)
	fingerprint := previewFingerprint(t, preview)
	warnings := toolBody(t, preview)["preview"].(map[string]any)["warnings"].([]any)
	if !strings.Contains(strings.Join(anyStrings(warnings), " "), "confirm_revoke:true") {
		t.Fatalf("delete-for-everyone preview must cite confirm_revoke:true: %v", warnings)
	}

	mutatedScope := map[string]any{
		"chat_id": "user:1000", "message_id": "8", "for_me": true,
		"confirm": true, "preview_fingerprint": fingerprint,
	}
	if got := toolCall(t, s, "delete_message", mutatedScope); got["isError"] != true || len(f.Writes()) != 0 {
		t.Fatalf("revoke preview fingerprint authorized account-only scope: response=%v writes=%d", got, len(f.Writes()))
	}

	args["confirm"] = true
	args["preview_fingerprint"] = fingerprint
	if got := toolCall(t, s, "delete_message", args); got["isError"] != true || len(f.Writes()) != 0 || !strings.Contains(toolBody(t, got)["error"].(string), "confirm_revoke:true") {
		t.Fatalf("generic confirmation revoked for everyone or returned an incoherent error: response=%v writes=%d", got, len(f.Writes()))
	}

	delete(args, "confirm")
	delete(args, "preview_fingerprint")
	fingerprint = previewFingerprint(t, toolCall(t, s, "delete_message", args))
	args["confirm"] = true
	args["preview_fingerprint"] = fingerprint
	args["confirm_revoke"] = true
	if got := toolCall(t, s, "delete_message", args); got["isError"] != false || len(f.Writes()) != 1 {
		t.Fatalf("explicit revoke confirmation did not execute approved preview: response=%v writes=%d", got, len(f.Writes()))
	}
	request := f.Writes()[0].Request.(engine.DeleteRequest)
	if !request.Revoke || !request.ConfirmRevoke {
		t.Fatalf("delete request lost explicit revoke confirmation: %+v", request)
	}

	forMeServer, forMeFake := newFakeServer(t, engine.ModeReadWrite)
	forMe := map[string]any{"chat_id": "user:1000", "message_id": "8", "for_me": true}
	forMePreview := toolCall(t, forMeServer, "delete_message", forMe)
	forMeFingerprint := previewFingerprint(t, forMePreview)
	encodedPreview, err := json.Marshal(toolBody(t, forMePreview))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedPreview), "confirm_revoke") {
		t.Fatalf("account-only preview asks for revoke confirmation: %s", encodedPreview)
	}
	forMe["confirm"] = true
	forMe["preview_fingerprint"] = forMeFingerprint
	if got := toolCall(t, forMeServer, "delete_message", forMe); got["isError"] != false || len(forMeFake.Writes()) != 1 {
		t.Fatalf("account-only delete incorrectly required confirm_revoke: response=%v writes=%d", got, len(forMeFake.Writes()))
	}
}

func anyStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

var previewParameterPattern = regexp.MustCompile("\\b([a-z][a-z0-9_]+)\\s*[:=]\\s*(?:true|false)\\b|\\b(confirm_[a-z0-9_]+)\\b|`([a-z][a-z0-9_]*)`")

func previewParameterReferences(text string) []string {
	var refs []string
	for _, match := range previewParameterPattern.FindAllStringSubmatch(text, -1) {
		for _, name := range match[1:] {
			if name != "" {
				refs = append(refs, name)
			}
		}
	}
	return refs
}

func TestPreviewParameterReferencesRecognizesLegacyWarning(t *testing.T) {
	for _, text := range []string{"explicit confirm_missing is required", "confirm_missing:true is required", "set confirm_missing=true", "set `confirm_missing`"} {
		if refs := previewParameterReferences(text); len(refs) != 1 || refs[0] != "confirm_missing" {
			t.Fatalf("missed preview field in %q: %v", text, refs)
		}
	}
}

func TestWritePreviewParametersExistInToolSchema(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"send_text_message", map[string]any{"to": "user:1001", "text": "hello"}},
		{"send_media_message", map[string]any{"to": "user:1001", "type": "document", "url": writePreviewMedia(t)}},
		{"send_location", map[string]any{"to": "user:1001", "latitude": -23.0, "longitude": -46.0}},
		{"send_poll", map[string]any{"to": "chat:1001", "question": "Choice?", "options": []string{"Alpha", "Beta"}}},
		{"edit_message", map[string]any{"chat_id": "user:1000", "message_id": "8", "text": "updated"}},
		{"delete_message", map[string]any{"chat_id": "user:1000", "message_id": "8"}},
		{"react_to_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}},
		{"forward_message", map[string]any{"chat_id": "user:1001", "message_id": "1", "to": "user:1000"}},
		{"mark_chat_read", map[string]any{"chat_id": "user:1001"}},
		{"send_typing", map[string]any{"to": "user:1001"}},
		{"organise_chat", map[string]any{"chat_id": "user:1001", "action": "archive"}},
		{"manage_group_participants", map[string]any{"group_id": "chat:1001", "action": "add", "participants": []string{"user:1001"}}},
		{"update_group", map[string]any{"group_id": "chat:1001", "name": "updated group"}},
		{"get_group_invite_link", map[string]any{"group_id": "chat:1001", "reset": true}},
		{"leave_group", map[string]any{"group_id": "chat:1001"}},
		{"purge_media", map[string]any{}},
	}
	covered := make(map[string]bool, len(cases))
	for _, tc := range cases {
		covered[tc.name] = true
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newFakeServer(t, engine.ModeReadWrite)
			response := toolCall(t, s, tc.name, tc.args)
			if response["isError"] != false {
				t.Fatalf("preview failed: %v", response)
			}
			body, err := json.Marshal(toolBody(t, response))
			if err != nil {
				t.Fatal(err)
			}
			refs := previewParameterReferences(string(body))
			if tc.name == "delete_message" && !containsParameter(refs, "confirm_revoke") {
				t.Fatalf("delete preview does not cite confirm_revoke:true: %s", body)
			}
			props := toolProperties(t, tc.name)
			for _, ref := range refs {
				if _, ok := props[ref]; !ok {
					t.Errorf("preview cites %s but %s inputSchema does not define it: %s", ref, tc.name, body)
				}
			}
		})
	}
	for _, raw := range toolDefinitions() {
		name := raw.(map[string]any)["name"].(string)
		if (telegramMutation(name) || name == "purge_media") && !covered[name] {
			t.Errorf("write-preview schema sweep does not exercise %s", name)
		}
	}
}

func writePreviewMedia(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preview.txt")
	if err := os.WriteFile(path, []byte("synthetic preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsParameter(refs []string, name string) bool {
	for _, ref := range refs {
		if ref == name {
			return true
		}
	}
	return false
}
