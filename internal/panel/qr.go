package panel

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// A Telegram login token (auth.exportLoginToken) lives for about 30 seconds.
// The engine is expected to export a new one and bump QRVersion before the
// old one expires. The panel does not rely on it: if the same code is still on
// screen after its lifetime plus a grace period, the panel restarts the QR
// login itself, so the code always renews by itself (P04).
const (
	defaultQRLifetime = 30 * time.Second
	maxQRLifetime     = 5 * time.Minute
	qrGrace           = 3 * time.Second
)

// qrWatch follows the QR code the page shows. Seq changes each time a new
// code is to be drawn, whether the engine or the panel renewed it, so the page
// never has to compare credentials: the token itself never leaves the engine
// except as the PNG of GET /api/pair/qr.png.
type qrWatch struct {
	mu   sync.Mutex
	key  string
	seen time.Time
	seq  int
	// now is the wall clock; tests replace it.
	now func() time.Time
}

func (q *qrWatch) clock() time.Time {
	if q.now != nil {
		return q.now()
	}
	return time.Now()
}

// qrKey tells one exported token from the next without reading the token.
func qrKey(l engine.Login) string {
	return strconv.Itoa(l.QRVersion) + "/" + strconv.FormatInt(l.StartedAt.UnixNano(), 10)
}

// qrLifetime is how long the engine said the code lives, measured in the
// engine's own clock (expiry minus the moment it was issued), so a fake clock
// or a skewed one does not matter.
func qrLifetime(l engine.Login) time.Duration {
	if l.QRExpiresAt == nil {
		return defaultQRLifetime
	}
	issued := l.UpdatedAt
	if issued.IsZero() || issued.After(*l.QRExpiresAt) {
		issued = l.StartedAt
	}
	d := l.QRExpiresAt.Sub(issued)
	if d <= 0 || d > maxQRLifetime {
		return defaultQRLifetime
	}
	return d
}

// observeQR records a new engine-issued QR and returns its display sequence.
// Unlike renewQR, it never starts or cancels a login and is safe on GET paths.
func (p *Panel) observeQR(login engine.Login) int {
	q := &p.qrw
	q.mu.Lock()
	defer q.mu.Unlock()
	if login.State != engine.LoginShowQR {
		q.key = ""
		return q.seq
	}
	if key := qrKey(login); key != q.key {
		q.key, q.seen = key, q.clock()
		q.seq++
	}
	return q.seq
}

// renewQR returns the login as it is now, with the sequence of the code to
// draw, after restarting a QR login whose code outlived its lifetime.
func (p *Panel) renewQR(ctx context.Context) (engine.Login, int) {
	q := &p.qrw
	q.mu.Lock()
	defer q.mu.Unlock()
	l := p.Engine.Login()
	if l.State != engine.LoginShowQR {
		q.key = ""
		return l, q.seq
	}
	now := q.clock()
	if k := qrKey(l); k != q.key {
		q.key, q.seen = k, now
		q.seq++
		return l, q.seq
	}
	if now.Sub(q.seen) < qrLifetime(l)+qrGrace {
		return l, q.seq
	}
	// The same code for too long: ask for a new one. Only a QR login is
	// restarted; one waiting for a code or the 2FA password never is.
	p.Engine.CancelLogin()
	if err := p.Engine.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginQR}); err != nil {
		return p.Engine.Login(), q.seq
	}
	l = p.Engine.Login()
	q.key, q.seen = qrKey(l), now
	q.seq++
	return l, q.seq
}
