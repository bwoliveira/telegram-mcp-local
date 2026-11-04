package gotdengine

import (
	"context"
	"errors"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"
)

// beginTransport atomically publishes the lifetime Disconnect must join. A
// prior optimistic paused check is not sufficient: Disconnect may finish
// between that check and a reconnect attempt.
func (e *Engine) beginTransport(parent context.Context) (context.Context, context.CancelFunc, chan struct{}, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked.Load() {
		return nil, nil, nil, engine.ErrRevoked
	}
	if e.sessionBlocked.Load() {
		return nil, nil, nil, engine.ErrNotAuthorized
	}
	if e.paused {
		return nil, nil, nil, engine.ErrPaused
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	e.cancel, e.transportDone = cancel, done
	return ctx, cancel, done, nil
}

// onConnectionState runs synchronously inside gotd. Never perform I/O, cancel
// and join a manager, or hold a persistence lock here. gotd's internal retries
// do not return from Client.Run; observing Run alone misses real reconnections.
func (e *Engine) onConnectionState(s telegram.ConnectionState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.paused || e.revoked.Load() || e.status.State == engine.StateLoggedOut || e.status.State == engine.StateStopped {
		return
	}
	switch s {
	case telegram.ConnectionStateDisconnected:
		if e.connectedBefore {
			e.transportDown = true
			e.status.State = engine.StateReconnecting
			e.status.Since = e.now()
		}
	case telegram.ConnectionStateConnecting:
		if e.transportDown {
			e.status.State = engine.StateReconnecting
			e.status.Since = e.now()
		}
	case telegram.ConnectionStateReady:
		if e.connectedBefore && e.transportDown {
			e.metrics.Restarts++
			e.status.Restarts++
			// A transport reconnect does not restart updates.Manager. Request a
			// joined/reset generation so a marker received while offline is
			// replayed now, not only after gotd's 15-minute idle timeout.
			select {
			case e.restart <- struct{}{}:
			default:
			}
		}
		e.connectedBefore = true
		e.transportDown = false
		if e.account.Authenticated {
			e.status.State = engine.StateConnected
			if e.held.Load() {
				e.status.State = engine.StateResyncing
			}
			e.status.Since = e.now()
		}
	}
	e.notify()
}

func revocationReason(err error) string {
	// These are server error types, not error strings or request details.
	for _, reason := range []string{"AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "AUTH_KEY_DUPLICATED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN"} {
		if tgerr.Is(err, reason) {
			return reason
		}
	}
	return ""
}
func (e *Engine) isRevocation(err error) bool {
	if revocationReason(err) == "" {
		return false
	}
	e.mu.Lock()
	authorized := e.account.Authenticated
	e.mu.Unlock()
	return authorized || e.hadSession.Load()
}
func (e *Engine) onSelfError(_ context.Context, err error) error {
	// observeRPC is the revocation gate. Do not reclassify a raw Self result
	// here: a pre-import probe can finish after binding the account, changing
	// its meaning if judged against the new state instead of request start.
	if errors.Is(err, engine.ErrRevoked) {
		return err
	}
	return nil
}

// revoke serializes cleanup against late gotd session saves. The engine is
// terminal after external revocation: a new explicit login uses a new controller.
// A failed secret-backend delete is reported, never disguised as successful.
func (e *Engine) revoke(ctx context.Context, err error) error {
	e.loginComplete.Lock()
	defer e.loginComplete.Unlock()
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if e.revoked.Load() {
		return engine.ErrRevoked
	}
	reason := revocationReason(err)
	if reason == "" {
		reason = "SESSION_REVOKED"
	}
	if e.cfg.Logger != nil {
		e.cfg.Logger.Warn("Telegram session revoked", "rpc_code", reason, "state", string(engine.StateRevoked))
	}
	e.revoked.Store(true)
	e.sessionBlocked.Store(true)
	e.mu.Lock()
	e.account.Authenticated = false
	e.status.State = engine.StateRevoked
	e.status.Since = e.now()
	e.status.LastError = reason
	cancel, loginCancel := e.cancel, e.loginCancel
	e.clearLoginDetails()
	e.previews = make(map[string]time.Time)
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if loginCancel != nil {
		loginCancel()
	}
	e.notify()
	// Cancellation caused by revocation must not cancel deletion itself.
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	del := e.cfg.Session.DeleteSession(cleanup)
	saved := e.cfg.Store.SetAuthState(cleanup, e.cfg.Account, engine.AuthRevoked, reason)
	if del != nil || saved != nil {
		e.mu.Lock()
		e.status.LastError = reason + "; local revocation cleanup failed"
		e.mu.Unlock()
		e.notify()
		return errors.Join(engine.ErrRevoked, errors.New("gotd: local revocation cleanup failed"))
	}
	return engine.ErrRevoked
}

// engineSession guards the compact-session adapter without changing the
// SessionStore contract. A session saved concurrently with revocation can never
// resurrect credentials after DeleteSession returns.
type engineSession struct {
	e     *Engine
	store engine.SessionStore
}

func (s engineSession) LoadSession(ctx context.Context) ([]byte, error) {
	s.e.sessionMu.Lock()
	defer s.e.sessionMu.Unlock()
	if s.e.revoked.Load() {
		return nil, engine.ErrRevoked
	}
	if s.e.sessionBlocked.Load() {
		return nil, engine.ErrNoSession
	}
	b, err := s.store.LoadSession(ctx)
	if err == nil && len(b) > 0 {
		s.e.hadSession.Store(true)
	}
	return b, err
}
func (s engineSession) StoreSession(ctx context.Context, b []byte) error {
	s.e.sessionMu.Lock()
	defer s.e.sessionMu.Unlock()
	if s.e.revoked.Load() {
		return engine.ErrRevoked
	}
	if s.e.sessionBlocked.Load() {
		return engine.ErrNotAuthorized
	}
	return s.store.StoreSession(ctx, b)
}
func (s engineSession) DeleteSession(ctx context.Context) error {
	s.e.sessionMu.Lock()
	defer s.e.sessionMu.Unlock()
	return s.store.DeleteSession(ctx)
}
