package gotdengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func productionRPC(e *Engine, raw tg.Invoker) tg.Invoker {
	all := e.middlewares()
	for i := len(all) - 1; i >= 0; i-- {
		raw = all[i].Handle(raw)
	}
	return raw
}
func TestProductionRPCRateLimitStopsBeforeNextInvoker(t *testing.T) {
	e, _ := offlineEngine(t)
	calls := 0
	rpc := productionRPC(e, invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return nil }))
	if err := rpc.Invoke(context.Background(), &tg.UpdatesGetStateRequest{}, &tg.UpdatesState{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := rpc.Invoke(ctx, &tg.UpdatesGetStateRequest{}, &tg.UpdatesState{}); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("rate limit bypassed", calls, err)
	}
}
func TestProductionRPCFloodWaitExceedsMaximum(t *testing.T) {
	e, _ := offlineEngine(t)
	e.cfg.FloodWaitMax = 100 * time.Millisecond
	calls := 0
	rpc := productionRPC(e, invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return tgerr.New(420, "FLOOD_WAIT_3") }))
	err := rpcError(rpc.Invoke(context.Background(), &tg.UpdatesGetStateRequest{}, &tg.UpdatesState{}))
	var flood *engine.FloodWaitError
	if !errors.As(err, &flood) || calls != 1 || e.Metrics().FloodWaits != 1 || e.Status().FloodWaitUntil == nil {
		t.Fatal("unbounded/hidden flood wait", err, calls, e.Metrics(), e.Status())
	}
}
func TestProductionRPCFloodWaitIsCancelable(t *testing.T) {
	e, _ := offlineEngine(t)
	e.cfg.FloodWaitMax = 3 * time.Second
	calls := 0
	rpc := productionRPC(e, invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return tgerr.New(420, "FLOOD_WAIT_1") }))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := rpc.Invoke(ctx, &tg.UpdatesGetStateRequest{}, &tg.UpdatesState{}); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("flood wait ignored cancellation", err, calls)
	}
}
func TestProductionRPCFloodWaitRetryReusesSendRandomID(t *testing.T) {
	e, _ := offlineEngine(t)
	calls := 0
	var randomID int64
	rpc := productionRPC(e, invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		req, ok := in.(*tg.MessagesSendMessageRequest)
		if !ok {
			t.Fatal("unexpected RPC")
		}
		calls++
		if calls == 1 {
			randomID = req.RandomID
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		if req.RandomID != randomID {
			t.Fatal("retry can duplicate a message")
		}
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rpc.Invoke(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "synthetic", RandomID: 123}, &tg.UpdatesBox{}); err != nil || calls != 2 || e.Metrics().FloodWaits != 1 {
		t.Fatal(err, calls, e.Metrics())
	}
}
