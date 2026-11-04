package gotdengine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestPausedUpdateCannotAdvanceCheckpoint(t *testing.T) {
	e, s := offlineEngine(t)
	if err := e.Disconnect(context.Background(), "explicit pause"); err != nil {
		t.Fatal(err)
	}
	raw := &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: &tg.Message{ID: 901, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "must replay after resume"}, Pts: 1, PtsCount: 1}}
	_ = e.handle(context.Background(), raw)
	if err := (stateAdapter{p: s, acc: "primary", e: e}).SetState(context.Background(), 99, updates.State{Pts: 1}); !errors.Is(err, engine.ErrCheckpointHeld) {
		t.Fatal("paused dispatcher silently discarded data but advanced checkpoint", err)
	}
	if _, ok, err := s.LoadUpdatesState(context.Background(), "primary"); err != nil || ok {
		t.Fatal("durable PTS passed discarded update", ok, err)
	}
}

type delayedDeleteSession struct {
	lifecycleSession
	entered, release chan struct{}
}

func (s *delayedDeleteSession) DeleteSession(ctx context.Context) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.lifecycleSession.DeleteSession(ctx)
}
func TestLogoutRejectsLateSessionSaveAndRequiresFreshController(t *testing.T) {
	e, _ := offlineEngine(t)
	s := &delayedDeleteSession{entered: make(chan struct{}), release: make(chan struct{})}
	s.data = []byte("synthetic old credential")
	e.cfg.Session = s
	e.hadSession.Store(true)
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return errors.New("synthetic offline logout") }))
	done := make(chan error, 1)
	go func() { done <- e.LogOut(context.Background()) }()
	<-s.entered
	saved := make(chan error, 1)
	go func() {
		saved <- (engineSession{e: e, store: s}).StoreSession(context.Background(), []byte("late credential"))
	}()
	close(s.release)
	if err := <-done; err == nil {
		t.Fatal("remote logout failure not reported")
	}
	if err := <-saved; !errors.Is(err, engine.ErrNotAuthorized) {
		t.Fatal("late save accepted", err)
	}
	if _, err := s.LoadSession(context.Background()); !errors.Is(err, engine.ErrNoSession) {
		t.Fatal("session resurrected", err)
	}
	if e.hadSession.Load() || e.isRevocation(tgerr.New(401, "AUTH_KEY_UNREGISTERED")) {
		t.Fatal("old session marker misclassifies new login key")
	}
	if err := e.Connect(context.Background()); !errors.Is(err, engine.ErrNotAuthorized) {
		t.Fatal("reused client with cached auth key", err)
	}
	if e.Status().State != engine.StateLoggedOut {
		t.Fatal(e.Status())
	}
	if err := e.require(); !errors.Is(err, engine.ErrNotAuthorized) {
		t.Fatal("logged-out network method should be unauthorized", err)
	}
}

type bindPublishBarrier struct {
	engine.Persister
	entered, release chan struct{}
	once             sync.Once
	mu               sync.Mutex
	state            engine.AuthState
}

func (s *bindPublishBarrier) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	a, err := s.Persister.Apply(ctx, b)
	s.once.Do(func() { close(s.entered); <-s.release })
	return a, err
}
func (s *bindPublishBarrier) SetAuthState(ctx context.Context, acc engine.AccountID, state engine.AuthState, reason string) error {
	err := s.Persister.SetAuthState(ctx, acc, state, reason)
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	return err
}
func TestRevocationWinsAgainstCompletingLogin(t *testing.T) {
	e, s := offlineEngine(t)
	p := &bindPublishBarrier{Persister: s, entered: make(chan struct{}), release: make(chan struct{})}
	e.cfg.Store = p
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.UsersGetUsersRequest); !ok {
			return errors.New("unexpected RPC")
		}
		out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 99, Self: true}}
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.mu.Lock()
	e.account.Authenticated = false
	e.loginGeneration = 1
	e.loginCtx, e.loginCancel = ctx, cancel
	e.login.State = engine.LoginStarting
	e.mu.Unlock()
	login := make(chan error, 1)
	go func() { login <- e.completeLogin(ctx, 1) }()
	<-p.entered
	revoked := make(chan error, 1)
	go func() { revoked <- e.revoke(context.Background(), tgerr.New(401, "SESSION_REVOKED")) }()
	close(p.release)
	<-login
	if err := <-revoked; !errors.Is(err, engine.ErrRevoked) {
		t.Fatal(err)
	}
	a, _ := e.Account(context.Background())
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()
	if a.Authenticated || state != engine.AuthRevoked || e.Status().State != engine.StateRevoked {
		t.Fatal("revocation lost to final bind publication", a, state, e.Status())
	}
	e.transition(engine.StateConnected)
	if e.Status().State != engine.StateRevoked {
		t.Fatal("late transition overwrote terminal revocation")
	}
}

func TestDisconnectFencesTransportPublicationAfterStalePauseCheck(t *testing.T) {
	e, _ := offlineEngine(t)
	// Simulate Run having observed paused=false before Disconnect wins.
	e.mu.Lock()
	observedPaused := e.paused
	e.mu.Unlock()
	if observedPaused {
		t.Fatal("bad initial fixture")
	}
	previous := make(chan struct{})
	close(previous)
	e.transportDone = previous
	if err := e.Disconnect(context.Background(), "between generations"); err != nil {
		t.Fatal(err)
	}
	_, _, done, err := e.beginTransport(context.Background())
	if !errors.Is(err, engine.ErrPaused) || done != nil {
		t.Fatal("stale decision published an unjoined transport", err)
	}
	e.resetGeneration()
	if !e.held.Load() {
		t.Fatal("stale generation cleared pause fence")
	}
	e.transition(engine.StateConnecting)
	if e.Status().State != engine.StatePaused {
		t.Fatal("stale reconnect overwrote paused status")
	}
}

func TestDisconnectJoinsTransportBeforeReturning(t *testing.T) {
	e, _ := offlineEngine(t)
	started, release := make(chan struct{}), make(chan struct{})
	e.runClient = func(ctx context.Context, _ func(context.Context) error) error {
		close(started)
		<-ctx.Done()
		<-release
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := make(chan error, 1)
	go func() { run <- e.Run(ctx) }()
	<-started
	done := make(chan error, 1)
	go func() { done <- e.Disconnect(context.Background(), "pause") }()
	select {
	case err := <-done:
		t.Fatal("returned before transport joined", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel()
	<-run
}
