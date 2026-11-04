package gotdengine

import (
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/telegram"
	"golang.org/x/time/rate"
)

func (e *Engine) middlewares() []telegram.Middleware {
	return []telegram.Middleware{
		floodwait.NewSimpleWaiter().WithMaxWait(e.cfg.FloodWaitMax).WithMaxRetries(5),
		ratelimit.New(rate.Every(500*time.Millisecond), 1),
		telegram.MiddlewareFunc(e.observeRPC),
		telegram.MiddlewareFunc(e.durableDifference),
	}
}
