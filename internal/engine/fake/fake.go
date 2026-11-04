package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

var _ engine.Controller = (*Controller)(nil)
var _ engine.Factory = New

// Write is one successfully executed synthetic write, never a preview or read.
// Request is an owned copy and contains no login credential or session data.
type Write struct {
	Sequence uint64
	At       time.Time
	Action   string
	Peer     engine.PeerRef
	Request  any
	Result   engine.WriteResult
}

type subscriber struct {
	ch    chan engine.Event
	kinds map[engine.EventKind]bool
	done  chan struct{}
}
type approval struct {
	at         time.Time
	generation uint64
	used       bool
}

// Controller implements engine.Controller. Its mutex serializes commits, writes
// and event publication. Every returned snapshot is detached from its state.
// New starts authenticated as the synthetic account, unlike the real engine.
type Controller struct {
	mu             sync.Mutex
	cfg            engine.Config
	clock          func() time.Time
	status         engine.Status
	account        engine.Account
	login          engine.Login
	peers          map[engine.PeerRef]engine.Peer
	dialogs        map[engine.PeerRef]engine.Dialog
	messages       map[engine.MessageKey]engine.Message
	groups         map[engine.PeerRef]engine.Group
	tombstones     map[engine.MessageKey]bool
	pendingDeletes map[int]bool
	cursors        map[engine.PeerRef]engine.Cursor
	approvals      map[string]approval
	nextApproval   uint64
	idempotent     map[string]cachedWrite
	writes         []Write
	seq            uint64
	nextID         int
	changes        chan struct{}
	subs           map[uint64]subscriber
	nextSub        uint64
	dropped        uint64
	running        bool
}
type cachedWrite struct {
	fingerprint   string
	requestDigest string
	result        engine.WriteResult
}

// New is directly assignable to engine.Factory. Empty account means primary;
// empty mode means read_only; nil clock means a fixed Epoch. Store and Session
// may be nil: the fake keeps an isolated in-memory mirror. With Store supplied,
// synthetic fixtures are committed there before the constructor returns.
func New(cfg engine.Config) (engine.Controller, error) {
	if cfg.Account == "" {
		cfg.Account = "primary"
	}
	if cfg.Mode == "" {
		cfg.Mode = engine.ModeReadOnly
	}
	if cfg.Mode != engine.ModeReadOnly && cfg.Mode != engine.ModeReadWrite {
		return nil, engine.ErrInvalid
	}
	clock := cfg.Clock
	if clock == nil {
		clock = Epoch
	}
	f := &Controller{cfg: cfg, clock: clock, status: engine.Status{State: engine.StateConnected, Since: clock(), Mode: cfg.Mode}, peers: make(map[engine.PeerRef]engine.Peer), dialogs: make(map[engine.PeerRef]engine.Dialog), messages: make(map[engine.MessageKey]engine.Message), groups: make(map[engine.PeerRef]engine.Group), tombstones: make(map[engine.MessageKey]bool), pendingDeletes: make(map[int]bool), cursors: make(map[engine.PeerRef]engine.Cursor), approvals: make(map[string]approval), idempotent: make(map[string]cachedWrite), changes: make(chan struct{}, 1), subs: make(map[uint64]subscriber), nextID: 100}
	data := Fixtures(cfg.Account)
	f.account = data.Account
	f.login = engine.Login{State: engine.LoginDone, UpdatedAt: clock()}
	if cfg.Store != nil {
		if err := cfg.Store.BindSelf(context.Background(), cfg.Account, data.Account); err != nil {
			return nil, err
		}
		if err := cfg.Store.SetAuthState(context.Background(), cfg.Account, engine.AuthAuthorized, ""); err != nil {
			return nil, err
		}
	}
	if _, err := f.applyLocked(context.Background(), data.Batch()); err != nil {
		return nil, err
	}
	if cfg.Store != nil {
		// Applied is a delta, not a snapshot: repeated construction against an
		// already seeded store must recover its current copies, not seed versions.
		for _, m := range data.Messages {
			stored, ok, err := cfg.Store.Message(context.Background(), m.Key)
			if err != nil {
				return nil, err
			}
			if ok {
				f.messages[m.Key] = copyOf(stored)
			} else {
				delete(f.messages, m.Key)
				f.tombstones[m.Key] = true
			}
		}
	}
	for _, g := range data.Groups {
		f.groups[g.Peer.Ref] = copyOf(g)
	}
	f.status.History = engine.HistoryProgress{Messages: int64(len(data.Messages)), Conversations: len(data.Dialogs), LastAt: clock()}
	return f, nil
}

