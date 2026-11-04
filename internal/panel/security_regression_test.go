package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

type securityHost struct {
	*fakeHost
	updates int
}

func (h *securityHost) InstallUpdate() error {
	h.updates++
	return nil
}

func TestBearerCannotInvokeHumanPanelActions(t *testing.T) {
	host := &securityHost{fakeHost: &fakeHost{settings: HostSettings{CanHide: true, Version: "audit"}}}
	b := newSettingsPanelWith(t, host)
	b.login()
	ctx := context.Background()

	cases := []struct {
		method, path, contentType, body string
	}{
		{http.MethodGet, "/configuracoes", "", ""},
		{http.MethodPost, "/configuracoes/encerrar", "application/x-www-form-urlencoded", ""},
		{http.MethodPost, "/configuracoes/apagar", "application/x-www-form-urlencoded", "confirm=apagar"},
		{http.MethodPost, "/conexoes/remover", "application/x-www-form-urlencoded", "client=codex"},
		{http.MethodPost, "/api/configuracoes", "application/json", `{"autostart":true}`},
		{http.MethodPost, "/api/atualizacao/instalar", "application/json", `{}`},
		{http.MethodPost, "/api/account/logout", "application/json", `{}`},
		{http.MethodPost, "/api/session/logout", "application/json", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			code, _, body := b.do(tc.method, tc.path, tc.contentType, tc.body, map[string]string{
				"Authorization":  "Bearer " + integrationToken,
				"Origin":         b.base,
				"Sec-Fetch-Site": "same-origin",
			})
			if code != http.StatusForbidden {
				t.Fatalf("bearer reached human route: %d %s", code, body)
			}
		})
	}

	if port, quit, erased := host.counts(); port != 0 || quit != 0 || erased != 0 {
		t.Fatalf("bearer caused host effects: port=%d quit=%d erased=%d", port, quit, erased)
	}
	if autostart, _ := host.autostart(); autostart || host.updates != 0 {
		t.Fatalf("bearer changed settings or installed update: autostart=%v updates=%d", autostart, host.updates)
	}
	if account, err := b.f.Account(ctx); err != nil || !account.Authenticated {
		t.Fatalf("bearer logged out account: account=%+v err=%v", account, err)
	}
	if code, _, body := b.do(http.MethodGet, "/api/session", "", "", nil); code != http.StatusOK {
		t.Fatalf("bearer logged out browser session: %d %s", code, body)
	}

	code, _, body := b.do(http.MethodGet, "/api/status", "", "", map[string]string{
		"Authorization": "Bearer " + integrationToken,
	})
	if code != http.StatusOK || !strings.Contains(body, `"app":"Telegram MCP"`) {
		t.Fatalf("programmatic bearer API stopped working: %d %s", code, body)
	}
}

func TestClientConflictResponseDoesNotExposeExistingEntry(t *testing.T) {
	b := newSettingsPanel(t)
	marker := "SYNTHETIC_UNRELATED_SECRET"
	path := DesktopConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"mcpServers": map[string]any{"telegram": map[string]any{
		"command": "/private/bin/other-mcp",
		"args":    []any{"--api-key", marker, "https://user:password@example.test/mcp?token=" + marker},
	}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, body := b.do(http.MethodPost, "/api/clients/claude-desktop", "application/json", `{}`, map[string]string{
		"Authorization": "Bearer " + integrationToken,
	})
	if code != http.StatusOK {
		t.Fatalf("conflict response: %d %s", code, body)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	if conflict, ok := response["conflict"].(bool); !ok || !conflict {
		t.Fatalf("conflict signal = %#v", response["conflict"])
	}
	if response["client"] != "Claude Desktop" || len(response) != 2 {
		t.Fatalf("conflict response exposed extra details: %#v", response)
	}
	for _, forbidden := range []string{marker, "/private/", "other-mcp", "password", "token="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("conflict response leaks %q: %s", forbidden, body)
		}
	}
}

func TestClientConflictWithInvalidURLStillBlocksReplacement(t *testing.T) {
	b := newSettingsPanel(t)
	marker := "SYNTHETIC_UNPARSEABLE_OTHER_SERVER"
	path := CursorConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"mcpServers": map[string]any{"telegram": map[string]any{"url": marker}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, body := b.do(http.MethodPost, "/api/clients/cursor", "application/json", `{}`, map[string]string{
		"Authorization": "Bearer " + integrationToken,
	})
	if code != http.StatusOK || !strings.Contains(body, `"conflict":true`) || strings.Contains(body, marker) {
		t.Fatalf("safe conflict response: %d %s", code, body)
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), marker) {
		t.Fatalf("replace=false overwrote existing invalid URL entry: %s", kept)
	}
}

func TestDescribeEntryRedactsCredentialsAndArguments(t *testing.T) {
	if got := describeEntry(map[string]any{
		"url": "https://user:password@example.test/mcp?token=SYNTHETIC#fragment",
	}); got != "https://example.test/mcp" {
		t.Fatalf("safe URL label = %q", got)
	}
	if got := describeEntry(map[string]any{
		"command": `C:\\private\\other-mcp.exe`,
		"args":    []any{"--api-key", "SYNTHETIC"},
	}); got != "other-mcp.exe" {
		t.Fatalf("safe command label = %q", got)
	}
}

func TestOversizedProgrammaticBodyIsRejectedBeforeEffect(t *testing.T) {
	b := newSettingsPanel(t)
	body := `{"replace":false}` + strings.Repeat(" ", (64<<10)+1)
	code, _, response := b.do(http.MethodPost, "/api/clients/cursor", "application/json", body, map[string]string{
		"Authorization": "Bearer " + integrationToken,
	})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d %s", code, response)
	}
	if _, err := os.Stat(CursorConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("oversized body changed client configuration: %v", err)
	}
}

