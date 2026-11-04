package gotdengine

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestResolveUsernameRequiresExactLocalMatch(t *testing.T) {
	e, _ := writeFixture(t)
	calls := 0
	sentinel := errors.New("synthetic remote username miss")
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		r, ok := in.(*tg.ContactsResolveUsernameRequest)
		if !ok || r.Username != "an" {
			t.Errorf("unexpected resolve RPC %T: %v", in, in)
		}
		calls++
		return sentinel
	}))
	got, err := e.Resolve(context.Background(), engine.ResolveRequest{Query: "@ana", Network: true, Limit: 1000})
	if err != nil || len(got) != 1 || calls != 0 {
		t.Fatalf("local username: %v %v calls=%d", got, err, calls)
	}
	got, err = e.Resolve(context.Background(), engine.ResolveRequest{Query: "@an", Network: false, Limit: 1000})
	if err != nil || len(got) != 0 || calls != 0 {
		t.Fatalf("substring treated as exact: %v %v calls=%d", got, err, calls)
	}
	_, err = e.Resolve(context.Background(), engine.ResolveRequest{Query: "@an", Network: true, Limit: 1000})
	if err == nil || calls != 1 {
		t.Fatalf("missing exact username did not reach remote: %v calls=%d", err, calls)
	}
}
