// Package webhook delivers persisted Telegram changes through signed HTTP POSTs.
// Queues and retries are process-local: pending deliveries are lost on restart.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/account"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

// Retry schedule: ten attempts, each with a short timeout, the waits between
// them growing. Ten failures in a row fit in under a minute; then the webhook
// is turned off.
var (
	retryWaits     = []time.Duration{500 * time.Millisecond, time.Second, 1500 * time.Millisecond, 2 * time.Second, 2500 * time.Millisecond, 3 * time.Second, 4 * time.Second, 5 * time.Second, 6 * time.Second}
	attemptTimeout = 3 * time.Second
	maxQueue       = 1000
)

const (
	maxEnabledWebhooks    = 32
	maxQueuedWebhookBytes = int64(16 << 20)
	localResolveTimeout   = 250 * time.Millisecond
	targetResolveTimeout  = time.Second
	privateSettingPrefix  = "webhook.allow_private_network."
)

// Same links migrated basic groups and supergroups using local snapshots.
type Same func(ctx context.Context, a, b string) bool

// Manager holds the webhooks and their queues.
type Manager struct {
	st            *state.State
	logger        *slog.Logger
	client        *http.Client
	version       string
	same          Same
	account       func(context.Context) account.Info
	engineAccount func(context.Context) (engine.Account, error)
	resolveLocal  func(context.Context, engine.ResolveRequest) ([]engine.Candidate, error)
	secure        *secureDialer
	limits        *Limiter
	configMu      sync.Mutex
	dispatchMu    sync.Mutex
	unbind        context.CancelFunc

	mu             sync.Mutex
	workers        map[string]*worker
	ctx            context.Context
	generation     uint64
	queuedBytes    int64
	maxQueuedBytes int64
	// defaultAllowPrivate is used only by same-package fixtures that predate the
	// explicit setting. Production constructors leave it false.
	defaultAllowPrivate bool
}

type worker struct {
	hook         state.Webhook
	allowPrivate bool
	queue        []*delivery
	active       *delivery
	queuedBytes  int64
	dropped      int
	wake         chan struct{}
	stop         context.CancelFunc
}

func New(st *state.State, logger *slog.Logger, version string, same Same) *Manager {
	return NewWithLimiter(st, logger, version, same, NewLimiter())
}

