package gotdengine

import (
	"context"
	"errors"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"io"
	"net"
	"time"
)

// Bound the whole attempt, including 2FA, rather than a single token's lifetime.
const qrLoginLifetime = 10 * time.Minute
const qrExpiredMessage = "Telegram login expired after 10 minutes; retry telegram-mcp auth --qr"

// Use gotd's Export/Import primitives instead of restarting QR.Auth: its initial
// AuthLoginTokenSuccess path waits for another update, which may already have
// been consumed when a successful import lost its response. Always confirm an
// accepted token immediately, including after an ambiguous transport failure.
func (e *Engine) qrAuth(ctx context.Context, q qrlogin.QR, show func(context.Context, qrlogin.Token) error) (*tg.AuthAuthorization, error) {
	confirm := false
	backoff := 100 * time.Millisecond
	for {
		var err error
		if confirm {
			var result *tg.AuthAuthorization
			result, err = q.Import(ctx)
			if err == nil {
				return result, nil
			} // finalize/compensate even if cancellation raced
		} else {
			var token qrlogin.Token
			var exported tg.AuthLoginTokenClass
			exported, err = e.api.AuthExportLoginToken(ctx, &tg.AuthExportLoginTokenRequest{APIID: e.cfg.APIID, APIHash: e.cfg.APIHash.Reveal()})
			if err == nil {
				switch result := exported.(type) {
				case *tg.AuthLoginToken:
					token = qrlogin.NewToken(result.Token, result.Expires)
				case *tg.AuthLoginTokenMigrateTo:
					confirm = true
					continue
				case *tg.AuthLoginTokenSuccess:
					authorization, ok := result.Authorization.(*tg.AuthAuthorization)
					if !ok {
						return nil, errors.New("gotd: unexpected QR authorization type")
					}
					// Preserve success directly; another import RPC could lose it to
					// a concurrent cancellation and leave an authorized key orphaned.
					return authorization, nil
				default:
					return nil, errors.New("gotd: unexpected QR export result")
				}
			}
			if err == nil {
				if err = show(ctx, token); err != nil {
					return nil, err
				}
				backoff = 100 * time.Millisecond
				timer := time.NewTimer(qrRenewAfter(token.Expires().Sub(e.now()).Truncate(time.Second)))
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-e.loginSignal:
					timer.Stop()
					confirm = true
				case <-timer.C:
				}
				continue
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		expired := tgerr.Is(err, "AUTH_TOKEN_EXPIRED")
		var network net.Error
		if !(expired || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &network)) {
			return nil, err
		}
		if expired {
			confirm = false
		}
		if e.cfg.Logger != nil {
			e.cfg.Logger.Warn("Telegram QR retry", "duration", backoff, "error", rpcError(err))
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if backoff < 2*time.Second {
			backoff *= 2
			if backoff > 2*time.Second {
				backoff = 2 * time.Second
			}
		}
	}
}
