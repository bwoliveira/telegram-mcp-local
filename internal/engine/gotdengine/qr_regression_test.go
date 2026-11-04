package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitLoginWorkers(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		e.mu.Lock()
		n := e.loginWorkers
		e.mu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-time.After(time.Millisecond):
		case <-deadline:
			t.Fatal("login worker did not join")
		}
	}
}

func TestQRAmbiguousImportSuccessNeedsNoSecondSignal(t *testing.T) {
	var exports atomic.Int32
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthExportLoginTokenRequest:
			switch exports.Add(1) {
			case 1:
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("synthetic"), Expires: int(time.Now().Add(time.Minute).Unix())}
			case 2:
				return context.Canceled // server authorized, response lost
			default:
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenSuccess{Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 99, Self: true}}}
			}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			t.Errorf("unexpected RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	waitLogin(t, e, engine.LoginShowQR)
	e.signalLogin(&tg.UpdateShort{Update: &tg.UpdateLoginToken{}}) // exactly one signal
	waitLogin(t, e, engine.LoginDone)
	waitLoginWorkers(t, e)
	if exports.Load() != 3 {
		t.Fatalf("exports=%d", exports.Load())
	}
}

func TestQRPreImportSelfResultArrivingAfterBindDoesNotRevoke(t *testing.T) {
	e := loginEngine(t, func(context.Context, bin.Encoder, bin.Decoder) error { return engine.ErrNotSupported })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.loginCtx = ctx
	e.login = engine.Login{Method: engine.LoginQR, State: engine.LoginShowQR}
	e.hadSession.Store(true)
	unauthorized := tgerr.New(401, "AUTH_KEY_UNREGISTERED")
	invoke := e.observeRPC(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		e.mu.Lock()
		e.account.Authenticated = true
		e.login.State = engine.LoginDone
		e.loginCtx = nil
		e.mu.Unlock()
		return unauthorized
	}))
	err := invoke.Invoke(ctx, &tg.UsersGetUsersRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}}, &tg.UserClassVector{})
	if !errors.Is(err, unauthorized) || e.revoked.Load() {
		t.Fatalf("late pre-import Self revoked accepted key: %v", err)
	}
	if err := e.onSelfError(ctx, unauthorized); err != nil {
		t.Fatal("callback reclassified pre-import result", err)
	}
}

func TestQRDeadlineIsFailureWithExpiryMessage(t *testing.T) {
	e := loginEngine(t, func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
		out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("synthetic"), Expires: int(time.Now().Add(time.Minute).Unix())}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	e.active = ctx
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	l := waitLogin(t, e, engine.LoginFailed)
	if !strings.Contains(l.Error, "expired after 10 minutes") || l.QRURL != "" {
		t.Fatalf("deadline snapshot %+v", l)
	}
	waitLoginWorkers(t, e)
}
