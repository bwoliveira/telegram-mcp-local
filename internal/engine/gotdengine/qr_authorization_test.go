package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"sync/atomic"
	"testing"
)

func TestConcurrentAuthorizationCannotCompensateWinningLogin(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var signins atomic.Int32
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthSignInRequest:
			signins.Add(1)
			close(entered)
			<-release
			out.(*tg.AuthAuthorizationBox).Authorization = &tg.AuthAuthorization{User: &tg.User{ID: 99, Self: true}}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			t.Errorf("unexpected RPC (winning login must not log out): %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	if err := e.ResumePhone(context.Background(), engine.Secret(`{"Phone":"synthetic","Hash":"synthetic","State":"code"}`)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- e.SubmitCode(context.Background(), "synthetic") }()
	<-entered
	if err := e.SubmitCode(context.Background(), "synthetic"); !errors.Is(err, engine.ErrLoginRunning) {
		t.Fatalf("duplicate authorization was not rejected: %v", err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if e.Login().State != engine.LoginDone || signins.Load() != 1 || e.sessionBlocked.Load() {
		t.Fatal("concurrent request invalidated successful login")
	}
}

func TestKnownExportSuccessRacingTransportCancelStillCompensates(t *testing.T) {
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	var logouts atomic.Int32
	e := loginEngine(t, func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthExportLoginTokenRequest:
			stop()
			out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenSuccess{Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 99, Self: true}}}
		case *tg.AuthLogOutRequest:
			if ctx.Err() != nil {
				t.Error("cleanup inherited cancellation")
			}
			logouts.Add(1)
		default:
			t.Errorf("known authorization should be compensated, RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	e.active = lifetime
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	waitLoginWorkers(t, e)
	l := waitLogin(t, e, engine.LoginFailed)
	if logouts.Load() != 1 || l.Error == "context canceled" || e.Status().State != engine.StateLoggedOut {
		t.Fatalf("accepted export escaped cleanup: state=%s logouts=%d", l.State, logouts.Load())
	}
}

func TestDefaultDeviceUsesApprovedBrand(t *testing.T) {
	e := loginEngine(t, func(context.Context, bin.Encoder, bin.Decoder) error { return engine.ErrNotSupported })
	if e.cfg.Device.Model != "Telegram MCP" {
		t.Fatalf("device model %q", e.cfg.Device.Model)
	}
}
