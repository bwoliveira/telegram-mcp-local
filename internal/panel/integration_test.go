package panel

// The panel behind the real HTTP boundary (internal/httpserver): a browser
// session made by the bootstrap exchange, the CSRF token from GET
// /api/session, and the panel's own mutations. The fake engine only; nothing
// here reaches Telegram.

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/account"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const integrationToken = "synthetic-panel-integration-bearer-not-real"
const integrationBootstrapSecret = "synthetic-panel-bootstrap-secret-not-real"

type browserPanel struct {
	t      *testing.T
	p      *Panel
	f      *fake.Controller
	base   string
	client *http.Client
	csrf   string
}

func newBrowserPanel(t *testing.T) *browserPanel {
	t.Helper()
	eng, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	server := mcp.New(mcp.Config{})
	b := &browserPanel{t: t, f: eng.(*fake.Controller)}
	b.p = &Panel{Engine: eng, State: st, Server: server, Token: integrationToken}
	h := httpserver.Handler(server, httpserver.Options{Token: integrationToken, PanelBootstrapSecret: integrationBootstrapSecret, Register: b.p.Register, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	b.base = ts.URL
	b.p.Endpoint = func() string { return ts.URL + "/mcp" }
	jar, _ := cookiejar.New(nil)
	b.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return b
}

// login is what `telegram-mcp open` and the browser do: the private local
// bootstrap authority asks for a one-time link, the browser exchanges it for
// the session cookie, and the page reads its CSRF token from GET /api/session.
func (b *browserPanel) login() {
	b.t.Helper()
	code, _, body := b.do("POST", "/api/session/bootstrap", "application/json", "{}", map[string]string{httpserver.PanelBootstrapHeader: integrationBootstrapSecret})
	var link struct{ URL string }
	if code != 200 || json.Unmarshal([]byte(body), &link) != nil || link.URL == "" {
		b.t.Fatalf("bootstrap: %d %s", code, body)
	}
	u, _ := url.Parse(link.URL)
	if code, _, body := b.do("GET", u.RequestURI(), "", "", map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate"}); code != http.StatusSeeOther {
		b.t.Fatalf("exchange: %d %s", code, body)
	}
	code, _, body = b.do("GET", "/api/session", "", "", nil)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &session) != nil || session.CSRF == "" {
		b.t.Fatalf("session: %d %s", code, body)
	}
	b.csrf = session.CSRF
}

func (b *browserPanel) do(method, path, ctype, body string, header map[string]string) (int, http.Header, string) {
	b.t.Helper()
	req, err := http.NewRequest(method, b.base+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw)
}

// page is a same-origin fetch/form post from the panel itself.
func (b *browserPanel) page(method, path, ctype, body string, csrf bool) (int, http.Header, string) {
	h := map[string]string{"Origin": b.base, "Sec-Fetch-Site": "same-origin"}
	if csrf && ctype == "application/json" {
		h["X-CSRF-Token"] = b.csrf
	}
	if csrf && ctype == "application/x-www-form-urlencoded" {
		if body != "" {
			body += "&"
		}
		body += "csrf=" + url.QueryEscape(b.csrf)
	}
	return b.do(method, path, ctype, body, h)
}

func TestIntegrationModeNeedsSessionCSRFAndExactBody(t *testing.T) {
	b := newBrowserPanel(t)
	ctx := context.Background()
	want := `{"mode":"read_write","confirm":true}`

	// Without a session nothing changes, with or without the bearer.
	if code, _, body := b.do("POST", "/api/mode", "application/json", want, map[string]string{"Authorization": "Bearer " + integrationToken}); code != http.StatusForbidden {
		t.Fatalf("bearer: %d %s", code, body)
	}
	if code, _, _ := b.page("POST", "/api/mode", "application/json", want, false); code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", code)
	}
	b.login()
	// A session without the CSRF token, or with a wrong one, is refused.
	if code, _, _ := b.page("POST", "/api/mode", "application/json", want, false); code != http.StatusForbidden {
		t.Fatalf("no CSRF: %d", code)
	}
	if code, _, _ := b.do("POST", "/api/mode", "application/json", want, map[string]string{"Origin": b.base, "Sec-Fetch-Site": "same-origin", "X-CSRF-Token": b.csrf + "x"}); code != http.StatusForbidden {
		t.Fatalf("wrong CSRF: %d", code)
	}
	// The session plus the bearer is still a program: refused.
	if code, _, _ := b.do("POST", "/api/mode", "application/json", want, map[string]string{"Origin": b.base, "Sec-Fetch-Site": "same-origin", "X-CSRF-Token": b.csrf, "Authorization": "Bearer " + integrationToken}); code != http.StatusForbidden {
		t.Fatalf("session+bearer: %d", code)
	}
	// Another origin with the right token: refused by the guard.
	if code, _, _ := b.do("POST", "/api/mode", "application/json", want, map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", "X-CSRF-Token": b.csrf}); code != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", code)
	}
	// A form carrying the token is not the exact JSON body.
	if code, _, _ := b.page("POST", "/api/mode", "application/x-www-form-urlencoded", "mode=read_write&confirm=true", true); code != http.StatusUnsupportedMediaType {
		t.Fatalf("form body: %d", code)
	}
	if code, _, _ := b.page("POST", "/api/mode", "application/json", `{"mode":"read_write"}`, true); code != http.StatusBadRequest {
		t.Fatalf("no confirm: %d", code)
	}
	if b.f.Mode() == engine.ModeReadWrite {
		t.Fatal("a refused request enabled writes")
	}
	if v, _ := b.p.State.Setting(ctx, ModeSetting); v != "" {
		t.Fatalf("a refused request persisted %q", v)
	}

	code, _, body := b.page("POST", "/api/mode", "application/json", want, true)
	if code != http.StatusOK || !strings.Contains(body, `"read_write"`) {
		t.Fatalf("mode: %d %s", code, body)
	}
	if b.f.Mode() != engine.ModeReadWrite {
		t.Fatalf("engine mode %s", b.f.Mode())
	}
	if v, _ := b.p.State.Setting(ctx, ModeSetting); v != "read_write" {
		t.Fatalf("persisted %q", v)
	}
	if code, _, _ := b.page("POST", "/api/mode", "application/json", `{"mode":"read_only","confirm":true}`, true); code != http.StatusOK || b.f.Mode() != engine.ModeReadOnly {
		t.Fatalf("back to read-only: %d %s", code, b.f.Mode())
	}
}

