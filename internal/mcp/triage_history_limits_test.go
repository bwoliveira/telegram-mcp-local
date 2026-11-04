package mcp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestTelegramHistoryFloodDefersRemainingPeers(t *testing.T) {
	f := peopleFake(t)
	var calls atomic.Int32
	until := time.Now().Add(time.Hour)
	e := &telegramHistoryEngine{Controller: f, backfill: func(_ context.Context, r engine.BackfillRequest) (engine.BackfillResult, error) {
		calls.Add(1)
		return engine.BackfillResult{Peer: r.Peer}, &engine.FloodWaitError{Method: "messages.getHistory", Until: until}
	}}
	st := &peopleTestStore{ReadStore: f, override: true, dialogs: []engine.Dialog{{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1}}, {Peer: engine.PeerRef{Type: engine.PeerUser, ID: 2}}}}
	s := New(Config{Engine: e, ReadStore: st})
	if _, _, err := s.requestTelegramHistory(context.Background(), "", 10, 3, 2); err != nil {
		t.Fatal(err)
	}
	done := waitTelegramHistory(t, s)
	if calls.Load() != 1 || done.Done != 1 || len(done.Failures) != 2 {
		t.Fatalf("continued during FloodWait: calls=%d job=%+v", calls.Load(), done)
	}
	deferred, ok := done.Failures["user:2"].(map[string]any)
	if !ok || deferred["deferred"] != true || deferred["flood_wait_until"] != until {
		t.Fatalf("unprocessed peer missing: %#v", done.Failures)
	}
	if _, started, err := s.requestTelegramHistory(context.Background(), "user:3", 10, 1, 0); err == nil || started {
		t.Fatalf("new peer bypassed account/method FloodWait: started=%v err=%v", started, err)
	}
	if calls.Load() != 1 {
		t.Fatal("new job made a rate-limited RPC")
	}
}
