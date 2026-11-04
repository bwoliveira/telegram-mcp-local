package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
)

func TestChannelAccountOnlyDeleteNeverEscalates(t *testing.T) {
	e, _ := actionFixture(t)
	for _, dry := range []bool{true, false} {
		_, err := e.DeleteConfirmed(context.Background(), engine.DeleteRequest{Peer: engine.PeerRef{Type: engine.PeerChannel, ID: 30}, IDs: []int{71}, WriteOptions: engine.WriteOptions{DryRun: dry}}, true)
		if !errors.Is(err, engine.ErrNotSupported) {
			t.Fatalf("dry=%v: %v", dry, err)
		}
	}
}
