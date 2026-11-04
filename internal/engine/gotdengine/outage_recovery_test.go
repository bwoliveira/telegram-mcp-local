package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// outageServer replaces only the RPC boundary. The production Engine.Run,
// updates.Manager, middleware chain and on-disk SQLite/FTS Store remain real.
// It deliberately replays overlapping difference messages at PTS=3 to exercise
// idempotence independently of the manager discarding old live-update PTS.
type outageServer struct {
	mu       sync.Mutex
	t        *testing.T
	date     int
	online   bool
	sliced   bool
	messages []tg.MessageClass
	requests []int
	failures int
}

func (s *outageServer) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r := in.(type) {
	case *tg.UsersGetUsersRequest:
		out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
	case *tg.MessagesGetDialogsRequest:
		out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{}
	case *tg.UpdatesGetDifferenceRequest:
		s.requests = append(s.requests, r.Pts)
		if !s.online {
			s.failures++
			return &net.OpError{Op: "read", Net: "tcp", Err: errors.New("synthetic network outage")}
		}
		if len(s.messages) == 0 {
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1000}
		} else if s.sliced && r.Pts == 1 {
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceSlice{
				NewMessages: s.messages[:1], IntermediateState: tg.UpdatesState{Pts: 2, Date: 1001},
			}
		} else {
			messages := s.messages
			if s.sliced && r.Pts == 2 {
				messages = messages[1:]
			}
			if r.Pts == 3 {
				s.date++ // a checkpoint fence AFTER full replay/handler delivery
			}
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifference{
				NewMessages: messages, State: tg.UpdatesState{Pts: 3, Date: s.date},
			}
		}
	default:
		// Manager can log/ignore an RPC error, so fail the test as well.
		// No send/edit/delete/readHistory (or any unexpected RPC) is allowed.
		s.t.Errorf("forbidden offline RPC %T", in)
		return fmt.Errorf("forbidden offline RPC %T", in)
	}
	return nil
}

func (s *outageServer) snapshot() ([]int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.requests...), s.failures
}

func startOutageEngine(t *testing.T, s *store.Store, server *outageServer) (*Engine, <-chan engine.Event, func()) {
	t.Helper()
	e := newBarrierEngine(t, s)
	e.api = tg.NewClient(productionRPC(e, server))
	// Never call telegram.Client.Run: transport notifications are synthetic,
	// while the callback and its manager restart loop are production code.
	e.runClient = func(ctx context.Context, f func(context.Context) error) error {
		e.onConnectionState(telegram.ConnectionStateConnecting)
		e.onConnectionState(telegram.ConnectionStateReady)
		return f(ctx)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16, Kinds: []engine.EventKind{engine.EventMessage}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("engine shutdown: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("engine did not join manager on shutdown")
			}
		})
	}
	t.Cleanup(stop)
	return e, events, stop
}

func assertOutageData(t *testing.T, s *store.Store, pts, count int) {
	t.Helper()
	ctx := context.Background()
	st, found, err := s.LoadUpdatesState(ctx, barrierAccount)
	if err != nil || !found || st.Pts != pts {
		t.Fatalf("durable checkpoint=%+v found=%v err=%v; want PTS=%d", st, found, err, pts)
	}
	hits, err := s.SearchLocal(ctx, barrierAccount, "outageneedle")
	if err != nil || len(hits) != count {
		t.Fatalf("durable FTS messages=%d err=%v; want %d", len(hits), err, count)
	}
	seen := map[int]bool{}
	for _, m := range hits {
		if seen[m.Key.ID] || m.Key.Account != barrierAccount || m.Key.Peer != (engine.PeerRef{Type: engine.PeerUser, ID: 99}) {
			t.Fatalf("duplicate or wrong message identity: %+v", m.Key)
		}
		seen[m.Key.ID] = true
		stored, ok, err := s.Message(ctx, m.Key)
		if err != nil || !ok || stored.Text != fmt.Sprintf("outageneedle %d", m.Key.ID) {
			t.Fatalf("message not durably readable: %+v exists=%v err=%v", stored, ok, err)
		}
	}
	if count == 2 && (!seen[7] || !seen[8]) {
		t.Fatalf("lost recovered message identities: %v", seen)
	}
}

