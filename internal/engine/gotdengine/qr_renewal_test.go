package gotdengine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestQRRenewalStartsWithSafetyMarginBeforeExpiry(t *testing.T) {
	var exports atomic.Int32
	renewed := make(chan time.Time, 1)
	expires := time.Now().Add(8 * time.Second).Truncate(time.Second)
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.AuthExportLoginTokenRequest); !ok {
			t.Errorf("unexpected QR RPC %T", in)
			return engine.ErrNotSupported
		}
		n := exports.Add(1)
		expiry := expires
		if n > 1 {
			renewed <- time.Now()
			expiry = time.Now().Add(time.Minute)
		}
		out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte{byte(n)}, Expires: int(expiry.Unix())}
		return nil
	})
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	first := waitLogin(t, e, engine.LoginShowQR)
	if first.QRVersion != 1 || first.QRExpiresAt == nil || !first.QRExpiresAt.Equal(expires) {
		t.Fatal("initial expiry/version not preserved")
	}
	select {
	case at := <-renewed:
		if margin := expires.Sub(at); margin < time.Second {
			t.Fatalf("QR renewal has no RPC safety margin: %v", margin)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("QR did not renew without a panel or token signal")
	}
	deadline := time.After(time.Second)
	for e.Login().QRVersion != 2 {
		select {
		case <-e.Changes():
		case <-deadline:
			t.Fatal("renewed QR not published")
		}
	}
	second := e.Login()
	if second.QRURL == first.QRURL || second.QRExpiresAt == nil || !second.QRExpiresAt.After(expires) {
		t.Fatal("renewal retained old credential or expiry")
	}
	e.CancelLogin()
}
