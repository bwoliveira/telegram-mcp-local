package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

type panelHistoryProbe struct {
	engine.Controller
	requests chan engine.BackfillRequest
	release  chan struct{}
	failure  error
}

func (e *panelHistoryProbe) Backfill(ctx context.Context, req engine.BackfillRequest) (engine.BackfillResult, error) {
	e.requests <- req
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return engine.BackfillResult{}, ctx.Err()
		}
	}
	if e.failure != nil {
		return engine.BackfillResult{}, e.failure
	}
	return e.Controller.Backfill(ctx, req)
}

func historyFixture(t *testing.T) (*Panel, http.Handler, *panelHistoryProbe, *fake.Controller) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), "panel-test", store.WithClock(fake.Epoch))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	controller, err := fake.New(engine.Config{Account: "panel-test", Mode: engine.ModeReadOnly, Clock: fake.Epoch, Store: s})
	if err != nil {
		t.Fatal(err)
	}
	f := controller.(*fake.Controller)
	probe := &panelHistoryProbe{Controller: controller, requests: make(chan engine.BackfillRequest, 20)}
	server := mcp.New(mcp.Config{Engine: probe, ReadStore: s, Clock: fake.Epoch})
	p := &Panel{Engine: probe, Server: server, ReadStore: s, AccountID: "panel-test", Token: "panel-test-token"}
	h := httpserver.Routes(server, httpserver.Options{Token: p.Token, Register: p.Register})
	return p, h, probe, f
}

func awaitPanelHistory(t *testing.T, h http.Handler) *mcp.HistoryJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w := conversationRequest(h, "GET", "/api/chats", "", true)
		var body struct {
			History *mcp.HistoryJob `json:"history"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("history polling: %d %s", w.Code, w.Body)
		}
		if body.History != nil && body.History.FinishedAt != nil {
			return body.History
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("history worker did not finish")
	return nil
}

func TestPanelHistoryInheritedContractUsesSyncHistory(t *testing.T) {
	for _, tc := range []struct {
		body   string
		rounds int
		single bool
	}{
		{`{"chat_jid":"user:1001"}`, 3, true}, {`{}`, 1, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			_, h, e, f := historyFixture(t)
			// Browsing never invokes Backfill, even with a configured engine.
			for _, path := range []string{"/api/chats", "/api/chats/user:1001", "/api/search?q=hello"} {
				if w := conversationRequest(h, "GET", path, "", true); w.Code != 200 {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body)
				}
			}
			if len(e.requests) != 0 || len(f.Writes()) != 0 {
				t.Fatal("GET browsing touched Telegram")
			}
			w := conversationRequest(h, "POST", "/api/history", tc.body, true)
			var out struct {
				Started bool           `json:"started"`
				Job     mcp.HistoryJob `json:"job"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Started || out.Job.Count != 50 || out.Job.Rounds != tc.rounds || len(out.Job.Chats) == 0 {
				t.Fatalf("history contract: %d %s", w.Code, w.Body)
			}
			if tc.single && (len(out.Job.Chats) != 1 || out.Job.Chats[0] != "user:1001") {
				t.Fatalf("wrong target: %s", w.Body)
			}
			job := awaitPanelHistory(t, h)
			if job.Done != len(job.Chats) || len(job.Failures) != 0 || len(job.Results) != len(job.Chats) {
				t.Fatalf("failed history: %+v", job)
			}
			if len(e.requests) == 0 || len(f.Writes()) != 0 || e.Mode() != engine.ModeReadOnly {
				t.Fatal("history skipped backfill, wrote Telegram or changed mode")
			}
			req := <-e.requests
			if req.Count != 50 || req.Rounds != 1 {
				t.Fatalf("unbounded backfill: %+v", req)
			}
		})
	}
}

func TestPanelHistoryExistingJobAndUnsupportedImport(t *testing.T) {
	_, h, e, f := historyFixture(t)
	e.release = make(chan struct{})
	// Release before store cleanup even if an assertion fails.
	t.Cleanup(func() {
		select {
		case <-e.release:
		default:
			close(e.release)
		}
	})
	e.failure = engine.ErrNotSupported
	w := conversationRequest(h, "POST", "/api/history", `{"chat_jid":"user:1001"}`, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"started":true`) {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	select {
	case <-e.requests:
	case <-time.After(time.Second):
		t.Fatal("no backfill call")
	}
	w = conversationRequest(h, "POST", "/api/history", `{"chat_jid":"user:1001"}`, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"started":false`) {
		t.Fatalf("duplicate job: %d %s", w.Code, w.Body)
	}
	close(e.release)
	job := awaitPanelHistory(t, h)
	if len(job.Failures) != 1 || job.Added != 0 || len(f.Writes()) != 0 || len(e.requests) != 0 {
		t.Fatalf("unsupported import claimed success: %+v", job)
	}
}

func TestPanelHistoryUnavailableIsVisibleInDesktop(t *testing.T) {
	p, _ := conversationFixture(t)
	mux := http.NewServeMux()
	p.Register(mux)
	w := panelRequest(t, Internal(mux), "POST", "/api/history", `{"chat_jid":"user:1"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "nenhuma importação foi iniciada") || strings.Contains(w.Body.String(), `"started":true`) {
		t.Fatalf("hidden/fake import: %d %s", w.Code, w.Body)
	}
}

func TestPanelHTTPDoesNotBypassWriteApproval(t *testing.T) {
	_, h, _, f := historyFixture(t)
	call := func(args string) map[string]any {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_text_message","arguments":` + args + `}}`
		w := conversationRequest(h, "POST", "/mcp", body, true)
		var response struct {
			Result map[string]any `json:"result"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Result == nil {
			t.Fatalf("MCP HTTP: %d %s", w.Code, w.Body)
		}
		return response.Result
	}
	for _, args := range []string{
		`{"to":"user:1001","text":"human approval required","confirm":true}`,
		`{"to":"user:1001","text":"human approval required","confirm":true,"preview_fingerprint":"invented"}`,
	} {
		if out := call(args); out["isError"] != true {
			t.Fatalf("unapproved mutation accepted: %+v", out)
		}
	}
	preview := call(`{"to":"user:1001","text":"human approval required"}`)
	if preview["isError"] != false {
		t.Fatalf("no preview: %+v", preview)
	}
	content := preview["content"].([]any)[0].(map[string]any)["text"].(string)
	var data struct {
		Preview struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"preview"`
	}
	if json.Unmarshal([]byte(content), &data) != nil || data.Preview.Fingerprint == "" {
		t.Fatalf("invalid preview: %s", content)
	}
	approved, _ := json.Marshal(map[string]any{"to": "user:1001", "text": "human approval required", "confirm": true, "preview_fingerprint": data.Preview.Fingerprint})
	if out := call(string(approved)); out["isError"] != true || len(f.Writes()) != 0 {
		t.Fatalf("read_only bypassed: %+v", out)
	}
}
