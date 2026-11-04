package webhook

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

type globalOrderFixture struct {
	newer, older               *state.State
	newerManager, olderManager *Manager
	old                        state.Webhook
}

// newGlobalOrderFixture stores 32 enabled hooks in one account and a single,
// older enabled hook in another; together they exceed the process limit.
func newGlobalOrderFixture(t *testing.T, ctx context.Context) globalOrderFixture {
	t.Helper()
	limits := NewLimiter()
	open := func(name string) (*state.State, *Manager) {
		t.Helper()
		st, err := state.Open(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st, NewWithLimiter(st, nil, "test", nil, limits)
	}
	var f globalOrderFixture
	f.newer, f.newerManager = open("newer-account")
	f.older, f.olderManager = open("older-account")
	for i := 0; i < maxEnabledWebhooks; i++ {
		h := state.Webhook{ID: fmt.Sprintf("newer-%02d", i), URL: "http://93.184.216.34:8080/hook", Secret: "secret", Events: []string{"message"}, Enabled: true, CreatedAt: time.Unix(int64(100+i), 0).UTC()}
		if err := f.newer.SaveWebhook(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	f.old = state.Webhook{ID: "older-00", URL: "http://93.184.216.34:8080/hook", Secret: "secret", Events: []string{"message"}, Enabled: true, CreatedAt: time.Unix(1, 0).UTC()}
	if err := f.older.SaveWebhook(ctx, f.old); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f globalOrderFixture) assertOldestRetained(t *testing.T, ctx context.Context) {
	t.Helper()
	gotOld, err := f.older.Webhook(ctx, f.old.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotNewest, err := f.newer.Webhook(ctx, "newer-31")
	if err != nil {
		t.Fatal(err)
	}
	if !gotOld.Enabled || gotNewest.Enabled || gotNewest.DisabledReason == "" {
		t.Fatalf("retention does not follow created_at: older_enabled=%v older_reason=%q newest_enabled=%v newest_reason=%q",
			gotOld.Enabled, gotOld.DisabledReason, gotNewest.Enabled, gotNewest.DisabledReason)
	}
	for i := 0; i < maxEnabledWebhooks-1; i++ {
		h, err := f.newer.Webhook(ctx, fmt.Sprintf("newer-%02d", i))
		if err != nil || !h.Enabled {
			t.Fatalf("newer-%02d enabled=%v err=%v, want retained", i, h.Enabled, err)
		}
	}
	f.newerManager.mu.Lock()
	_, running := f.newerManager.workers["newer-31"]
	f.newerManager.mu.Unlock()
	if running || f.olderManager.limits.hasHook(f.newerManager, "newer-31") {
		t.Fatal("displaced webhook still has a worker or a reserved slot")
	}
	if !f.olderManager.limits.hasHook(f.olderManager, f.old.ID) {
		t.Fatal("older webhook has no reserved slot")
	}
}

// Review r2 P3: the account that starts first must not decide which legacy
// overflow hooks survive.
func TestStartupRetentionUsesGlobalCreationOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newerFirst bool
	}{{"newer_account_starts_first", true}, {"older_account_starts_first", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newGlobalOrderFixture(t, ctx)
			first, second := f.olderManager, f.newerManager
			if tc.newerFirst {
				first, second = second, first
			}
			if err := first.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if err := second.Start(ctx); err != nil {
				t.Fatal(err)
			}
			f.assertOldestRetained(t, ctx)
		})
	}
}

// Displacement is a startup reconciliation of legacy state only: runtime
// creation and re-enabling keep rejecting at the limit.
func TestRuntimeEnableDoesNotDisplaceRunningHooks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newGlobalOrderFixture(t, ctx)
	if err := f.newerManager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.older.DisableWebhook(ctx, f.old.ID, time.Now(), "manual"); err != nil {
		t.Fatal(err)
	}
	if err := f.olderManager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := f.olderManager.Update(ctx, f.old.ID, Settings{Enabled: &enabled}); err == nil {
		t.Fatal("re-enabling an older hook at the limit succeeded, want limit error")
	}
	if h, err := f.newer.Webhook(ctx, "newer-31"); err != nil || !h.Enabled {
		t.Fatalf("runtime enable displaced newer-31: enabled=%v err=%v", h.Enabled, err)
	}
}

// Concurrent startups must not deadlock while displacing each other's hooks.
func TestConcurrentStartupDisplacementDoesNotDeadlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newGlobalOrderFixture(t, ctx)
	errs := make(chan error, 2)
	for _, m := range []*Manager{f.newerManager, f.olderManager} {
		go func(m *Manager) { errs <- m.Start(ctx) }(m)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("concurrent Start deadlocked")
		}
	}
	f.assertOldestRetained(t, ctx)
}
