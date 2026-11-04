package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	fakeengine "github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

type resolveSpy struct {
	engine.Engine
	resolves int
}

func (s *resolveSpy) Resolve(ctx context.Context, request engine.ResolveRequest) ([]engine.Candidate, error) {
	s.resolves++
	return s.Engine.Resolve(ctx, request)
}

func TestToolSchemasRejectAdditionalProperties(t *testing.T) {
	defs := toolDefinitions()
	if len(defs) != 40 {
		t.Fatalf("want 40 tools, got %d", len(defs))
	}
	for _, raw := range defs {
		def := raw.(map[string]any)
		schema := def["inputSchema"].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("%s allows additional properties", def["name"])
		}
	}
}

func TestToolArgumentsAreAllowlistedPerTool(t *testing.T) {
	s := New(Config{})
	result := toolCall(t, s, "health", map[string]any{"confirm": true})
	if result["isError"] != true {
		t.Fatalf("cross-tool argument was accepted: %+v", result)
	}
	errText, _ := toolBody(t, result)["error"].(string)
	if !strings.Contains(errText, "health") || !strings.Contains(errText, "confirm") {
		t.Fatalf("error does not identify tool and argument: %q", errText)
	}
}

func TestRuntimeArgumentsAreAdvertisedAndAccepted(t *testing.T) {
	cases := map[string]map[string]any{
		"get_group":          {"limit": 25},
		"send_text_message":  {"topic_id": 7},
		"send_media_message": {"topic_id": 7},
		"edit_message":       {"mentions": []string{"user:1"}},
		"get_poll_results":   {"limit": 25},
		"organise_chat":      {"until": "2026-11-04T12:00:00Z"},
		"forward_message":    {"topic_id": 7},
		"send_typing":        {"topic_id": 7},
		"snooze_chat":        {"clear": true},
	}
	properties := map[string]map[string]any{}
	for _, raw := range toolDefinitions() {
		def := raw.(map[string]any)
		properties[def["name"].(string)] = def["inputSchema"].(map[string]any)["properties"].(map[string]any)
	}
	for tool, args := range cases {
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		var decoded arguments
		if err := decodeArguments(tool, encoded, &decoded); err != nil {
			t.Errorf("%s rejects supported arguments: %v", tool, err)
		}
		for name := range args {
			if _, ok := properties[tool][name]; !ok {
				t.Errorf("%s runtime argument %s is absent from its schema", tool, name)
			}
		}
	}
}

func TestMCPTranscriptionRejectsNativeArgumentBeforeEngineCall(t *testing.T) {
	s, _, _ := mediaStoreServer(t)
	spy := &mediaSpy{Engine: s.eng}
	s.eng = spy
	result, handled := s.mcpMediaTool(context.Background(), "transcribe_audio", json.RawMessage(`{"message_id":"user:1001/5","native":true}`))
	if !handled || result["isError"] != true {
		t.Fatalf("native argument was accepted: handled=%v result=%+v", handled, result)
	}
	if spy.native != 0 || spy.downloads != 0 {
		t.Fatalf("rejected argument reached engine: native=%d download=%d", spy.native, spy.downloads)
	}
}

func TestMCPPurgeConfirmationRequiresFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "user_1001", "voice.ogg")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("approved bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	result := toolCall(t, s, "purge_media", map[string]any{"confirm": true})
	if result["isError"] != true {
		t.Fatalf("purge without preview fingerprint executed: %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unapproved media was removed: %v", err)
	}
}

