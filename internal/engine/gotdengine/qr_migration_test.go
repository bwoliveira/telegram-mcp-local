package gotdengine

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"testing"
	"time"
)

// Run the real gotd QR importer with only the network/migration replaced. gotd
// probes Self on the destination DC before importing the accepted token. The
// session storage is read-before-written by gotd even during a fresh login.
func TestQRScanMigrationAllowsUnauthenticatedDestinationProbe(t *testing.T) {
	var exports, imports, migrations int
	var e *Engine
	e = loginEngine(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.AuthExportLoginTokenRequest:
			exports++
			if exports == 1 {
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginToken{Token: []byte("synthetic-qr"), Expires: int(time.Now().Add(time.Minute).Unix())}
			} else {
				out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenMigrateTo{DCID: 4, Token: []byte("synthetic-import")}
			}
		case *tg.AuthImportLoginTokenRequest:
			imports++
			out.(*tg.AuthLoginTokenBox).LoginToken = &tg.AuthLoginTokenSuccess{Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 99, Self: true}}}
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		default:
			t.Errorf("unexpected RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})
	session := &lifecycleSession{}
	e.cfg.Session = session
	e.qrMigrate = func(ctx context.Context, dc int) error {
		migrations++
		if dc != 4 {
			t.Errorf("DC %d", dc)
		}
		s := engineSession{e: e, store: session}
		if err := s.StoreSession(ctx, []byte("synthetic-unauthorized-MTProto-key")); err != nil {
			return err
		}
		if _, err := s.LoadSession(ctx); err != nil {
			return err
		}
		unauthorized := tgerr.New(401, "AUTH_KEY_UNREGISTERED")
		if err := e.onSelfError(ctx, unauthorized); err != nil {
			return err
		}
		probe := e.observeRPC(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return unauthorized }))
		err := probe.Invoke(ctx, &tg.UsersGetUsersRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}}, &tg.UserClassVector{})
		if err == engine.ErrRevoked {
			return err
		}
		return nil
	}
	if err := e.StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		t.Fatal(err)
	}
	waitLogin(t, e, engine.LoginShowQR)
	e.signalLogin(&tg.UpdateShort{Update: &tg.UpdateLoginToken{}})
	l := waitLogin(t, e, engine.LoginDone)
	if l.Error != "" || l.QRURL != "" {
		t.Fatal("completed login retains QR/error")
	}
	// LoginDone is published under e.mu after all RPC counters were written.
	if exports != 2 || imports != 1 || migrations != 1 {
		t.Fatalf("exports=%d imports=%d migrations=%d", exports, imports, migrations)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.deleted != 0 || len(session.data) == 0 {
		t.Fatal("migration revoked/deleted an unauthenticated key")
	}
}
