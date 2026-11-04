// Package gotdengine implements the Telegram controller using gotd. All durable
// state is supplied through engine.Persister; network-free tests inject an Invoker.
package gotdengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type subscriber struct {
	ch    chan engine.Event
	kinds []engine.EventKind
}
type Metrics struct {
	Messages      int64 `json:"messages"`
	Edits         int64 `json:"edits"`
	Deletes       int64 `json:"deletes"`
	FloodWaits    int64 `json:"flood_waits"`
	Restarts      int64 `json:"restarts"`
	DroppedEvents int64 `json:"dropped_events"`
}

// Options permits a custom transport without replacing controller behavior.
// Invoker and RunClient must be provided together; neither authorizes an account.
// Authentication and durable binding still run through the normal lifecycle.
type Options struct {
	Resolver  dcs.Resolver
	Invoker   tg.Invoker
	RunClient func(context.Context, func(context.Context) error) error
}
type Engine struct {
	cfg      engine.Config
	client   *telegram.Client
	api      *tg.Client
	manager  *updates.Manager
	mu       sync.Mutex
	commitMu sync.Mutex
	// Difference proofs are protected by commitMu and consumed only after the
	// manager durably saves the matching final checkpoint, never a slice/reset.
	globalProof        *differenceProof
	channelProofs      map[int64]*differenceProof
	loginComplete      sync.Mutex
	status             engine.Status
	account            engine.Account
	login              engine.Login
	loginCancel        context.CancelFunc
	loginCtx           context.Context
	loginGeneration    uint64
	loginFinalizing    bool
	loginWorkers       int
	loginOperation     bool
	phoneHash          string
	active             context.Context
	cancel             context.CancelFunc
	paused             bool
	running            bool
	ready              chan struct{}
	authReady          chan struct{}
	restart            chan struct{}
	changes            chan struct{}
	loginSignal        chan struct{}
	held               atomic.Bool
	recovering         atomic.Bool
	seq                uint64
	subscribers        map[uint64]subscriber
	metrics            Metrics
	backfill           chan struct{}
	dialogRefreshEvery time.Duration
	dialogRefresh      chan struct{}
	previews           map[string]time.Time
	// runClient is the gotd transport lifetime, replaced only by offline tests.
	runClient       func(context.Context, func(context.Context) error) error
	qrMigrate       func(context.Context, int) error
	connectedBefore bool
	transportDown   bool
	hadSession      atomic.Bool
	freshAccount    atomic.Bool // first successful bind, before any update baseline exists
	revoked         atomic.Bool
	sessionBlocked  atomic.Bool
	transportDone   chan struct{}
	sessionMu       sync.Mutex
}

var _ engine.Controller = (*Engine)(nil)

