package gotdengine

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"reflect"
	"time"
)

// loginAuth shares the production API transport, including middleware and DC migration.
// Tests replace only its tg.Invoker; the auth and SRP flows remain real.
func (e *Engine) loginAuth() *auth.Client {
	return auth.NewClient(e.api, rand.Reader, e.cfg.APIID, e.cfg.APIHash.Reveal())
}

func (e *Engine) signalLogin(raw tg.UpdatesClass) {
	var us []tg.UpdateClass
	switch u := raw.(type) {
	case *tg.Updates:
		us = u.Updates
	case *tg.UpdatesCombined:
		us = u.Updates
	case *tg.UpdateShort:
		us = []tg.UpdateClass{u.Update}
	}
	for _, u := range us {
		if _, ok := u.(*tg.UpdateLoginToken); ok {
			select {
			case e.loginSignal <- struct{}{}:
			default:
			}
		}
	}
}
func (e *Engine) Login() engine.Login {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.login
	if l.QRExpiresAt != nil {
		v := *l.QRExpiresAt
		l.QRExpiresAt = &v
	}
	if l.ResendAfter != nil {
		v := *l.ResendAfter
		l.ResendAfter = &v
	}
	return l
}

// clearLoginDetails is called with mu held after an attempt becomes terminal.
func (e *Engine) clearLoginDetails() {
	e.phoneHash = ""
	e.login.Phone = ""
	e.login.QRURL = ""
	e.login.QRExpiresAt = nil
	e.login.CodeType = ""
	e.login.CodeLength = 0
	e.login.ResendAfter = nil
	e.login.PasswordHint = ""
}
func (e *Engine) loginState(gen uint64, s engine.LoginState) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gen != e.loginGeneration || e.loginCtx == nil || e.loginCtx.Err() != nil {
		return context.Canceled
	}
	e.login.State = s
	e.login.Error = ""
	e.login.UpdatedAt = e.now()
	if s != engine.LoginShowQR {
		e.login.QRURL = ""
		e.login.QRExpiresAt = nil
	}
	if s == engine.LoginDone {
		e.loginFinalizing = false
		e.clearLoginDetails()
		if e.loginCancel != nil {
			e.loginCancel()
		}
		e.loginCtx, e.loginCancel = nil, nil
	}
	e.notify()
	return nil
}
func (e *Engine) loginError(gen uint64, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var postAuthorization *authorizedLoginCleanupError
	isCleanup := errors.As(err, &postAuthorization)
	if gen != e.loginGeneration || (e.loginCtx == nil && !isCleanup) || (e.loginFinalizing && !isCleanup) {
		return
	}
	e.loginFinalizing = false
	e.login.State = engine.LoginFailed
	if isCleanup {
		e.login.Error = postAuthorization.Error()
	} else {
		e.login.Error = rpcError(err).Error()
	}
	// CancelLogin invalidates the QR generation synchronously. A cancelled QR
	// child RPC/transport is a failure, not user intent. Preserve the existing
	// synchronous phone-request cancellation contract.
	if postAuthorization == nil && e.login.Method == engine.LoginPhone && errors.Is(err, context.Canceled) {
		e.login.State, e.login.Error = engine.LoginCancelled, ""
	}
	if postAuthorization == nil && e.login.Method == engine.LoginQR && errors.Is(err, context.DeadlineExceeded) {
		e.login.Error = qrExpiredMessage
	}
	e.login.UpdatedAt = e.now()
	if e.cfg.Logger != nil {
		e.cfg.Logger.Warn("Telegram login ended", "state", string(e.login.State), "duration", e.now().Sub(e.login.StartedAt), "error_type", reflect.TypeOf(err), "error", rpcError(err))
	}
	e.clearLoginDetails()
	if e.loginCancel != nil {
		e.loginCancel()
	}
	e.loginCtx, e.loginCancel = nil, nil
	// Publish state, timestamp and sanitized error as a single snapshot.
	e.notify()
}

