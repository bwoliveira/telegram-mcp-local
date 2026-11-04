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

func TestAccessLossSummarySurvivesSyncCyclesAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	now := confTime
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	var latest int64
	for restart := 0; restart < 3; restart++ {
		s, err := Open(ctx, path, confAccount, WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		for cycle := 0; cycle < 4; cycle++ {
			// Dialog metadata alternates with inaccessible callbacks even without a restart.
			confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Access: engine.AccessLeft}}}})
			now = now.Add(time.Second)
			confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLost}}})
			now = now.Add(time.Second)
			confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessOK}}})
			now = now.Add(time.Second)
			latest = now.Unix()
			confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLost}}, Gaps: []engine.GapMark{{Peer: &peer, Reason: engine.GapAccessLost, Detected: now}}})
			if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE reason='access_lost' AND filled_at IS NULL`); n != 1 {
				t.Errorf("restart=%d cycle=%d: terminal rows=%d; want 1", restart, cycle, n)
			}
			if got := confCount(t, s, `SELECT max(detected_at) FROM gaps WHERE reason='access_lost'`); got != latest {
				t.Errorf("latest detection=%d; want %d", got, latest)
			}
			now = now.Add(time.Minute)
		}
		// A later callback updates the summary even with no access-state change.
		latest = now.Unix()
		confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{{Peer: &peer, Reason: engine.GapAccessLost, Detected: now}}})
		// Late delivery must not move the evidence backwards.
		confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{{Peer: &peer, Reason: engine.GapAccessLost, Detected: confTime}}})
		if got := confCount(t, s, `SELECT max(detected_at) FROM gaps WHERE reason='access_lost'`); got != latest {
			t.Errorf("latest callback=%d; want %d", got, latest)
		}
		if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NOT NULL`); n != 0 {
			t.Errorf("invented coverage proof: %d", n)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAccessLossExactReplayDoesNotWrite(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	b := engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLost}}, Gaps: []engine.GapMark{{Peer: &peer, Reason: engine.GapAccessLost, Detected: confTime}}}
	confApply(t, s, b)
	var before, after int64
	if err := s.writer.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	confNoEvents(t, confApply(t, s, b))
	if err := s.writer.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("exact replay wrote %d rows", after-before)
	}
}

func TestAccessLossRepeatedAccessObservationRefreshesSummary(t *testing.T) {
	for _, state := range []engine.AccessState{engine.AccessLost, engine.AccessLeft, engine.AccessKicked} {
		t.Run(string(state), func(t *testing.T) {
			now := confTime
			s := confOpen(t, WithClock(func() time.Time { return now }))
			peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
			batch := engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: state}}}
			confApply(t, s, batch)
			now = now.Add(time.Minute)
			confNoEvents(t, confApply(t, s, batch))
			if got := confCount(t, s, `SELECT detected_at FROM gaps WHERE reason='access_lost'`); got != now.Unix() {
				t.Errorf("latest access-only observation=%d; want %d", got, now.Unix())
			}
			now = now.Add(time.Minute)
			confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Access: state}}}}))
			if got := confCount(t, s, `SELECT detected_at FROM gaps WHERE reason='access_lost'`); got != now.Unix() {
				t.Errorf("latest metadata observation=%d; want %d", got, now.Unix())
			}
		})
	}
}

