package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

// WH10/WH14: the lifecycle controller publishes committed deletes with source
// ttl. Preserve that distinction and never retain the expired message payload.
func TestLifecycleTTLDeleteReason(t *testing.T) {
	m, _ := manager(t)
	srv, ch := wireServer(t, nil)
	_, _, err := m.Create(context.Background(), Settings{URL: ptr(srv.URL), Events: ptr([]string{"delete"})})
	if err != nil {
		t.Fatal(err)
	}
	eng, _ := fake.New(engine.Config{})
	f := eng.(*fake.Controller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.BindEngine(ctx, eng)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 77}
	if err := f.Publish(ctx, engine.Batch{Source: engine.Source("ttl"), Deletes: []engine.Deletion{{Peer: &peer, IDs: []int{9910}}}}); err != nil {
		t.Fatal(err)
	}
	got := receive(t, ch)
	if got.event.Source != engine.Source("ttl") || got.event.Delete.Reason != "ttl" {
		t.Fatalf("TTL delete: %s", got.body)
	}
}

// WH03: revocation is own even when submitted by a lifecycle integration using
// the public Event seam rather than an engine update.
func TestRevokedByMeFilteredAndReadNeverOwn(t *testing.T) {
	m, _ := manager(t)
	srv, ch := wireServer(t, nil)
	_, _, err := m.Create(context.Background(), Settings{URL: ptr(srv.URL), Events: ptr([]string{"delete", "read"})})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m.Dispatch(ctx, Event{Kind: "delete", EventID: "revoke", Identity: &Identity{ID: "primary"}, Chat: &Chat{ID: "user:7"}, Delete: &Deletion{MessageIDs: []int{66}, Reason: "revoked_by_me"}})
	quiet(t, ch)
	m.Dispatch(ctx, Event{Kind: "read", EventID: "read", Identity: &Identity{ID: "primary"}, Chat: &Chat{ID: "user:7"}, Read: &Read{MaxID: 66, MessageIDs: []int{66}}, fromMe: true})
	if got := receive(t, ch); got.event.Kind != "read" {
		t.Fatalf("read: %s", got.body)
	}
}

