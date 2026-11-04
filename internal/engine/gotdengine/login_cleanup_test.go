package gotdengine

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestCleanupAuthorizedLoginCanceledContextAndStaleGeneration(t *testing.T) {
	var calls atomic.Int32
	e := loginEngine(t, func(ctx context.Context, in bin.Encoder, _ bin.Decoder) error {
		if _, ok := in.(*tg.AuthLogOutRequest); !ok {
			t.Fatalf("cleanup RPC %T, want auth.logOut", in)
		}
		calls.Add(1)
		if err := ctx.Err(); err != nil {
			t.Fatalf("cleanup inherited cancellation: %v", err)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
			t.Fatal("cleanup must have its own five-second deadline")
		}
		return nil
	})
	session := &lifecycleSession{data: []byte("recoverable session")}
	e.cfg.Session = session
	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	e.mu.Lock()
	e.loginGeneration = 2
	e.loginCtx, e.loginCancel = newCtx, newCancel
	e.login = engine.Login{Method: engine.LoginQR, State: engine.LoginShowQR, QRURL: "synthetic-new-generation"}
	wantLogin := e.login
	e.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.cleanupAuthorizedLogin(ctx, 1, context.Canceled)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cleanup must preserve original cancellation as its cause", err)
	}
	if calls.Load() != 1 || !e.sessionBlocked.Load() || !e.held.Load() || e.Status().State != engine.StateLoggedOut {
		t.Fatalf("stale authorized generation escaped cleanup: calls=%d state=%s", calls.Load(), e.Status().State)
	}
	if !reflect.DeepEqual(e.Login(), wantLogin) || newCtx.Err() != nil {
		t.Fatal("cleanup modified or canceled a newer login generation")
	}
	if session.deleted != 1 || len(session.data) != 0 {
		t.Fatal("confirmed logout did not delete the local session")
	}
	guard := engineSession{e: e, store: session}
	if err := guard.StoreSession(context.Background(), []byte("late session save")); !errors.Is(err, engine.ErrNotAuthorized) {
		t.Fatal("cleanup allowed a late session save", err)
	}
}

func TestCleanupAuthorizedLoginLocalDeleteFailureIsSanitized(t *testing.T) {
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		if _, ok := in.(*tg.AuthLogOutRequest); !ok {
			t.Fatalf("unexpected cleanup RPC %T", in)
		}
		return nil
	})
	e.cfg.Session = &lifecycleSession{data: []byte("synthetic session"), deleteErr: errors.New("secret backend path")}
	cause := errors.New("secret original persistence error")
	err := e.cleanupAuthorizedLogin(context.Background(), 0, cause)
	if !errors.Is(err, cause) || e.Status().State != engine.StateLoggedOut {
		t.Fatal("confirmed remote logout must remain terminal despite local deletion failure", err)
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "type=local_session_delete_failed") {
		t.Fatalf("local cleanup failure did not expose its sanitized phase/type: %q", err)
	}
}

func TestCleanupAuthorizedLoginFailedLogoutPreservesRecovery(t *testing.T) {
	var calls atomic.Int32
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		if _, ok := in.(*tg.AuthLogOutRequest); !ok {
			t.Fatalf("unexpected cleanup RPC %T", in)
		}
		calls.Add(1)
		return errors.New("remote secret session key")
	})
	persisted := []byte("recoverable authorized session")
	session := &lifecycleSession{data: append([]byte(nil), persisted...)}
	e.cfg.Session = session
	cause := errors.New("local secret path and session data")
	err := e.cleanupAuthorizedLogin(context.Background(), 0, cause)
	if err == nil || !errors.Is(err, cause) {
		t.Fatal("original persistence error not wrapped", err)
	}
	if calls.Load() != 1 || session.deleted != 0 || !bytes.Equal(session.data, persisted) {
		t.Fatal("failed remote logout destroyed the recoverable session")
	}
	if !e.sessionBlocked.Load() || !e.held.Load() || e.Status().State == engine.StateLoggedOut {
		t.Fatal("failed remote logout must block ingestion without claiming logout")
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "Devices") || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("error must sanitize details and request manual device cleanup: %q", err)
	}
	if e.Status().LastError != err.Error() {
		t.Fatal("status does not expose the same sanitized cleanup outcome")
	}
}