func (f *Controller) Status() engine.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyOf(f.status)
}
func (f *Controller) Mode() engine.Mode        { f.mu.Lock(); defer f.mu.Unlock(); return f.status.Mode }
func (f *Controller) Login() engine.Login      { f.mu.Lock(); defer f.mu.Unlock(); return copyOf(f.login) }
func (f *Controller) Changes() <-chan struct{} { return f.changes }
func (f *Controller) Writes() []Write          { f.mu.Lock(); defer f.mu.Unlock(); return copyOf(f.writes) }
func (f *Controller) ResetWrites()             { f.mu.Lock(); defer f.mu.Unlock(); f.writes = nil }
func (f *Controller) DroppedEvents() uint64    { f.mu.Lock(); defer f.mu.Unlock(); return f.dropped }

// Snapshot returns currently accessible fixture data, including successful writes.
func (f *Controller) Snapshot() Dataset {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := Dataset{Account: f.account}
	for _, p := range f.peers {
		d.Peers = append(d.Peers, p)
	}
	sort.Slice(d.Peers, func(i, j int) bool { return peerText(d.Peers[i].Ref) < peerText(d.Peers[j].Ref) })
	for _, p := range d.Peers {
		if dialog, ok := f.dialogs[p.Ref]; ok {
			d.Dialogs = append(d.Dialogs, dialog)
		}
		if g, ok := f.groups[p.Ref]; ok {
			d.Groups = append(d.Groups, g)
		}
	}
	for _, m := range f.messages {
		if !expired(m, f.clock()) {
			d.Messages = append(d.Messages, m)
		}
	}
	sortMessages(d.Messages)
	return copyOf(d)
}
func (f *Controller) notifyLocked() {
	select {
	case f.changes <- struct{}{}:
	default:
	}
}
func (f *Controller) transitionLocked(state engine.ConnState, reason string) {
	old := f.status.State
	if old == state {
		return
	}
	f.status.State = state
	f.status.Since = f.clock()
	f.status.PausedFor = ""
	if state == engine.StatePaused {
		f.status.PausedFor = reason
	}
	f.status.Recent = append(f.status.Recent, engine.LifecycleEvent{At: f.clock(), Event: "connection", Data: map[string]any{"state": string(state)}})
	if len(f.status.Recent) > 20 {
		f.status.Recent = f.status.Recent[len(f.status.Recent)-20:]
	}
	f.notifyLocked()
	f.emitLocked(engine.Event{Kind: engine.EventConnection, Connection: &engine.ConnectionChange{From: old, To: state, Reason: reason}}, string(old)+"|"+string(state))
}
func (f *Controller) networkLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch f.status.State {
	case engine.StatePaused:
		return engine.ErrPaused
	case engine.StateRevoked:
		return engine.ErrRevoked
	case engine.StateStopped:
		return engine.ErrPaused
	}
	if !f.account.Authenticated {
		return engine.ErrNotAuthorized
	}
	return nil
}
func (f *Controller) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return fmt.Errorf("%w: fake engine is already running", engine.ErrInvalid)
	}
	f.running = true
	f.notifyLocked()
	if f.account.Authenticated && f.status.State != engine.StatePaused {
		f.transitionLocked(engine.StateConnected, "")
	}
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	f.running = false
	f.transitionLocked(engine.StateStopped, "")
	for id, s := range f.subs {
		delete(f.subs, id)
		close(s.ch)
		close(s.done)
	}
	f.mu.Unlock()
	return ctx.Err()
}
func (f *Controller) SetMode(ctx context.Context, m engine.Mode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m != engine.ModeReadOnly && m != engine.ModeReadWrite {
		return engine.ErrInvalid
	}
	if f.status.Mode != m {
		f.status.Mode = m
		f.status.Recent = append(f.status.Recent, engine.LifecycleEvent{At: f.clock(), Event: "mode", Data: map[string]any{"mode": string(m)}})
		f.notifyLocked()
	}
	return nil
}
func (f *Controller) Disconnect(ctx context.Context, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !f.account.Authenticated {
		return engine.ErrNotAuthorized
	}
	f.transitionLocked(engine.StatePaused, reason)
	return nil
}
func (f *Controller) Connect(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.status.State == engine.StateRevoked {
		return engine.ErrRevoked
	}
	if !f.account.Authenticated {
		return engine.ErrNotAuthorized
	}
	f.transitionLocked(engine.StateConnected, "")
	return nil
}
func (f *Controller) LogOut(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.cfg.Session != nil {
		if err := f.cfg.Session.DeleteSession(ctx); err != nil {
			return err
		}
	}
	if f.cfg.Store != nil {
		if err := f.cfg.Store.SetAuthState(ctx, f.cfg.Account, engine.AuthLoggedOut, ""); err != nil {
			return err
		}
	}
	f.account.Authenticated = false
	f.approvals = make(map[string]approval)
	f.login = engine.Login{State: engine.LoginIdle, UpdatedAt: f.clock()}
	f.transitionLocked(engine.StateLoggedOut, "")
	f.emitLocked(engine.Event{Kind: engine.EventAuth, Auth: &engine.AuthChange{State: engine.AuthLoggedOut}}, "logged_out")
	return nil
}
func (f *Controller) StartLogin(ctx context.Context, req engine.LoginRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.login.State == engine.LoginWantCode || f.login.State == engine.LoginWantPass || f.login.State == engine.LoginShowQR {
		return engine.ErrLoginRunning
	}
	if req.Method != engine.LoginPhone && req.Method != engine.LoginQR {
		return engine.ErrInvalid
	}
	if req.Method == engine.LoginPhone && (len(req.Phone) < 3 || !strings.HasPrefix(req.Phone, "+")) {
		return engine.ErrInvalid
	}
	f.login = engine.Login{State: engine.LoginWantCode, Method: req.Method, Phone: req.Phone, CodeType: "app", CodeLength: 5, StartedAt: f.clock(), UpdatedAt: f.clock()}
	if req.Method == engine.LoginQR {
		until := f.clock().Add(time.Minute)
		f.login.State = engine.LoginShowQR
		f.login.QRVersion = 1
		f.login.QRURL = "tg://login?token=c3ludGhldGljLW5vdC1hLXJlYWwtY3JlZGVudGlhbA"
		f.login.QRExpiresAt = &until
	}
	f.notifyLocked()
	return nil
}

