package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

var errLoginRateLimited = errors.New("too many login attempts; try again in one minute")

// Login attempts share a panel-local limit, so changing browser sessions or
// bearer clients cannot bypass the five-attempt-per-minute credential gate.
type loginAttempts struct {
	mu       sync.Mutex
	code     []time.Time
	password []time.Time
}

func (a *loginAttempts) allow(password bool, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	times := &a.code
	if password {
		times = &a.password
	}
	var live []time.Time
	for _, at := range *times {
		if now.Sub(at) < time.Minute {
			live = append(live, at)
		}
	}
	if len(live) >= 5 {
		*times = live
		return false
	}
	*times = append(live, now)
	return true
}

func (p *Panel) apiCode(r *http.Request) (any, error) {
	return p.submitCredential(r, false)
}

func (p *Panel) apiPassword(r *http.Request) (any, error) {
	return p.submitCredential(r, true)
}

func (p *Panel) submitCredential(r *http.Request, password bool) (any, error) {
	if !p.loginAttempts.allow(password, time.Now()) {
		return nil, errLoginRateLimited
	}
	var body struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, engine.ErrInvalid
	}
	var err error
	if password {
		if body.Password == "" {
			return nil, engine.ErrInvalid
		}
		err = p.Engine.SubmitPassword(r.Context(), engine.Secret(body.Password))
	} else {
		if body.Code == "" {
			return nil, engine.ErrInvalid
		}
		err = p.Engine.SubmitCode(r.Context(), engine.Secret(body.Code))
	}
	if err != nil {
		return nil, loginError(err)
	}
	return p.Engine.Login(), nil
}

// loginErrors says Telegram's login refusals in the person's language. The
// error carries only the RPC type, never the code or the password typed
// (security.md §6).
var loginErrors = map[string]string{
	"PHONE_CODE_INVALID":      i18n.N("código incorreto; confira e digite de novo"),
	"PHONE_CODE_EXPIRED":      i18n.N("o código expirou; peça um novo"),
	"PHONE_CODE_EMPTY":        i18n.N("digite o código que o Telegram mandou"),
	"PASSWORD_HASH_INVALID":   i18n.N("senha incorreta; confira e digite de novo"),
	"PHONE_NUMBER_INVALID":    i18n.N("número de telefone inválido; use DDI e DDD"),
	"PHONE_NUMBER_BANNED":     i18n.N("o Telegram bloqueou este número"),
	"SESSION_PASSWORD_NEEDED": i18n.N("a conta tem verificação em duas etapas; digite a senha"),
}

func loginError(err error) error {
	var rpc *engine.RPCError
	if errors.As(err, &rpc) {
		if text, ok := loginErrors[rpc.Type]; ok {
			// The type stays beside the words: it is what a person or a
			// support thread searches for.
			return userError{i18n.T(text) + " (" + rpc.Type + ")"}
		}
		return userError{i18n.Tf("o Telegram recusou: %s", rpc.Type)}
	}
	var flood *engine.FloodWaitError
	if errors.As(err, &flood) {
		return userError{i18n.Tf("muitas tentativas; o Telegram pede para esperar até %s", momentAny(flood.Until))}
	}
	return err
}

func (p *Panel) apiResendCode(r *http.Request) (any, error) {
	if err := p.Engine.ResendCode(r.Context()); err != nil {
		return nil, loginError(err)
	}
	return p.Engine.Login(), nil
}

func (p *Panel) apiDisconnect(r *http.Request) (any, error) {
	// Disconnect keeps both the authorization and the local history. It is
	// never an alias for LogOut or a temporary exclusive subprocess lock.
	if err := p.Engine.Disconnect(r.Context(), "user"); err != nil {
		return nil, err
	}
	return p.Engine.Status(), nil
}

func (p *Panel) apiConnect(r *http.Request) (any, error) {
	if err := p.Engine.Connect(r.Context()); err != nil {
		return nil, err
	}
	return p.Engine.Status(), nil
}

var logoutWarning = i18n.N("Encerre também em Configurações → Dispositivos do Telegram.")

func (p *Panel) apiLogout(r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	err := p.Engine.LogOut(ctx)
	if err != nil && p.Engine.Status().State != engine.StateLoggedOut {
		return nil, userError{logoutWarning}
	}
	_ = p.State.SetSetting(r.Context(), setupSetting, "")
	out := map[string]any{"logged_out": true}
	if err != nil {
		out["warning"] = logoutWarning
	}
	return out, nil
}