// NewWithLimiter creates a manager that shares process-wide webhook resources
// with every other manager receiving the same limiter.
func NewWithLimiter(st *state.State, logger *slog.Logger, version string, same Same, limits *Limiter) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	if limits == nil {
		limits = NewLimiter()
	}
	transport, secure := newSecureTransportWithSemaphore(nil, nil, limits.dialSem)
	return &Manager{st: st, logger: logger, version: version, same: same, workers: map[string]*worker{},
		secure: secure, limits: limits, maxQueuedBytes: maxQueuedWebhookBytes,
		client: &http.Client{Transport: transport, Timeout: attemptTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// SetAccount remains source compatible with the panel/daemon; phone is never
// copied to the wire identity. BindEngine supplies the actual Telegram identity.
func (m *Manager) SetAccount(f func(context.Context) account.Info) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.account = f
}

func (m *Manager) stamp(ctx context.Context, ev *Event) {
	m.mu.Lock()
	read, legacy := m.engineAccount, m.account
	m.mu.Unlock()
	if ev.Identity == nil {
		ev.Identity = &Identity{}
	}
	if read != nil {
		if a, err := read(ctx); err == nil && (ev.Identity.ID == "" || ev.Identity.ID == a.ID) {
			ev.Identity = &Identity{ID: a.ID, SelfID: a.SelfID, Username: a.Username}
		}
	} else if legacy != nil {
		a := legacy(ctx)
		if ev.Identity.ID == "" {
			ev.Identity.ID = engine.AccountID(a.Slug)
		}
	}
}

// A delivery owns immutable wire bytes: retry cannot change the signature.
type delivery struct {
	event     Event
	body      []byte
	invalid   bool // guarded by Manager.mu
	accounted bool // guarded by Manager.mu
}

var errDiscard = errors.New("webhook: queued message was deleted or expired")

func privateSettingKey(id string) string { return privateSettingPrefix + id }

func (m *Manager) privateAllowed(ctx context.Context, id string) (bool, error) {
	value, err := m.st.Setting(ctx, privateSettingKey(id))
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

func (m *Manager) setPrivateAllowed(ctx context.Context, id string, allow bool) error {
	value := ""
	if allow {
		value = "true"
	}
	return m.st.SetSetting(ctx, privateSettingKey(id), value)
}

func (m *Manager) checkTarget(ctx context.Context, raw string, allowPrivate bool) error {
	ctx, cancel := context.WithTimeout(ctx, targetResolveTimeout)
	defer cancel()
	return m.secure.checkURL(ctx, raw, allowPrivate)
}

func (m *Manager) releaseDeliveryLocked(w *worker, d *delivery) {
	if d == nil || !d.accounted {
		return
	}
	size := int64(len(d.body))
	if size > m.queuedBytes || size > w.queuedBytes {
		panic("webhook: queued byte accounting underflow")
	}
	m.queuedBytes -= size
	w.queuedBytes -= size
	if m.limits != nil {
		m.limits.releaseQueued(size)
	}
	d.accounted = false
}

func (m *Manager) replaceDeliveryBytesLocked(w *worker, victim *delivery, replacementSize int64) bool {
	oldSize := int64(0)
	if victim != nil && victim.accounted {
		oldSize = int64(len(victim.body))
	}
	if m.limits != nil && !m.limits.replaceQueued(oldSize, replacementSize) {
		return false
	}
	if oldSize > 0 {
		if oldSize > m.queuedBytes || oldSize > w.queuedBytes {
			panic("webhook: queued byte accounting underflow")
		}
		m.queuedBytes -= oldSize
		w.queuedBytes -= oldSize
		victim.accounted = false
	}
	return true
}

func (m *Manager) stopWorkerLocked(w *worker) {
	w.stop()
	for _, d := range w.queue {
		m.releaseDeliveryLocked(w, d)
	}
	clear(w.queue)
	w.queue = nil
}

// Start begins delivering to the enabled webhooks, until ctx ends. Legacy
// configuration is reconciled before any worker starts: unsafe hooks fail
// closed, and the process-wide limit keeps the oldest enabled hooks across all
// accounts (creation time, then ID). A newer hook already running for another
// account is disabled when an older one needs its slot, so the retained set
// does not depend on account startup order.
func (m *Manager) Start(ctx context.Context) error {
	m.limits.startMu.Lock()
	defer m.limits.startMu.Unlock()
	m.configMu.Lock()
	defer m.configMu.Unlock()
	started := false
	defer func() {
		if started {
			return
		}
		m.mu.Lock()
		for id, w := range m.workers {
			m.stopWorkerLocked(w)
			delete(m.workers, id)
		}
		m.mu.Unlock()
		m.limits.releaseHooks(m)
	}()
	hooks, err := m.st.Webhooks(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	for id, w := range m.workers {
		m.stopWorkerLocked(w)
		delete(m.workers, id)
	}
	m.ctx = ctx
	m.generation++
	generation := m.generation
	m.mu.Unlock()
	m.limits.releaseHooks(m)

	for i := range hooks {
		h := hooks[i]
		if !h.Enabled {
			continue
		}
		allowPrivate, err := m.privateAllowed(ctx, h.ID)
		if err == nil {
			err = m.checkTarget(ctx, h.URL, allowPrivate)
		}
		switch {
		case err != nil:
			reason := i18n.T("webhook desativado: o destino não passou pela política de rede segura")
			if disableErr := m.st.DisableWebhook(ctx, h.ID, time.Now(), reason); disableErr != nil {
				return disableErr
			}
		case !m.reserveAtStart(ctx, h):
			if err := m.st.DisableWebhook(ctx, h.ID, time.Now(), limitReason()); err != nil {
				return err
			}
		default:
			m.run(h, allowPrivate)
		}
	}
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.generation != generation {
			return
		}
		if m.unbind != nil {
			m.unbind()
		}
		for id, w := range m.workers {
			m.stopWorkerLocked(w)
			delete(m.workers, id)
		}
		m.limits.releaseHooks(m)
	}()
	started = true
	return nil
}

func limitReason() string {
	return i18n.Tf("webhook desativado: o limite de %d webhooks habilitados foi atingido", maxEnabledWebhooks)
}

// reserveAtStart reserves a slot for h, displacing newer hooks of other
// managers while h precedes them in the global retention order. The caller
// holds limits.startMu and m.configMu.
func (m *Manager) reserveAtStart(ctx context.Context, h state.Webhook) bool {
	for {
		ok, victim, victimID := m.limits.reserveOrNewest(m, h.ID, h.CreatedAt, true)
		if ok {
			return true
		}
		if victim == nil || !victim.disableForLimit(ctx, victimID) {
			return false
		}
	}
}

// disableForLimit persists and stops a hook displaced by an older one. Only
// Start calls it, for another manager, under limits.startMu.
func (m *Manager) disableForLimit(ctx context.Context, id string) bool {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	if !m.limits.hasHook(m, id) {
		// Released concurrently (deleted, disabled or shut down): retry.
		return true
	}
	if err := m.st.DisableWebhook(context.WithoutCancel(ctx), id, time.Now(), limitReason()); err != nil {
		m.logger.Warn("could not disable a newer webhook above the limit", "id", id, "error", err)
		return false
	}
	m.halt(id)
	m.limits.releaseHook(m, id)
	m.logger.Warn("webhook disabled: an older webhook needs its slot", "id", id)
	return true
}

// run starts (or restarts, with an empty queue) the worker of a webhook.
func (m *Manager) run(h state.Webhook, allowPrivate bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.workers[h.ID]; ok {
		m.stopWorkerLocked(old)
		delete(m.workers, h.ID)
	}
	if m.ctx == nil || !h.Enabled {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	// Workers own filter snapshots, independent of settings/results retained
	// by the caller (including the Webhook returned by Create and Update).
	h.Events, h.Chats = slices.Clone(h.Events), slices.Clone(h.Chats)
	w := &worker{hook: h, allowPrivate: allowPrivate, wake: make(chan struct{}, 1), stop: cancel}
	m.workers[h.ID] = w
	go m.loop(ctx, w)
}

func (m *Manager) halt(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.workers[id]; ok {
		m.stopWorkerLocked(w)
		delete(m.workers, id)
	}
}

// Dispatch queues only the five contract event kinds; never receipts.
func (m *Manager) Dispatch(ctx context.Context, ev Event) {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	if ctx.Err() != nil || !slices.Contains(Events, ev.Kind) {
		return
	}
	if ev.EventID == "" {
		ev.EventID = ev.ID
	}
	if ev.EventID == "" {
		return
	}
	ev.Schema = 1
	ev.At = time.Now().UTC()
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = ev.At
	}
	if ev.Source == "" {
		ev.Source = engine.SourceUpdate
	}
	if ev.chat == "" && ev.Chat != nil {
		ev.chat = ev.Chat.ID
	}
	if ev.Message != nil {
		ev.fromMe = ev.Message.FromMe
	}
	m.stamp(ctx, &ev)
	if ev.Reaction != nil && !ev.reactionPartial && ev.Identity != nil && ev.Identity.SelfID != 0 {
		self := fmt.Sprintf("user:%d", ev.Identity.SelfID)
		knownOwn := len(ev.Reaction.Added)+len(ev.Reaction.Removed) > 0
		for _, r := range ev.Reaction.Added {
			knownOwn = knownOwn && r.By == self
		}
		for _, r := range ev.Reaction.Removed {
			knownOwn = knownOwn && r.By == self
		}
		ev.fromMe = ev.fromMe || knownOwn
	}

	m.mu.Lock()
	if m.ctx == nil || m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	generation := m.generation
	workers := make([]*worker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
		// Invalidate even when the hook does not subscribe to deletes.
		if ev.Delete != nil {
			m.invalidateLocked(w, ev)
		}
	}
	same, resolve := m.same, m.resolveLocal
	m.mu.Unlock()

	for _, w := range workers {
		if !wants(ctx, w.hook, ev, same, resolve) {
			continue
		}
		out := ev
		out.ID = deliveryID(w.hook.ID, ev.EventID)
		body, err := json.Marshal(out)
		if err != nil {
			continue
		}
		owned := deliveryEvent(out)

		m.mu.Lock()
		if m.generation != generation || m.ctx == nil || m.ctx.Err() != nil || m.workers[w.hook.ID] != w {
			m.mu.Unlock()
			continue
		}
		pending := len(w.queue)
		if w.active != nil && !w.active.invalid {
			pending++
		}
		var victim *delivery
		if pending >= maxQueue {
			if len(w.queue) == 0 {
				w.dropped++
				m.mu.Unlock()
				continue
			}
			victim = w.queue[0]
		}
		size := int64(len(body))
		projected := m.queuedBytes + size
		if victim != nil && victim.accounted {
			projected -= int64(len(victim.body))
		}
		if size > m.maxQueuedBytes || projected > m.maxQueuedBytes {
			w.dropped++
			m.mu.Unlock()
			continue
		}
		if !m.replaceDeliveryBytesLocked(w, victim, size) {
			w.dropped++
			m.mu.Unlock()
			continue
		}
		if victim != nil {
			w.queue[0] = nil
			w.queue = w.queue[1:]
			victim.invalid = true
			m.releaseDeliveryLocked(w, victim)
			w.dropped++
		}
		d := &delivery{event: owned, body: body, accounted: true}
		w.queue = append(w.queue, d)
		m.queuedBytes += size
		w.queuedBytes += size
		select {
		case w.wake <- struct{}{}:
		default:
		}
		m.mu.Unlock()
	}
}

// deliveryEvent keeps only the immutable metadata needed for expiry,
// invalidation, and headers. The complete immutable payload lives in body, so
// queued memory is not doubled by retaining a second decoded event tree.
func deliveryEvent(ev Event) Event {
	out := Event{Kind: ev.Kind, ID: ev.ID}
	if ev.Identity != nil {
		out.Identity = &Identity{ID: ev.Identity.ID}
	}
	if ev.Chat != nil {
		out.Chat = &Chat{ID: ev.Chat.ID}
	}
	if ev.Message != nil {
		out.Message = &Message{ChatID: ev.Message.ChatID, MessageID: ev.Message.MessageID, Timestamp: ev.Message.Timestamp, TTLPeriod: ev.Message.TTLPeriod}
		if ev.Message.ExpiresAt != nil {
			expires := *ev.Message.ExpiresAt
			out.Message.ExpiresAt = &expires
		}
	}
	return out
}

func (m *Manager) invalidateLocked(w *worker, ev Event) {
	matches := func(d *delivery) bool {
		msg := d.event.Message
		if msg == nil || d.event.Identity == nil || ev.Identity == nil || d.event.Identity.ID != ev.Identity.ID {
			return false
		}
		if ev.Chat != nil {
			return msg.ChatID == ev.Chat.ID && slices.Contains(ev.Delete.MessageIDs, msg.MessageID)
		}
		return !strings.HasPrefix(msg.ChatID, "channel:") && slices.Contains(ev.Delete.UnresolvedIDs, msg.MessageID)
	}
	original := w.queue
	kept := original[:0]
	for _, d := range w.queue {
		if matches(d) {
			d.invalid = true
			m.releaseDeliveryLocked(w, d)
			continue
		}
		kept = append(kept, d)
	}
	clear(original[len(kept):])
	w.queue = kept
	if w.active != nil && matches(w.active) {
		w.active.invalid = true
	}
}

func wants(ctx context.Context, h state.Webhook, ev Event, same Same, resolve func(context.Context, engine.ResolveRequest) ([]engine.Candidate, error)) bool {
	if !h.Enabled || !slices.Contains(h.Events, ev.Kind) {
		return false
	}
	if ev.Kind != "read" && (ev.fromMe || (ev.Delete != nil && ev.Delete.Reason == "revoked_by_me")) && !h.IncludeOwn {
		return false
	}
	if len(h.Chats) == 0 {
		return true
	}
	for _, c := range h.Chats {
		if ev.chat != "" && (c == ev.chat || sameChat(ctx, c, ev.chat, same, resolve)) {
			return true
		}
	}
	return false
}

func (m *Manager) loop(ctx context.Context, w *worker) {
	for {
		m.mu.Lock()
		if ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		var d *delivery
		if len(w.queue) > 0 {
			d = w.queue[0]
			w.queue[0] = nil
			w.queue = w.queue[1:]
			w.active = d
		}
		m.mu.Unlock()
		if d == nil {
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
				continue
			}
		}
		last, err := m.deliverWithRetries(ctx, w.hook, w.allowPrivate, d)
		m.mu.Lock()
		if d.invalid {
			err = errDiscard
		}
		m.releaseDeliveryLocked(w, d)
		w.active = nil
		m.mu.Unlock()
		if errors.Is(err, errDiscard) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reason := i18n.Tf("%d tentativas seguidas sem sucesso em menos de um minuto (a última: %s)", len(retryWaits)+1, last)
			m.configMu.Lock()
			m.mu.Lock()
			// A replaced worker must not disable/cancel the new configuration.
			if m.workers[w.hook.ID] == w {
				if err := m.st.DisableWebhook(context.WithoutCancel(ctx), w.hook.ID, time.Now(), reason); err == nil {
					m.limits.releaseHook(m, w.hook.ID)
				}
				m.stopWorkerLocked(w)
				delete(m.workers, w.hook.ID)
				m.logger.Warn("webhook disabled", "id", w.hook.ID)
			}
			m.mu.Unlock()
			m.configMu.Unlock()
			return
		}
	}
}