// SubmitCode accepts any nonempty synthetic code and moves to a synthetic 2FA
// step. SubmitPassword then completes login. For QR tests use CompleteLogin.
func (f *Controller) SubmitCode(ctx context.Context, code engine.Secret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.login.State != engine.LoginWantCode {
		return engine.ErrNoLogin
	}
	if code.Empty() {
		return engine.ErrInvalid
	}
	f.login.State = engine.LoginWantPass
	f.login.PasswordHint = "Synthetic password (any nonempty value)"
	f.login.UpdatedAt = f.clock()
	f.notifyLocked()
	return nil
}
func (f *Controller) SubmitPassword(ctx context.Context, password engine.Secret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.login.State != engine.LoginWantPass {
		return engine.ErrNoLogin
	}
	if password.Empty() {
		return engine.ErrInvalid
	}
	return f.completeLoginLocked(ctx)
}
func (f *Controller) CompleteLogin(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.login.State != engine.LoginShowQR && f.login.State != engine.LoginWantPass && f.login.State != engine.LoginWantCode {
		return engine.ErrNoLogin
	}
	return f.completeLoginLocked(ctx)
}
func (f *Controller) completeLoginLocked(ctx context.Context) error {
	a := f.account
	a.Authenticated = true
	if f.cfg.Store != nil {
		if err := f.cfg.Store.BindSelf(ctx, f.cfg.Account, a); err != nil {
			return err
		}
		if err := f.cfg.Store.SetAuthState(ctx, f.cfg.Account, engine.AuthAuthorized, ""); err != nil {
			return err
		}
	}
	if f.cfg.Session != nil {
		if err := f.cfg.Session.StoreSession(ctx, []byte("synthetic-fake-session-not-mtproto")); err != nil {
			return err
		}
	}
	f.account = a
	f.login = engine.Login{State: engine.LoginDone, Method: f.login.Method, StartedAt: f.login.StartedAt, UpdatedAt: f.clock(), Synced: int64(len(f.messages)), Conversations: len(f.dialogs)}
	f.transitionLocked(engine.StateConnected, "")
	f.notifyLocked()
	f.emitLocked(engine.Event{Kind: engine.EventAuth, Auth: &engine.AuthChange{State: engine.AuthAuthorized}}, "authorized")
	return nil
}
func (f *Controller) ResendCode(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.login.State != engine.LoginWantCode {
		return engine.ErrNoLogin
	}
	f.login.UpdatedAt = f.clock()
	f.notifyLocked()
	return nil
}
func (f *Controller) CancelLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.login.State == engine.LoginWantCode || f.login.State == engine.LoginWantPass || f.login.State == engine.LoginShowQR {
		f.login = engine.Login{State: engine.LoginCancelled, UpdatedAt: f.clock()}
		f.notifyLocked()
	}
}

