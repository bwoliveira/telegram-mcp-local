package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/state"

	"github.com/bwoliveira/telegram-mcp-local/internal/account"
)

func init() {
	// The real schedule spans most of a minute; the tests keep its shape.
	retryWaits = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond,
		time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	attemptTimeout = time.Second
}

type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []Event
	sigOK  []bool
	calls  atomic.Int32
	status func(n int32) int
	secret string
}

func newReceiver(t *testing.T, status func(n int32) int) *receiver {
	r := &receiver{status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := r.calls.Add(1)
		body, _ := io.ReadAll(req.Body)
		var ev Event
		_ = json.Unmarshal(body, &ev)
		r.mu.Lock()
		r.got = append(r.got, ev)
		r.sigOK = append(r.sigOK, req.Header.Get("X-Telegram-MCP-Signature") == Sign(r.secret, body) && req.Header.Get("X-Telegram-MCP-Event") == ev.Kind)
		r.mu.Unlock()
		w.WriteHeader(r.status(n))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.got...)
}

func manager(t *testing.T) (*Manager, *state.State) {
	t.Helper()
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := New(st, nil, "test", nil)
	m.defaultAllowPrivate = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return m, st
}

func message(id, chat string, fromMe bool) Event {
	return Event{Kind: "message", EventID: id, At: time.Now(), Chat: &Chat{ID: chat}, Identity: &Identity{ID: "primary"}, chat: chat, fromMe: fromMe,
		Message: &Message{MessageID: int(id[0]), ChatID: chat, FromMe: fromMe, Text: "oi " + id}}
}

func ptr[T any](v T) *T { return &v }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDeliversInOrderSigned(t *testing.T) {
	m, st := manager(t)
	r := newReceiver(t, func(int32) int { return http.StatusNoContent })
	h, secret, err := m.Create(context.Background(), Settings{URL: ptr(r.srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	r.secret = secret
	for _, id := range []string{"A", "B", "C"} {
		m.Dispatch(context.Background(), message(id, "user:1", false))
	}
	waitFor(t, "three deliveries", func() bool { return len(r.events()) == 3 })
	for i, ev := range r.events() {
		if ev.Message.MessageID != []int{65, 66, 67}[i] || !r.sigOK[i] {
			t.Errorf("delivery %d: %+v signed %v", i, ev.Message, r.sigOK[i])
		}
	}
	waitFor(t, "the record", func() bool { w, _ := st.Webhook(context.Background(), h.ID); return w.Delivered == 3 })
}

func TestTenFailuresDisableAndDropTheQueue(t *testing.T) {
	m, st := manager(t)
	r := newReceiver(t, func(int32) int { return http.StatusInternalServerError })
	h, _, err := m.Create(context.Background(), Settings{URL: ptr(r.srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "C"} {
		m.Dispatch(context.Background(), message(id, "user:1", false))
	}
	waitFor(t, "the webhook to be disabled", func() bool { w, _ := st.Webhook(context.Background(), h.ID); return !w.Enabled })
	time.Sleep(50 * time.Millisecond)
	if n := r.calls.Load(); n != 10 {
		t.Errorf("%d attempts, want 10", n)
	}
	for _, ev := range r.events() {
		if ev.Message.MessageID != 65 {
			t.Errorf("the queue should wait behind the first event, got %d", ev.Message.MessageID)
		}
	}
	got, _ := m.Get(context.Background(), h.ID)
	if got.Queued != 0 || got.DisabledReason == "" || got.DisabledAt == nil {
		t.Errorf("after disabling: %+v", got)
	}
	// Turned back on, it starts with an empty queue: B and C are gone.
	r.status = func(int32) int { return http.StatusOK }
	if _, err := m.Update(context.Background(), h.ID, Settings{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	m.Dispatch(context.Background(), message("D", "user:1", false))
	waitFor(t, "the new event", func() bool { evs := r.events(); return evs[len(evs)-1].Message.MessageID == 68 })
	if w, _ := st.Webhook(context.Background(), h.ID); !w.Enabled || w.DisabledReason != "" {
		t.Errorf("re-enabled: %+v", w)
	}
}

func TestRetriesThroughABlip(t *testing.T) {
	m, st := manager(t)
	r := newReceiver(t, func(n int32) int {
		if n <= 3 {
			return http.StatusBadGateway
		}
		return http.StatusOK
	})
	h, _, _ := m.Create(context.Background(), Settings{URL: ptr(r.srv.URL)})
	m.Dispatch(context.Background(), message("A", "user:1", false))
	m.Dispatch(context.Background(), message("B", "user:1", false))
	waitFor(t, "both delivered", func() bool { w, _ := st.Webhook(context.Background(), h.ID); return w.Delivered == 2 })
	if w, _ := st.Webhook(context.Background(), h.ID); !w.Enabled {
		t.Error("a webhook that recovered within its retries must stay on")
	}
}

func TestFilters(t *testing.T) {
	m, _ := manager(t)
	r := newReceiver(t, func(int32) int { return http.StatusOK })
	_, _, err := m.Create(context.Background(), Settings{URL: ptr(r.srv.URL), Events: ptr([]string{"message"}), Chats: ptr([]string{"user:1"})})
	if err != nil {
		t.Fatal(err)
	}
	m.Dispatch(context.Background(), message("other-chat", "user:2", false))
	m.Dispatch(context.Background(), message("own", "user:1", true))
	reaction := message("reaction", "user:1", false)
	reaction.Kind = "reaction"
	m.Dispatch(context.Background(), reaction)
	m.Dispatch(context.Background(), message("wanted", "user:1", false))
	waitFor(t, "the wanted event", func() bool { return len(r.events()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	if evs := r.events(); len(evs) != 1 || evs[0].Message.MessageID != 119 {
		t.Errorf("delivered %+v, want only the wanted one", evs)
	}
	if _, _, err := m.Create(context.Background(), Settings{URL: ptr("ftp://x")}); err == nil {
		t.Error("a non-http url must be refused")
	}
	if _, _, err := m.Create(context.Background(), Settings{URL: ptr(r.srv.URL), Events: ptr([]string{"presence"})}); err == nil {
		t.Error("an unknown event must be refused")
	}
}

// Every event says which number it came from, so one script can serve
// several.
func TestEventsCarryTheNumber(t *testing.T) {
	m, _ := manager(t)
	m.SetAccount(func(context.Context) account.Info {
		return account.Info{Slug: "trabalho", Label: "Trabalho", Phone: "+55 (11) 98765-4321", Name: "Bruno"}
	})
	r := newReceiver(t, func(int32) int { return http.StatusNoContent })
	h, secret, err := m.Create(context.Background(), Settings{URL: ptr(r.srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	r.secret = secret
	m.Dispatch(context.Background(), message("A", "user:1", false))
	waitFor(t, "the delivery", func() bool { return len(r.events()) == 1 })
	got := r.events()[0].Identity
	if got == nil || got.ID != "primary" {
		t.Errorf("account on the event: %+v", got)
	}
	if _, err := m.Test(context.Background(), h.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the test delivery", func() bool { return len(r.events()) == 2 })
	if got := r.events()[1].Identity; got == nil || got.ID != "trabalho" {
		t.Errorf("account on the test event: %+v", got)
	}
}
