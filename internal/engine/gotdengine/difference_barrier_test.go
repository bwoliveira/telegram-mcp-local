package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

const barrierAccount engine.AccountID = "barrier-test"

var barrierPeer = engine.PeerRef{Type: engine.PeerChannel, ID: 42}

func barrierStore(t *testing.T, path string, opts ...store.Option) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), path, barrierAccount, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func seedBarrier(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	_, err := s.Apply(ctx, engine.Batch{Account: barrierAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: barrierPeer}, AccessHash: 123}}, Messages: []engine.Message{
		{Key: engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 7}, Date: time.Unix(1000, 0), Text: "oldeditmarker"},
		{Key: engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 8}, Date: time.Unix(1000, 0), Text: "deletedmarker"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveUpdatesState(ctx, barrierAccount, engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveChannelPts(ctx, barrierAccount, 42, 1); err != nil {
		t.Fatal(err)
	}
}
func newBarrierEngine(t *testing.T, s *store.Store) *Engine {
	t.Helper()
	e, err := New(engine.Config{Account: barrierAccount, APIID: 1, APIHash: "synthetic-test-only", Store: s, Session: &memorySession{}})
	if err != nil {
		t.Fatal(err)
	}
	e.account = engine.Account{ID: barrierAccount, SelfID: 99, Authenticated: true}
	return e
}
func barrierDiff() *tg.UpdatesChannelDifference {
	return &tg.UpdatesChannelDifference{Final: true, Pts: 3, NewMessages: []tg.MessageClass{
		&tg.Message{ID: 9, PeerID: &tg.PeerChannel{ChannelID: 42}, Date: 1001, Message: "newmessagemarker"},
	}, OtherUpdates: []tg.UpdateClass{
		&tg.UpdateEditChannelMessage{Pts: 2, PtsCount: 1, Message: &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 42}, Date: 1000, EditDate: 1001, Message: "neweditmarker"}},
		&tg.UpdateDeleteChannelMessages{ChannelID: 42, Messages: []int{8}, Pts: 3, PtsCount: 1},
	}}
}
func assertBarrierData(t *testing.T, s *store.Store, applied bool) {
	t.Helper()
	ctx := context.Background()
	m, ok, err := s.Message(ctx, engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 7})
	text := "oldeditmarker"
	if applied {
		text = "neweditmarker"
	}
	if err != nil || !ok || m.Text != text {
		t.Errorf("edit=%+v exists=%v err=%v want=%s", m, ok, err, text)
	}
	_, ok, err = s.Message(ctx, engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 8})
	if err != nil || ok == applied {
		t.Errorf("delete exists=%v applied=%v err=%v", ok, applied, err)
	}
	m, ok, err = s.Message(ctx, engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 9})
	if err != nil || ok != applied || (ok && m.Text != "newmessagemarker") {
		t.Errorf("new message=%+v exists=%v applied=%v err=%v", m, ok, applied, err)
	}
	for _, q := range []string{"oldeditmarker", "deletedmarker", "neweditmarker"} {
		hits, err := s.SearchLocal(ctx, barrierAccount, q)
		want := 0
		if (applied && q == "neweditmarker") || (!applied && q != "neweditmarker") {
			want = 1
		}
		if err != nil || len(hits) != want {
			t.Errorf("FTS %s: count=%d want=%d err=%v", q, len(hits), want, err)
		}
	}
}
func waitBarrier(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for !f() {
		select {
		case <-deadline:
			t.Fatal("manager did not reach expected barrier")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func runBarrierManager(t *testing.T, e *Engine, beforeResponse func(), delayedGlobal ...<-chan struct{}) func() {
	t.Helper()
	e.api = tg.NewClient(productionRPC(e, invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			// Hold the global worker in its startup RPC: it cannot drain the
			// channel's OtherUpdates queue while the channel saves its PTS.
			if len(delayedGlobal) > 0 {
				select {
				case <-delayedGlobal[0]:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1000}
		case *tg.UpdatesGetChannelDifferenceRequest:
			if r.Pts != 1 {
				t.Errorf("restart skipped durable PTS: %d", r.Pts)
			}
			out.(*tg.UpdatesChannelDifferenceBox).ChannelDifference = barrierDiff()
			if beforeResponse != nil {
				beforeResponse()
			}
		default:
			t.Errorf("forbidden offline RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})))
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	done := make(chan error, 1)
	go func() { done <- e.manager.Run(ctx, e.api, 99, updates.AuthOptions{}) }()
	return func() { cancel(); <-done; e.manager.Reset() }
}

// The manager is real and its storage is production SQLite/FTS, not pocstore.
// No dependence on dispatch ordering: channelDifference can save PTS even when
// its queued OtherUpdates are discarded as old by the channel worker.
func TestChannelDifferenceBarrierRealManagerAndStore(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	seedBarrier(t, s)
	e := newBarrierEngine(t, s)
	delayed := make(chan struct{})
	stop := runBarrierManager(t, e, nil, delayed)
	defer stop()
	waitBarrier(t, func() bool { v, _, _ := s.LoadChannelPts(context.Background(), barrierAccount, 42); return v == 3 })
	assertBarrierData(t, s, true)
	if e.Metrics().Edits != 1 || e.Metrics().Deletes != 1 {
		t.Errorf("duplicate/lost events: %+v", e.Metrics())
	}
}

func TestChannelDifferencePersistenceFailureKeepsBothCheckpoints(t *testing.T) {
	var armed atomic.Bool
	var failed atomic.Bool
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"), store.FaultAt(func(stage string) error {
		if stage == "apply_before_commit" && armed.Load() {
			failed.Store(true)
			return errors.New("synthetic disk-full")
		}
		return nil
	}))
	seedBarrier(t, s)
	e := newBarrierEngine(t, s)
	stop := runBarrierManager(t, e, func() { armed.Store(true) })
	waitBarrier(t, func() bool { return failed.Load() && e.held.Load() })
	stop()
	assertBarrierData(t, s, false)
	st, _, err := s.LoadUpdatesState(context.Background(), barrierAccount)
	pts, _, ce := s.LoadChannelPts(context.Background(), barrierAccount, 42)
	if err != nil || ce != nil || st.Pts != 1 || pts != 1 {
		t.Fatalf("checkpoint ahead of failed batch: %+v channel=%d err=%v/%v", st, pts, err, ce)
	}
	sa := stateAdapter{p: s, acc: barrierAccount, e: e}
	if err = sa.SetState(context.Background(), 99, updates.State{Pts: 99}); !errors.Is(err, engine.ErrCheckpointHeld) {
		t.Fatal(err)
	}
	if err = sa.SetChannelPts(context.Background(), 99, 42, 99); !errors.Is(err, engine.ErrCheckpointHeld) {
		t.Fatal(err)
	}
	armed.Store(false)
	// Recreate the entire engine (a process restart), not just clear the latch.
	e = newBarrierEngine(t, s)
	stop = runBarrierManager(t, e, nil)
	defer stop()
	waitBarrier(t, func() bool { v, _, _ := s.LoadChannelPts(context.Background(), barrierAccount, 42); return v == 3 })
	assertBarrierData(t, s, true)
}

func TestChannelDifferenceCrashRestart(t *testing.T) {
	if path := os.Getenv("TELEGRAM_BARRIER_CRASH_DB"); path != "" {
		stage := os.Getenv("TELEGRAM_BARRIER_CRASH_STAGE")
		var armed atomic.Bool
		s := barrierStore(t, path, store.FaultAt(func(at string) error {
			if armed.Load() && at == stage {
				os.Exit(137)
			}
			return nil
		}))
		e := newBarrierEngine(t, s)
		defer runBarrierManager(t, e, func() { armed.Store(true) })()
		time.Sleep(10 * time.Second)
		t.Fatal("crash hook not reached")
	}
	for _, stage := range []string{"apply_before_commit", "apply_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			s := barrierStore(t, path)
			seedBarrier(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestChannelDifferenceCrashRestart$", "-test.count=1")
			child.Env = append(os.Environ(), "TELEGRAM_BARRIER_CRASH_DB="+path, "TELEGRAM_BARRIER_CRASH_STAGE="+stage)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 137 {
				t.Fatalf("child did not crash: %v %s", err, output)
			}
			s = barrierStore(t, path)
			pts, _, err := s.LoadChannelPts(context.Background(), barrierAccount, 42)
			if err != nil || pts != 1 {
				t.Fatalf("crash checkpoint=%d err=%v", pts, err)
			}
			assertBarrierData(t, s, stage == "apply_after_commit")
			e := newBarrierEngine(t, s)
			stop := runBarrierManager(t, e, nil)
			defer stop()
			waitBarrier(t, func() bool { v, _, _ := s.LoadChannelPts(context.Background(), barrierAccount, 42); return v == 3 })
			assertBarrierData(t, s, true)
			// Replay an old page cannot resurrect deletion or regress the edited FTS.
			seed := engine.Batch{Account: barrierAccount, Source: engine.SourceBackfill, Messages: []engine.Message{{Key: engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 8}, Date: time.Unix(1000, 0), Text: "deletedmarker"}}}
			if _, err = s.Apply(context.Background(), seed); err != nil {
				t.Fatal(err)
			}
			assertBarrierData(t, s, true)
			t.Log(fmt.Sprintf("exit=137 at %s; restart from pts=1, final pts=3, deleted FTS absent", stage))
		})
	}
}