func TestMCPPurgeFingerprintIsSingleUseAndBoundToApprovedFiles(t *testing.T) {
	root := t.TempDir()
	approved := filepath.Join(root, "user_1001", "approved.ogg")
	if err := os.MkdirAll(filepath.Dir(approved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approved, []byte("approved bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	preview := toolCall(t, s, "purge_media", map[string]any{})
	if preview["isError"] != false {
		t.Fatalf("preview failed: %+v", preview)
	}
	fingerprint, _ := toolBody(t, preview)["preview_fingerprint"].(string)
	if fingerprint == "" {
		t.Fatalf("preview lacks fingerprint: %+v", toolBody(t, preview))
	}

	unapproved := filepath.Join(root, "user_1001", "arrived-after-preview.ogg")
	if err := os.WriteFile(unapproved, []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"confirm": true, "preview_fingerprint": fingerprint}
	if result := toolCall(t, s, "purge_media", args); result["isError"] != false {
		t.Fatalf("confirmation failed: %+v", result)
	}
	if _, err := os.Stat(approved); !os.IsNotExist(err) {
		t.Fatalf("approved file still exists: %v", err)
	}
	if _, err := os.Stat(unapproved); err != nil {
		t.Fatalf("file outside approved set was removed: %v", err)
	}
	if result := toolCall(t, s, "purge_media", args); result["isError"] != true {
		t.Fatalf("single-use fingerprint was replayed: %+v", result)
	}
}

func TestMCPPurgeRepreviewDoesNotReactivateExpiredFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "user_1001", "voice.ogg")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := New(Config{MediaDir: root, Clock: func() time.Time { return now }})
	first := toolBody(t, toolCall(t, s, "purge_media", map[string]any{}))["preview_fingerprint"].(string)
	now = now.Add(11 * time.Minute)
	second := toolBody(t, toolCall(t, s, "purge_media", map[string]any{}))["preview_fingerprint"].(string)
	if first == second {
		t.Fatal("identical re-preview reused an expired fingerprint")
	}
	result := toolCall(t, s, "purge_media", map[string]any{"confirm": true, "preview_fingerprint": first})
	if result["isError"] != true {
		t.Fatalf("expired fingerprint was reactivated: %+v", result)
	}
}

func TestMCPPurgeFingerprintRejectsChangedFilters(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "user_1001", "voice.ogg")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	preview := toolCall(t, s, "purge_media", map[string]any{})
	fingerprint, _ := toolBody(t, preview)["preview_fingerprint"].(string)
	result := toolCall(t, s, "purge_media", map[string]any{"older_than_days": 1, "confirm": true, "preview_fingerprint": fingerprint})
	if result["isError"] != true {
		t.Fatalf("changed purge filters were accepted: %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("changed filters removed media: %v", err)
	}
}

func TestMCPPurgeFingerprintRejectsChangedApprovedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "user_1001", "voice.ogg")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	preview := toolCall(t, s, "purge_media", map[string]any{})
	fingerprint, _ := toolBody(t, preview)["preview_fingerprint"].(string)
	if fingerprint == "" {
		t.Fatalf("preview lacks fingerprint: %+v", preview)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := toolCall(t, s, "purge_media", map[string]any{"confirm": true, "preview_fingerprint": fingerprint})
	if result["isError"] != true {
		t.Fatalf("changed file was purged: %+v", result)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "replacement" {
		t.Fatalf("replacement was removed or changed: body=%q err=%v", body, err)
	}
}

func TestReadOnlyDispatcherRejectsConfirmedWritesBeforeResolution(t *testing.T) {
	ctrl, err := fakeengine.New(engine.Config{Mode: engine.ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	spy := &resolveSpy{Engine: ctrl.(*fakeengine.Controller)}
	s := New(Config{Engine: spy})
	cases := []map[string]any{
		{"to": "@fake_ana", "text": "hello", "confirm": true, "preview_fingerprint": "untrusted"},
		{"to": "user:1001", "text": "hello", "mentions": []string{"@fake_ana"}, "confirm": true, "preview_fingerprint": "untrusted"},
	}
	for _, args := range cases {
		result := toolCall(t, s, "send_text_message", args)
		if result["isError"] != true || !strings.Contains(toolBody(t, result)["error"].(string), "panel") {
			t.Fatalf("confirmed read-only write did not fail at the gate: %+v", result)
		}
	}
	if spy.resolves != 0 {
		t.Fatalf("read-only gate allowed %d network resolutions", spy.resolves)
	}
}

func TestUnknownToolNameIsSanitizedInLogs(t *testing.T) {
	var logs bytes.Buffer
	s := New(Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": "attacker-controlled-name", "arguments": map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Handle(context.Background(), request)
	if strings.Contains(logs.String(), "attacker-controlled-name") || !strings.Contains(logs.String(), "<unknown>") {
		t.Fatalf("unsafe tool name in log: %s", logs.String())
	}
}

func TestLocalMediaWritersAreNotAnnotatedReadOnly(t *testing.T) {
	wantLocal := map[string]bool{"download_media": true, "transcribe_audio": true, "get_profile_picture": true}
	for _, raw := range toolDefinitions() {
		def := raw.(map[string]any)
		name := def["name"].(string)
		if wantLocal[name] {
			if def["annotations"].(map[string]any)["readOnlyHint"] != false {
				t.Errorf("%s is incorrectly annotated read-only", name)
			}
			delete(wantLocal, name)
		}
	}
	if len(wantLocal) != 0 {
		t.Fatalf("missing tools: %v", wantLocal)
	}
}

func TestDownloadMediaUsesAuthenticatedLinkAboveSmallInlineLimit(t *testing.T) {
	s, _, key := mediaStoreServer(t)
	root := s.mediaDir
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, safeName(peerID(key.Peer)))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "voice.ogg")
	body := make([]byte, 1<<20)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	download := &engine.Download{Path: path, Bytes: int64(len(body)), MediaType: engine.MediaVoice, MimeType: "audio/ogg", Filename: "voice.ogg", SHA256: hex.EncodeToString(sum[:])}
	spy := &mediaSpy{Engine: s.eng, downloadResult: download}
	s.eng = spy
	result := s.mediaDownloadTool(context.Background(), key, false)
	if result["isError"] != false {
		t.Fatalf("download failed: %+v", result)
	}
	response := toolBody(t, result)
	url, _ := response["url"].(string)
	if url == "" || !strings.Contains(url, "/media/") || response["authentication"] == nil {
		t.Fatalf("large inline payload was not replaced by an authenticated link: %+v", response)
	}
}

func TestStoreBackedPurgeDoesNotQueueChangedReplacement(t *testing.T) {
	s, st, _ := reviewCanonicalServer(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: fakeengine.PrivatePeer, ID: 4}
	download, err := s.DownloadMedia(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, fingerprint, err := s.previewMediaPurge("", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(download.Path, []byte("changed-after-verification"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.confirmMediaPurge(ctx, "", 0, 0, fingerprint); err == nil {
		t.Fatal("changed replacement was accepted")
	}
	if err := st.UnlinkQueuedMedia(ctx, "primary"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(download.Path)
	if err != nil || string(body) != "changed-after-verification" {
		t.Fatalf("changed replacement was deleted: body=%q err=%v", body, err)
	}
}

func TestStoreBackedPurgeDoesNotDrainUnrelatedQueue(t *testing.T) {
	s, st, _ := reviewCanonicalServer(t)
	ctx := context.Background()
	unrelated := filepath.Join(s.exportDir, "already-queued.ndjson")
	if err := os.MkdirAll(s.exportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterStatsExport(ctx, "primary", unrelated, "{}", 1, 9); err != nil {
		t.Fatal(err)
	}
	if err := st.PurgeMediaPaths(ctx, "primary", []string{unrelated}); err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: fakeengine.PrivatePeer, ID: 4}
	if _, err := s.DownloadMedia(ctx, key); err != nil {
		t.Fatal(err)
	}
	_, _, _, fingerprint, err := s.previewMediaPurge("", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.confirmMediaPurge(ctx, "", 0, 0, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unrelated); errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmation drained an unrelated queue entry")
	} else if err != nil {
		t.Fatal(err)
	}
}

func TestStageUploadRejectsSymlinkedStagingDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "send-staging")); err != nil {
		t.Skip(err)
	}
	source := filepath.Join(t.TempDir(), "voice.ogg")
	if err := os.WriteFile(source, []byte("voice"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	if _, err := s.stageUpload(source); err == nil {
		t.Fatal("symlinked staging directory was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging escaped media root: %v", entries)
	}
}

func TestDownloadMediaTightensExistingRootPermissions(t *testing.T) {
	s, _, key := mediaStoreServer(t)
	root := s.mediaDir
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.LoosenPermissions(t, root) // Actual public DACL on Windows, not synthetic mode bits.
	dir := filepath.Join(root, safeName(peerID(key.Peer)))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "voice.ogg")
	voice := []byte("voice")
	if err := os.WriteFile(path, voice, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(voice)
	download := &engine.Download{Path: path, Bytes: int64(len(voice)), MediaType: engine.MediaVoice, MimeType: "audio/ogg", Filename: "voice.ogg", SHA256: hex.EncodeToString(sum[:])}
	spy := &mediaSpy{Engine: s.eng, downloadResult: download}
	s.eng = spy
	if _, err := s.DownloadMedia(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	assertMediaPrivate(t, root, true)
	assertMediaPrivate(t, dir, true)
	assertMediaPrivate(t, path, false)
}
