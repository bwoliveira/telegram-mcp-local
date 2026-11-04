package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestForumTopicMigrationV2AtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	now := time.Unix(2000, 0)
	st, err := Open(ctx, path, "primary", WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	p := engine.PeerRef{Type: engine.PeerChannel, ID: 42}
	other := engine.PeerRef{Type: engine.PeerChannel, ID: 43}
	mk := func(id, topic, reply, top int, peer engine.PeerRef) engine.Message {
		m := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: id}, Date: time.Unix(int64(1000+id), 0), Text: "preserve content", TopicID: topic, Mentioned: true}
		if reply > 0 {
			m.ReplyTo = &engine.ReplyRef{ID: reply, TopID: top}
		}
		return m
	}
	batch := engine.Batch{Account: "primary", Source: engine.SourceBackfill, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, Kind: engine.KindSupergroup, Forum: true}}, {Peer: engine.Peer{Ref: other, Kind: engine.KindSupergroup}}}, Messages: []engine.Message{
		mk(101, 0, 100, 0, p), mk(102, 0, 101, 100, p), mk(103, 0, 102, 0, p), mk(201, 0, 0, 0, p), mk(202, 0, 201, 0, p), mk(301, 77, 300, 0, p), mk(401, 0, 400, 399, other),
	}}
	if _, err = st.Apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	// Downgrade only the fixture to the actual v1 persisted representation.
	if _, err = st.writer.Exec(`UPDATE messages SET topic_id=0 WHERE msg_id<>301; UPDATE mentions SET topic_id=0,answered_at=1234,answered_by=999; PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	injected := errors.New("migration interrupted")
	failed, err := Open(ctx, path, "primary", WithClock(func() time.Time { return now }), FaultAt(func(stage string) error {
		if stage == "migration_v2_before_commit" {
			return injected
		}
		return nil
	}))
	if failed != nil {
		failed.Close()
	}
	if !errors.Is(err, injected) {
		t.Fatalf("migration fault=%v; want injected rollback", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, topic int
	if err = raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow(`SELECT topic_id FROM messages WHERE msg_id=101`).Scan(&topic); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if version != 1 || topic != 0 {
		t.Fatalf("partial migration: version=%d topic=%d", version, topic)
	}
	for restart := 0; restart < 2; restart++ {
		st, err = Open(ctx, path, "primary", WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		if err = st.reader.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v; want %d", version, err, currentSchemaVersion)
		}
		wants := map[int]int{101: 100, 102: 100, 103: 100, 201: 1, 202: 1, 301: 77, 401: 0}
		messages, err := st.ReadMessages(ctx, "primary")
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != len(wants) {
			t.Fatalf("message count=%d", len(messages))
		}
		for _, m := range messages {
			if m.TopicID != wants[m.Key.ID] || m.Text != "preserve content" {
				t.Errorf("migration message=%+v; want topic=%d", m, wants[m.Key.ID])
			}
		}
		var stale int
		if err = st.reader.QueryRow(`SELECT count(*) FROM mentions n JOIN messages m ON n.row_id=m.row_id WHERE n.topic_id<>m.topic_id OR n.answered_by IS NOT NULL`).Scan(&stale); err != nil || stale != 0 {
			t.Errorf("stale mention rows=%d err=%v", stale, err)
		}
		st.Close()
	}
}
