package gotdengine

import (
	"time"

	"github.com/gotd/td/clock"
)

// qrClock leaves time for the export RPC before the currently displayed token
// expires. gotd's default Auth timer only truncates expiry to whole seconds,
// which gives no reliable safety margin for that round trip. Keep the real
// token expiry in Login; only the renewal timer is shortened. Short-lived
// tokens retain 80% of their remaining lifetime instead of spinning immediately.
// Both the first timer and subsequent Reset calls need the same policy.
type qrClock struct {
	clock.Clock
	now func() time.Time
}

func (c qrClock) Now() time.Time { return c.now() }
func (c qrClock) Timer(d time.Duration) clock.Timer {
	return qrTimer{Timer: c.Clock.Timer(qrRenewAfter(d))}
}

type qrTimer struct{ clock.Timer }

func (t qrTimer) Reset(d time.Duration) { t.Timer.Reset(qrRenewAfter(d)) }

func qrRenewAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	margin := d / 5
	if margin > 5*time.Second {
		margin = 5 * time.Second
	}
	return d - margin
}
