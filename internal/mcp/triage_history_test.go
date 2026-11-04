package mcp

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestTelegramHistorySelectsRecentTypedLocalDialogs(t *testing.T) {
	f := peopleFake(t)
	user := engine.PeerRef{Type: engine.PeerUser, ID: 42}
	group := engine.PeerRef{Type: engine.PeerChat, ID: 42}
	st := &peopleTestStore{ReadStore: f, override: true, dialogs: []engine.Dialog{{Peer: user}, {Peer: group}, {Peer: user}}, messages: []engine.Message{
		{Key: engine.MessageKey{Account: "people", Peer: user, ID: 200}, Date: time.Unix(100, 0)},
		{Key: engine.MessageKey{Account: "people", Peer: group, ID: 1}, Date: time.Unix(200, 0)},
		{Key: engine.MessageKey{Account: "another", Peer: user, ID: 300}, Date: time.Unix(999, 0)},
	}}
	s := New(Config{Engine: f, ReadStore: st})
	account, targets, err := s.telegramHistoryTargets(context.Background(), "", 10)
	if err != nil || account != "people" || !reflect.DeepEqual(targets, []string{"chat:42", "user:42"}) {
		t.Fatalf("account=%q targets=%v err=%v", account, targets, err)
	}
	_, targets, err = s.telegramHistoryTargets(context.Background(), "", 1)
	if err != nil || !reflect.DeepEqual(targets, []string{"chat:42"}) {
		t.Fatalf("limit: %v %v", targets, err)
	}
}

type telegramHistoryEngine struct {
	engine.Controller
	backfill func(context.Context, engine.BackfillRequest) (engine.BackfillResult, error)
}

func (e *telegramHistoryEngine) Backfill(ctx context.Context, r engine.BackfillRequest) (engine.BackfillResult, error) {
	return e.backfill(ctx, r)
}

func waitTelegramHistory(t *testing.T, s *Server) HistoryJob {
	t.Helper()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if j := s.HistoryStatus(); j != nil && j.FinishedAt != nil {
			return *j
		}
		select {
		case <-deadline:
			t.Fatal("history did not finish")
		case <-tick.C:
		}
	}
}

func TestTelegramHistoryPublishesEachDurablePageAndRetainsFloodProgress(t *testing.T) {
	f := peopleFake(t)
	second := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	until := time.Unix(5000, 0)
	e := &telegramHistoryEngine{Controller: f, backfill: func(ctx context.Context, r engine.BackfillRequest) (engine.BackfillResult, error) {
		if r.Count != 100 || r.Rounds != 1 {
			t.Errorf("unbounded page request: %+v", r)
		}
		if calls.Add(1) == 1 {
			return engine.BackfillResult{Peer: r.Peer, Fetched: 100, Added: 80, Rounds: 1, OldestID: 101}, nil
		}
		close(second)
		select {
		case <-release:
		case <-ctx.Done():
			return engine.BackfillResult{}, ctx.Err()
		}
		return engine.BackfillResult{Peer: r.Peer, FloodUntil: &until}, &engine.FloodWaitError{Method: "messages.getHistory", Until: until}
	}}
	s := New(Config{Engine: e, ReadStore: f})
	j, started, err := s.requestTelegramHistory(context.Background(), "user:1001", 999, 999, 0)
	if err != nil || !started || j.Count != 100 || j.Rounds != 20 {
		t.Fatalf("start: %+v %v %v", j, started, err)
	}
	select {
	case <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("second page not requested")
	}
	progress := s.HistoryStatus()
	if progress.Added != 80 || len(progress.Results) != 1 || progress.Results[0].OldestID != 101 || progress.Done != 0 {
		t.Errorf("no incremental progress: %+v", progress)
	}
	// The active job is returned instead of creating concurrent work.
	_, started, err = s.requestTelegramHistory(context.Background(), "user:1001", 1, 1, 0)
	if err != nil || started {
		t.Errorf("duplicate job: %v %v", started, err)
	}
	close(release)
	done := waitTelegramHistory(t, s)
	if done.Added != 80 || done.Done != 1 || done.Results[0].Fetched != 100 || done.Results[0].OldestID != 101 || done.Results[0].FloodUntil == nil || calls.Load() != 2 {
		t.Fatalf("lost committed progress: %+v", done)
	}
	failure, ok := done.Failures["user:1001"].(map[string]any)
	if !ok || failure["flood_wait_until"] != until {
		t.Fatalf("lost typed FloodWait: %#v", done.Failures)
	}
	if _, ok := failure["error"]; !ok {
		t.Fatal("missing failure")
	}
}

func TestTelegramHistoryCoverageDoesNotConfuseAccessibleStartWithGapFreeStore(t *testing.T) {
	f := peopleFake(t)
	s := New(Config{Engine: f, ReadStore: &peopleTestStore{ReadStore: f, fail: "coverage"}})
	job := HistoryJob{Chats: []string{"user:1001"}, Results: []engine.BackfillResult{{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1001}, Complete: true}}}
	coverage := s.telegramHistoryCoverage(context.Background(), job)
	if len(coverage) != 1 || coverage[0].Complete || !coverage[0].Partial || coverage[0].Error == "" {
		t.Fatalf("failed coverage advertised complete: %+v", coverage)
	}
}

func TestTelegramHistoryInvalidPeerAndStoreFailureDoNotSchedule(t *testing.T) {
	f := peopleFake(t)
	s := New(Config{Engine: f, ReadStore: &peopleTestStore{ReadStore: f, fail: "dialogs"}})
	for _, target := range []string{"user:01", "42", "channel:-1"} {
		if _, _, err := s.requestTelegramHistory(context.Background(), target, 10, 1, 0); err == nil {
			t.Fatalf("accepted %q", target)
		}
	}
	if _, _, err := s.requestTelegramHistory(context.Background(), "", 10, 1, 0); err == nil {
		t.Fatal("silenced store failure")
	}
	if s.HistoryStatus() != nil {
		t.Fatal("scheduled invalid request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.requestTelegramHistory(ctx, "user:1001", 10, 1, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestTelegramHistorySnapshotsAreOwned(t *testing.T) {
	stamp := time.Unix(1000, 0)
	job := &HistoryJob{Chats: []string{"user:1"}, FinishedAt: &stamp, Results: []engine.BackfillResult{{OldestDate: &stamp, FloodUntil: &stamp}}, Failures: map[string]any{"user:1": map[string]any{"error": "original"}}}
	copy := cloneTelegramHistory(job)
	copy.Chats[0] = "chat:2"
	*copy.FinishedAt = time.Time{}
	*copy.Results[0].OldestDate = time.Time{}
	*copy.Results[0].FloodUntil = time.Time{}
	copy.Failures["user:1"].(map[string]any)["error"] = "changed"
	if job.Chats[0] != "user:1" || job.FinishedAt.Unix() != 1000 || job.Results[0].OldestDate.Unix() != 1000 || job.Results[0].FloodUntil.Unix() != 1000 || job.Failures["user:1"].(map[string]any)["error"] != "original" {
		t.Fatalf("snapshot aliases live job: %+v", job)
	}
}
