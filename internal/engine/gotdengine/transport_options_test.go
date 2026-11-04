package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"testing"
)

func TestInjectedTransportRequiresBothLifetimeAndInvoker(t *testing.T) {
	e, _ := offlineEngine(t)
	inv := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return nil })
	run := func(c context.Context, f func(context.Context) error) error { return f(c) }
	for _, opt := range []Options{{Invoker: inv}, {RunClient: run}} {
		if _, err := NewWithOptions(e.cfg, opt); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("incomplete transport allowed: %v", err)
		}
	}
}
