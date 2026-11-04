package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

// Exercise the registered HTTP boundary, using synthetic engine state only.
// Authentication/CSRF for network requests belongs to the outer HTTP guard;
// these requests model the trusted in-memory desktop window.
func panelWithFake(t *testing.T) (*Panel, http.Handler) {
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
	p := &Panel{Engine: eng, State: st, Server: &mcp.Server{}, Endpoint: func() string { return "http://127.0.0.1:47831/mcp" }}
	mux := http.NewServeMux()
	p.Register(mux)
	return p, Internal(mux)
}

func panelRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:47831"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPanelFakeLoginQRPhoneCancelAndSecrets(t *testing.T) {
	p, h := panelWithFake(t)
	ctx := context.Background()
	if err := p.Engine.LogOut(ctx); err != nil {
		t.Fatal(err)
	}
	w := panelRequest(t, h, "POST", "/api/pair", `{}`)
	if w.Code != http.StatusOK || p.Engine.Login().State != engine.LoginShowQR {
		t.Fatalf("QR: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "tg://") || strings.Contains(w.Body.String(), "qr_url") {
		t.Fatalf("QR credential exposed: %s", w.Body)
	}
	w = panelRequest(t, h, "GET", "/api/pair/qr.png", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Body.String(), "\x89PNG") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("QR image: %d %v", w.Code, w.Header())
	}

	w = panelRequest(t, h, "POST", "/api/pair", `{"phone":"+55 11 91234-5678"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("phone: %d %s", w.Code, w.Body)
	}
	login := p.Engine.Login()
	if login.State != engine.LoginWantCode || login.Method != engine.LoginPhone || login.Phone != "+5511912345678" {
		t.Fatalf("phone login: %+v", login)
	}
	// An automatic QR request must not cancel a phone flow waiting for input.
	w = panelRequest(t, h, "POST", "/api/pair", `{}`)
	if w.Code != http.StatusOK || p.Engine.Login().State != engine.LoginWantCode {
		t.Fatalf("phone replaced: %d %s", w.Code, w.Body)
	}
	w = panelRequest(t, h, "POST", "/api/pair/resend", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resend: %d %s", w.Code, w.Body)
	}

	const code = "synthetic-code-marker"
	const password = "synthetic-password-marker"
	w = panelRequest(t, h, "POST", "/api/pair/code", `{"code":"`+code+`"}`)
	if w.Code != http.StatusOK || p.Engine.Login().State != engine.LoginWantPass || strings.Contains(w.Body.String(), code) {
		t.Fatalf("code: %d %s", w.Code, w.Body)
	}
	w = panelRequest(t, h, "POST", "/api/pair", `{"phone":"+55 11 91234-5678"}`)
	if w.Code != http.StatusOK || p.Engine.Login().State != engine.LoginWantPass {
		t.Fatalf("same phone restarted 2FA flow: %d %s", w.Code, w.Body)
	}
	w = panelRequest(t, h, "POST", "/api/pair/password", `{"password":"`+password+`"}`)
	if w.Code != http.StatusOK || p.Engine.Login().State != engine.LoginDone || strings.Contains(w.Body.String(), password) {
		t.Fatalf("password: %d %s", w.Code, w.Body)
	}
	w = panelRequest(t, h, "POST", "/api/pair", `{}`)
	if w.Code == http.StatusOK {
		t.Fatalf("allowed login over authorized slot: %s", w.Body)
	}

	if err := p.Engine.LogOut(ctx); err != nil {
		t.Fatal(err)
	}
	panelRequest(t, h, "POST", "/api/pair", `{}`)
	w = panelRequest(t, h, "POST", "/api/pair/cancel", `{}`)
	if w.Code != http.StatusOK || p.Engine.Login().State != engine.LoginCancelled {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	w = panelRequest(t, h, "GET", "/api/pair/qr.png", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cancelled QR still served: %d", w.Code)
	}
}

func TestPanelFakeDisconnectConnectAndLogout(t *testing.T) {
	p, h := panelWithFake(t)
	w := panelRequest(t, h, "POST", "/api/account/disconnect", `{}`)
	if w.Code != http.StatusOK || p.Engine.Status().State != engine.StatePaused || settling(p.Engine.Status()) {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body)
	}
	a, err := p.Engine.Account(context.Background())
	if err != nil || !a.Authenticated {
		t.Fatalf("disconnect lost session: %+v %v", a, err)
	}
	w = panelRequest(t, h, "POST", "/api/account/connect", `{}`)
	if w.Code != http.StatusOK || p.Engine.Status().State != engine.StateConnected {
		t.Fatalf("connect: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", "/conta/sair", nil)
	to, err := p.logout(r)
	if err != nil || to != "/instalacao" || p.Engine.Status().State != engine.StateLoggedOut {
		t.Fatalf("logout: %s %v", to, err)
	}
	a, err = p.Engine.Account(context.Background())
	if err != nil || a.Authenticated {
		t.Fatalf("logout retained authorization: %+v %v", a, err)
	}
}

func TestPanelPendingStoreRoutesAreExplicit(t *testing.T) {
	_, h := panelWithFake(t)
	for _, route := range []struct{ method, path string }{{"GET", "/api/chats"}, {"GET", "/api/chats/user:123"}, {"POST", "/api/history"}} {
		w := panelRequest(t, h, route.method, route.path, `{}`)
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusConflict || body["error"] == "" || strings.Contains(body["error"], "not implemented yet") {
			t.Errorf("%s must report its unavailable dependency explicitly: %d %s", route.path, w.Code, w.Body)
		}
	}
}

func TestPanelCredentialAttemptLimits(t *testing.T) {
	_, h := panelWithFake(t)
	for _, path := range []string{"/api/pair/code", "/api/pair/password"} {
		for i := 0; i < 5; i++ {
			w := panelRequest(t, h, "POST", path, `{}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s attempt %d: %d %s", path, i, w.Code, w.Body)
			}
		}
		w := panelRequest(t, h, "POST", path, `{}`)
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("%s limit: %d %s", path, w.Code, w.Body)
		}
	}
}

