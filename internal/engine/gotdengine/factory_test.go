package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
)

func TestFactoryPreservesInjectedPersisterWithoutTelegram(t *testing.T) {
	fixture, _ := offlineEngine(t)
	c, err := Factory(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	constructed := c.(*Engine)
	if constructed.cfg.Store != fixture.cfg.Store {
		t.Fatal("factory discarded injected Persister")
	}
	if _, err := c.SendText(context.Background(), engine.SendTextRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, Text: "must not send"}); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatal(err)
	}
}
func TestFactoryFailsClosedWithoutProductionDependencies(t *testing.T) {
	for _, cfg := range []engine.Config{{}, {APIID: 1, APIHash: "synthetic"}} {
		if c, err := Factory(cfg); err == nil || c != nil {
			t.Fatal("missing persistence/session", err)
		}
	}
}
