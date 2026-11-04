package gotdengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestHistoryProgressLoadsDurableCursorWithoutRPCOrAuthentication(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	oldest := time.Unix(1000, 0)
	flood := time.Unix(2000, 0)
	cursor := engine.Cursor{Peer: peer, OldestID: 7, OldestDate: &oldest, State: "flood_wait", FloodUntil: &flood, Fetched: 200}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Cursor: &cursor}); err != nil {
		t.Fatal(err)
	}
	e.account.Authenticated = false
	result, err := e.HistoryProgress(ctx, peer)
	if err != nil || result.Peer != peer || result.OldestID != 7 || result.OldestDate == nil || !result.OldestDate.Equal(oldest) || result.FloodUntil == nil || !result.FloodUntil.Equal(flood) || result.Fetched != 0 || result.Complete {
		t.Fatalf("lost cursor or confused lifetime count: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.HistoryProgress(ctx, peer); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
	if _, err := e.HistoryProgress(context.Background(), engine.PeerRef{Type: "invalid", ID: 1}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("invalid typed peer: %v", err)
	}
}
