package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	fakeengine "github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
)

func mediaAPIHandler(server *mcp.Server) http.Handler {
	return Routes(server, Options{Token: testToken, PanelBootstrapSecret: testBootstrapSecret, Register: func(mux *http.ServeMux) { RegisterMediaAPIs(mux, server, nil) }})
}

func mediaAPIRequest(h http.Handler, method, path, body string, bearer bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if bearer {
		r.Header.Set("Authorization", "Bearer "+testToken)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMediaAPIInventoryAuthenticated(t *testing.T) {
	h := mediaAPIHandler(mcp.New(mcp.Config{MediaDir: t.TempDir()}))
	if w := mediaAPIRequest(h, "GET", "/api/media", "", false); w.Code != 401 {
		t.Fatalf("anonymous: %d %s", w.Code, w.Body)
	}
	w := mediaAPIRequest(h, "GET", "/api/media", "", true)
	var inv mcp.MediaInventory
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &inv) != nil || inv.RetentionDays != 30 || inv.Files != 0 {
		t.Fatalf("inventory: %d %s", w.Code, w.Body)
	}
}

func TestMediaAPIAllRoutesRequireAuthentication(t *testing.T) {
	h := mediaAPIHandler(mcp.New(mcp.Config{MediaDir: t.TempDir()}))
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/webhooks"}, {"POST", "/api/webhooks"}, {"GET", "/api/webhooks/absent"},
		{"PATCH", "/api/webhooks/absent"}, {"DELETE", "/api/webhooks/absent"}, {"POST", "/api/webhooks/absent/test"},
		{"GET", "/api/media"}, {"POST", "/api/media/retention"}, {"POST", "/api/media/purge"}, {"POST", "/api/media/download"},
		{"GET", "/api/transcription"}, {"POST", "/api/transcription/transcribe"}, {"POST", "/api/transcription/save"}, {"GET", "/media/absent"},
	} {
		if w := mediaAPIRequest(h, tc.method, tc.path, "{}", false); w.Code != 401 {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestMediaAPISessionRequiresCSRFBeforeSideEffects(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := mcp.New(mcp.Config{State: st, MediaDir: t.TempDir()})
	h := mediaAPIHandler(server)
	cookie, csrf, _ := bootstrapSession(t, h)
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 403}, {"wrong", 403}, {csrf, 200}} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8765/api/media/retention", strings.NewReader(`{"days":0}`))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:8765")
		r.Header.Set("X-CSRF-Token", tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("CSRF: %d %s", w.Code, w.Body)
		}
		value, err := st.Setting(context.Background(), mcp.RetentionSetting)
		if err != nil {
			t.Fatal(err)
		}
		if tc.status == 403 && value != "" {
			t.Fatal("CSRF failure changed retention")
		}
		if tc.status == 200 && value != "0" {
			t.Fatal("valid CSRF did not change retention")
		}
	}
}