func TestOversizedHumanFormIsRejectedBeforeEffect(t *testing.T) {
	host := &fakeHost{settings: HostSettings{CanHide: true, Version: "audit"}}
	b := newSettingsPanelWith(t, host)
	b.login()
	body := "confirm=apagar&padding=" + strings.Repeat("x", (64<<10)+1)
	code, _, response := b.page(http.MethodPost, "/configuracoes/apagar", "application/x-www-form-urlencoded", body, true)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized form: %d %s", code, response)
	}
	if _, _, erased := host.counts(); erased != 0 {
		t.Fatalf("oversized form erased data: effects=%d", erased)
	}
}

func TestJSONRequestRejectsTrailingValueBeforeEffect(t *testing.T) {
	b := newSettingsPanel(t)
	body := `{"replace":false}{"replace":true}`
	code, _, response := b.do(http.MethodPost, "/api/clients/cursor", "application/json", body, map[string]string{
		"Authorization": "Bearer " + integrationToken,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("trailing JSON: %d %s", code, response)
	}
	if _, err := os.Stat(CursorConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("trailing JSON changed client configuration: %v", err)
	}
}

func TestJSONRequestRejectsUnsupportedContentTypeBeforeEffect(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		t.Run(contentType, func(t *testing.T) {
			b := newSettingsPanel(t)
			body := `{"replace":false}{"replace":true}`
			code, _, response := b.do(http.MethodPost, "/api/clients/cursor", contentType, body, map[string]string{
				"Authorization":  "Bearer " + integrationToken,
				"Origin":         b.base,
				"Sec-Fetch-Site": "same-origin",
			})
			if code != http.StatusUnsupportedMediaType {
				t.Fatalf("content type %q: %d %s", contentType, code, response)
			}
			if _, err := os.Stat(CursorConfigPath()); !os.IsNotExist(err) {
				t.Fatalf("unsupported content type changed client configuration: %v", err)
			}
		})
	}
}

func TestTerminalAndRedirectResponsesHaveCSP(t *testing.T) {
	host := &fakeHost{settings: HostSettings{CanHide: true, Version: "audit"}}
	b := newSettingsPanelWith(t, host)
	b.login()
	cases := []struct {
		method, path, contentType, body string
		csrf                            bool
	}{
		{http.MethodGet, "/estado", "", "", false},
		{http.MethodPost, "/configuracoes/encerrar", "application/x-www-form-urlencoded", "", true},
		{http.MethodPost, "/configuracoes/apagar", "application/x-www-form-urlencoded", "confirm=apagar", true},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			code, headers, body := b.page(tc.method, tc.path, tc.contentType, tc.body, tc.csrf)
			if code < 200 || code >= 400 {
				t.Fatalf("response: %d %s", code, body)
			}
			if policy := headers.Get("Content-Security-Policy"); !strings.Contains(policy, "default-src") || !strings.Contains(policy, "frame-ancestors 'none'") {
				t.Fatalf("missing terminal CSP: %q", policy)
			}
		})
	}
}

type qrEffectProbe struct {
	engine.Controller
	starts  int
	cancels int
}

func (p *qrEffectProbe) StartLogin(ctx context.Context, request engine.LoginRequest) error {
	p.starts++
	return p.Controller.StartLogin(ctx, request)
}

func (p *qrEffectProbe) CancelLogin() {
	p.cancels++
	p.Controller.CancelLogin()
}

func TestGetStateDoesNotRenewQRPairing(t *testing.T) {
	p, h := panelWithFake(t)
	if err := p.Engine.LogOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe := &qrEffectProbe{Controller: p.Engine}
	p.Engine = probe
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	p.qrw.now = func() time.Time { return now }
	if w := panelRequest(t, h, http.MethodPost, "/api/pair", `{"method":"qr"}`); w.Code != http.StatusOK {
		t.Fatalf("start QR: %d %s", w.Code, w.Body)
	}
	if w := panelRequest(t, h, http.MethodGet, "/api/state", ""); w.Code != http.StatusOK {
		t.Fatalf("initial state: %d %s", w.Code, w.Body)
	}
	probe.starts, probe.cancels = 0, 0
	now = now.Add(qrLifetime(p.Engine.Login()) + qrGrace + time.Second)
	if w := panelRequest(t, h, http.MethodGet, "/api/state", ""); w.Code != http.StatusOK {
		t.Fatalf("stale state: %d %s", w.Code, w.Body)
	}
	if probe.starts != 0 || probe.cancels != 0 {
		t.Fatalf("GET /api/state caused pairing effects: starts=%d cancels=%d", probe.starts, probe.cancels)
	}
	if w := panelRequest(t, h, http.MethodPost, "/api/pair/refresh", `{}`); w.Code != http.StatusOK {
		t.Fatalf("refresh QR: %d %s", w.Code, w.Body)
	}
	if probe.starts != 1 || probe.cancels != 1 {
		t.Fatalf("protected refresh did not renew QR: starts=%d cancels=%d", probe.starts, probe.cancels)
	}
}
