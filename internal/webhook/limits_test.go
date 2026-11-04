package webhook

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

func TestFailedStartupReleasesProcessReservations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := state.Open(filepath.Join(t.TempDir(), "failed-start"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i := 0; i < 2; i++ {
		h := state.Webhook{
			ID: fmt.Sprintf("hook-%d", i), URL: "https://receiver.example/hook", Secret: "secret",
			Events: []string{"message"}, Enabled: true, CreatedAt: time.Unix(int64(i+1), 0).UTC(),
		}
		if err := st.SaveWebhook(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	limits := NewLimiter()
	manager := NewWithLimiter(st, nil, "test", nil, limits)
	var calls atomic.Int32
	manager.secure.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		if calls.Add(1) == 2 {
			cancel()
			return nil, context.Canceled
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	if err := manager.Start(ctx); err == nil {
		t.Fatal("startup unexpectedly succeeded after cancellation")
	}

	other := &Manager{}
	for i := 0; i < maxEnabledWebhooks; i++ {
		if !limits.reserveHook(other, fmt.Sprintf("replacement-%d", i), time.Now()) {
			t.Fatalf("failed startup leaked a reservation; replacement %d was rejected", i+1)
		}
	}
}

func TestProcessLimiterSharesAggregateQueueBudget(t *testing.T) {
	limits := newLimiter(maxEnabledWebhooks, 10, maxConcurrentWebhookDials)
	first := &Manager{limits: limits}
	second := &Manager{limits: limits}
	firstWorker := &worker{}
	secondWorker := &worker{}

	if !first.replaceDeliveryBytesLocked(firstWorker, nil, 6) {
		t.Fatal("first manager could not reserve queue bytes")
	}
	first.queuedBytes, firstWorker.queuedBytes = 6, 6
	if second.replaceDeliveryBytesLocked(secondWorker, nil, 5) {
		t.Fatal("second manager exceeded the aggregate queue budget")
	}
	firstDelivery := &delivery{body: make([]byte, 6), accounted: true}
	first.releaseDeliveryLocked(firstWorker, firstDelivery)
	if !second.replaceDeliveryBytesLocked(secondWorker, nil, 5) {
		t.Fatal("released aggregate queue bytes were not reusable")
	}
}

func TestProcessLimiterDisablesLegacyOverflowInStableOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	limits := NewLimiter()
	states := make([]*state.State, 2)
	managers := make([]*Manager, 2)
	for i := range states {
		st, err := state.Open(filepath.Join(t.TempDir(), fmt.Sprintf("legacy-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		states[i] = st
		t.Cleanup(func() { _ = st.Close() })
		managers[i] = NewWithLimiter(st, nil, "test", nil, limits)
	}
	for i, count := range []int{20, 13} {
		for n := 0; n < count; n++ {
			h := state.Webhook{
				ID: fmt.Sprintf("account-%d-%02d", i, n), URL: "http://93.184.216.34:8080/hook",
				Secret: "secret", Events: []string{"message"}, Enabled: true,
				CreatedAt: time.Unix(int64(n+1), 0).UTC(),
			}
			if err := states[i].SaveWebhook(ctx, h); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, manager := range managers {
		if err := manager.Start(ctx); err != nil {
			t.Fatalf("start manager %d: %v", i, err)
		}
	}

	// 33 hooks for 32 slots: the globally newest (created_at, then ID) is
	// account-0-19 at t=20, even though account 0 started first.
	overflow, err := states[0].Webhook(ctx, "account-0-19")
	if err != nil {
		t.Fatal(err)
	}
	if overflow.Enabled || overflow.DisabledAt == nil || !strings.Contains(overflow.DisabledReason, "limite de") {
		t.Fatalf("legacy overflow was not visibly disabled: %+v", overflow)
	}
	for _, kept := range []struct {
		account int
		id      string
	}{{1, "account-1-12"}, {0, "account-0-18"}} {
		h, err := states[kept.account].Webhook(ctx, kept.id)
		if err != nil {
			t.Fatal(err)
		}
		if !h.Enabled {
			t.Fatalf("older legacy hook was not kept deterministically: %+v", h)
		}
	}
}

func TestProcessLimiterRejectsThirtyThirdEnabledHookAcrossManagers(t *testing.T) {
	ctx := context.Background()
	limits := NewLimiter()
	managers := make([]*Manager, 2)
	for i := range managers {
		st, err := state.Open(filepath.Join(t.TempDir(), fmt.Sprintf("state-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		managers[i] = NewWithLimiter(st, nil, "test", nil, limits)
	}

	for i := 0; i < maxEnabledWebhooks; i++ {
		raw := fmt.Sprintf("http://93.184.216.34:%d", 2000+i)
		manager := managers[i%len(managers)]
		if _, _, err := manager.Create(ctx, Settings{URL: &raw}); err != nil {
			t.Fatalf("create hook %d: %v", i+1, err)
		}
	}

	raw := "http://93.184.216.34:9999"
	if _, _, err := managers[1].Create(ctx, Settings{URL: &raw}); err == nil {
		t.Fatal("created the 33rd enabled webhook across two managers")
	}
	if managers[0].secure.sem != managers[1].secure.sem {
		t.Fatal("managers do not share the process dial semaphore")
	}
	if got := cap(managers[0].secure.sem); got != maxConcurrentWebhookDials {
		t.Fatalf("shared dial capacity = %d, want %d", got, maxConcurrentWebhookDials)
	}
}