func TestPanelCredentialLimitExpires(t *testing.T) {
	var attempts loginAttempts
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if !attempts.allow(false, at) {
			t.Fatal("limited before fifth attempt")
		}
	}
	if attempts.allow(false, at.Add(time.Second)) {
		t.Fatal("sixth code attempt accepted")
	}
	if !attempts.allow(true, at.Add(time.Second)) {
		t.Fatal("password shares code limit")
	}
	if !attempts.allow(false, at.Add(time.Minute)) {
		t.Fatal("expired attempts still count")
	}
}

func TestPanelEngineConnectionStates(t *testing.T) {
	for _, tc := range []struct {
		state engine.ConnState
		busy  bool
		tone  string
	}{
		{engine.StateStarting, true, "warn"}, {engine.StateConnecting, true, "warn"}, {engine.StateConnected, false, "ok"},
		{engine.StateReconnecting, false, "warn"}, {engine.StateResyncing, true, "warn"}, {engine.StatePaused, false, "warn"},
		{engine.StateNotAuthorized, false, "off"}, {engine.StateLoggedOut, false, "off"}, {engine.StateRevoked, false, "off"},
		{engine.StateError, false, "fail"}, {engine.StateStopped, false, "off"},
	} {
		s := engine.Status{State: tc.state}
		if got := settling(s); got != tc.busy {
			t.Errorf("%s settling=%v", tc.state, got)
		}
		if got, label := syncLabel(s); got != tc.tone || label == "" {
			t.Errorf("%s label=(%s,%s)", tc.state, got, label)
		}
		if got := syncKey(s); got != string(tc.state)+"/" {
			t.Errorf("%s key=%s", tc.state, got)
		}
	}
}