func TestMediaAPIPurgePreviewConfirmAndRetention(t *testing.T) {
	root := t.TempDir()
	media, exports := filepath.Join(root, "media"), filepath.Join(root, "exports")
	st, err := state.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := mediaAPIHandler(mcp.New(mcp.Config{State: st, MediaDir: media, ExportDir: exports, Clock: func() time.Time { return now }}))
	for _, dir := range []string{media, exports} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"old.ogg", "new.ogg"} {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			at := now
			if name == "old.ogg" {
				at = now.AddDate(0, 0, -40)
			}
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	w := mediaAPIRequest(h, "POST", "/api/media/retention", `{"days":0}`, true)
	if w.Code != 200 {
		t.Fatalf("retention: %d %s", w.Code, w.Body)
	}
	for _, what := range []string{"media", "exports"} {
		dir := media
		if what == "exports" {
			dir = exports
		}
		for _, confirm := range []bool{false, true} {
			settings := map[string]any{"what": what, "older_than_days": 30}
			if confirm {
				settings["confirm"] = true
			}
			body, _ := json.Marshal(settings)
			w := mediaAPIRequest(h, "POST", "/api/media/purge", string(body), true)
			if w.Code != 200 {
				t.Fatalf("purge %s %v: %d %s", what, confirm, w.Code, w.Body)
			}
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			key := "would_delete_files"
			if confirm {
				key = "deleted_files"
			}
			if out[key] != float64(1) {
				t.Fatalf("count: %s", w.Body)
			}
			_, err := os.Stat(filepath.Join(dir, "old.ogg"))
			if !confirm && err != nil {
				t.Fatal("preview deleted file")
			}
			if confirm && !os.IsNotExist(err) {
				t.Fatalf("confirm failed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "new.ogg")); err != nil {
				t.Fatal("age filter removed recent file")
			}
		}
	}
}

func TestMediaAPIRejectsInvalidBodies(t *testing.T) {
	h := mediaAPIHandler(mcp.New(mcp.Config{MediaDir: t.TempDir()}))
	for _, tc := range []struct{ path, body string }{
		{"/api/media/retention", `{}`}, {"/api/media/retention", `{"days":-1}`}, {"/api/media/retention", `{"days":null}`},
		{"/api/media/retention", `{"days":1} {"days":0}`}, {"/api/media/retention", `null`},
		{"/api/media/purge", `{"what":"all","confirm":true}`}, {"/api/media/purge", `{"what":"media","older_than_days":365001}`},
		{"/api/media/purge", `{"what":"media","confirm":"true"}`}, {"/api/media/purge", `{"what":"media","unknown":true}`},
		{"/api/media/download", `{"chat_id":"user:01","message_id":"5"}`}, {"/api/media/download", `{"chat_id":"user:1001","message_id":"05"}`},
		{"/api/media/download", `{"chat_id":"user:1001","message_id":"2147483648"}`},
		{"/api/transcription/transcribe", `{"chat_id":"user:1001","message_id":"5","native":"true"}`},
		{"/api/transcription/save", `{"chat_id":"user:1001","message_id":"5","text":"  "}`},
		{"/api/media/retention", strings.Repeat(" ", 65537) + `{"days":0}`},
	} {
		want := http.StatusBadRequest
		if len(tc.body) > maxPanelBody {
			want = http.StatusRequestEntityTooLarge
		}
		if w := mediaAPIRequest(h, "POST", tc.path, tc.body, true); w.Code != want {
			t.Errorf("%s: got %d want %d: %s", tc.path, w.Code, want, w.Body)
		}
	}
}

func TestMediaAPIWebhookCRUDTestAndSecret(t *testing.T) {
	ctx := context.Background()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hooks := webhook.New(st, nil, "test", nil)
	server := mcp.New(mcp.Config{MediaDir: t.TempDir()})
	h := Routes(server, Options{Token: testToken, Register: func(mux *http.ServeMux) { RegisterMediaAPIs(mux, server, hooks) }})
	var mu sync.Mutex
	var wire []byte
	var signature, event, delivery, attempt string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		wire, signature, event, delivery, attempt = body, r.Header.Get("X-Telegram-MCP-Signature"), r.Header.Get("X-Telegram-MCP-Event"), r.Header.Get("X-Telegram-MCP-Delivery"), r.Header.Get("X-Telegram-MCP-Attempt")
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	body, _ := json.Marshal(map[string]any{"url": receiver.URL, "events": []string{"message"}, "enabled": false, "allow_private_network": true})
	w := mediaAPIRequest(h, "POST", "/api/webhooks", string(body), true)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct {
		Webhook webhook.Status `json:"webhook"`
		Secret  string         `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Secret == "" || created.Webhook.ID == "" {
		t.Fatalf("create: %s %v", w.Body, err)
	}
	id := created.Webhook.ID
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/webhooks", ""}, {"GET", "/api/webhooks/" + id, ""},
		{"PATCH", "/api/webhooks/" + id, `{"include_own":true}`}, {"POST", "/api/webhooks/" + id + "/test", "{}"},
	} {
		w := mediaAPIRequest(h, tc.method, tc.path, tc.body, true)
		if w.Code != 200 || strings.Contains(w.Body.String(), created.Secret) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	persisted, err := st.Webhook(ctx, id)
	if err != nil || persisted.Secret != created.Secret || !persisted.IncludeOwn || persisted.URL != receiver.URL {
		t.Fatal("PATCH did not preserve configuration and secret", err)
	}
	mu.Lock()
	if signature != webhook.Sign(created.Secret, wire) || event != "test" || delivery == "" || attempt != "1" {
		t.Error("invalid signed webhook", event, delivery, attempt)
	}
	mu.Unlock()
	w = mediaAPIRequest(h, "DELETE", "/api/webhooks/"+id, "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/webhooks/" + id, ""}, {"PATCH", "/api/webhooks/" + id, `{}`}, {"DELETE", "/api/webhooks/" + id, ""}, {"POST", "/api/webhooks/" + id + "/test", `{}`},
	} {
		if w := mediaAPIRequest(h, tc.method, tc.path, tc.body, true); w.Code != 404 {
			t.Errorf("missing %s: %d %s", tc.method, w.Code, w.Body)
		}
	}
	if _, err := st.Webhook(ctx, id); !errors.Is(err, state.ErrNoWebhook) {
		t.Fatal("webhook not deleted", err)
	}
	for _, body := range []string{`{"url":"private-marker://bad"}`, `{"url":"http://localhost","events":["private-marker"]}`, `{"url":"http://localhost","chats":["private-marker"]}`, `null`, `{"url":"http://localhost"} {}`} {
		w := mediaAPIRequest(h, "POST", "/api/webhooks", body, true)
		if w.Code != 400 || strings.Contains(w.Body.String(), "private-marker") {
			t.Fatalf("invalid webhook: %d %s", w.Code, w.Body)
		}
	}
}

func mediaAPIFake(t *testing.T) (*mcp.Server, *store.Store, engine.MessageKey) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "store.db"), "primary", store.WithClock(fakeengine.Epoch))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrl, err := fakeengine.New(engine.Config{Account: "primary", Store: st, Clock: fakeengine.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1001}, ID: 5}
	server := mcp.New(mcp.Config{Engine: ctrl, ReadStore: st, MediaDir: filepath.Join(dir, "media"), Clock: fakeengine.Epoch})
	return server, st, key
}

func TestMediaAPITranscriptionLocalDefaultCachedAndCorrection(t *testing.T) {
	server, st, key := mediaAPIFake(t)
	h := mediaAPIHandler(server)
	body := `{"chat_id":"user:1001","message_id":"5"}`
	w := mediaAPIRequest(h, "POST", "/api/transcription/transcribe", body, true)
	if w.Code != 503 {
		t.Fatalf("missing local ASR: %d %s", w.Code, w.Body)
	}
	inv, err := server.Inventory(context.Background())
	if err != nil || inv.Files != 0 {
		t.Fatal("missing ASR downloaded media", err)
	}
	if err := st.SaveTranscript(context.Background(), key, store.Transcript{Text: "rawtoken", RawText: "rawtoken", Language: "pt", Model: "test", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	w = mediaAPIRequest(h, "POST", "/api/transcription/transcribe", body, true)
	var result mcp.MediaTranscription
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Cached || result.Transcript.Raw != "rawtoken" {
		t.Fatalf("cache: %d %s", w.Code, w.Body)
	}
	w = mediaAPIRequest(h, "POST", "/api/transcription/save", `{"chat_id":"user:1001","message_id":"5","text":"correctedtoken"}`, true)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	tr, ok, err := st.ReadTranscript(context.Background(), key)
	if err != nil || !ok || tr.Raw != "rawtoken" || tr.Text != "correctedtoken" || tr.Language != "pt" || tr.Model != "test" {
		t.Fatalf("persisted: %+v %v", tr, err)
	}
	for query, want := range map[string]int{"rawtoken": 0, "correctedtoken": 1} {
		hits, err := st.SearchLocal(context.Background(), key.Account, query)
		if err != nil || len(hits) != want {
			t.Fatalf("FTS %s: %d %v", query, len(hits), err)
		}
	}
}

func TestMediaAPIDownloadAuthenticatedLinkAndProtected(t *testing.T) {
	server, st, key := mediaAPIFake(t)
	h := mediaAPIHandler(server)
	w := mediaAPIRequest(h, "POST", "/api/media/download", `{"chat_id":"user:1001","message_id":"5"}`, true)
	var out struct {
		URL   string `json:"url"`
		Bytes int64  `json:"bytes"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.URL == "" || out.Bytes == 0 {
		t.Fatalf("download: %d %s", w.Code, w.Body)
	}
	if w := mediaAPIRequest(h, "GET", out.URL, "", false); w.Code != 401 {
		t.Fatalf("anonymous link: %d", w.Code)
	}
	if w := mediaAPIRequest(h, "GET", out.URL, "", true); w.Code != 200 || int64(w.Body.Len()) != out.Bytes {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	msg, ok, err := st.Message(context.Background(), key)
	if err != nil || !ok {
		t.Fatal(err)
	}
	msg.Protected = true
	at := fakeengine.Epoch().Add(time.Minute)
	msg.EditDate = &at
	if _, err := st.Apply(context.Background(), engine.Batch{Account: key.Account, Source: engine.SourceUpdate, Messages: []engine.Message{msg}}); err != nil {
		t.Fatal(err)
	}
	if w := mediaAPIRequest(h, "POST", "/api/media/download", `{"chat_id":"user:1001","message_id":"5"}`, true); w.Code != 403 {
		t.Fatalf("protected: %d %s", w.Code, w.Body)
	}
	if w := mediaAPIRequest(h, "GET", out.URL, "", true); w.Code != 404 {
		t.Fatalf("stale protected link: %d %s", w.Code, w.Body)
	}
}

func TestMediaAPIConfigurationAndSafeErrors(t *testing.T) {
	root := t.TempDir()
	asr := localasr.New(filepath.Join(root, "private-marker"))
	h := mediaAPIHandler(mcp.New(mcp.Config{MediaDir: filepath.Join(root, "missing"), ASR: asr}))
	w := mediaAPIRequest(h, "GET", "/api/transcription", "", true)
	if w.Code != 200 || strings.Contains(w.Body.String(), root) || !strings.Contains(w.Body.String(), `"local":true`) {
		t.Fatalf("ASR: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "private-marker")); !os.IsNotExist(err) {
		t.Fatal("status installed ASR", err)
	}
	for _, err := range []error{errors.New("private-marker /secret/path stdout"), webhook.UserError{Msg: "private-marker"}} {
		w := httptest.NewRecorder()
		mediaAPIError(w, err)
		if strings.Contains(w.Body.String(), "private-marker") {
			t.Fatal("sensitive error exposed")
		}
	}
	w = mediaAPIRequest(h, "GET", "/api/webhooks", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatalf("unavailable hooks: %d %s", w.Code, w.Body)
	}
}

// A native fake proves the opt-in flag crosses the HTTP/MCP seam, while local
// sidecar stubs exercise the real local ASR execution path without installation.
type mediaAPIEngineSpy struct {
	engine.Engine
	downloads, native int
}

func (s *mediaAPIEngineSpy) Download(ctx context.Context, req engine.DownloadRequest) (engine.Download, error) {
	s.downloads++
	return s.Engine.Download(ctx, req)
}
func (s *mediaAPIEngineSpy) TranscribeNative(context.Context, engine.MessageKey) (engine.NativeTranscript, error) {
	s.native++
	return engine.NativeTranscript{Text: "nativetoken", Pending: false}, nil
}

func TestMediaAPILocalTranscribeAndNativeExplicitOnly(t *testing.T) {
	data := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(data, "store.db"), "primary", store.WithClock(fakeengine.Epoch))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctrl, err := fakeengine.New(engine.Config{Account: "primary", Store: st, Clock: fakeengine.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	spy := &mediaAPIEngineSpy{Engine: ctrl}
	bin := filepath.Join(data, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	testutil.InstallASRTools(t, bin, "localasrtoken")
	models := filepath.Join(data, "models")
	if err := os.Mkdir(models, 0700); err != nil {
		t.Fatal(err)
	}
	model, err := os.Create(filepath.Join(models, localasr.ModelName))
	if err != nil {
		t.Fatal(err)
	}
	if err := model.Truncate(574041195); err != nil {
		model.Close()
		t.Fatal(err)
	}
	if err := model.Close(); err != nil {
		t.Fatal(err)
	}
	asr := localasr.New(data)
	server := mcp.New(mcp.Config{Engine: spy, ReadStore: st, ASR: asr, MediaDir: filepath.Join(data, "media"), Clock: fakeengine.Epoch})
	h := mediaAPIHandler(server)
	for i, body := range []string{
		`{"chat_id":"user:1001","message_id":"5","language":"pt"}`,
		`{"chat_id":"user:1001","message_id":"5","language":"pt"}`,
		`{"chat_id":"user:1001","message_id":"5","language":"pt","refresh":true,"native":true}`,
	} {
		w := mediaAPIRequest(h, "POST", "/api/transcription/transcribe", body, true)
		if w.Code != 200 {
			t.Fatalf("transcription %d: %d %s", i, w.Code, w.Body)
		}
		var result mcp.MediaTranscription
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		want := "localasrtoken"
		if i == 2 {
			want = "nativetoken"
		}
		if result.Transcript.Text != want || result.Transcript.Raw != "localasrtoken" || result.Cached != (i == 1) {
			t.Fatalf("result %d: %+v", i, result)
		}
		wantNative := 0
		if i == 2 {
			wantNative = 1
		}
		if spy.native != wantNative || spy.downloads != 1 {
			t.Fatalf("operation counters %d: downloads=%d native=%d", i, spy.downloads, spy.native)
		}
	}
}