// loginRetryError keeps recoverable code/password errors retryable, without
// publishing transport details that can contain secrets.
func (e *Engine) loginRetryError(gen uint64, err error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gen != e.loginGeneration || e.loginCtx == nil || e.loginCtx.Err() != nil {
		return context.Canceled
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		e.login.Error = rpcError(err).Error()
		e.login.UpdatedAt = e.now()
		e.notify()
	}
	return rpcError(err)
}

// operation joins per-call cancellation to the attempt's cancelable lifetime.
func (e *Engine) operation(ctx context.Context, state engine.LoginState) (context.Context, context.CancelFunc, uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.login.State != state || e.loginCtx == nil || e.loginCtx.Err() != nil {
		return nil, nil, 0, engine.ErrNoLogin
	}
	if e.loginOperation {
		return nil, nil, 0, engine.ErrLoginRunning
	}
	e.loginOperation = true
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.loginCtx, cancel)
	if e.loginCtx.Err() != nil {
		cancel()
	}
	e.loginWorkers++
	return c, func() {
		stop()
		cancel()
		e.mu.Lock()
		e.loginOperation = false
		e.mu.Unlock()
		e.finishLoginWork()
	}, e.loginGeneration, nil
}

func (e *Engine) finishLoginWork() {
	e.mu.Lock()
	e.loginWorkers--
	e.mu.Unlock()
	e.notify()
}

// watchLoginCancellation also clears credentials while waiting for user input.
// Its barrier matches CancelLogin: identity binding must finish before cleanup.
func (e *Engine) watchLoginCancellation(c context.Context, gen uint64) {
	context.AfterFunc(c, func() {
		e.loginComplete.Lock()
		defer e.loginComplete.Unlock()
		e.loginError(gen, c.Err())
	})
}

// beginLogin serializes attempt creation with cancellation and identity binding.
// Never hold loginComplete during an RPC: cancellation must be able to proceed.
func (e *Engine) beginLogin(r engine.LoginRequest) (context.Context, uint64, error) {
	e.loginComplete.Lock()
	defer e.loginComplete.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked.Load() {
		return nil, 0, engine.ErrRevoked
	}
	if e.sessionBlocked.Load() {
		return nil, 0, engine.ErrNotAuthorized
	}
	if e.account.Authenticated {
		return nil, 0, engine.ErrInvalid
	}
	switch e.login.State {
	case engine.LoginStarting, engine.LoginShowQR, engine.LoginWantCode, engine.LoginWantPass:
		return nil, 0, engine.ErrLoginRunning
	}
	// Do not reuse the client/key until cancelled RPCs and any compensating
	// logout have joined. A late success must not revoke a replacement login.
	if e.loginWorkers != 0 {
		return nil, 0, engine.ErrLoginRunning
	}
	if e.active == nil || e.active.Err() != nil {
		return nil, 0, engine.ErrPaused
	}
	if e.loginCancel != nil {
		e.loginCancel()
	}
	if r.Method == engine.LoginQR {
		e.loginCtx, e.loginCancel = context.WithTimeout(e.active, qrLoginLifetime)
	} else {
		e.loginCtx, e.loginCancel = context.WithCancel(e.active)
	}
	e.loginGeneration++
	e.loginFinalizing = false
	e.phoneHash = ""
	// Do not let a previous attempt's queued token signal import a fresh QR.
	select {
	case <-e.loginSignal:
	default:
	}
	phone := ""
	if r.Method == engine.LoginPhone {
		phone = r.Phone
	}
	now := e.now()
	e.login = engine.Login{Method: r.Method, State: engine.LoginStarting, Phone: phone, StartedAt: now, UpdatedAt: now}
	e.loginWorkers++
	e.watchLoginCancellation(e.loginCtx, e.loginGeneration)
	e.notify()
	return e.loginCtx, e.loginGeneration, nil
}

