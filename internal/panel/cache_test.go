package panel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotCacheCoalescesAndExpires(t *testing.T) {
	var c snapshotCache[int]
	var calls atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})
	load := func(context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return 7, nil
	}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			got, err := c.get(context.Background(), load)
			if err != nil || got != 7 {
				t.Errorf("get=%d %v", got, err)
			}
		})
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("load storm: %d", calls.Load())
	}
	c.mu.Lock()
	c.at = time.Now().Add(-3 * time.Second)
	c.mu.Unlock()
	if _, err := c.get(context.Background(), load); err != nil || calls.Load() != 2 {
		t.Fatalf("expiry: %d %v", calls.Load(), err)
	}
}

func TestSnapshotCacheInvalidationRejectsInFlight(t *testing.T) {
	var c snapshotCache[int]
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := c.get(context.Background(), func(context.Context) (int, error) { close(started); <-release; return 1, nil })
		result <- err
	}()
	<-started
	c.invalidate()
	close(release)
	if err := <-result; !errors.Is(err, errSnapshotInvalidated) {
		t.Fatalf("stale result: %v", err)
	}
	if _, _, fresh := c.peek(); fresh {
		t.Fatal("invalidated work was published")
	}
	got, err := c.get(context.Background(), func(context.Context) (int, error) { return 2, nil })
	if err != nil || got != 2 {
		t.Fatalf("refresh: %d %v", got, err)
	}
}

func TestSnapshotCacheCancellationAndFailureBackoff(t *testing.T) {
	var c snapshotCache[int]
	ctx, cancel := context.WithCancel(context.Background())
	_, err := c.get(ctx, func(context.Context) (int, error) { cancel(); return 9, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, _, fresh := c.peek(); fresh {
		t.Fatal("canceled result cached")
	}
	calls := 0
	load := func(context.Context) (int, error) { calls++; return 0, errors.New("offline") }
	for range 10 {
		if _, err := c.get(context.Background(), load); err == nil {
			t.Fatal("failure hidden")
		}
	}
	if calls != 1 {
		t.Fatalf("failure storm: %d", calls)
	}
}
