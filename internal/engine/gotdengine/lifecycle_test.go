package gotdengine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestInternalReconnectIsCountedInMetricsAndControllerStatus(t *testing.T) {
	e, _ := offlineEngine(t)
	var controller engine.Controller = e
	e.onConnectionState(telegram.ConnectionStateConnecting)
	e.onConnectionState(telegram.ConnectionStateReady)
	if e.Metrics().Restarts != 0 || controller.Status().Restarts != 0 {
		t.Fatal("initial connection counted as reconnect")
	}
	e.transition(engine.StateConnected)
	for i := 1; i <= 2; i++ {
		e.onConnectionState(telegram.ConnectionStateDisconnected)
		e.onConnectionState(telegram.ConnectionStateDisconnected)
		if e.Status().State != engine.StateReconnecting {
			t.Fatal(e.Status())
		}
		e.onConnectionState(telegram.ConnectionStateConnecting)
		e.onConnectionState(telegram.ConnectionStateReady)
		e.onConnectionState(telegram.ConnectionStateReady) // duplicate callbacks are not reconnects
		status := controller.Status()
		if e.Metrics().Restarts != int64(i) || status.Restarts != i || status.State != engine.StateConnected {
			t.Fatalf("reconnect not exposed: %+v %+v", e.Metrics(), e.Status())
		}
	}
	if err := e.Disconnect(context.Background(), "manual pause"); err != nil {
		t.Fatal(err)
	}
	e.onConnectionState(telegram.ConnectionStateDisconnected)
	e.onConnectionState(telegram.ConnectionStateConnecting)
	e.onConnectionState(telegram.ConnectionStateReady)
	if e.Status().State != engine.StatePaused || e.Metrics().Restarts != 2 {
		t.Fatal("intentional pause counted", e.Status())
	}
}

type lifecycleSession struct {
	mu        sync.Mutex
	data      []byte
	deleted   int
	deleteErr error
}

func (s *lifecycleSession) LoadSession(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data) == 0 {
		return nil, engine.ErrNoSession
	}
	return append([]byte(nil), s.data...), nil
}
func (s *lifecycleSession) StoreSession(_ context.Context, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append([]byte(nil), b...)
	return nil
}
func (s *lifecycleSession) DeleteSession(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted++
	s.data = nil
	return s.deleteErr
}

type authObserver struct {
	engine.Persister
	mu     sync.Mutex
	state  engine.AuthState
	reason string
}

func (p *authObserver) SetAuthState(ctx context.Context, a engine.AccountID, s engine.AuthState, r string) error {
	p.mu.Lock()
	p.state, p.reason = s, r
	p.mu.Unlock()
	return p.Persister.SetAuthState(ctx, a, s, r)
}