func TestOutageRecoveryRealManagerAndStore(t *testing.T) {
	for _, sliced := range []bool{false, true} {
		t.Run(fmt.Sprintf("slice=%v", sliced), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "store.db")
			s := barrierStore(t, path)
			if err := s.SaveUpdatesState(ctx, barrierAccount, engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
				t.Fatal(err)
			}
			server := &outageServer{t: t, date: 1002, online: true, sliced: sliced}
			e, events, stop := startOutageEngine(t, s, server)
			waitBarrier(t, func() bool { requests, _ := server.snapshot(); return len(requests) == 1 })

			e.onConnectionState(telegram.ConnectionStateDisconnected)
			server.mu.Lock()
			server.online = false
			server.mu.Unlock()
			// Force a difference while disconnected, not merely a status change.
			if err := e.manager.Handle(ctx, &tg.UpdatesTooLong{}); err != nil {
				t.Fatal(err)
			}
			waitBarrier(t, func() bool { _, failures := server.snapshot(); return failures == 1 })
			// The remote backlog is created AFTER the failed getDifference. No
			// live Handle or Store.Apply injects these messages into the engine.
			server.mu.Lock()
			for _, id := range []int{7, 8} {
				server.messages = append(server.messages, &tg.Message{ID: id, PeerID: &tg.PeerUser{UserID: 99}, Date: 994 + id, Message: fmt.Sprintf("outageneedle %d", id)})
			}
			server.mu.Unlock()
			assertOutageData(t, s, 1, 0)

			server.mu.Lock()
			server.online = true
			server.mu.Unlock()
			e.onConnectionState(telegram.ConnectionStateConnecting)
			e.onConnectionState(telegram.ConnectionStateReady)
			// No manual manager reset/restart: Engine.Run must consume the
			// transport callback's restart request and recover from durable PTS.
			waitBarrier(t, func() bool { st, _, _ := s.LoadUpdatesState(ctx, barrierAccount); return st.Pts == 3 })
			assertOutageData(t, s, 3, 2)
			eventIDs := map[int]bool{}
			for range 2 {
				select {
				case ev := <-events:
					if ev.Message == nil || !ev.CatchUp || ev.Source != engine.SourceDifference {
						t.Fatalf("not a difference recovery event: %+v", ev)
					}
					if ev.Message.Key.ID != 7 && ev.Message.Key.ID != 8 || eventIDs[ev.Message.Key.ID] {
						t.Fatalf("duplicate or unexpected recovery event: %+v", ev)
					}
					eventIDs[ev.Message.Key.ID] = true
				case <-time.After(time.Second):
					t.Fatal("missing recovered message event")
				}
			}
			requests, failures := server.snapshot()
			want := []int{1, 1, 1}
			if sliced {
				want = append(want, 2)
			}
			if fmt.Sprint(requests) != fmt.Sprint(want) || failures != 1 || e.Metrics().Messages != 2 || e.Metrics().Restarts != 1 {
				t.Fatalf("recovery requests=%v want=%v failures=%d metrics=%+v", requests, want, failures, e.Metrics())
			}

			// Replay the exact backlog through getDifference at the current PTS.
			if err := e.manager.Handle(ctx, &tg.UpdatesTooLong{}); err != nil {
				t.Fatal(err)
			}
			waitBarrier(t, func() bool { st, _, _ := s.LoadUpdatesState(ctx, barrierAccount); return st.Date == 1003 })
			stop() // the new durable Date fences completed manager replay
			assertOutageData(t, s, 3, 2)
			if e.Metrics().Messages != 2 {
				t.Fatalf("replay emitted duplicates: %+v", e.Metrics())
			}
			for ev := range events {
				t.Errorf("unexpected duplicate event: %+v", ev)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// A fresh engine and freshly opened SQLite connection restore the
			// stored checkpoint, then process the same overlapping backlog.
			s = barrierStore(t, path)
			assertOutageData(t, s, 3, 2)
			before, _ := server.snapshot()
			restarted, restartedEvents, stopRestarted := startOutageEngine(t, s, server)
			waitBarrier(t, func() bool { st, _, _ := s.LoadUpdatesState(ctx, barrierAccount); return st.Date == 1004 })
			stopRestarted()
			assertOutageData(t, s, 3, 2)
			after, _ := server.snapshot()
			if after[len(before)] != 3 || restarted.Metrics().Messages != 0 {
				t.Fatalf("restart checkpoint/replay: requests=%v metrics=%+v", after, restarted.Metrics())
			}
			for ev := range restartedEvents {
				t.Errorf("restart emitted duplicate event: %+v", ev)
			}
			t.Logf("failed RPCs=1, recovery/replay/restart PTS=%v, durable messages=2, duplicate events=0", after)
		})
	}
}
