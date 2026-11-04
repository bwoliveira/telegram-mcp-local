package gotdengine

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// PhoneContinuation returns sensitive, opaque login state. The CLI stores it
// ONLY as a Session credential in a separate internal/secrets account namespace.
func (e *Engine) PhoneContinuation() (engine.Secret, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loginCtx == nil || e.loginCtx.Err() != nil || e.login.Method != engine.LoginPhone || (e.login.State != engine.LoginWantCode && e.login.State != engine.LoginWantPass) {
		return "", engine.ErrNoLogin
	}
	b, err := json.Marshal(struct {
		Phone, Hash string
		State       engine.LoginState
	}{e.login.Phone, e.phoneHash, e.login.State})
	defer clear(b)
	return engine.Secret(b), err
}
func (e *Engine) ResumePhone(ctx context.Context, s engine.Secret) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var v struct {
		Phone, Hash string
		State       engine.LoginState
	}
	b := []byte(s.Reveal())
	defer clear(b)
	if json.Unmarshal(b, &v) != nil || v.Phone == "" || v.Hash == "" || (v.State != engine.LoginWantCode && v.State != engine.LoginWantPass) {
		return engine.ErrInvalid
	}
	e.loginComplete.Lock()
	defer e.loginComplete.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil || e.active.Err() != nil {
		return engine.ErrPaused
	}
	if e.loginWorkers != 0 {
		return engine.ErrLoginRunning
	}
	if e.account.Authenticated {
		return engine.ErrInvalid
	}
	if e.loginCancel != nil {
		e.loginCancel()
	}
	e.loginCtx, e.loginCancel = context.WithCancel(e.active)
	e.loginGeneration++
	e.login = engine.Login{Method: engine.LoginPhone, State: v.State, Phone: v.Phone, StartedAt: e.now(), UpdatedAt: e.now()}
	e.phoneHash = v.Hash
	e.watchLoginCancellation(e.loginCtx, e.loginGeneration)
	e.notify()
	return nil
}
