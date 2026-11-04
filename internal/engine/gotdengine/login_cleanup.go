package gotdengine

import (
	"context"
	"errors"
	"syscall"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// authorizedLoginCleanupError is deliberately not formatted with either the
// persistence error or a cleanup error: both may contain credentials or paths.
// Keep the original cause available for errors.Is/As without exposing its text.
// Callers must recognize this type before classifying cancellation or rpcError.
// Authorization already succeeded, so this is always a failed login, never a
// canceled/expired one, even when the original cause is context.Canceled.
type loginSetupError struct {
	stage string
	cause error
}

func (e *loginSetupError) Error() string { return "gotd: local login setup failed at " + e.stage }
func (e *loginSetupError) Unwrap() error { return e.cause }

func loginFailureReason(cause error) string {
	stage := "post_authorization"
	var setup *loginSetupError
	if errors.As(cause, &setup) {
		stage = setup.stage
	}
	reason := "local_setup_failed"
	switch {
	case errors.Is(cause, engine.ErrAccountMismatch):
		reason = "account_mismatch"
	case errors.Is(cause, syscall.ENOSPC):
		reason = "disk_full"
	case errors.Is(cause, syscall.EACCES):
		reason = "permission_denied"
	case errors.Is(cause, context.Canceled):
		reason = "context_cancelled"
	case errors.Is(cause, context.DeadlineExceeded):
		reason = "deadline_exceeded"
	}
	return " stage=" + stage + " reason=" + reason
}

type authorizedLoginCleanupError struct {
	cause        error
	remoteFailed bool
	deleteFailed bool
}

func (e *authorizedLoginCleanupError) Error() string {
	return e.message() + loginFailureReason(e.cause)
}
func (e *authorizedLoginCleanupError) message() string {
	switch {
	case e.remoteFailed:
		return "gotd: login failed after authorization (phase=post_authorization type=remote_logout_failed); local session retained for recovery; manual cleanup required: check Telegram Devices and terminate this attempt"
	case e.deleteFailed:
		return "gotd: login failed after authorization (phase=post_authorization type=local_session_delete_failed); remote logout confirmed; local cleanup required before retrying"
	default:
		return "gotd: login failed after authorization (phase=post_authorization type=local_persistence_failed); remote logout confirmed and local session removed; start a new attempt"
	}
}

func (e *authorizedLoginCleanupError) Unwrap() error { return e.cause }

// cleanupAuthorizedLogin compensates a known successful remote authorization
// that could not be completed locally. The caller has released loginComplete.
// It publishes no Login state: only the caller may publish LoginFailed for its
// own generation. In particular, stale gen must still revoke the real remote
// authorization, but must not clear/cancel/overwrite a newer login attempt.
func (e *Engine) cleanupAuthorizedLogin(ctx context.Context, gen uint64, cause error) error {
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	result := &authorizedLoginCleanupError{cause: cause}

	// Fence identity publication, checkpoints and late session saves before
	// compensating remotely. Do not hold any of these locks across an RPC:
	// observeRPC may synchronously enter revoke and acquire loginComplete.
	e.loginComplete.Lock()
	e.commitMu.Lock()
	e.held.Store(true)
	e.sessionMu.Lock()
	e.sessionBlocked.Store(true)
	e.sessionMu.Unlock()
	e.mu.Lock()
	e.account.Authenticated = false
	e.mu.Unlock()
	e.commitMu.Unlock()
	e.loginComplete.Unlock()

	// The in-memory gotd client still owns the authorized key, including when
	// the persistence attempt or its context has failed. Try it before stopping
	// the transport, and never delete recoverable credentials on an uncertain
	// remote result. No generation test may skip this compensation.
	_, remoteErr := e.api.AuthLogOut(cleanup)
	result.remoteFailed = remoteErr != nil

	e.loginComplete.Lock()
	if remoteErr == nil {
		e.sessionMu.Lock()
		result.deleteFailed = e.cfg.Session.DeleteSession(cleanup) != nil
		e.hadSession.Store(false)
		e.sessionMu.Unlock()
	}
	e.mu.Lock()
	e.account.Authenticated = false
	e.paused = true
	e.status.State = engine.StateError
	if remoteErr == nil {
		e.status.State = engine.StateLoggedOut
	}
	e.status.Since = e.now()
	e.status.LastError = result.Error()
	e.previews = make(map[string]time.Time)
	// A stale completion must not cancel the transport of a newer attempt.
	// Its Login snapshot and loginCancel are never touched here.
	cancel := e.cancel
	if gen != e.loginGeneration {
		cancel = nil
	}
	e.mu.Unlock()
	e.loginComplete.Unlock()
	// Publish the compensated failure before transport cancellation wakes the
	// lifetime watcher, so it cannot replace this with a generic cancellation.
	e.loginError(gen, result)
	if cancel != nil {
		cancel()
	}
	e.notify()
	return result
}