func TestAccessLossSummaryRollbackAndStaleFill(t *testing.T) {
	ctx := context.Background()
	interrupted := errors.New("synthetic summary interruption")
	fail := false
	s := confOpen(t, FaultAt(func(stage string) error {
		if fail && stage == "apply_before_commit" {
			return interrupted
		}
		return nil
	}))
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	mark := engine.GapMark{Peer: &peer, Reason: engine.GapAccessLost, Detected: confTime}
	confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{mark}})
	later := mark
	later.Detected = confTime.Add(time.Minute)
	fail = true
	if _, err := s.Apply(ctx, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{later}, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLeft}}}); !errors.Is(err, interrupted) {
		t.Fatalf("apply error=%v", err)
	}
	if got := confCount(t, s, `SELECT detected_at FROM gaps`); got != confTime.Unix() {
		t.Errorf("failed transaction advanced summary=%d", got)
	}
	if got := confCount(t, s, `SELECT count(*) FROM peers WHERE access<>'ok'`); got != 0 {
		t.Errorf("failed transaction changed access: %d", got)
	}
	fail = false
	confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{later}})
	confApply(t, s, engine.Batch{Account: confAccount, FilledGaps: []engine.GapMark{mark}})
	if got := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NULL`); got != 1 {
		t.Errorf("stale proof closed newer loss: %d", got)
	}
	confApply(t, s, engine.Batch{Account: confAccount, FilledGaps: []engine.GapMark{later}})
	confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{later}})
	if got := confCount(t, s, `SELECT count(*) FROM gaps`); got != 1 {
		t.Errorf("exact filled replay reopened evidence: %d", got)
	}
	later.Detected = later.Detected.Add(time.Minute)
	confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{later}})
	if got := confCount(t, s, `SELECT count(*) FROM gaps`); got != 2 {
		t.Errorf("new loss after explicit proof suppressed: %d", got)
	}
	if got := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NULL`); got != 1 {
		t.Errorf("new terminal summary=%d", got)
	}
}

func TestAccessLossMigrationV9CompactsAtomically(t *testing.T) {
	ctx := context.Background()
	path, key := reactionV5Fixture(t)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &Store{writer: raw}
	for _, migrate := range []func(context.Context) error{fixture.migrateV6, fixture.migrateV7, fixture.migrateV8} {
		if err := migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Equal IDs across peer types/accounts, NULL account scope, filled evidence,
	// and other causes must remain independent. Largest id is not latest time.
	_, err = raw.Exec(`INSERT INTO gaps(id,account,peer_type,peer_id,reason,detected_at,filled_at) VALUES
 (1,'store-conformance','channel',6,'access_lost',100,NULL),
 (2,'store-conformance','channel',6,'access_lost',300,NULL),
 (3,'store-conformance','channel',6,'access_lost',200,NULL),
 (4,'store-conformance','channel',6,'access_lost',50,60),
 (5,'store-conformance','chat',6,'access_lost',100,NULL),
 (6,'other','channel',6,'access_lost',100,NULL),
 (7,'store-conformance',NULL,NULL,'access_lost',100,NULL),
 (8,'store-conformance',NULL,NULL,'access_lost',200,NULL),
 (9,'store-conformance','channel',6,'channel_too_long',100,NULL),
 (10,'store-conformance','channel',6,'state_lost',100,NULL),
 (11,'store-conformance','channel',6,'access_lost',300,NULL);
 CREATE TABLE original_gaps AS SELECT * FROM gaps;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("synthetic v9 interruption")
	s, err := Open(ctx, path, confAccount, FaultAt(func(stage string) error {
		if stage == "migration_v9_before_commit" {
			return interrupted
		}
		return nil
	}))
	if s != nil {
		s.Close()
	}
	if !errors.Is(err, interrupted) {
		t.Fatalf("migration error=%v; want transactional rollback", err)
	}
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, changed, indexes int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM (SELECT * FROM original_gaps EXCEPT SELECT * FROM gaps)`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='gaps_access_lost_terminal'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if version != 8 || changed != 0 || indexes != 0 {
		t.Fatalf("partial migration: version=%d deleted=%d indexes=%d", version, changed, indexes)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		s, err = Open(ctx, path, confAccount, FaultAt(func(stage string) error {
			if reopen > 0 && stage == "migration_v9_before_commit" {
				return errors.New("v9 reran")
			}
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if n := confCount(t, s, `SELECT count(*) FROM gaps`); n != 7 {
			t.Errorf("compacted rows=%d; want 7", n)
		}
		if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE id IN (4,5,6,8,9,10,11)`); n != 7 {
			t.Errorf("wrong rows retained=%d", n)
		}
		if got := confGet(t, s, key); got.Text != "synthetic v5" {
			t.Errorf("message changed: %q", got.Text)
		}
		if n := confCount(t, s, `PRAGMA user_version`); n != 9 {
			t.Errorf("schema version=%d; want 9", n)
		}
		// A database constraint, not just a process-local check, bounds future growth.
		for _, scope := range []string{"'channel',6", "NULL,NULL"} {
			if _, err := s.writer.Exec(`INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('store-conformance',` + scope + `,'access_lost',400)`); err == nil {
				t.Errorf("duplicate terminal scope %s accepted", scope)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