// WH02/WH03: settings snapshots must not alias slices supplied by the caller.
func TestConfigurationOwnsEventFilters(t *testing.T) {
	m, _ := manager(t)
	srv, ch := wireServer(t, nil)
	ctx := context.Background()
	events := []string{"message"}
	h, _, err := m.Create(ctx, Settings{URL: ptr(srv.URL), Events: &events})
	if err != nil {
		t.Fatal(err)
	}
	events[0] = "read"
	m.Dispatch(ctx, message("A", "user:7", false))
	if got := receive(t, ch); got.event.Kind != "message" {
		t.Fatalf("create aliased settings: %s", got.body)
	}
	events = []string{"message"}
	if _, err := m.Update(ctx, h.ID, Settings{Events: &events}); err != nil {
		t.Fatal(err)
	}
	events[0] = "read"
	m.Dispatch(ctx, message("B", "user:7", false))
	if got := receive(t, ch); got.event.Kind != "message" {
		t.Fatalf("update aliased settings: %s", got.body)
	}
	// Returned snapshots must not be capable of modifying a running worker.
	h, _, err = m.Create(ctx, Settings{URL: ptr(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	h.Events[0] = "read"
	m.Dispatch(ctx, message("C", "user:7", false))
	_ = receive(t, ch) // original hook
	_ = receive(t, ch) // newly created hook
}

// WH10: compatibility shorthand requires a known author, not the fabricated
// peer string ":0". Counts remain useful for anonymous reaction deltas.
func TestReactionWithUnknownAuthorHasNoShorthand(t *testing.T) {
	m, _ := manager(t)
	srv, ch := wireServer(t, nil)
	_, _, err := m.Create(context.Background(), Settings{URL: ptr(srv.URL), Events: ptr([]string{"reaction"})})
	if err != nil {
		t.Fatal(err)
	}
	eng, _ := fake.New(engine.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.BindEngine(ctx, eng)
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 77}, ID: 9920}
	if err := eng.(*fake.Controller).Publish(ctx, engine.Batch{Messages: []engine.Message{{Key: key, Date: fake.Epoch()}}}); err != nil {
		t.Fatal(err)
	}
	if err := eng.(*fake.Controller).Publish(ctx, engine.Batch{Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{{Emoji: "👍", Count: 1}}, Recent: []engine.PeerReaction{{Emoji: "👍"}}}}}); err != nil {
		t.Fatal(err)
	}
	got := receive(t, ch)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(got.body, &envelope); err != nil {
		t.Fatal(err)
	}
	var reaction map[string]json.RawMessage
	if err := json.Unmarshal(envelope["reaction"], &reaction); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"by", "to", "emoji", "added"} {
		if _, ok := reaction[field]; ok {
			t.Errorf("unknown author exported %q: %s", field, got.body)
		}
	}
	// A mixed delta must still reach include_own:false hooks: counts include
	// an unknown author even though the one identifiable author is ourselves.
	if err := eng.(*fake.Controller).Publish(ctx, engine.Batch{Reactions: []engine.ReactionUpdate{{Key: key,
		Counts: []engine.ReactionCount{{Emoji: "👍", Count: 2}},
		Recent: []engine.PeerReaction{{By: engine.PeerRef{Type: engine.PeerUser, ID: 1000}, Emoji: "👍", My: true}, {Emoji: "👍"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	mixed := receive(t, ch)
	if mixed.event.Reaction.Counts[0].Count != 2 || len(mixed.event.Reaction.Added) != 1 {
		t.Fatalf("mixed reaction: %s", mixed.body)
	}
}

// WH05: the HMAC example uses independently known bytes, not Sign compared to
// itself. This is the published HMAC-SHA256 quick-brown-fox test vector.
func TestSignatureKnownVectorAndWrongSecret(t *testing.T) {
	body := []byte("The quick brown fox jumps over the lazy dog")
	const want = "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got := Sign("key", body); got != want {
		t.Fatalf("signature: %s", got)
	}
	if Sign("wrong", body) == want {
		t.Fatal("wrong secret verified")
	}
}

// WH06/WH07: redirects fail without forwarding a signed body to another URL.
func TestRedirectNeverForwardsSignedBody(t *testing.T) {
	m, _ := manager(t)
	destination, forwarded := wireServer(t, nil)
	origin, attempts := wireServer(t, func(w http.ResponseWriter, _ int32) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	h, _, err := m.Create(context.Background(), Settings{URL: ptr(origin.URL)})
	if err != nil {
		t.Fatal(err)
	}
	m.Dispatch(context.Background(), message("A", "user:7", false))
	first := receive(t, attempts)
	for i := 1; i < 10; i++ {
		next := receive(t, attempts)
		if !bytes.Equal(first.body, next.body) {
			t.Fatal("redirect retry body changed")
		}
	}
	waitFor(t, "redirect failures disable", func() bool { status, _ := m.Get(context.Background(), h.ID); return !status.Enabled })
	quiet(t, forwarded)
}

// WH14: expiry remains a delivery-time check, including retry attempts.
func TestExpiryStopsRetriesWithoutDisablingWebhook(t *testing.T) {
	m, _ := manager(t)
	ctx := context.Background()
	expires := time.Now().Add(300 * time.Millisecond)
	srv, ch := wireServer(t, func(w http.ResponseWriter, _ int32) {
		// Hold the first request until its immutable message snapshot expires.
		// The subsequent retry must re-check expiry instead of posting again.
		if remaining := time.Until(expires); remaining > 0 {
			time.Sleep(remaining + time.Millisecond)
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	h, _, err := m.Create(ctx, Settings{URL: ptr(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	ev := message("A", "user:7", false)
	ev.Message.ExpiresAt = &expires
	m.Dispatch(ctx, ev)
	_ = receive(t, ch) // proves an attempt happened before expiry
	waitFor(t, "expired retry discarded", func() bool {
		status, _ := m.Get(ctx, h.ID)
		return status.Queued == 0
	})
	quiet(t, ch)
	status, err := m.Get(ctx, h.ID)
	if err != nil || !status.Enabled || status.Delivered != 0 || status.Queued != 0 {
		t.Fatalf("discarded expiry: %+v %v", status, err)
	}
}