func (m *Manager) deliverWithRetries(ctx context.Context, h state.Webhook, allowPrivate bool, d *delivery) (string, error) {
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		m.mu.Lock()
		invalid := d.invalid
		m.mu.Unlock()
		if invalid || expired(d.event, time.Now()) {
			return "", errDiscard
		}
		result, err := m.post(ctx, h, d.event, d.body, attempt+1, allowPrivate)
		_ = m.st.RecordDelivery(context.WithoutCancel(ctx), h.ID, time.Now(), result, err == nil)
		if err == nil || attempt >= len(retryWaits) {
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(retryWaits[attempt]):
		}
	}
}

func expired(ev Event, now time.Time) bool {
	msg := ev.Message
	if msg == nil {
		return false
	}
	if msg.ExpiresAt != nil && !msg.ExpiresAt.After(now) {
		return true
	}
	return msg.TTLPeriod > 0 && !msg.Timestamp.Add(time.Duration(msg.TTLPeriod)*time.Second).After(now)
}

// post sends one event once. Any 2xx answer is a delivery.
func (m *Manager) post(ctx context.Context, h state.Webhook, ev Event, body []byte, attempt int, allowPrivate bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	ctx = withPrivateNetwork(ctx, allowPrivate)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return err.Error(), err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "telegram-mcp-local/"+m.version)
	req.Header.Set("X-Telegram-MCP-Event", ev.Kind)
	req.Header.Set("X-Telegram-MCP-Delivery", ev.ID)
	req.Header.Set("X-Telegram-MCP-Signature", Sign(h.Secret, body))
	req.Header.Set("X-Telegram-MCP-Attempt", strconv.Itoa(attempt))
	resp, err := m.client.Do(req)
	if err != nil {
		return describe(err), err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return i18n.Tf("respondeu %s", resp.Status), fmt.Errorf("answered %s", resp.Status)
	}
	return resp.Status, nil
}

