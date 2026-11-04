package gotdengine

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQRPermanentMigrationErrorIsNotUserCancellation(t *testing.T) {
	var exports atomic.Int32
	e := loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.AuthExportLoginTokenRequest); !ok {
			t.Errorf("unexpected RPC %T", in)
			return engine.ErrNotSupported
		}
		if exports.Add(1) == 1 {
			out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("synthetic"), Expires: int(time.Now().Add(time.Minute).Unix())}
		} else {
			out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenMigrateTo{DCID: 4, Token: []byte("synthetic")}
		}
		return nil
	})
	e.qrMigrate = func(context.Context, int) error { return tgerr.New(400, "DC_ID_INVALID") }
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	waitLogin(t, e, engine.LoginShowQR)
	e.signalLogin(&tg.UpdateShort{Update: &tg.UpdateLoginToken{}})
	l := waitLogin(t, e, engine.LoginFailed)
	if !strings.Contains(l.Error, "DC_ID_INVALID") || l.QRURL != "" || e.revoked.Load() {
		t.Fatalf("migration failure snapshot %+v", l)
	}
	waitLoginWorkers(t, e)
}