func (e *Engine) StartLogin(ctx context.Context, r engine.LoginRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Method != engine.LoginQR && r.Method != engine.LoginPhone {
		return engine.ErrInvalid
	}
	c, gen, err := e.beginLogin(r)
	if err != nil {
		return err
	}
	if r.Method == engine.LoginPhone {
		defer e.finishLoginWork()
		op, done, opGen, err := e.operation(ctx, engine.LoginStarting)
		if err != nil {
			if c.Err() != nil {
				err = c.Err()
			} else if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = context.Canceled
			}
			e.loginError(gen, err)
			return err
		}
		defer done()
		if opGen != gen {
			return context.Canceled
		}
		s, err := e.loginAuth().SendCode(op, r.Phone, auth.SendCodeOptions{})
		if op.Err() != nil {
			err = op.Err()
		}
		if err != nil {
			e.loginError(gen, err)
			return rpcError(err)
		}
		err = e.sentCode(op, gen, s)
		if err != nil {
			e.loginError(gen, err)
		}
		return err
	}
	go func() {
		defer e.finishLoginWork()
		q := qrlogin.NewQR(e.api, e.cfg.APIID, e.cfg.APIHash.Reveal(), qrlogin.Options{Migrate: e.qrMigrate, Clock: qrClock{Clock: clock.System, now: e.now}})
		_, err := e.qrAuth(c, q, func(c context.Context, t qrlogin.Token) error {
			expires := t.Expires()
			e.mu.Lock()
			defer e.mu.Unlock()
			if gen != e.loginGeneration || c.Err() != nil {
				return context.Canceled
			}
			e.login.QRURL = t.URL()
			e.login.QRVersion++
			e.login.QRExpiresAt = &expires
			e.login.State = engine.LoginShowQR
			e.login.UpdatedAt = e.now()
			if e.cfg.Logger != nil {
				e.cfg.Logger.Info("Telegram QR refreshed", "count", e.login.QRVersion, "duration", expires.Sub(e.now()))
			}
			e.notify()
			return nil
		})
		if err != nil && c.Err() != nil {
			e.loginError(gen, c.Err())
			return
		}
		if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
			_ = e.loginState(gen, engine.LoginWantPass)
			return
		}
		if err != nil {
			e.loginError(gen, err)
			return
		}
		if err := e.completeLogin(c, gen); err != nil {
			e.loginError(gen, err)
		}
	}()
	return nil
}
func sentCodeDetails(s tg.AuthSentCodeTypeClass) (string, int) {
	switch s := s.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "app", s.Length
	case *tg.AuthSentCodeTypeSMS:
		return "sms", s.Length
	case *tg.AuthSentCodeTypeCall:
		return "call", s.Length
	case *tg.AuthSentCodeTypeFlashCall:
		return "flash_call", 0
	case *tg.AuthSentCodeTypeMissedCall:
		return "missed_call", s.Length
	case *tg.AuthSentCodeTypeEmailCode:
		return "email", s.Length
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "fragment", s.Length
	default:
		return "", 0
	}
}

func (e *Engine) sentCode(ctx context.Context, gen uint64, s tg.AuthSentCodeClass) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch s := s.(type) {
	case *tg.AuthSentCode:
		e.mu.Lock()
		if gen != e.loginGeneration || e.loginCtx == nil || e.loginCtx.Err() != nil || ctx.Err() != nil {
			e.mu.Unlock()
			return context.Canceled
		}
		e.phoneHash = s.PhoneCodeHash
		e.login.CodeType, e.login.CodeLength = sentCodeDetails(s.Type)
		e.login.ResendAfter = nil
		if seconds, ok := s.GetTimeout(); ok {
			after := e.now().Add(time.Duration(seconds) * time.Second)
			e.login.ResendAfter = &after
		}
		e.login.State = engine.LoginWantCode
		e.login.Error = ""
		e.login.UpdatedAt = e.now()
		e.notify()
		e.mu.Unlock()
		return nil
	case *tg.AuthSentCodeSuccess:
		return e.completeLogin(ctx, gen)
	default:
		return engine.ErrNotSupported
	}
}
func (e *Engine) completeLogin(ctx context.Context, gen uint64) error {
	e.mu.Lock()
	if gen == e.loginGeneration {
		e.loginFinalizing = true
	}
	e.mu.Unlock()
	if err := e.finishLogin(ctx, gen); err != nil {
		return e.cleanupAuthorizedLogin(ctx, gen, err)
	}
	return nil
}