// describe says why an attempt failed in words the settings page can show.
func describe(err error) string {
	var dns *net.DNSError
	var ue *url.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return i18n.T("nenhum programa respondeu nesse endereço")
	case errors.As(err, &dns):
		return i18n.T("o endereço não foi encontrado")
	case errors.As(err, &ue) && ue.Timeout():
		return i18n.Tf("não respondeu em %d segundos", int(attemptTimeout.Seconds()))
	case errors.As(err, &ue):
		return ue.Err.Error()
	}
	return err.Error()
}

// Sign is the signature a webhook receives in X-Telegram-MCP-Signature: the
// HMAC-SHA256 of the body with the webhook's secret, in hex, after "sha256=".
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- configuration ----

// Settings is what a webhook is created or changed with.
type Settings struct {
	URL                 *string   `json:"url"`
	Events              *[]string `json:"events"`
	IncludeOwn          *bool     `json:"include_own"`
	Chats               *[]string `json:"chats"`
	Enabled             *bool     `json:"enabled"`
	AllowPrivateNetwork *bool     `json:"allow_private_network"`
}

// UserError is a request to fix rather than a failure.
type UserError struct{ Msg string }

func (e UserError) Error() string { return e.Msg }

func validURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return UserError{i18n.T("url precisa ser um endereço http:// ou https:// completo")}
	}
	return nil
}