func TestRevokedRPCStopsRunDeletesSessionAndKeepsHistory(t *testing.T) {
	e, s := offlineEngine(t)
	session := &lifecycleSession{data: []byte("synthetic session")}
	e.cfg.Session = session
	store := &authObserver{Persister: s}
	e.cfg.Store = store
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	key := engine.MessageKey{Account: "primary", Peer: peer, ID: 7}
	if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Text: "keep local history", Date: time.Unix(1000, 0)}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan struct{})
	e.runClient = func(c context.Context, _ func(context.Context) error) error {
		close(entered)
		<-c.Done()
		return c.Err()
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	<-entered
	inv := e.observeRPC(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(401, "AUTH_KEY_UNREGISTERED") }))
	if err := inv.Invoke(context.Background(), &tg.MessagesGetHistoryRequest{}, &tg.MessagesMessagesBox{}); !errors.Is(err, engine.ErrRevoked) {
		t.Fatalf("expected typed revocation, got %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, engine.ErrRevoked) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not stop on RPC revocation")
	}
	status := e.Status()
	if status.State != engine.StateRevoked || status.LastError != "AUTH_KEY_UNREGISTERED" {
		t.Fatal(status)
	}
	a, _ := e.Account(context.Background())
	if a.Authenticated {
		t.Fatal("account remains authorized")
	}
	session.mu.Lock()
	deleted, present := session.deleted, len(session.data)
	session.mu.Unlock()
	if deleted < 1 || present != 0 {
		t.Fatal("session survived revocation")
	}
	store.mu.Lock()
	state, reason := store.state, store.reason
	store.mu.Unlock()
	if state != engine.AuthRevoked || reason != "AUTH_KEY_UNREGISTERED" {
		t.Fatal(state, reason)
	}
	if _, ok, err := s.Message(context.Background(), key); err != nil || !ok {
		t.Fatal("revocation erased history", err)
	}
	var calls atomic.Int32
	guarded := e.observeRPC(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls.Add(1); return nil }))
	if err := guarded.Invoke(context.Background(), &tg.MessagesGetHistoryRequest{}, &tg.MessagesMessagesBox{}); !errors.Is(err, engine.ErrRevoked) || calls.Load() != 0 {
		t.Fatal("revoked session made another RPC", err)
	}
	if err := e.Connect(context.Background()); !errors.Is(err, engine.ErrRevoked) {
		t.Fatal("revoked Run can reconnect", err)
	}
}

func TestUnauthenticatedNewKeyCanRequestLogin(t *testing.T) {
	e, _ := offlineEngine(t)
	e.account.Authenticated = false
	inv := e.observeRPC(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(401, "AUTH_KEY_UNREGISTERED") }))
	err := inv.Invoke(context.Background(), &tg.UsersGetUsersRequest{}, &tg.UserClassVector{})
	if errors.Is(err, engine.ErrRevoked) || e.Status().State == engine.StateRevoked {
		t.Fatal("fresh unauthenticated key mistaken for revoked session", err)
	}
}

func TestFollowRecoversMessageSentDuringTransportDrop(t *testing.T) {
	e, s := offlineEngine(t)
	if err := s.SaveUpdatesState(context.Background(), "primary", engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	replay := make(chan struct{}, 1)
	var differences atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		case *tg.MessagesGetDialogsRequest:
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{}
		case *tg.UpdatesGetDifferenceRequest:
			n := differences.Add(1)
			if n == 1 {
				if r.Pts != 1 {
					t.Errorf("initial pts %d", r.Pts)
				}
				e.onConnectionState(telegram.ConnectionStateDisconnected)
				e.onConnectionState(telegram.ConnectionStateConnecting)
				e.onConnectionState(telegram.ConnectionStateReady)
				return errors.New("synthetic transport drop")
			}
			if r.Pts == 1 {
				out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifference{NewMessages: []tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 1001, Message: "sent while disconnected"}}, State: tg.UpdatesState{Pts: 2, Date: 1001}}
				replay <- struct{}{}
			} else {
				out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001}
			}
		default:
			t.Errorf("forbidden RPC %T", in)
			return errors.New("forbidden RPC")
		}
		return nil
	}))
	e.runClient = func(c context.Context, f func(context.Context) error) error {
		e.onConnectionState(telegram.ConnectionStateConnecting)
		e.onConnectionState(telegram.ConnectionStateReady)
		return f(c)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-replay:
	case <-ctx.Done():
		t.Fatal("message during drop was not replayed", ctx.Err())
	}
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 8}
	for {
		_, ok, err := s.Message(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		st, _, stateErr := s.LoadUpdatesState(ctx, "primary")
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		if ok && st.Pts == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	if e.Metrics().Messages != 1 || e.Metrics().Restarts < 1 || e.Status().Restarts < 1 {
		t.Fatalf("no idempotent replay/reconnect metric: %+v %+v", e.Metrics(), e.Status())
	}
	state, ok, err := s.LoadUpdatesState(context.Background(), "primary")
	if err != nil || !ok || state.Pts != 2 {
		t.Fatal(state, ok, err)
	}
}
