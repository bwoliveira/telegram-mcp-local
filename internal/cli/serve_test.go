package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

// TestServe exercises the executable, not an in-process daemon or HTTP mux.
// No Telegram credentials, desktop session, GTK or external programs are used.
func TestServe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM shutdown is a Unix executable contract")
	}
	wantTools, oldTools := serveParityTools(t)
	root := t.TempDir()
	binary := filepath.Join(root, "telegram-mcp")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "../../cmd/telegram-mcp")
	// Prove the headless CLI builds without GTK/cgo; build only once.
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	var token string
	logPath := filepath.Join(root, "serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	cmd := exec.Command(binary, "serve")
	// Do not inherit credentials, display, proxy settings, client configuration
	// or installation paths from the developer's environment.
	cmd.Env = []string{
		"HOME=" + root,
		"USERPROFILE=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(root, "share"),
		"TMPDIR=" + root,
		"PATH=" + filepath.Join(root, "no-external-programs"),
		"TELEGRAM_MCP_DATA=" + filepath.Join(root, "data"),
		"TELEGRAM_MCP_STORE_DIR=" + filepath.Join(root, "store"),
		"TELEGRAM_MCP_ENGINE=fake",
		"TELEGRAM_MCP_PORT=" + strconv.Itoa(port),
		"TELEGRAM_MCP_LANG=en",
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// Release the reserved ephemeral slot immediately before starting serve.
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			output, err := os.ReadFile(logPath)
			if err != nil {
				t.Logf("read serve log: %v", err)
			} else {
				t.Logf("serve output:\n%s", output)
			}
		}
	})

	transport := &http.Transport{Proxy: nil}
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case <-done:
			t.Fatalf("serve exited before readiness: %v", waitErr)
		default:
		}
		resp, err := client.Get(baseURL + "/healthz")
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && readErr == nil && string(body) == "ok\n" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not become healthy at %s: %v", baseURL, err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	token, err = service.ResolveToken(filepath.Join(root, "data"), "", false)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapSecret, err := httpserver.ReadPanelBootstrapSecret(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("authenticated_panel", func(t *testing.T) {
		// A daemon bearer authenticates programmatic clients only. It must not
		// become a browser session or bypass the panel's CSRF boundary.
		_, rejected := serveRequest(t, client, baseURL, "GET", "/", token, "", http.StatusForbidden)
		if !strings.Contains(string(rejected), "panel session required") {
			t.Fatalf("bearer rejection = %q", rejected)
		}
		for _, private := range []string{`class="shell"`, `/assets/app.js`, "Test Account", "fake_self"} {
			if strings.Contains(string(rejected), private) {
				t.Errorf("bearer rejection exposed panel marker %q", private)
			}
		}

		serveRequest(t, client, baseURL, "POST", "/api/session/bootstrap", token, `{}`, http.StatusForbidden)
		bootstrapBody := serveBootstrapRequest(t, client, baseURL, bootstrapSecret)
		var bootstrap struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(bootstrapBody, &bootstrap); err != nil || bootstrap.URL == "" {
			t.Fatalf("bootstrap response: %s (%v)", bootstrapBody, err)
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		sessionClient := &http.Client{Timeout: 3 * time.Second, Transport: transport, Jar: jar}
		resp, err := sessionClient.Get(bootstrap.URL)
		if err != nil {
			t.Fatalf("exchange panel session: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("session panel: got %s, want 200; body: %.1000s", resp.Status, body)
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("panel Content-Type = %q", resp.Header.Get("Content-Type"))
		}
		for _, marker := range []string{`class="shell"`, `class="masthead"`, `/assets/app.js`, `/assets/i18n.js`} {
			if !strings.Contains(string(body), marker) {
				t.Errorf("session root is not the real panel: missing %q", marker)
			}
		}
		if strings.Contains(string(body), "supply the local bearer token") || strings.Contains(string(body), "<title>Local access</title>") {
			t.Error("session root served access instructions instead of the panel")
		}
	})
	t.Run("public_root_is_static_instructions", func(t *testing.T) {
		_, first := serveRequest(t, client, baseURL, "GET", "/", "", "", http.StatusOK)
		_, second := serveRequest(t, client, baseURL, "GET", "/", "", "", http.StatusOK)
		if !bytes.Equal(first, second) {
			t.Error("unauthenticated instructions are not static")
		}
		if !strings.Contains(string(first), "telegram-mcp open") || !strings.Contains(string(first), "telegram-mcp open --print") {
			t.Fatalf("unauthenticated root did not return access instructions: %s", first)
		}
		for _, private := range []string{"Test Account", "fake_self", "fake_ana", token, `class="shell"`, "/assets/app.js"} {
			if strings.Contains(string(first), private) {
				t.Errorf("unauthenticated root exposed private panel marker %q", private)
			}
		}
	})
	t.Run("state_requires_bearer", func(t *testing.T) {
		_, body := serveRequest(t, client, baseURL, "GET", "/api/state", "", "", http.StatusUnauthorized)
		for _, private := range []string{"Test Account", "fake_self", "fake_ana", token} {
			if strings.Contains(string(body), private) {
				t.Errorf("unauthenticated state exposed %q", private)
			}
		}
	})
	t.Run("mcp_initialize_and_exact_parity_catalogue", func(t *testing.T) {
		init := serveRPC(t, client, baseURL, token, 1, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"serve-integration-test","version":"1"}}`)
		var initialized struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if err := json.Unmarshal(init, &initialized); err != nil {
			t.Fatal(err)
		}
		if initialized.ProtocolVersion != "2025-06-18" || initialized.ServerInfo.Name != "telegram-mcp-local" || initialized.Capabilities["tools"] == nil {
			t.Fatalf("unexpected initialize result: %s", init)
		}
		serveRequest(t, client, baseURL, "POST", "/mcp", token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, http.StatusAccepted)
		result := serveRPC(t, client, baseURL, token, 2, "tools/list", `{}`)
		var list struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Tools) != 40 || list.NextCursor != "" {
			t.Fatalf("tools/list must return all 40 tools, got %d, nextCursor=%q", len(list.Tools), list.NextCursor)
		}
		names := make([]string, 0, len(list.Tools))
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
			if legacyToolName(tool.Name, oldTools) {
				t.Errorf("legacy tool still advertised: %q", tool.Name)
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, wantTools) {
			t.Errorf("tools/list differs from docs/PARIDADE.md Telegram column\ngot:  %v\nwant: %v", names, wantTools)
		}
	})
	t.Run("bridge_loads_persisted_token_without_environment", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		bridgeCmd := exec.CommandContext(ctx, binary, "bridge")
		bridgeCmd.Env = cmd.Env
		bridgeCmd.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":77,\"method\":\"ping\"}\n")
		out, err := bridgeCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bridge: %v: %s", err, out)
		}
		var result struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal(out, &result); err != nil || result.ID != 77 || result.Error != nil || result.Result == nil {
			t.Fatalf("bridge response: %s (%v)", out, err)
		}
		if bytes.Contains(out, []byte(token)) {
			t.Fatal("bridge exposed persisted token")
		}
	})
	t.Run("open_print_and_headless_refusal", func(t *testing.T) {
		for _, args := range [][]string{{"open", "--print"}} {
			openCmd := exec.Command(binary, args...)
			openCmd.Env = cmd.Env // no display, browser executable or bearer environment
			var stdout, stderr bytes.Buffer
			openCmd.Stdout, openCmd.Stderr = &stdout, &stderr
			if err := openCmd.Run(); err != nil {
				t.Fatalf("open command: %v: %s", err, stderr.String())
			}
			target := strings.TrimSpace(stdout.String())
			if !strings.HasPrefix(target, baseURL+"/bootstrap?c=") || strings.Contains(target, "\n") {
				t.Fatal("open command did not print exactly one bootstrap URL")
			}
			if !strings.Contains(stderr.String(), "Anyone possessing") || !strings.Contains(stderr.String(), "ssh -L "+strconv.Itoa(port)+":127.0.0.1:"+strconv.Itoa(port)) {
				t.Fatal("missing capability warning or matching SSH tunnel port")
			}
			if strings.Contains(stdout.String()+stderr.String(), token) {
				t.Fatal("open command leaked persisted bearer")
			}
			privateClient := daemonClient()
			resp, err := privateClient.Get(target)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 {
				t.Fatal("printed executable URL did not create a panel session")
			}
			resp, err = privateClient.Get(target)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			privateClient.CloseIdleConnections()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatal("printed executable URL was reusable")
			}
		}
		if runtime.GOOS == "linux" {
			headless := exec.Command(binary, "open")
			headless.Env = cmd.Env
			var stdout, stderr bytes.Buffer
			headless.Stdout, headless.Stderr = &stdout, &stderr
			if err := headless.Run(); err == nil || !strings.Contains(stderr.String(), "open --print") {
				t.Fatalf("headless open must fail with --print guidance: err=%v stderr=%q", err, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("headless open disclosed URL: %q", stdout.String())
			}
		}
	})
	t.Run("sigterm_exits_zero", func(t *testing.T) {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("SIGTERM: %v", err)
		}
		select {
		case <-done:
			if waitErr != nil || cmd.ProcessState.ExitCode() != 0 {
				t.Fatalf("serve must exit 0 after SIGTERM: %v (%s)", waitErr, cmd.ProcessState)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not shut down after SIGTERM")
		}
		if resp, err := client.Get(baseURL + "/healthz"); err == nil {
			resp.Body.Close()
			t.Fatal("healthz still answers after serve exited")
		}
	})
}

func serveRequest(t *testing.T, client *http.Client, baseURL, method, path, token, body string, wantStatus int) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: got %s, want %d; body: %.1000s", method, path, resp.Status, wantStatus, data)
	}
	return resp, data
}

func serveBootstrapRequest(t *testing.T, client *http.Client, baseURL, secret string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/session/bootstrap", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(httpserver.PanelBootstrapHeader, secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("panel bootstrap: got %s: %.1000s", resp.Status, body)
	}
	return body
}

func serveRPC(t *testing.T, client *http.Client, baseURL, token string, id int, method, params string) json.RawMessage {
	t.Helper()
	request := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)
	_, body := serveRequest(t, client, baseURL, "POST", "/mcp", token, request, http.StatusOK)
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode %s: %v; body: %.1000s", method, err, body)
	}
	if response.JSONRPC != "2.0" || response.ID != id || (len(response.Error) != 0 && string(response.Error) != "null") || len(response.Result) == 0 || string(response.Result) == "null" {
		t.Fatalf("invalid %s JSON-RPC response: %.1000s", method, body)
	}
	return response.Result
}

// Read the acceptance document rather than duplicating the implementation's
// catalogue: renames in its original/Telegram columns also define old tools.
func serveParityTools(t *testing.T) (names, oldNames []string) {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "docs", "PARIDADE.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	seen := make(map[string]bool)
	for scanner.Scan() {
		cells := strings.Split(scanner.Text(), "|")
		if len(cells) < 8 {
			continue
		}
		id := strings.TrimSpace(cells[1])
		if len(id) != 3 || id[0] != 'T' || id[1] < '0' || id[1] > '9' || id[2] < '0' || id[2] > '9' {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[6]), "`")
		original := strings.Trim(strings.TrimSpace(cells[2]), "`")
		if name == "" || seen[name] {
			t.Fatalf("invalid/duplicate Telegram tool in parity row %s: %q", id, name)
		}
		seen[name] = true
		names = append(names, name)
		if original != name {
			oldNames = append(oldNames, original)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(names) != 40 {
		t.Fatalf("docs/PARIDADE.md must define 40 Telegram tools, parsed %d", len(names))
	}
	slices.Sort(names)
	return names, oldNames
}