func (e *Engine) finishLogin(ctx context.Context, gen uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	users, err := e.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		return rpcError(err)
	}
	u, ok := tg.UserClassArray(users).FirstAsNotEmpty()
	if !ok {
		return engine.ErrInvalid
	}
	e.loginComplete.Lock()
	defer e.loginComplete.Unlock()
	e.mu.Lock()
	valid := gen == e.loginGeneration && e.loginCtx != nil && e.loginCtx.Err() == nil && ctx.Err() == nil
	e.mu.Unlock()
	if !valid {
		return context.Canceled
	}
	if err := e.bindLocked(ctx, u); err != nil {
		return err
	}
	if err := e.loginState(gen, engine.LoginDone); err != nil {
		return err
	}
	select {
	case e.authReady <- struct{}{}:
	default:
	}
	return nil
}

// loginPhone snapshots credentials only for the operation's own generation;
// a replacement attempt must never receive the old operation's code.
func (e *Engine) loginPhone(gen uint64) (string, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gen != e.loginGeneration || e.loginCtx == nil || e.loginCtx.Err() != nil {
		return "", "", context.Canceled
	}
	return e.login.Phone, e.phoneHash, nil
}

func (e *Engine) SubmitCode(ctx context.Context, code engine.Secret) error {
	op, done, gen, err := e.operation(ctx, engine.LoginWantCode)
	if err != nil {
		return err
	}
	defer done()
	phone, hash, err := e.loginPhone(gen)
	if err != nil {
		return err
	}
	_, err = e.loginAuth().SignIn(op, phone, code.Reveal(), hash)
	if op.Err() != nil {
		return op.Err()
	}
	if errors.Is(err, auth.ErrPasswordAuthNeeded) {
		return e.loginState(gen, engine.LoginWantPass)
	}
	if err != nil {
		return e.loginRetryError(gen, err)
	}
	return e.completeLogin(op, gen)
}
func (e *Engine) SubmitPassword(ctx context.Context, p engine.Secret) error {
	op, done, gen, err := e.operation(ctx, engine.LoginWantPass)
	if err != nil {
		return err
	}
	defer done()
	b := []byte(p.Reveal())
	defer clear(b)
	_, err = e.loginAuth().PasswordWith(op, auth.PasswordHashFor(b))
	if op.Err() != nil {
		return op.Err()
	}
	if err != nil {
		return e.loginRetryError(gen, err)
	}
	return e.completeLogin(op, gen)
}
func (e *Engine) ResendCode(ctx context.Context) error {
	op, done, gen, err := e.operation(ctx, engine.LoginWantCode)
	if err != nil {
		return err
	}
	defer done()
	phone, hash, err := e.loginPhone(gen)
	if err != nil {
		return err
	}
	s, err := e.loginAuth().ResendCode(op, phone, hash)
	if op.Err() != nil {
		return op.Err()
	}
	if err != nil {
		return e.loginRetryError(gen, err)
	}
	return e.sentCode(op, gen, s)
}
func (e *Engine) CancelLogin() {
	e.loginComplete.Lock()
	defer e.loginComplete.Unlock()
	e.mu.Lock()
	cancel := e.loginCancel
	e.loginGeneration++
	e.loginFinalizing = false
	e.loginCancel = nil
	e.loginCtx = nil
	e.clearLoginDetails()
	e.login.Error = ""
	e.login.UpdatedAt = e.now()
	if e.login.State != engine.LoginDone {
		e.login.State = engine.LoginCancelled
	}
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.notify()
}
