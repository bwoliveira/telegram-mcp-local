package panel

// This opt-in probe measures actual curl time_total through the production
// loopback/auth guard, using an offline controller and isolated SQLite copies.
// Bodies, bootstrap links and cookies exist only in memory. Never print them.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
)

type curlEvidence struct {
	Schema   int           `json:"schema"`
	Samples  int           `json:"samples"`
	Method   string        `json:"method"`
	Datasets []curlDataset `json:"datasets"`
}
type curlDataset struct {
	Dataset       string            `json:"dataset"`
	Messages      int64             `json:"messages"`
	Conversations int               `json:"conversations"`
	Dialogs       int               `json:"dialogs"`
	GuardVerified bool              `json:"guard_verified"`
	Measurements  []curlMeasurement `json:"measurements"`
}
type curlMeasurement struct {
	Route             string         `json:"route"`
	Mode              string         `json:"mode"`
	N                 int            `json:"n"`
	SamplesMS         []float64      `json:"samples_ms"`
	MedianMS          float64        `json:"median_ms"`
	P95MS             float64        `json:"p95_ms"`
	HTTPStatuses      map[string]int `json:"http_statuses"`
	BudgetMS          int            `json:"budget_ms,omitempty"`
	OverBudget        int            `json:"over_budget"`
	EngineStatusReads int64          `json:"engine_status_reads"`
	FullMessageReads  int64          `json:"full_message_reads"`
	ContentComplete   int            `json:"content_complete"`
	ContentIncomplete int            `json:"content_incomplete"`
	DeferredContent   int            `json:"deferred_content"`
	Unsupported       int            `json:"unsupported"`
}

// Credentials are passed over stdin, not argv, files or evidence logs.
func curlHit(t *testing.T, base, path, cookie string) ([]byte, int, float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "curl", "--config", "-")
	config := "silent\nshow-error\nnoproxy = \"*\"\nmax-time = 85\nurl = " + strconv.Quote(base+path) + "\nheader = " + strconv.Quote("Origin: "+base) + "\nheader = \"Sec-Fetch-Site: same-origin\"\nwrite-out = \"\\nCURL_PERF_RESULT:%{http_code}:%{time_total}\"\n"
	if cookie != "" {
		config += "cookie = " + strconv.Quote(cookie) + "\n"
	}
	cmd.Stdin = strings.NewReader(config)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatal("curl failed (private output suppressed)")
	}
	raw := stdout.Bytes()
	pos := bytes.LastIndex(raw, []byte("\nCURL_PERF_RESULT:"))
	if pos < 0 {
		t.Fatal("curl timing marker missing")
	}
	fields := strings.Split(string(raw[pos+len("\nCURL_PERF_RESULT:"):]), ":")
	if len(fields) != 2 {
		t.Fatal("invalid curl metadata")
	}
	code, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal("invalid curl status")
	}
	seconds, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		t.Fatal("invalid curl elapsed")
	}
	return raw[:pos], code, seconds * 1000
}

