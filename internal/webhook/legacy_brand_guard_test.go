// Legacy literals are isolated here to verify that inherited branding and APIs stay absent.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestRetryBytesHeadersAndPerHookStableIdentity(t *testing.T) {
	m, _ := manager(t)
	srv, ch := wireServer(t, func(w http.ResponseWriter, n int32) {
		if n <= 3 {
			w.WriteHeader(502)
		} else {
			w.WriteHeader(204)
		}
	})
	h, secret, err := m.Create(context.Background(), Settings{URL: ptr(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	ev := message("A", "user:7", false)
	m.Dispatch(context.Background(), ev)
	first := receive(t, ch)
	for i := 1; i <= 4; i++ {
		got := first
		if i > 1 {
			got = receive(t, ch)
		}
		if !bytes.Equal(got.body, first.body) {
			t.Fatalf("retry %d changed body", i)
		}
		if got.header.Get("X-Telegram-MCP-Attempt") != strconv.Itoa(i) || got.header.Get("X-Telegram-MCP-Signature") != Sign(secret, got.body) || got.header.Get("X-Telegram-MCP-Event") != "message" || got.header.Get("X-Telegram-MCP-Delivery") != got.event.ID || got.header.Get("User-Agent") != "telegram-mcp-local/test" {
			t.Fatalf("headers: %v", got.header)
		}
		if got.header.Get("X-WhatsApp-MCP-Signature") != "" {
			t.Fatal("legacy header")
		}
	}
	m.Dispatch(context.Background(), ev)
	again := receive(t, ch)
	if again.event.ID != first.event.ID || again.event.EventID != "A" {
		t.Fatal("same fact received a new delivery identity")
	}
	srv2, ch2 := wireServer(t, nil)
	_, _, err = m.Create(context.Background(), Settings{URL: ptr(srv2.URL)})
	if err != nil {
		t.Fatal(err)
	}
	m.Dispatch(context.Background(), ev)
	_ = receive(t, ch)
	other := receive(t, ch2)
	if other.event.ID == first.event.ID || !strings.HasPrefix(other.event.ID, "d_") || len(other.event.ID) != 34 {
		t.Fatal("identity is not webhook-specific")
	}
	if len(secret) != 64 {
		t.Fatalf("secret has %d hex chars, need 32 random bytes", len(secret))
	}
	got, _ := m.Get(context.Background(), h.ID)
	all, _ := m.List(context.Background())
	for _, value := range []any{got, all} {
		b, _ := json.Marshal(value)
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("secret leaked outside creation")
		}
	}
}
