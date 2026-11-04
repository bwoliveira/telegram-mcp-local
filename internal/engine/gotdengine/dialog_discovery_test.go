package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestDialogRefreshErrorSignalsAndCancellation(t *testing.T) {
	for _, rpcErr := range []error{tgerr.New(420, "FLOOD_WAIT_3600"), tgerr.New(500, "INTERNAL")} {
		t.Run(rpcErr.Error(), func(t *testing.T) {
			testDialogRefreshErrorSignalsAndCancellation(t, rpcErr)
		})
	}
}

func testDialogRefreshErrorSignalsAndCancellation(t *testing.T, rpcErr error) {
	// The real refresh/RPC-error path, without Telegram, a store, or long sleeps.
	now := time.Now()
	e := &Engine{
		cfg:                engine.Config{Clock: func() time.Time { return now }},
		account:            engine.Account{Authenticated: true},
		dialogRefreshEvery: time.Hour,
		dialogRefresh:      make(chan struct{}),
	}
	var calls atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetDialogsRequest); !ok {
			t.Errorf("unexpected RPC %T", in)
		}
		calls.Add(1)
		return rpcErr
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stop := e.startDialogRefresh(ctx)
	defer stop()
	// Unbuffered handoffs ensure each previous signal was handled before the
	// next one is accepted. The clock stays well before the flood deadline.
	for i := 0; i < 5; i++ {
		select {
		case e.dialogRefresh <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("worker stopped accepting signals during flood wait")
		}
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("worker did not cancel and join during one-hour flood wait")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("channel signals bypassed error backoff: calls=%d, want 1", got)
	}
}

func TestDialogRefreshBackoffDeadlineAndRecovery(t *testing.T) {
	start := time.Unix(1000, 0)
	until := start.Add(time.Hour)
	for _, tc := range []struct {
		name string
		err  error
		want time.Time
	}{
		{"flood", &engine.FloodWaitError{Until: until}, until},
		{"wrapped flood", fmt.Errorf("refresh: %w", &engine.FloodWaitError{Until: until}), until},
		{"transient", errors.New("offline"), start.Add(time.Minute)},
		{"expired flood", &engine.FloodWaitError{Until: start.Add(-time.Second)}, start.Add(time.Minute)},
		{"short flood", &engine.FloodWaitError{Until: start.Add(time.Second)}, start.Add(time.Minute)},
		{"zero flood", &engine.FloodWaitError{}, start.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := start
			clock := func() time.Time { return now }
			var backoff dialogRefreshBackoff
			calls := 0
			refresh := func(context.Context) error {
				calls++
				if calls == 1 {
					return tc.err
				}
				return nil
			}
			attempt := func() { backoff.refresh(context.Background(), time.Minute, clock, refresh) }
			attempt()
			if !backoff.retryAt.Equal(tc.want) {
				t.Fatalf("retry deadline = %v, want %v", backoff.retryAt, tc.want)
			}
			// Minute ticks and interleaved channel signals share this gate.
			for now = start.Add(time.Minute); now.Before(tc.want); now = now.Add(time.Minute) {
				attempt()
				attempt()
			}
			now = tc.want.Add(-time.Nanosecond)
			attempt()
			if calls != 1 {
				t.Fatalf("retried before deadline: calls=%d", calls)
			}
			now = tc.want
			attempt()
			if calls != 2 || !backoff.retryAt.IsZero() {
				t.Fatalf("did not recover at deadline: calls=%d retryAt=%v", calls, backoff.retryAt)
			}
			attempt()
			if calls != 3 {
				t.Fatal("successful recovery retained error backoff")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			backoff.refresh(ctx, time.Minute, clock, refresh)
			if calls != 3 {
				t.Fatal("canceled worker attempted discovery")
			}
		})
	}
}

func TestDialogRefreshBackoffRepeatedFloodWait(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	until := now.Add(time.Hour)
	var backoff dialogRefreshBackoff
	calls := 0
	refresh := func(context.Context) error {
		calls++
		if calls == 2 {
			until = now.Add(2 * time.Hour)
		}
		return &engine.FloodWaitError{Until: until}
	}
	backoff.refresh(context.Background(), time.Minute, clock, refresh)
	now = until
	backoff.refresh(context.Background(), time.Minute, clock, refresh)
	if calls != 2 || !backoff.retryAt.Equal(until) {
		t.Fatalf("new flood deadline was lost: calls=%d retryAt=%v want=%v", calls, backoff.retryAt, until)
	}
	now = until.Add(-time.Nanosecond)
	backoff.refresh(context.Background(), time.Minute, clock, refresh)
	if calls != 2 {
		t.Fatal("retried before extended flood deadline")
	}
}

func TestDialogRefreshBackoffStartsAfterFailureCompletes(t *testing.T) {
	now := time.Unix(1000, 0)
	var backoff dialogRefreshBackoff
	backoff.refresh(context.Background(), time.Minute, func() time.Time { return now }, func(context.Context) error {
		now = now.Add(30 * time.Second)
		return errors.New("slow failure")
	})
	if want := now.Add(time.Minute); !backoff.retryAt.Equal(want) {
		t.Fatalf("backoff measured from request start: got=%v want=%v", backoff.retryAt, want)
	}
}

func TestDialogRefreshCancelsInFlight(t *testing.T) {
	e := &Engine{
		cfg:                engine.Config{Clock: time.Now},
		account:            engine.Account{Authenticated: true},
		dialogRefreshEvery: time.Hour,
		dialogRefresh:      make(chan struct{}, 1),
	}
	entered := make(chan struct{})
	e.api = tg.NewClient(invokeFunc(func(ctx context.Context, _ bin.Encoder, _ bin.Decoder) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stop := e.startDialogRefresh(ctx)
	defer stop()
	e.requestDialogDiscovery([]tg.UpdateClass{&tg.UpdateChannel{ChannelID: 81}})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("channel signal did not start refresh")
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("worker did not cancel and join its in-flight RPC")
	}
}
