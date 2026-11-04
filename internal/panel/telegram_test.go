package panel

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"net/http"
	"strings"
	"testing"
	"time"
)

func pageBody(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	w := panelRequest(t, h, "GET", path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	return w.Body.String()
}

func mustContain(t *testing.T, where, page string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(page, s) {
			t.Errorf("%s lacks %q", where, s)
		}
	}
}

// The QR code renews by itself: a new token from the engine changes the
// sequence the page draws, and a token kept past its lifetime makes the
// panel ask for a new QR login.
func TestPanelQRRenewsByItself(t *testing.T) {
	p, h := panelWithFake(t)
	if err := p.Engine.LogOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	p.qrw.now = func() time.Time { return now }
	if w := panelRequest(t, h, "POST", "/api/pair", `{"method":"qr"}`); w.Code != http.StatusOK {
		t.Fatalf("start QR: %d %s", w.Code, w.Body)
	}
	_, first := p.renewQR(context.Background())
	if _, again := p.renewQR(context.Background()); again != first {
		t.Fatalf("same code redrawn: %d then %d", first, again)
	}
	lifetime := qrLifetime(p.Engine.Login())
	now = now.Add(lifetime + qrGrace + time.Second)
	login, renewed := p.renewQR(context.Background())
	if renewed == first || login.State != engine.LoginShowQR {
		t.Fatalf("stale code was not renewed: seq %d→%d, %s", first, renewed, login.State)
	}
	w := panelRequest(t, h, "GET", "/api/state", "")
	if !strings.Contains(w.Body.String(), `"qr_seq":`) || strings.Contains(w.Body.String(), "tg://") {
		t.Fatalf("state: %s", w.Body)
	}
	if w := panelRequest(t, h, "GET", "/api/pair/qr.png", ""); w.Code != http.StatusOK {
		t.Fatalf("renewed QR image: %d", w.Code)
	}
}

// A login waiting for the code or the 2FA password is never restarted by the
// QR renewal, however long the person takes.
func TestPanelQRRenewalLeavesPhoneLoginAlone(t *testing.T) {
	p, h := panelWithFake(t)
	if err := p.Engine.LogOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	p.qrw.now = func() time.Time { return now }
	panelRequest(t, h, "POST", "/api/pair", `{"phone":"+55 11 91234-5678"}`)
	panelRequest(t, h, "POST", "/api/pair/code", `{"code":"12345"}`)
	now = now.Add(time.Hour)
	if l, _ := p.renewQR(context.Background()); l.State != engine.LoginWantPass {
		t.Fatalf("2FA step replaced: %s", l.State)
	}
}

func TestQRLifetimeFromEngineClock(t *testing.T) {
	issued := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	until := issued.Add(30 * time.Second)
	if d := qrLifetime(engine.Login{UpdatedAt: issued, QRExpiresAt: &until}); d != 30*time.Second {
		t.Fatalf("lifetime %s", d)
	}
	far := issued.Add(time.Hour)
	if d := qrLifetime(engine.Login{UpdatedAt: issued, QRExpiresAt: &far}); d != defaultQRLifetime {
		t.Fatalf("absurd lifetime accepted: %s", d)
	}
	if d := qrLifetime(engine.Login{}); d != defaultQRLifetime {
		t.Fatalf("missing expiry: %s", d)
	}
}