// Every Conta form and every login call works with the session's CSRF token
// and is refused without it.
func TestIntegrationContaFormsAndLoginNeedCSRF(t *testing.T) {
	b := newBrowserPanel(t)
	ctx := context.Background()
	b.login()
	form := "application/x-www-form-urlencoded"

	for _, path := range []string{"/conta/nome", "/conta/desconectar", "/conta/reconectar", "/conta/sair"} {
		if code, _, body := b.page("POST", path, form, "label=Trabalho", false); code != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d %s", path, code, body)
		}
	}
	if v, _ := b.p.State.Setting(ctx, account.LabelSetting); v != "" || b.f.Status().State == engine.StatePaused {
		t.Fatal("a refused form acted")
	}

	// Under the guard's Referrer-Policy: no-referrer a browser posts a plain
	// form with "Origin: null", which the guard refuses even with the token;
	// this is why csrf.js sends the panel's forms as same-origin fetches.
	if code, _, _ := b.do("POST", "/conta/nome", form, "label=Trabalho&csrf="+url.QueryEscape(b.csrf), map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}); code != http.StatusForbidden {
		t.Fatalf("Origin: null form: %d", code)
	}
	code, hdr, _ := b.page("POST", "/conta/nome", form, "label=Trabalho", true)
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/conta?ok=") {
		t.Fatalf("name: %d %s", code, hdr.Get("Location"))
	}
	if v, _ := b.p.State.Setting(ctx, account.LabelSetting); v != "Trabalho" {
		t.Fatalf("label %q", v)
	}
	if code, _, _ := b.page("POST", "/conta/desconectar", form, "", true); code != http.StatusSeeOther || b.f.Status().State != engine.StatePaused {
		t.Fatalf("disconnect: %d %s", code, b.f.Status().State)
	}
	if code, _, _ := b.page("POST", "/conta/reconectar", form, "", true); code != http.StatusSeeOther || b.f.Status().State != engine.StateConnected {
		t.Fatalf("reconnect: %d %s", code, b.f.Status().State)
	}

	// The page the session sees loads the CSRF script and offers the switch.
	code, _, page := b.do("GET", "/conta", "", "", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if code != 200 {
		t.Fatalf("conta: %d", code)
	}
	mustContain(t, "/conta via session", page, `src="/assets/csrf.js"`, `href="#modo-escrita"`, `data-mode-target="read_write"`, `data-mode-confirm`)
	if code, _, js := b.do("GET", "/assets/csrf.js", "", "", nil); code != 200 || !strings.Contains(js, "/api/session") {
		t.Fatalf("csrf.js: %d", code)
	}

	if code, _, _ := b.page("POST", "/conta/sair", form, "", true); code != http.StatusSeeOther {
		t.Fatalf("logout: %d", code)
	}
	if a, _ := b.f.Account(ctx); a.Authenticated {
		t.Fatal("still logged in")
	}

	// Account logout also revokes the browser capability; even the old CSRF
	// token must not authorize another pairing attempt.
	if code, _, _ := b.page("POST", "/api/pair", "application/json", `{"method":"qr"}`, true); code != http.StatusUnauthorized {
		t.Fatalf("revoked browser session can pair: %d", code)
	}
	if b.f.Login().State == engine.LoginShowQR {
		t.Fatal("revoked browser session started pairing")
	}
	// A new bootstrap restores browser access, not the Telegram account. Login
	// calls then still require this new session's X-CSRF-Token.
	b.login()
	if a, _ := b.f.Account(ctx); a.Authenticated {
		t.Fatal("browser bootstrap authenticated the Telegram account")
	}

	// Login: the JSON calls carry X-CSRF-Token.
	if code, _, _ := b.page("POST", "/api/pair", "application/json", `{"method":"qr"}`, false); code != http.StatusForbidden {
		t.Fatalf("pair without CSRF: %d", code)
	}
	if code, _, body := b.page("POST", "/api/pair", "application/json", `{"method":"qr"}`, true); code != 200 || b.f.Login().State != engine.LoginShowQR {
		t.Fatalf("pair: %d %s", code, body)
	}
	if code, _, _ := b.page("POST", "/api/pair/cancel", "application/json", `{}`, false); code != http.StatusForbidden {
		t.Fatalf("cancel without CSRF: %d", code)
	}
	if code, _, body := b.page("POST", "/api/pair/cancel", "application/json", `{}`, true); code != 200 {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if code, _, body := b.page("POST", "/api/pair", "application/json", `{"method":"phone","phone":"+15550100"}`, true); code != 200 {
		t.Fatalf("phone: %d %s", code, body)
	}
	if code, _, _ := b.page("POST", "/api/pair/code", "application/json", `{"code":"12345"}`, false); code != http.StatusForbidden {
		t.Fatalf("code without CSRF: %d", code)
	}
	if code, _, body := b.page("POST", "/api/pair/code", "application/json", `{"code":"12345"}`, true); code >= 500 || code == http.StatusForbidden {
		t.Fatalf("code with CSRF: %d %s", code, body)
	}
}
