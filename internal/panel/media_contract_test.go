package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The panel shares J's actual API registration, never a second implementation.
func TestIntegrationSettingsUsesMediaWebhookContract(t *testing.T) {
	b := newSettingsPanel(t)
	b.login()
	code, _, body := b.page("GET", "/api/webhooks", "", "", false)
	var list struct {
		Events    []string `json:"events"`
		Signature string   `json:"signature"`
		QueueNote string   `json:"queue_note"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil {
		t.Fatalf("webhook list: %d %s", code, body)
	}
	if !reflect.DeepEqual(list.Events, []string{"message", "edit", "delete", "reaction", "read"}) || !strings.Contains(list.Signature, "X-Telegram-MCP-Signature") || list.QueueNote == "" {
		t.Fatalf("webhook contract: %+v", list)
	}
	if code, _, body := b.page("GET", "/api/webhooks/missing", "", "", false); code != http.StatusNotFound {
		t.Fatalf("missing webhook: %d %s", code, body)
	}
	if code, _, body := b.page("POST", "/api/webhooks", "application/json", `{"url":"invalid"}`, true); code != http.StatusBadRequest {
		t.Fatalf("invalid webhook: %d %s", code, body)
	}
	code, _, body = b.page("GET", "/webhooks/documentacao", "", "", false)
	if code != http.StatusOK {
		t.Fatalf("documentation: %d %s", code, body)
	}
	for _, want := range []string{"X-Telegram-MCP-Signature", "X-Telegram-MCP-Event", "X-Telegram-MCP-Delivery", "X-Telegram-MCP-Attempt", "delivery_id", "schema"} {
		if !strings.Contains(body, want) {
			t.Errorf("documentation missing %s", want)
		}
	}
	code, _, body = b.page("GET", "/api/transcription", "", "", false)
	var status map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &status) != nil || status["available"] != false {
		t.Fatalf("transcription without an engine: %d %s", code, body)
	}
	// The page polls the canonical, flat envelope and explicitly confirms purge.
	app, _ := assets.ReadFile("assets/app.js")
	settings, _ := assets.ReadFile("assets/settings.js")
	if !strings.Contains(string(app), `fetch("/api/transcription"`) || strings.Contains(string(app), `fetch("/api/asr"`) || !strings.Contains(string(app), "var i = d.install || {};") {
		t.Error("install progress does not consume J's flat status envelope")
	}
	if !strings.Contains(string(settings), "older_than_days: 0, confirm: true") {
		t.Error("the confirmed purge action must send confirm:true")
	}
}

// A28: the single private backend endpoint configures AND sweeps immediately.
// The frontend must not issue a second purge or bypass J's authenticated mux.
func TestIntegrationSettingsRetentionSweepsImmediately(t *testing.T) {
	b := newSettingsPanel(t)
	b.login()
	dir := filepath.Join(b.p.DataDir, "media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.txt")
	fresh := filepath.Join(dir, "fresh.txt")
	for _, path := range []string{old, fresh} {
		if err := os.WriteFile(path, []byte("local fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	code, _, body := b.page("POST", "/api/media/retention", "application/json", `{"days":1}`, true)
	var swept struct {
		Days  int   `json:"retention_days"`
		Files int   `json:"deleted_files"`
		Bytes int64 `json:"freed_bytes"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &swept) != nil || swept.Days != 1 || swept.Files != 1 || swept.Bytes != 13 {
		t.Fatalf("retention must configure and sweep: %d %s", code, body)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file survived sweep: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh file removed: %v", err)
	}
}