func curlServer(t *testing.T, fixture *Panel) (*httptest.Server, string, func()) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal("test credential generation failed")
	}
	token := hex.EncodeToString(secret)
	p := &Panel{Engine: fixture.Engine, ReadStore: fixture.ReadStore, AccountID: fixture.AccountID, State: fixture.State, Server: fixture.Server, Token: token}
	base := ""
	p.Endpoint = func() string { return base + "/mcp" }
	server := httptest.NewServer(httpserver.Handler(p.Server, httpserver.Options{Token: token, PanelBootstrapSecret: integrationBootstrapSecret, Register: p.Register, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	base = server.URL
	cleanup := func() {
		server.Close()
		if stopper, ok := any(p).(interface{ Stop() }); ok {
			stopper.Stop()
		}
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequest(http.MethodPost, base+"/api/session/bootstrap", nil)
	if err != nil {
		cleanup()
		t.Fatal("bootstrap request failed")
	}
	request.Header.Set(httpserver.PanelBootstrapHeader, integrationBootstrapSecret)
	response, err := client.Do(request)
	if err != nil {
		cleanup()
		t.Fatal("bootstrap issue failed")
	}
	var issued struct {
		URL string `json:"url"`
	}
	err = json.NewDecoder(response.Body).Decode(&issued)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || issued.URL == "" {
		cleanup()
		t.Fatal("bootstrap issue invalid")
	}
	response, err = client.Get(issued.URL)
	if err != nil {
		cleanup()
		t.Fatal("bootstrap exchange failed")
	}
	response.Body.Close()
	cookies := response.Cookies()
	if response.StatusCode != 303 || len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		cleanup()
		t.Fatal("bootstrap cookie invalid")
	}
	return server, cookies[0].Name + "=" + cookies[0].Value, cleanup
}

func curlCoverageRows(body []byte) (int, bool) {
	_, tail, found := strings.Cut(string(body), "data-conversation-coverage>")
	list, _, closed := strings.Cut(tail, "</ul>")
	return strings.Count(list, "<li class=\"row\">"), found && closed
}

func curlCheckContent(t *testing.T, route string, body []byte, dialogs int) string {
	t.Helper()
	if route == "/status" {
		rows, found := curlCoverageRows(body)
		if found && rows == dialogs {
			return "complete"
		}
		if bytes.Contains(body, []byte("data-status-snapshot")) {
			return "deferred"
		}
		if dialogs == 0 && !found {
			return "complete"
		}
		return "incomplete"
	}
	if route == "/api/panel/status" {
		var snapshot struct {
			HTML  string `json:"html"`
			Fresh bool   `json:"fresh"`
		}
		if json.Unmarshal(body, &snapshot) != nil || !snapshot.Fresh {
			return "incomplete"
		}
		rows, found := curlCoverageRows([]byte(snapshot.HTML))
		if (found && rows == dialogs) || (!found && dialogs == 0) {
			return "complete"
		}
		return "incomplete"
	}
	if strings.HasPrefix(route, "/api/") {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil {
			return "incomplete"
		}
		keys := []string{"key", "tone", "busy"}
		if route == "/api/state" {
			keys = []string{"account", "sync", "health", "clients", "endpoint"}
		}
		for _, key := range keys {
			if _, ok := fields[key]; !ok {
				return "incomplete"
			}
		}
		return "complete"
	}
	if bytes.Contains(bytes.ToLower(body), []byte("<!doctype html")) && bytes.Contains(body, []byte("</html>")) {
		return "complete"
	}
	return "incomplete"
}

func TestPanelAuthenticatedCurlPerformance(t *testing.T) {
	if os.Getenv("PANEL_CURL_PERF") != "1" {
		t.Skip("opt-in isolated authenticated curl evidence")
	}
	output := os.Getenv("PANEL_CURL_OUTPUT")
	if output == "" {
		t.Fatal("PANEL_CURL_OUTPUT required")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Fatal("curl required")
	}
	evidence := curlEvidence{Schema: 1, Samples: 5, Method: "curl -w time_total; cookie-only requests; production Handler and loopback guard; cold=fresh Panel/HTTP auth per sample, warm=same Panel after fifth cold; OS/SQLite caches not flushed; real PATH/HOME; nearest-rank p95; no daemon prefetch"}
	inputs := []struct{ name, path string }{{"synthetic_25000", ""}, {"isolated_real", os.Getenv("PANEL_CURL_REAL_DB")}}
	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			skipWithoutBackup(t, input.name == "isolated_real", input.path, "PANEL_CURL_REAL_DB")
			fixture, _, eng := phaseFixture(t, input.path)
			history, _, err := eng.store.ReadStatus(context.Background(), eng.account)
			if err != nil {
				t.Fatal("fixture totals failed")
			}
			dialogs, err := eng.store.ReadDialogs(context.Background(), eng.account)
			if err != nil {
				t.Fatal("fixture dialogs failed")
			}
			d := curlDataset{Dataset: input.name, Messages: history.Messages, Conversations: history.Conversations, Dialogs: len(dialogs)}
			guardServer, cookie, closeGuard := curlServer(t, fixture)
			_, code, _ := curlHit(t, guardServer.URL, "/api/state", "")
			if code != 401 {
				t.Fatal("production auth guard not active")
			}
			request, _ := http.NewRequest(http.MethodGet, guardServer.URL+"/api/state", nil)
			request.Host = "evil.invalid"
			request.Header.Set("Cookie", cookie)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal("guard probe failed")
			}
			response.Body.Close()
			if response.StatusCode != 403 {
				t.Fatal("production Host guard not active")
			}
			closeGuard()
			d.GuardVerified = true
			routes := []string{"/", "/conta", "/funcoes", "/receitas", "/ajuda", "/configuracoes", "/status", "/api/pulse", "/api/state", "/api/panel/status"}
			for _, route := range routes {
				var server *httptest.Server
				var session string
				var closeServer func()
				for _, mode := range []string{"cold", "warm"} {
					m := curlMeasurement{Route: route, Mode: mode, N: 5, HTTPStatuses: map[string]int{}}
					if !strings.HasPrefix(route, "/api/") {
						m.BudgetMS = 150
						if route == "/status" {
							m.BudgetMS = 300
						}
					}
					beforeCalls, beforeMessages := eng.calls.Load(), eng.store.messageReads.Load()
					for i := 0; i < 5; i++ {
						if mode == "cold" {
							if closeServer != nil {
								closeServer()
							}
							server, session, closeServer = curlServer(t, fixture)
						}
						body, status, ms := curlHit(t, server.URL, route, session)
						m.SamplesMS = append(m.SamplesMS, ms)
						m.HTTPStatuses[strconv.Itoa(status)]++
						if m.BudgetMS > 0 && ms >= float64(m.BudgetMS) {
							m.OverBudget++
						}
						if status == 404 && route == "/api/panel/status" {
							m.Unsupported++
							continue
						}
						if status != 200 {
							t.Fatalf("%s %s HTTP %d (body suppressed)", route, mode, status)
						}
						switch curlCheckContent(t, route, body, len(dialogs)) {
						case "complete":
							m.ContentComplete++
						case "deferred":
							m.DeferredContent++
						default:
							m.ContentIncomplete++
						}
					}
					m.EngineStatusReads = eng.calls.Load() - beforeCalls
					m.FullMessageReads = eng.store.messageReads.Load() - beforeMessages
					sorted := append([]float64(nil), m.SamplesMS...)
					sort.Float64s(sorted)
					m.MedianMS, m.P95MS = sorted[2], sorted[4]
					d.Measurements = append(d.Measurements, m)
					t.Logf("%s %s %s n=5 median=%.3fms p95=%.3fms complete=%d incomplete=%d deferred=%d unsupported=%d", input.name, route, mode, m.MedianMS, m.P95MS, m.ContentComplete, m.ContentIncomplete, m.DeferredContent, m.Unsupported)
				}
				closeServer()
			}
			evidence.Datasets = append(evidence.Datasets, d)
		})
	}
	body, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal("evidence encoding failed")
	}
	if err := os.WriteFile(output, append(body, '\n'), 0600); err != nil {
		t.Fatal("evidence write failed")
	}

}
