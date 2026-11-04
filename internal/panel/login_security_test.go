package panel

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Keep the real synthetic controller for every behavior except Telegram's
// rejected-credential response, which the accepting fake cannot produce.
type rejectedLogin struct{ engine.Controller }

func (rejectedLogin) SubmitCode(_ context.Context, code engine.Secret) error {
	slog.Info("synthetic rejected code", "credential", code)
	return &engine.RPCError{Code: 400, Type: "PHONE_CODE_INVALID", Method: "auth.signIn"}
}

func (rejectedLogin) SubmitPassword(_ context.Context, password engine.Secret) error {
	slog.Info("synthetic rejected password", "credential", password)
	return &engine.RPCError{Code: 400, Type: "PASSWORD_HASH_INVALID", Method: "auth.checkPassword"}
}

func TestPanelRejectedCredentialsAreAbsentFromResponseAndLogs(t *testing.T) {
	p, h := panelWithFake(t)
	p.Engine = rejectedLogin{p.Engine}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, tc := range []struct{ path, field, marker, rpcType string }{
		{"/api/pair/code", "code", "synthetic-rejected-code-marker", "PHONE_CODE_INVALID"},
		{"/api/pair/password", "password", "synthetic-rejected-password-marker", "PASSWORD_HASH_INVALID"},
	} {
		w := panelRequest(t, h, "POST", tc.path, `{"`+tc.field+`":"`+tc.marker+`"}`)
		if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), tc.rpcType) {
			t.Fatalf("rejected credential: %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), tc.marker) || strings.Contains(logs.String(), tc.marker) {
			t.Fatalf("credential marker exposed in response/logs")
		}
	}
	if !strings.Contains(logs.String(), "[redacted]") {
		t.Fatal("negative test did not exercise Secret logging")
	}
}

type failedRemoteLogout struct{ engine.Controller }

func (f failedRemoteLogout) LogOut(ctx context.Context) error {
	// Model the required engine behavior: local authorization is removed even
	// when remote revocation fails. The panel cannot delete engine secrets.
	if err := f.Controller.LogOut(ctx); err != nil {
		return err
	}
	return errors.New("synthetic remote logout unavailable")
}

func TestPanelLogoutFailureShowsRemoteRevocationWarning(t *testing.T) {
	p, h := panelWithFake(t)
	p.Engine = failedRemoteLogout{p.Engine}
	w := panelRequest(t, h, "POST", "/api/account/logout", `{}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), logoutWarning) || !strings.Contains(w.Body.String(), `"logged_out":true`) {
		t.Fatalf("logout warning: %d %s", w.Code, w.Body)
	}
	if p.Engine.Status().State != engine.StateLoggedOut {
		t.Fatal("logout did not clear local authorization")
	}
	if err := p.Engine.Connect(context.Background()); !errors.Is(err, engine.ErrNotAuthorized) {
		t.Fatalf("logout was only a pause: %v", err)
	}
}

func TestPanelUnauthorizedPairCannotBeCancelledCrossOrigin(t *testing.T) {
	p, h := panelWithFake(t)
	// Use the untrusted HTTP mux; Internal is reserved for the desktop host.
	mux := http.NewServeMux()
	p.Register(mux)
	if err := p.Engine.LogOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := panelRequest(t, h, "POST", "/api/pair", `{}`); w.Code != http.StatusOK {
		t.Fatalf("start login: %d %s", w.Code, w.Body)
	}
	w := panelRequest(t, mux, "POST", "/api/pair/cancel", `{}`)
	if w.Code != http.StatusForbidden || p.Engine.Login().State != engine.LoginShowQR {
		t.Fatalf("untrusted request cancelled login: %d %s", w.Code, w.Body)
	}
}
