package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

func strictManager(t *testing.T) (*Manager, *state.State) {
	t.Helper()
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := New(st, nil, "test", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return m, st
}

func TestPrivateDestinationsRequirePersistedPerWebhookOptIn(t *testing.T) {
	m, _ := strictManager(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)

	if _, _, err := m.Create(context.Background(), Settings{URL: ptr(receiver.URL)}); err == nil {
		t.Fatal("loopback webhook was accepted without allow_private_network")
	}
	allow := true
	h, _, err := m.Create(context.Background(), Settings{URL: ptr(receiver.URL), AllowPrivateNetwork: &allow})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(context.Background(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AllowPrivateNetwork {
		t.Fatal("private-network opt-in was not exposed in webhook status")
	}

	next := New(m.st, nil, "test", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = next.Get(context.Background(), h.ID)
	if err != nil || !got.Enabled || !got.AllowPrivateNetwork {
		t.Fatalf("persisted private-network opt-in after restart: %+v %v", got, err)
	}
}

func TestUpdatePersistenceFailureStopsExistingPrivateWorker(t *testing.T) {
	m, st := strictManager(t)
	allow := true
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	h, _, err := m.Create(context.Background(), Settings{URL: ptr(receiver.URL), AllowPrivateNetwork: &allow})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := m.Update(context.Background(), h.ID, Settings{Enabled: &disabled, AllowPrivateNetwork: &disabled}); err == nil {
		t.Fatal("update unexpectedly succeeded after persistence was closed")
	}
	m.mu.Lock()
	_, running := m.workers[h.ID]
	m.mu.Unlock()
	if running {
		t.Fatal("private webhook worker remained active after a persistence failure")
	}
}

func TestMetadataAndSpecialAddressesStayBlockedWithOptIn(t *testing.T) {
	m, _ := strictManager(t)
	allow := true
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.200/latest/meta-data",
		"http://[fd00:ec2::254]/latest/meta-data",
		"http://192.0.2.1/hook",
		"http://224.0.0.1/hook",
	} {
		if _, _, err := m.Create(context.Background(), Settings{URL: &raw, AllowPrivateNetwork: &allow}); err == nil {
			t.Errorf("special destination %s was accepted", raw)
		}
	}
}

func TestStartFailsClosedForLegacyPrivateAndExcessEnabledHooks(t *testing.T) {
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	legacy := state.Webhook{ID: "legacy-private", URL: "http://127.0.0.1:8080/hook", Secret: "secret", Events: []string{"message"}, Enabled: true, CreatedAt: time.Unix(1, 0)}
	if err := st.SaveWebhook(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxEnabledWebhooks+1; i++ {
		h := state.Webhook{
			ID:        fmt.Sprintf("public-%02d", i),
			URL:       "http://93.184.216.34:8080/hook",
			Secret:    "secret",
			Events:    []string{"message"},
			Enabled:   true,
			CreatedAt: time.Unix(int64(10+i), 0),
		}
		if err := st.SaveWebhook(ctx, h); err != nil {
			t.Fatal(err)
		}
	}

	m := New(st, nil, "test", nil)
	boot, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(boot); err != nil {
		t.Fatal(err)
	}
	hooks, err := st.Webhooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	for _, h := range hooks {
		if h.Enabled {
			enabled++
		}
		if h.ID == legacy.ID && h.Enabled {
			t.Fatal("legacy loopback hook stayed enabled without an opt-in")
		}
	}
	if enabled != maxEnabledWebhooks {
		t.Fatalf("enabled hooks after legacy reconciliation = %d, want %d", enabled, maxEnabledWebhooks)
	}
	m.mu.Lock()
	workers := len(m.workers)
	m.mu.Unlock()
	if workers != maxEnabledWebhooks {
		t.Fatalf("workers = %d, want %d", workers, maxEnabledWebhooks)
	}
}

func TestAggregateQueueByteBudgetDropsOversizeDelivery(t *testing.T) {
	m, _ := strictManager(t)
	m.maxQueuedBytes = 1
	allow := true
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	h, _, err := m.Create(context.Background(), Settings{URL: ptr(receiver.URL), AllowPrivateNetwork: &allow})
	if err != nil {
		t.Fatal(err)
	}
	m.Dispatch(context.Background(), message("A", "user:1", false))
	got, err := m.Get(context.Background(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dropped != 1 || got.Queued != 0 || got.QueuedBytes != 0 {
		t.Fatalf("oversize queue status: %+v", got)
	}
	m.mu.Lock()
	bytes := m.queuedBytes
	m.mu.Unlock()
	if bytes != 0 {
		t.Fatalf("aggregate queued bytes = %d, want 0", bytes)
	}
}

func TestInvalidationClearsReleasedPayloadReferences(t *testing.T) {
	d1 := &delivery{event: Event{Identity: &Identity{ID: "primary"}, Message: &Message{ChatID: "user:1", MessageID: 1}}, body: []byte("keep"), accounted: true}
	d2 := &delivery{event: Event{Identity: &Identity{ID: "primary"}, Message: &Message{ChatID: "user:1", MessageID: 2}}, body: []byte("drop-two"), accounted: true}
	d3 := &delivery{event: Event{Identity: &Identity{ID: "primary"}, Message: &Message{ChatID: "user:1", MessageID: 3}}, body: []byte("drop-three"), accounted: true}
	backing := []*delivery{d1, d2, d3}
	w := &worker{queue: backing, queuedBytes: int64(len(d1.body) + len(d2.body) + len(d3.body)), stop: func() {}}
	m := &Manager{queuedBytes: w.queuedBytes}
	m.invalidateLocked(w, Event{Identity: &Identity{ID: "primary"}, Chat: &Chat{ID: "user:1"}, Delete: &Deletion{MessageIDs: []int{2, 3}}})

	if len(w.queue) != 1 || w.queue[0] != d1 {
		t.Fatalf("kept queue = %#v, want only first delivery", w.queue)
	}
	if backing[1] != nil || backing[2] != nil {
		t.Fatal("released payload references remain in the queue backing array")
	}
	if m.queuedBytes != int64(len(d1.body)) || w.queuedBytes != int64(len(d1.body)) {
		t.Fatalf("queued bytes after invalidation: manager=%d worker=%d", m.queuedBytes, w.queuedBytes)
	}
}

func TestAggregateQueueByteBudgetIsSharedAcrossWorkersAndActiveDeliveries(t *testing.T) {
	m, _ := strictManager(t)
	m.maxQueuedBytes = 3_500_000
	release := make(chan struct{})
	defer close(release)
	allow := true
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	var ids []string
	for i := 0; i < 2; i++ {
		h, _, err := m.Create(context.Background(), Settings{URL: ptr(receiver.URL), AllowPrivateNetwork: &allow})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, h.ID)
	}
	m.Dispatch(context.Background(), message(strings.Repeat("A", 1<<20), "user:1", false))

	var queuedBytes int64
	queued, dropped := 0, 0
	for _, id := range ids {
		got, err := m.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		queuedBytes += got.QueuedBytes
		queued += got.Queued
		dropped += got.Dropped
	}
	if queued != 1 || dropped != 1 || queuedBytes <= 1<<20 || queuedBytes > m.maxQueuedBytes {
		t.Fatalf("aggregate status: queued=%d dropped=%d bytes=%d budget=%d", queued, dropped, queuedBytes, m.maxQueuedBytes)
	}
}

func TestLocalResolutionRunsOutsideManagerMutexWithDeadline(t *testing.T) {
	m, _ := strictManager(t)
	allow := true
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	h, _, err := m.Create(context.Background(), Settings{
		URL:                 ptr(receiver.URL),
		AllowPrivateNetwork: &allow,
		Chats:               ptr([]string{"chat:1"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	m.mu.Lock()
	m.resolveLocal = func(ctx context.Context, _ engine.ResolveRequest) ([]engine.Candidate, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Dispatch(context.Background(), message("A", "channel:2", false))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("local resolver was not called")
	}

	statusDone := make(chan error, 1)
	go func() {
		_, err := m.Get(context.Background(), h.ID)
		statusDone <- err
	}()
	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Get blocked behind local resolution under Manager.mu")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("local resolution had no short deadline")
	}
}

func TestEnabledHookLimitAppliesToCreate(t *testing.T) {
	m, _ := strictManager(t)
	for i := 0; i < maxEnabledWebhooks; i++ {
		raw := fmt.Sprintf("http://93.184.216.34:%d", 2000+i)
		if _, _, err := m.Create(context.Background(), Settings{URL: &raw}); err != nil {
			t.Fatal(err)
		}
	}
	raw := "http://93.184.216.34:9999"
	if _, _, err := m.Create(context.Background(), Settings{URL: &raw}); err == nil {
		t.Fatal("created more than the global enabled-webhook limit")
	} else {
		var user UserError
		if !errors.As(err, &user) {
			t.Fatalf("limit error type = %T, want UserError", err)
		}
	}
}