func targetUserError(err error) error {
	switch {
	case errors.Is(err, errPrivateDestination):
		return UserError{i18n.T("destinos privados, loopback e link-local exigem allow_private_network=true")}
	case errors.Is(err, errForbiddenDestination):
		return UserError{i18n.T("o destino usa um endereço de metadata ou uma faixa de rede especial bloqueada")}
	default:
		return UserError{i18n.T("não foi possível validar com segurança o endereço do webhook")}
	}
}

func (m *Manager) enabledCount(ctx context.Context, except string) (int, error) {
	hooks, err := m.st.Webhooks(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, h := range hooks {
		if h.Enabled && h.ID != except {
			n++
		}
	}
	return n, nil
}

func validEvents(events []string) error {
	if len(events) == 0 {
		return UserError{i18n.Tf("events precisa ter ao menos um de: %s", strings.Join(Events, ", "))}
	}
	for _, e := range events {
		if !slices.Contains(Events, e) {
			return UserError{i18n.Tf("evento %q desconhecido; os eventos são %s", e, strings.Join(Events, ", "))}
		}
	}
	return nil
}

// Create adds a webhook and starts delivering to it. The secret is returned
// here only: it signs every delivery.
func (m *Manager) Create(ctx context.Context, s Settings) (state.Webhook, string, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	if s.URL == nil {
		return state.Webhook{}, "", UserError{i18n.T("url é obrigatório")}
	}
	h := state.Webhook{ID: randomHex(6), URL: strings.TrimSpace(*s.URL), Secret: randomHex(32), Events: []string{"message"},
		Enabled: true, CreatedAt: time.Now().UTC()}
	if err := validURL(h.URL); err != nil {
		return h, "", err
	}
	if s.Events != nil {
		h.Events = slices.Clone(*s.Events)
	}
	if err := validEvents(h.Events); err != nil {
		return h, "", err
	}
	if s.IncludeOwn != nil {
		h.IncludeOwn = *s.IncludeOwn
	}
	if s.Chats != nil {
		h.Chats = cleanList(*s.Chats)
		if err := validChats(h.Chats); err != nil {
			return h, "", err
		}
	}
	if s.Enabled != nil {
		h.Enabled = *s.Enabled
	}
	allowPrivate := m.defaultAllowPrivate
	if s.AllowPrivateNetwork != nil {
		allowPrivate = *s.AllowPrivateNetwork
	}
	if h.Enabled {
		if err := m.checkTarget(ctx, h.URL, allowPrivate); err != nil {
			return h, "", targetUserError(err)
		}
		n, err := m.enabledCount(ctx, "")
		if err != nil {
			return h, "", err
		}
		if n >= maxEnabledWebhooks {
			return h, "", UserError{i18n.Tf("o limite de %d webhooks habilitados foi atingido", maxEnabledWebhooks)}
		}
		if !m.limits.reserveHook(m, h.ID, h.CreatedAt) {
			return h, "", UserError{i18n.Tf("o limite de %d webhooks habilitados foi atingido", maxEnabledWebhooks)}
		}
	}
	if err := m.setPrivateAllowed(ctx, h.ID, allowPrivate); err != nil {
		if h.Enabled {
			m.limits.releaseHook(m, h.ID)
		}
		return h, "", err
	}
	if err := m.st.SaveWebhook(ctx, h); err != nil {
		if h.Enabled {
			m.limits.releaseHook(m, h.ID)
		}
		_ = m.setPrivateAllowed(context.WithoutCancel(ctx), h.ID, false)
		return h, "", err
	}
	m.run(h, allowPrivate)
	return h, h.Secret, nil
}

// Update changes a webhook. Turning a disabled one back on starts it with an
// empty queue.
func (m *Manager) Update(ctx context.Context, id string, s Settings) (state.Webhook, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	h, err := m.st.Webhook(ctx, id)
	if err != nil {
		m.halt(id)
		return h, err
	}
	hadReservation := m.limits.hasHook(m, id)
	reservedNow := false
	oldAllowPrivate, err := m.privateAllowed(ctx, id)
	if err != nil {
		m.halt(id)
		return h, err
	}
	allowPrivate := oldAllowPrivate
	if s.AllowPrivateNetwork != nil {
		allowPrivate = *s.AllowPrivateNetwork
	}
	if s.URL != nil {
		if err := validURL(*s.URL); err != nil {
			return h, err
		}
		h.URL = strings.TrimSpace(*s.URL)
	}
	if s.Events != nil {
		if err := validEvents(*s.Events); err != nil {
			return h, err
		}
		h.Events = slices.Clone(*s.Events)
	}
	if s.IncludeOwn != nil {
		h.IncludeOwn = *s.IncludeOwn
	}
	if s.Chats != nil {
		h.Chats = cleanList(*s.Chats)
		if err := validChats(h.Chats); err != nil {
			return h, err
		}
	}
	if s.Enabled != nil {
		h.Enabled = *s.Enabled
	}
	if h.Enabled {
		if err := m.checkTarget(ctx, h.URL, allowPrivate); err != nil {
			return h, targetUserError(err)
		}
		n, err := m.enabledCount(ctx, id)
		if err != nil {
			return h, err
		}
		if n >= maxEnabledWebhooks {
			return h, UserError{i18n.Tf("o limite de %d webhooks habilitados foi atingido", maxEnabledWebhooks)}
		}
		if !hadReservation {
			if !m.limits.reserveHook(m, id, h.CreatedAt) {
				return h, UserError{i18n.Tf("o limite de %d webhooks habilitados foi atingido", maxEnabledWebhooks)}
			}
			reservedNow = true
		}
	}
	if err := m.setPrivateAllowed(ctx, id, allowPrivate); err != nil {
		if reservedNow {
			m.limits.releaseHook(m, id)
		}
		m.halt(id)
		return h, err
	}
	if err := m.st.SaveWebhook(ctx, h); err != nil {
		if reservedNow {
			m.limits.releaseHook(m, id)
		}
		m.halt(id)
		_ = m.setPrivateAllowed(context.WithoutCancel(ctx), id, oldAllowPrivate)
		return h, err
	}
	if !h.Enabled {
		m.limits.releaseHook(m, id)
	}
	if h.Enabled {
		if err := m.st.EnableWebhook(ctx, id); err != nil {
			m.halt(id)
			return h, err
		}
	}
	if h, err = m.st.Webhook(ctx, id); err != nil {
		m.halt(id)
		return h, err
	}
	if h.Enabled {
		m.run(h, allowPrivate)
	} else {
		m.halt(id)
	}
	return h, nil
}

// Delete removes a webhook and drops its queue.
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	m.halt(id)
	if err := m.st.DeleteWebhook(ctx, id); err != nil {
		return err
	}
	m.limits.releaseHook(m, id)
	return m.setPrivateAllowed(ctx, id, false)
}

