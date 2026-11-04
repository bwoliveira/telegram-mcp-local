package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"sync/atomic"
	"testing"
	"time"
)

func TestQRTransportCancellationAndExpiredTokenRenewUntilExplicitCancel(t *testing.T) {
	var exports atomic.Int32
	e := loginEngine(t, func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.AuthExportLoginTokenRequest); !ok {
			t.Errorf("unexpected RPC %T", in)
			return engine.ErrNotSupported
		}
		n := exports.Add(1)
		switch n {
		case 2:
			return context.Canceled
		case 4:
			return tgerr.New(400, "AUTH_TOKEN_EXPIRED")
		}
		expiry := time.Now().Add(-time.Second)
		if n >= 7 {
			expiry = time.Now().Add(time.Minute)
		}
		out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte{byte(n)}, Expires: int(expiry.Unix())}
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(4 * time.Second)
	for exports.Load() < 7 {
		if l := e.Login(); l.State == engine.LoginCancelled || l.State == engine.LoginFailed {
			t.Fatalf("transport error ended live attempt: state=%s exports=%d", l.State, exports.Load())
		}
		select {
		case <-e.Changes():
		case <-deadline:
			t.Fatalf("only %d exports", exports.Load())
		}
	}
	waitLogin(t, e, engine.LoginShowQR)
	e.CancelLogin()
	if l := e.Login(); l.State != engine.LoginCancelled || l.QRURL != "" {
		t.Fatal("explicit cancellation did not clear QR")
	}
	before := exports.Load()
	time.Sleep(150 * time.Millisecond)
	if exports.Load() != before {
		t.Fatal("RPC after explicit cancellation")
	}
}

func TestQRMigrationOperationCancellationIsNotUserCancellation(t *testing.T) {
	var n atomic.Int32
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthExportLoginTokenRequest:
			if n.Add(1) == 1 {
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("synthetic"), Expires: int(time.Now().Add(time.Minute).Unix())}
				return nil
			}
			return errors.Join(errors.New("migration transport failed"), context.Canceled)
		default:
			return engine.ErrNotSupported
		}
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	waitLogin(t, e, engine.LoginShowQR)
	e.signalLogin(&tg.UpdateShort{Update: &tg.UpdateLoginToken{}})
	deadline := time.After(2 * time.Second)
	for n.Load() < 3 {
		select {
		case <-e.Changes():
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("migration/import failure was not retried")
		}
	}
	if e.Login().State == engine.LoginCancelled {
		t.Fatal("migration failure reported as user cancel")
	}
	e.CancelLogin()
}