func New(cfg engine.Config) (*Engine, error) { return NewWithOptions(cfg, Options{}) }
func NewWithOptions(cfg engine.Config, opt Options) (*Engine, error) {
	if (opt.Invoker == nil) != (opt.RunClient == nil) {
		return nil, engine.ErrInvalid
	}
	if cfg.APIID <= 0 || cfg.APIHash.Empty() || cfg.Store == nil || cfg.Session == nil || cfg.Account == "" {
		return nil, engine.ErrInvalid
	}
	if cfg.Mode == "" {
		cfg.Mode = engine.ModeReadOnly
	}
	if cfg.Mode != engine.ModeReadOnly && cfg.Mode != engine.ModeReadWrite {
		return nil, engine.ErrInvalid
	}
	if cfg.FloodWaitMax <= 0 {
		cfg.FloodWaitMax = 60 * time.Second
	}
	if cfg.MaxChannelDifference <= 0 {
		cfg.MaxChannelDifference = 1
	}
	if cfg.BackfillConcurrency <= 0 {
		cfg.BackfillConcurrency = 1
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Device.Model == "" {
		cfg.Device.Model = "Telegram MCP"
	}
	e := &Engine{cfg: cfg, ready: make(chan struct{}), authReady: make(chan struct{}, 1), restart: make(chan struct{}, 1), changes: make(chan struct{}, 1), loginSignal: make(chan struct{}, 1), subscribers: map[uint64]subscriber{}, backfill: make(chan struct{}, cfg.BackfillConcurrency), previews: map[string]time.Time{}}
	e.dialogRefresh = make(chan struct{}, 1)
	e.status = engine.Status{State: engine.StateStarting, Mode: cfg.Mode, Since: cfg.Clock()}
	sa := stateAdapter{p: cfg.Store, acc: cfg.Account, e: e}
	e.manager = updates.New(updates.Config{Handler: telegram.UpdateHandlerFunc(e.handle), Storage: sa, AccessHasher: sa, UserAccessHasher: sa, MaxChannelDifferenceConcurrency: cfg.MaxChannelDifference,
		OnChannelTooLong: func(id int64) {
			p := engine.PeerRef{Type: engine.PeerChannel, ID: id}
			e.gap(&p, engine.GapChannelTooLong)
		}, OnTooLong: func() { e.gap(nil, engine.GapDifferenceTooLong) }, OnLoadUserStateFailed: func() { e.gap(nil, engine.GapStateLost) }, OnLoadChannelStateFailed: func(id int64) { p := engine.PeerRef{Type: engine.PeerChannel, ID: id}; e.gap(&p, engine.GapStateLost) }, OnChannelInaccessible: func(id int64) { p := engine.PeerRef{Type: engine.PeerChannel, ID: id}; e.gap(&p, engine.GapAccessLost) }})
	e.client = telegram.NewClient(cfg.APIID, cfg.APIHash.Reveal(), telegram.Options{Logger: transportLogger(cfg.Logger), Resolver: opt.Resolver, SessionStorage: sessAdapter{engineSession{e: e, store: cfg.Session}}, OnConnectionState: e.onConnectionState, OnSelfError: e.onSelfError, Device: telegram.DeviceConfig{DeviceModel: cfg.Device.Model, SystemVersion: cfg.Device.System, AppVersion: cfg.Device.AppVersion, LangCode: cfg.Device.LangCode}, UpdateHandler: telegram.UpdateHandlerFunc(func(ctx context.Context, u tg.UpdatesClass) error { e.signalLogin(u); return e.manager.Handle(ctx, u) }), Middlewares: e.middlewares()})
	e.api = e.client.API()
	e.runClient = e.client.Run
	if opt.Invoker != nil {
		invoker := opt.Invoker
		middlewares := e.middlewares()
		for i := len(middlewares) - 1; i >= 0; i-- {
			invoker = middlewares[i].Handle(invoker)
		}
		e.api = tg.NewClient(invoker)
		e.runClient = opt.RunClient
	}
	e.qrMigrate = e.client.MigrateTo
	return e, nil
}
func itoa(v int64) string        { return strconv.FormatInt(v, 10) }
func (e *Engine) now() time.Time { return e.cfg.Clock() }
func (e *Engine) notify() {
	select {
	case e.changes <- struct{}{}:
	default:
	}
}
func (e *Engine) transition(s engine.ConnState) {
	e.mu.Lock()
	if e.paused && s != engine.StatePaused && s != engine.StateLoggedOut && s != engine.StateRevoked {
		e.mu.Unlock()
		return
	}
	if e.revoked.Load() && s != engine.StateRevoked {
		e.mu.Unlock()
		return
	}
	if e.sessionBlocked.Load() && !e.revoked.Load() && s != engine.StateLoggedOut {
		e.mu.Unlock()
		return
	}
	e.status.State = s
	e.status.Since = e.now()
	e.mu.Unlock()
	e.notify()
}
func (e *Engine) Changes() <-chan struct{} { return e.changes }
func (e *Engine) Status() engine.Status {
	e.mu.Lock()
	s := e.status
	e.mu.Unlock()
	// SQLite reads must not hold the engine lock: updates and lifecycle
	// transitions need it independently of local status consumers.
	s = e.withStoreStatus(s)
	s.Updates.Held = s.Updates.Held || e.held.Load() || e.recovering.Load()
	return s
}
func (e *Engine) Mode() engine.Mode { e.mu.Lock(); defer e.mu.Unlock(); return e.status.Mode }
func (e *Engine) SetMode(ctx context.Context, m engine.Mode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m != engine.ModeReadOnly && m != engine.ModeReadWrite {
		return engine.ErrInvalid
	}
	e.mu.Lock()
	e.status.Mode = m
	e.mu.Unlock()
	e.notify()
	return nil
}
func (e *Engine) Metrics() Metrics { e.mu.Lock(); defer e.mu.Unlock(); return e.metrics }
func (e *Engine) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.ready:
		return nil
	}
}
func (e *Engine) require() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked.Load() || e.status.State == engine.StateRevoked {
		return engine.ErrRevoked
	}
	if !e.account.Authenticated {
		return engine.ErrNotAuthorized
	}
	if e.paused {
		return engine.ErrPaused
	}
	return nil
}
func (e *Engine) Account(ctx context.Context) (engine.Account, error) {
	if err := ctx.Err(); err != nil {
		return engine.Account{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.account, nil
}
func (e *Engine) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	a, _ := e.Account(ctx)
	b, err := e.cfg.Session.LoadSession(ctx)
	if err != nil && !errors.Is(err, engine.ErrNoSession) {
		return engine.Diagnostics{}, err
	}
	return engine.Diagnostics{Library: "gotd/td v0.163.0", SessionPresent: len(b) > 0, Authorized: a.Authenticated, Connected: e.LightStatus().State == engine.StateConnected, CheckedAt: e.now()}, nil
}
func (e *Engine) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return engine.ErrInvalid
	}
	e.running = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.running = false; e.mu.Unlock() }()
	backoff := time.Second
	for ctx.Err() == nil {
		e.mu.Lock()
		paused := e.paused
		e.mu.Unlock()
		if paused {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-e.changes:
				continue
			case <-time.After(200 * time.Millisecond):
				continue
			}
		}
		runctx, cancel, lifetimeDone, startErr := e.beginTransport(ctx)
		if errors.Is(startErr, engine.ErrPaused) {
			continue
		}
		if startErr != nil {
			return startErr
		}
		e.resetGeneration() // the preceding client/manager lifetime is joined
		e.transition(engine.StateConnecting)
		err := e.runClient(runctx, func(c context.Context) error {
			e.mu.Lock()
			e.active = c
			e.mu.Unlock()
			st, err := e.loginAuth().Status(c)
			if err != nil {
				return err
			}
			if st.Authorized {
				if err := e.bind(c, st.User); err != nil {
					return err
				}
			} else {
				e.transition(engine.StateNotAuthorized)
			}
			select {
			case <-e.ready:
			default:
				close(e.ready)
			}
			if !st.Authorized {
				select {
				case <-c.Done():
					return c.Err()
				case <-e.authReady:
				}
			}
			a, _ := e.Account(c)
			if !a.Authenticated {
				return engine.ErrNotAuthorized
			}
			e.transition(engine.StateConnected)
			// Defer auxiliary migration probes to the cancellable refresh worker,
			// whose backoff respects FloodWait.Until. They must not prevent the
			// updates manager/difference recovery from starting.
			initial := context.WithValue(c, migrationDiscoveryDeferredKey{}, true)
			if _, err := e.RefreshDialogs(initial, engine.RefreshRequest{Limit: 100}); err != nil {
				return err
			}
			defer e.startDialogRefresh(c)()
			for c.Err() == nil {
				mctx, stop := context.WithCancel(c)
				done := make(chan error, 1)
				go func() { done <- e.manager.Run(mctx, e.api, a.SelfID, updates.AuthOptions{Forget: false}) }()
				select {
				case <-c.Done():
					stop()
					<-done
					e.manager.Reset()
					return c.Err()
				case err := <-done:
					stop()
					e.manager.Reset()
					return err
				case <-e.restart:
					stop()
					<-done
					e.manager.Reset()
					e.resetGeneration()
					e.transition(engine.StateResyncing)
					select {
					case <-c.Done():
						return c.Err()
					case <-time.After(time.Second):
					}
				}
			}
			return c.Err()
		})
		cancel()
		close(lifetimeDone)
		e.mu.Lock()
		e.active = nil
		paused = e.paused
		e.mu.Unlock()
		if ctx.Err() != nil {
			e.transition(engine.StateStopped)
			return ctx.Err()
		}
		if paused {
			e.transition(engine.StatePaused)
			continue
		}
		if e.revoked.Load() {
			return engine.ErrRevoked
		}
		if e.isRevocation(err) {
			return e.revoke(ctx, err)
		}
		if errors.Is(err, engine.ErrAccountMismatch) {
			return err
		}
		e.resetGeneration()
		e.onConnectionState(telegram.ConnectionStateDisconnected)
		e.transition(engine.StateReconnecting)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
	return ctx.Err()
}
func (e *Engine) bind(ctx context.Context, u *tg.User) error {
	e.loginComplete.Lock()
	defer e.loginComplete.Unlock()
	return e.bindLocked(ctx, u)
}
func (e *Engine) bindLocked(ctx context.Context, u *tg.User) error {
	if e.revoked.Load() {
		return engine.ErrRevoked
	}
	if e.sessionBlocked.Load() {
		return engine.ErrNotAuthorized
	}
	if u == nil {
		return engine.ErrInvalid
	}
	oldSelf, err := e.cfg.Store.SelfID(ctx, e.cfg.Account)
	if err != nil {
		return &loginSetupError{stage: "load_self", cause: err}
	}
	a := engine.Account{ID: e.cfg.Account, Authenticated: true, SelfID: u.ID, Username: u.Username, FirstName: u.FirstName, LastName: u.LastName, Premium: u.Premium}
	if err := e.cfg.Store.BindSelf(ctx, e.cfg.Account, a); err != nil {
		return &loginSetupError{stage: "bind_self", cause: err}
	}
	if err := e.cfg.Store.SetAuthState(ctx, e.cfg.Account, engine.AuthAuthorized, ""); err != nil {
		return &loginSetupError{stage: "set_auth_state", cause: err}
	}
	if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: e.cfg.Account, Peers: peerUpserts([]tg.UserClass{u}, nil)}); err != nil {
		return &loginSetupError{stage: "apply_self_peer", cause: err}
	}
	e.mu.Lock()
	e.account = a
	e.mu.Unlock()
	if oldSelf == 0 {
		e.freshAccount.Store(true)
	}
	return nil
}
func (e *Engine) Disconnect(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.CancelLogin()
	// Fence the manager generation before require() starts rejecting updates.
	// gotd may ignore handler errors and save PTS anyway; adapter writes must
	// see a latched hold until the old generation has been joined and reset.
	e.commitMu.Lock()
	e.held.Store(true)
	e.mu.Lock()
	e.paused = true
	cancel, done := e.cancel, e.transportDone
	e.status.PausedFor = reason
	e.mu.Unlock()
	e.commitMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.transition(engine.StatePaused)
	return nil
}
func (e *Engine) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.revoked.Load() {
		return engine.ErrRevoked
	}
	if e.sessionBlocked.Load() {
		return engine.ErrNotAuthorized
	}
	e.mu.Lock()
	e.status.PausedFor = ""
	e.paused = false
	e.mu.Unlock()
	e.notify()
	return nil
}
func (e *Engine) LogOut(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, remote := e.api.AuthLogOut(ctx)
	// Block credentials before cancelling the transport; a late onSession may
	// run while Client.Run is stopping. Only a new controller can log in again.
	e.sessionBlocked.Store(true)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	stopped := e.Disconnect(cleanup, "logout")
	e.loginComplete.Lock()
	defer e.loginComplete.Unlock()
	e.sessionMu.Lock()
	del := e.cfg.Session.DeleteSession(cleanup)
	e.hadSession.Store(false)
	e.sessionMu.Unlock()
	saved := e.cfg.Store.SetAuthState(cleanup, e.cfg.Account, engine.AuthLoggedOut, "")
	e.mu.Lock()
	e.account.Authenticated = false
	e.previews = make(map[string]time.Time)
	e.mu.Unlock()
	e.transition(engine.StateLoggedOut)
	return errors.Join(rpcError(remote), stopped, del, saved)
}
func (e *Engine) observeRPC(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		e.mu.Lock()
		paused, active := e.paused, e.active
		pending := !e.account.Authenticated && e.loginCtx != nil && e.loginCtx.Err() == nil
		e.mu.Unlock()
		// DC migration probes Self before importing the accepted token. gotd
		// has already saved/reloaded an MTProto key, which is not authorization.
		// Snapshot request intent: even a late pre-import response is harmless.
		preImportSelf := false
		if req, ok := in.(*tg.UsersGetUsersRequest); ok && pending && len(req.ID) == 1 {
			_, preImportSelf = req.ID[0].(*tg.InputUserSelf)
		}
		if e.revoked.Load() {
			return engine.ErrRevoked
		}
		if paused {
			return engine.ErrPaused
		}
		// A caller's request cannot outlive the controller's transport lifetime.
		if active != nil {
			c, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(active, cancel)
			defer func() { stop(); cancel() }()
			if active.Err() != nil {
				cancel()
			}
			ctx = c
		}
		err := next.Invoke(ctx, in, out)
		if preImportSelf && tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
			return err
		}
		if e.isRevocation(err) {
			return e.revoke(ctx, err)
		}
		if d, ok := tgerr.AsFloodWait(err); ok {
			until := e.now().Add(d)
			e.mu.Lock()
			e.metrics.FloodWaits++
			e.status.FloodWaitUntil = &until
			e.mu.Unlock()
			e.notify()
		}
		return err
	}
}
func rpcError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, engine.ErrRevoked) || errors.Is(err, engine.ErrPaused) || errors.Is(err, engine.ErrNotAuthorized) {
		return err
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return &engine.FloodWaitError{Until: time.Now().Add(d), Method: "rpc"}
	}
	var r *tgerr.Error
	if errors.As(err, &r) {
		return &engine.RPCError{Code: r.Code, Type: r.Type, Method: "rpc"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("gotd: operation failed (details suppressed)")
}
func fingerprint(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }

// resetGeneration is only called after the previous manager has stopped and
// joined (or client.Run has returned). The old in-memory PTS cannot survive it.
func (e *Engine) resetGeneration() {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	e.mu.Lock()
	if e.paused || e.sessionBlocked.Load() {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	e.globalProof = nil
	e.channelProofs = nil
	e.held.Store(false)
	e.recovering.Store(true)
	select {
	case <-e.restart:
	default:
	}
}
func (e *Engine) holdCheckpoint() {
	e.held.Store(true)
	select {
	case e.restart <- struct{}{}:
	default:
	}
}