// List is every webhook with how much waits in its queue.
func (m *Manager) List(ctx context.Context) ([]Status, error) {
	hooks, err := m.st.Webhooks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(hooks))
	for _, h := range hooks {
		allowPrivate, err := m.privateAllowed(ctx, h.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, m.status(h, allowPrivate))
	}
	return out, nil
}

// Get is one webhook with its queue.
func (m *Manager) Get(ctx context.Context, id string) (Status, error) {
	h, err := m.st.Webhook(ctx, id)
	if err != nil {
		return Status{}, err
	}
	allowPrivate, err := m.privateAllowed(ctx, h.ID)
	if err != nil {
		return Status{}, err
	}
	return m.status(h, allowPrivate), nil
}

// Status is a webhook as the API shows it.
type Status struct {
	state.Webhook
	AllowPrivateNetwork bool  `json:"allow_private_network"`
	Queued              int   `json:"queued"`
	QueuedBytes         int64 `json:"queued_bytes"`
	Dropped             int   `json:"dropped,omitempty"`
}

func (m *Manager) status(h state.Webhook, allowPrivate bool) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Webhook: h, AllowPrivateNetwork: allowPrivate}
	if w, ok := m.workers[h.ID]; ok {
		st.Queued, st.QueuedBytes, st.Dropped = len(w.queue), w.queuedBytes, w.dropped
		if w.active != nil && !w.active.invalid {
			st.Queued++
		}
	}
	return st
}