func (f *Controller) Subscribe(ctx context.Context, opts engine.SubscribeOptions) <-chan engine.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	size := opts.Buffer
	if size <= 0 {
		size = 64
	}
	ch := make(chan engine.Event, size)
	if ctx.Err() != nil || f.status.State == engine.StateStopped {
		close(ch)
		return ch
	}
	f.nextSub++
	id := f.nextSub
	s := subscriber{ch: ch, kinds: make(map[engine.EventKind]bool), done: make(chan struct{})}
	for _, k := range opts.Kinds {
		s.kinds[k] = true
	}
	f.subs[id] = s
	go func() {
		select {
		case <-ctx.Done():
		case <-s.done:
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.subs[id]; ok {
			delete(f.subs, id)
			close(ch)
			close(s.done)
		}
	}()
	return ch
}
func (f *Controller) emitLocked(e engine.Event, version string) {
	f.seq++
	e.Seq = f.seq
	e.Account = f.cfg.Account
	if e.ReceivedAt.IsZero() {
		e.ReceivedAt = f.clock()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = e.ReceivedAt
	}
	if e.Source == "" {
		e.Source = engine.SourceUpdate
	}
	peer := ""
	if e.Peer != nil {
		peer = peerText(*e.Peer)
	}
	id := 0
	if e.Message != nil {
		id = e.Message.Key.ID
	}
	if e.Reaction != nil {
		id = e.Reaction.Key.ID
	}
	if e.Delete != nil && len(e.Delete.IDs) > 0 {
		id = e.Delete.IDs[0]
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%s", e.Account, e.Kind, peer, id, version)))
	e.ID = hex.EncodeToString(sum[:])[:32]
	for _, s := range f.subs {
		if len(s.kinds) > 0 && !s.kinds[e.Kind] {
			continue
		}
		out := copyOf(e)
		select {
		case s.ch <- out:
		default:
			select {
			case <-s.ch:
				f.dropped++
			default:
			}
			s.ch <- out
		}
	}
}

// Publish simulates an incoming server batch, committing before it emits. It is
// not a Telegram write and therefore never enters Writes(). Store.Apply decides
// what events are emitted; duplicate Applied results produce no duplicate event.
func (f *Controller) Publish(ctx context.Context, b engine.Batch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := f.applyLocked(ctx, copyOf(b))
	return err
}
func (f *Controller) applyLocked(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	if err := ctx.Err(); err != nil {
		return engine.Applied{}, err
	}
	if b.Account == "" {
		b.Account = f.cfg.Account
	}
	if b.Account != f.cfg.Account {
		return engine.Applied{}, engine.ErrAccountMismatch
	}
	if b.Source == "" {
		b.Source = engine.SourceUpdate
	}
	if b.Received.IsZero() {
		b.Received = f.clock()
	}
	// A page/update may repeat one row with different versions. Normalize once
	// before Apply so persistence and the event mirror see the winning copy.
	chosen := make(map[engine.MessageKey]engine.Message)
	var order []engine.MessageKey
	newKeys := make(map[engine.MessageKey]bool)
	for _, m := range b.Messages {
		newKeys[m.Key] = true
	}
	for _, m := range append(copyOf(b.Messages), b.Edits...) {
		old, exists := chosen[m.Key]
		if !exists {
			order = append(order, m.Key)
		}
		if !exists || newer(m, old) {
			chosen[m.Key] = m
		}
	}
	b.Messages = nil
	b.Edits = nil
	for _, k := range order {
		if newKeys[k] {
			b.Messages = append(b.Messages, chosen[k])
		} else {
			b.Edits = append(b.Edits, chosen[k])
		}
	}
	for _, m := range append(copyOf(b.Messages), b.Edits...) {
		if m.Key.Account != f.cfg.Account {
			return engine.Applied{}, engine.ErrAccountMismatch
		}
		if m.Key.ID <= 0 {
			return engine.Applied{}, engine.ErrInvalid
		}
	}
	var a engine.Applied
	if f.cfg.Store != nil {
		var err error
		a, err = f.cfg.Store.Apply(ctx, b)
		if err != nil {
			return a, err
		}
	} else {
		for _, m := range b.Messages {
			if f.tombstones[m.Key] || (m.Key.Peer.Type != engine.PeerChannel && f.pendingDeletes[m.Key.ID]) || expired(m, f.clock()) {
				a.Skipped++
				continue
			}
			old, ok := f.messages[m.Key]
			if !ok {
				a.Inserted = append(a.Inserted, m.Key)
			} else if newer(m, old) {
				a.Edited = append(a.Edited, m.Key)
			}
		}
		for _, m := range b.Edits {
			if f.tombstones[m.Key] || (m.Key.Peer.Type != engine.PeerChannel && f.pendingDeletes[m.Key.ID]) || expired(m, f.clock()) {
				a.Skipped++
				continue
			}
			old, ok := f.messages[m.Key]
			if !ok {
				a.Inserted = append(a.Inserted, m.Key)
			} else if newer(m, old) {
				a.Edited = append(a.Edited, m.Key)
			}
		}
		for _, d := range b.Deletes {
			for _, id := range d.IDs {
				found := false
				for k := range f.messages {
					if k.ID == id && ((d.Peer != nil && k.Peer == *d.Peer) || (d.Peer == nil && k.Peer.Type != engine.PeerChannel)) {
						if !f.tombstones[k] {
							a.Deleted = append(a.Deleted, k)
						}
						found = true
					}
				}
				if !found && d.Peer != nil {
					k := engine.MessageKey{Account: f.cfg.Account, Peer: *d.Peer, ID: id}
					if !f.tombstones[k] {
						a.Deleted = append(a.Deleted, k)
					}
				} else if !found && !f.pendingDeletes[id] {
					a.UnresolvedDeletes = append(a.UnresolvedDeletes, id)
				}
			}
		}
		for _, r := range b.Reactions {
			if m, ok := f.messages[r.Key]; ok && !equalJSON(m.Reactions, r.Counts) {
				a.Reactions = append(a.Reactions, engine.ReactionEvent{Key: r.Key, Counts: r.Counts, Added: r.Recent})
			}
		}
		for _, r := range b.Reads {
			d := f.dialogs[r.Peer]
			max := d.ReadInboxMaxID
			if r.Outbox {
				max = d.ReadOutboxMaxID
			}
			if r.MaxID > max {
				a.Reads = append(a.Reads, r)
			}
		}
		for _, x := range b.Access {
			if f.peers[x.Peer].Access != x.State {
				a.Access = append(a.Access, x)
			}
		}
		a.Gaps = append(a.Gaps, b.Gaps...)
	}
	// Mirror only acknowledged changes; a failed Apply never mutates local data.
	for _, p := range b.Peers {
		f.peers[p.Peer.Ref] = copyOf(p.Peer)
	}
	for _, d := range b.Dialogs {
		f.dialogs[d.Peer] = copyOf(d)
	}
	for _, p := range b.Participants {
		g := f.groups[p.Group]
		g.Peer = f.peers[p.Group]
		g.Participants = copyOf(p.Participants)
		g.Size = p.Size
		g.Partial = p.Partial
		g.About = p.About
		g.SnapshotAt = p.At
		f.groups[p.Group] = g
	}
	updates := append(copyOf(b.Messages), b.Edits...)
	for _, k := range append(copyOf(a.Inserted), a.Edited...) {
		for _, m := range updates {
			if m.Key == k {
				if f.cfg.Store != nil {
					stored, ok, err := f.cfg.Store.Message(ctx, k)
					if err != nil {
						return a, err
					}
					if !ok {
						return a, engine.ErrMessageNotFound
					}
					m = stored
				}
				f.messages[k] = copyOf(m)
				if k.ID >= f.nextID {
					f.nextID = k.ID + 1
				}
			}
		}
	}
	for _, k := range a.Deleted {
		delete(f.messages, k)
		f.tombstones[k] = true
	}
	for _, id := range a.UnresolvedDeletes {
		f.pendingDeletes[id] = true
	}
	for _, m := range updates {
		if m.Key.Peer.Type != engine.PeerChannel && f.pendingDeletes[m.Key.ID] {
			f.tombstones[m.Key] = true
			delete(f.messages, m.Key)
		}
	}
	if b.Cursor != nil {
		f.cursors[b.Cursor.Peer] = copyOf(*b.Cursor)
	}
	for _, r := range a.Reactions {
		m := f.messages[r.Key]
		m.Reactions = copyOf(r.Counts)
		f.messages[r.Key] = m
	}
	for _, r := range a.Reads {
		d := f.dialogs[r.Peer]
		d.Peer = r.Peer
		if r.Outbox {
			d.ReadOutboxMaxID = r.MaxID
		} else {
			d.ReadInboxMaxID = r.MaxID
			d.UnreadCount = r.StillUnread
			d.UnreadMentions = 0
		}
		f.dialogs[r.Peer] = d
	}
	for _, x := range a.Access {
		p := f.peers[x.Peer]
		p.Access = x.State
		f.peers[x.Peer] = p
	}
	base := engine.Event{Source: b.Source, CatchUp: b.CatchUp, ReceivedAt: b.Received}
	for _, k := range a.Inserted {
		e := base
		m := copyOf(f.messages[k])
		e.Kind = engine.EventMessage
		e.Peer = &k.Peer
		e.Message = &m
		e.OccurredAt = m.Date
		f.emitLocked(e, "0")
	}
	for _, k := range a.Edited {
		e := base
		m := copyOf(f.messages[k])
		e.Kind = engine.EventEdit
		e.Peer = &k.Peer
		e.Message = &m
		v := "0"
		if m.EditDate != nil {
			e.OccurredAt = *m.EditDate
			v = m.EditDate.UTC().Format(time.RFC3339Nano)
		}
		f.emitLocked(e, v)
	}
	for _, k := range a.Deleted {
		e := base
		e.Kind = engine.EventDelete
		e.Peer = &k.Peer
		e.Delete = &engine.Deletion{Peer: &k.Peer, IDs: []int{k.ID}}
		f.emitLocked(e, "0")
	}
	for _, id := range a.UnresolvedDeletes {
		e := base
		e.Kind = engine.EventDelete
		e.Delete = &engine.Deletion{IDs: []int{id}}
		f.emitLocked(e, "0")
	}
	for _, r := range a.Reactions {
		e := base
		e.Kind = engine.EventReaction
		e.Peer = &r.Key.Peer
		e.Reaction = &r
		v, _ := json.Marshal(r.Counts)
		sum := sha256.Sum256(v)
		f.emitLocked(e, hex.EncodeToString(sum[:]))
	}
	for _, r := range a.Reads {
		if !r.Outbox {
			continue
		}
		e := base
		e.Kind = engine.EventRead
		e.Peer = &r.Peer
		e.Read = &r
		f.emitLocked(e, fmt.Sprint(r.MaxID))
	}
	for _, x := range a.Access {
		e := base
		e.Kind = engine.EventAccess
		e.Peer = &x.Peer
		e.Access = &x
		f.emitLocked(e, string(x.State))
	}
	for _, g := range a.Gaps {
		e := base
		e.Kind = engine.EventCoverage
		e.Peer = g.Peer
		e.Coverage = &g
		f.emitLocked(e, string(g.Reason))
	}
	f.notifyLocked()
	return a, nil
}
func peerText(p engine.PeerRef) string { return fmt.Sprintf("%s:%d", p.Type, p.ID) }
func expired(m engine.Message, t time.Time) bool {
	if m.ExpiresAt != nil && !m.ExpiresAt.After(t) {
		return true
	}
	return m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(t)
}
func newer(m, old engine.Message) bool {
	return m.EditDate != nil && (old.EditDate == nil || m.EditDate.After(*old.EditDate))
}
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func sortMessages(ms []engine.Message) {
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].Date.Equal(ms[j].Date) {
			if ms[i].Key.Peer != ms[j].Key.Peer {
				return peerText(ms[i].Key.Peer) < peerText(ms[j].Key.Peer)
			}
			return ms[i].Key.ID < ms[j].Key.ID
		}
		return ms[i].Date.Before(ms[j].Date)
	})
}
