package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestStatusCountsRespectVisibilityAndDurableCheckpoint(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"), "status", WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	q := engine.PeerRef{Type: engine.PeerChannel, ID: 1}
	contact := engine.PeerRef{Type: engine.PeerUser, ID: 2}
	m := engine.Message{Key: engine.MessageKey{Account: "status", Peer: p, ID: 1}, Date: now.Add(-24 * time.Hour), Text: "synthetic retained"}
	ttl := m
	ttl.Key.Peer = q
	ttl.Date, ttl.TTLPeriod = now, 60
	_, err = s.Apply(ctx, engine.Batch{Account: "status", Source: engine.SourceUpdate,
		Peers:    []engine.PeerUpsert{{Peer: engine.Peer{Ref: contact}}},
		Messages: []engine.Message{m, ttl},
		Cursor:   &engine.Cursor{Peer: p, State: "running"},
		Gaps:     []engine.GapMark{{Reason: engine.GapStateLost, Detected: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUpdatesState(ctx, "status", engine.UpdatesState{Pts: 42, Qts: 3, Seq: 4, Date: int(now.Unix())}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(ctx, "status", q.ID, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE updates_state SET held=1,last_update_at=? WHERE account='status'`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	h, u, err := s.ReadStatus(ctx, "status")
	if err != nil || h.Messages != 2 || h.Conversations != 2 || h.Running == nil || *h.Running != p || !h.LastAt.Equal(now) {
		t.Fatalf("history=%+v err=%v", h, err)
	}
	if u.Pts != 42 || u.Qts != 3 || u.Seq != 4 || !u.Date.Equal(now) || u.Channels != 1 || u.OpenGaps != 0 || u.CoverageGaps != 1 || u.TerminalGaps != 1 || u.GapCauses[engine.GapRecoveryUnknownBaseline] != 1 || !u.Held || u.LastUpdateAt == nil || !u.LastUpdateAt.Equal(now) {
		t.Fatalf("checkpoint=%+v", u)
	}
	// Logical TTL visibility applies before the janitor physically removes rows.
	now = now.Add(time.Minute)
	h, _, err = s.ReadStatus(ctx, "status")
	if err != nil || h.Messages != 1 || h.Conversations != 1 {
		t.Fatalf("expired rows counted: %+v %v", h, err)
	}
	// Pending deletes and tombstones cannot resurrect counters before cleanup.
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO message_tombstones(account,peer_type,peer_id,msg_id,reason,deleted_at) VALUES('status','user',1,1,'deleted',?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	h, _, err = s.ReadStatus(ctx, "status")
	if err != nil || h.Messages != 0 || h.Conversations != 0 || !h.LastAt.IsZero() {
		t.Fatalf("tombstone counted: %+v %v", h, err)
	}
	if _, _, err := s.ReadStatus(ctx, "other"); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("cross-account read: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s.ReadStatus(cancelled, "status"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}