// TestMessage is the message of a test event. Like every event, it is the
// same whatever language the panel shows: the language is the interface's,
// and the programs that receive the webhooks see one contract.
func TestMessage(at time.Time) *Message {
	return &Message{MessageID: 1, ID: "1", ChatID: "user:777000", ChatName: "Teste", Timestamp: at,
		SenderID: "user:777000", SenderName: "Teste", Text: "Mensagem de teste do Telegram MCP"}
}

// Test sends one test event to a webhook now, once, and says what came back.
// It neither queues nor counts toward turning the webhook off.
func (m *Manager) Test(ctx context.Context, id string) (map[string]any, error) {
	h, err := m.st.Webhook(ctx, id)
	if err != nil {
		return nil, err
	}
	allowPrivate, err := m.privateAllowed(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := m.checkTarget(ctx, h.URL, allowPrivate); err != nil {
		return nil, targetUserError(err)
	}
	ev := Event{Schema: 1, Kind: "test", ID: "d_" + randomHex(16), EventID: randomHex(16), At: time.Now().UTC(), OccurredAt: time.Now().UTC(), Source: engine.SourceUpdate, Test: true, Message: TestMessage(time.Now().UTC()), Chat: &Chat{ID: "user:777000", Type: engine.PeerUser, Kind: engine.KindUser, Name: "Teste"}}
	m.stamp(ctx, &ev)
	started := time.Now()
	body, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	result, err := m.post(ctx, h, ev, body, 1, allowPrivate)
	out := map[string]any{"ok": err == nil, "result": result, "ms": time.Since(started).Milliseconds()}
	if err != nil {
		out["error"] = err.Error()
	}
	return out, nil
}

func cleanList(v []string) []string {
	var out []string
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func validChats(chats []string) error {
	for _, chat := range chats {
		parts := strings.Split(chat, ":")
		if len(parts) != 2 || (parts[0] != "user" && parts[0] != "chat" && parts[0] != "channel") {
			return UserError{i18n.Tf("chat_id %q inválido; use user:id, chat:id ou channel:id", chat)}
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[1] {
			return UserError{i18n.Tf("chat_id %q inválido; o id deve ser um inteiro positivo", chat)}
		}
	}
	return nil
}

// sameChat uses only local resolution (Network:false) for migrated peer links.
// Callers snapshot the callbacks under Manager.mu and resolve after releasing it.
func sameChat(ctx context.Context, a, b string, same Same, resolve func(context.Context, engine.ResolveRequest) ([]engine.Candidate, error)) bool {
	if same != nil {
		checkCtx, cancel := context.WithTimeout(ctx, localResolveTimeout)
		matched := same(checkCtx, a, b)
		cancel()
		if matched {
			return true
		}
	}
	if resolve == nil {
		return false
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		checkCtx, cancel := context.WithTimeout(ctx, localResolveTimeout)
		peers, err := resolve(checkCtx, engine.ResolveRequest{Query: pair[0], Network: false, Limit: 1})
		cancel()
		if err == nil && len(peers) == 1 && peers[0].Peer.MigratedTo != nil && peerText(*peers[0].Peer.MigratedTo) == pair[1] {
			return true
		}
	}
	return false
}