// The 2FA password and the code never come back in a page, a response or the
// state the page polls.
func TestPanelLoginStateNeverEchoesCredentials(t *testing.T) {
	p, h := panelWithFake(t)
	if err := p.Engine.LogOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	const code = "code-marker-31337"
	const password = "password-marker-31337"
	panelRequest(t, h, "POST", "/api/pair", `{"phone":"+55 11 91234-5678"}`)
	state := panelRequest(t, h, "GET", "/api/state", "").Body.String()
	mustContain(t, "/api/state", state, `"code_via":"O Telegram mandou o código no app`)
	panelRequest(t, h, "POST", "/api/pair/code", `{"code":"`+code+`"}`)
	state = panelRequest(t, h, "GET", "/api/state", "").Body.String()
	page := pageBody(t, h, "/instalacao")
	for _, s := range []string{state, page} {
		if strings.Contains(s, code) || strings.Contains(s, password) {
			t.Fatal("credential echoed")
		}
	}
	w := panelRequest(t, h, "POST", "/api/pair/password", `{"password":"`+password+`"}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), password) {
		t.Fatalf("password: %d %s", w.Code, w.Body)
	}
}

func TestPanelLoginErrorsAreReadable(t *testing.T) {
	err := loginError(&engine.RPCError{Code: 400, Type: "PHONE_CODE_EXPIRED", Method: "auth.signIn"})
	if !strings.Contains(err.Error(), "o código expirou") || !strings.Contains(err.Error(), "PHONE_CODE_EXPIRED") {
		t.Fatalf("expired code: %v", err)
	}
	err = loginError(&engine.RPCError{Code: 400, Type: "SOMETHING_NEW", Method: "auth.signIn"})
	if !strings.Contains(err.Error(), "SOMETHING_NEW") {
		t.Fatalf("unknown type hidden: %v", err)
	}
	err = loginError(&engine.FloodWaitError{Method: "auth.sendCode", Until: time.Now().Add(time.Hour)})
	if _, ok := err.(userError); !ok {
		t.Fatalf("flood wait: %T %v", err, err)
	}
}

// Conta: who is logged in, the mode, and Disconnect kept apart from LogOut.
func TestPanelContaTab(t *testing.T) {
	p, h := panelWithFake(t)
	page := pageBody(t, h, "/conta")
	mustContain(t, "/conta", page, "Test Account", "@fake_self", `data-mode="read_only"`, "Somente leitura",
		`action="/conta/desconectar"`, `action="/conta/sair"`, "não a marca como lida")
	if to, err := p.pause(newFormRequest("/conta/desconectar")); err != nil || !strings.HasPrefix(to, "/conta?ok=") {
		t.Fatalf("pause: %s %v", to, err)
	}
	if st := p.Engine.Status().State; st != engine.StatePaused {
		t.Fatalf("pause state: %s", st)
	}
	if a, _ := p.Engine.Account(context.Background()); !a.Authenticated {
		t.Fatal("disconnect logged out")
	}
	page = pageBody(t, h, "/conta")
	mustContain(t, "/conta paused", page, `action="/conta/reconectar"`)
	if _, err := p.resume(newFormRequest("/conta/reconectar")); err != nil || p.Engine.Status().State != engine.StateConnected {
		t.Fatalf("resume: %v %s", err, p.Engine.Status().State)
	}
}

// Status: connection, reconnections, history progress and coverage, mode.
func TestPanelStatusTab(t *testing.T) {
	p, h := panelWithFake(t)
	ctx := context.Background()
	_ = p.Engine.Disconnect(ctx, "user")
	_ = p.Engine.Connect(ctx)
	page := hydratedStatusBody(t, h)
	mustContain(t, "/status", page, "Reconexões", "data-restarts", "Mensagens guardadas", "data-coverage=",
		"Lacunas em recuperação", "Lacunas sem recuperação", "Somente leitura", "fakeengine", "Eventos recentes", "Conexão: Desconectado")
	tone, text := coverageText(engine.Status{State: engine.StateConnected, Updates: engine.UpdatesStatus{OpenGaps: 2}}, nil)
	if tone != "sync" || !strings.Contains(text, "2") {
		t.Fatalf("gaps: %s %s", tone, text)
	}
	if tone, _ := coverageText(engine.Status{State: engine.StateConnected}, nil); tone != "ok" {
		t.Fatalf("clean: %s", tone)
	}
}

// Funções: every tool, and the ones that change Telegram marked as such.
func TestPanelFuncoesTab(t *testing.T) {
	_, h := panelWithFake(t)
	page := pageBody(t, h, "/funcoes")
	mustContain(t, "/funcoes", page, `data-tool="list_chats"`, `data-tool="send_text_message"`, "data-functions-readonly", "data-changes")
	send := page[strings.Index(page, `data-tool="send_text_message"`):]
	send = send[:strings.Index(send, "</article>")]
	if !strings.Contains(send, "data-changes") {
		t.Fatal("send_text_message is not marked as changing Telegram")
	}
	read := page[strings.Index(page, `data-tool="list_chats"`):]
	read = read[:strings.Index(read, "</article>")]
	if strings.Contains(read, "data-changes") {
		t.Fatal("list_chats is marked as changing Telegram")
	}
}

func TestPanelTabsInOtherLanguages(t *testing.T) {
	defer i18n.Set(i18n.Source)
	_, h := panelWithFake(t)
	for lang, want := range map[string][]string{
		"en": {"Telegram MCP", "not a Telegram tool or endorsed by Telegram", "Reconnections"},
		"es": {"Telegram MCP", "no es una herramienta de Telegram ni cuenta con su respaldo", "Reconexiones"},
	} {
		i18n.Set(lang)
		mustContain(t, lang+" /status", hydratedStatusBody(t, h), want...)
	}
}

func newFormRequest(path string) *http.Request {
	r, _ := http.NewRequest("POST", "http://127.0.0.1:47831"+path, nil)
	return r
}
