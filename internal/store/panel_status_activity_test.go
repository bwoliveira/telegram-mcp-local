package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
)

// activityFromMessages mirrors the MCP fallback, including its second TTL check
// against the caller's clock after ReadMessages applied the store's clock.
func activityFromMessages(t *testing.T, s *Store, now time.Time) index.Activity {
	t.Helper()
	messages, err := s.ReadMessages(context.Background(), "primary")
	if err != nil {
		t.Fatal(err)
	}
	var activity index.Activity
	for _, message := range messages {
		if message.ExpiresAt != nil && !message.ExpiresAt.After(now) {
			continue
		}
		if message.TTLPeriod > 0 && !message.Date.Add(time.Duration(message.TTLPeriod)*time.Second).After(now) {
			continue
		}
		date := message.Date
		if activity.Oldest == nil || date.Before(*activity.Oldest) {
			activity.Oldest = &date
		}
		if activity.NewestAny == nil || date.After(*activity.NewestAny) {
			activity.NewestAny = &date
		}
		if !message.Out && (activity.NewestIncoming == nil || date.After(*activity.NewestIncoming)) {
			activity.NewestIncoming = &date
		}
		if !date.Before(now.Add(-time.Hour)) && !date.After(now) {
			activity.LastHour++
		}
		if !date.Before(now.Add(-24*time.Hour)) && !date.After(now) {
			activity.LastDay++
		}
	}
	return activity
}

func assertActivityMatchesMessages(t *testing.T, s *Store, now time.Time) index.Activity {
	t.Helper()
	want := activityFromMessages(t, s, now)
	got, err := s.ReadActivity(context.Background(), "primary", now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("activity = %+v; fallback = %+v", got, want)
	}
	return got
}

func TestReadActivityDatesMatchMessages(t *testing.T) {
	s := queryStore(t)
	now := time.Unix(200000, 0).UTC()
	if got := assertActivityMatchesMessages(t, s, now); !reflect.DeepEqual(got, index.Activity{}) {
		t.Fatalf("empty activity = %+v", got)
	}
	queryPeer(t, s, "user", 1, "user")
	for i, date := range []int{0, 113599, 113600, 113601, 196399, 196400, 196401, 200000, 200001, 200002} {
		queryMsg(t, s, "user", 1, i+1, date, "fixture")
	}
	querySQL(t, s, `UPDATE messages SET out=1 WHERE msg_id=10`)
	got := assertActivityMatchesMessages(t, s, now)
	if got.LastHour != 3 || got.LastDay != 6 || got.Oldest == nil || got.Oldest.Unix() != 0 || got.NewestAny == nil || got.NewestAny.Unix() != 200002 || got.NewestIncoming == nil || got.NewestIncoming.Unix() != 200001 {
		t.Fatalf("date boundaries/future/incoming = %+v", got)
	}
	// SQLite dates are whole seconds; fractional lower bounds must round up.
	assertActivityMatchesMessages(t, s, now.Add(time.Nanosecond))
	assertActivityMatchesMessages(t, s, now.Add(-time.Nanosecond))
	querySQL(t, s, `UPDATE messages SET out=1`)
	if got := assertActivityMatchesMessages(t, s, now); got.NewestIncoming != nil {
		t.Fatalf("outgoing-only incoming = %v", got.NewestIncoming)
	}
	querySQL(t, s, `DELETE FROM messages WHERE date<=200000`)
	got = assertActivityMatchesMessages(t, s, now)
	if got.LastHour != 0 || got.LastDay != 0 || got.Oldest == nil || got.Oldest.Unix() != 200001 {
		t.Fatalf("future-only activity = %+v", got)
	}
}

func TestReadActivityVisibilityMatchesMessages(t *testing.T) {
	s := queryStore(t)
	for _, peer := range []struct{ typ, kind string }{{"user", "user"}, {"chat", "group"}, {"channel", "channel"}} {
		queryPeer(t, s, peer.typ, 1, peer.kind)
		for id := 1; id <= 12; id++ {
			queryMsg(t, s, peer.typ, 1, id, 1900+id, "fixture")
		}
	}
	// Each TTL representation is authoritative independently, even when the
	// other is absent or disagrees. Expiry must not depend on a janitor.
	querySQL(t, s, `UPDATE messages SET expires_at=2000 WHERE msg_id=1`)
	querySQL(t, s, `UPDATE messages SET ttl_period=98 WHERE msg_id=2`)
	querySQL(t, s, `UPDATE messages SET ttl_period=97,expires_at=2200 WHERE msg_id=3`)
	querySQL(t, s, `UPDATE messages SET ttl_period=500,expires_at=2000 WHERE msg_id=4`)
	querySQL(t, s, `UPDATE messages SET expires_at=2001 WHERE msg_id=5`)
	querySQL(t, s, `UPDATE messages SET ttl_period=95 WHERE msg_id=6`)
	querySQL(t, s, `UPDATE messages SET ttl_period=-1,media_ttl=1 WHERE msg_id=7`)
	querySQL(t, s, `INSERT INTO message_tombstones VALUES('primary','user',1,8,'deleted',1900)`)
	querySQL(t, s, `INSERT INTO pending_deletes VALUES('primary',9,1900)`)
	// Same numeric message IDs in other accounts/typed peers must not leak or
	// turn account-scoped and typed deletion records into global deletions.
	querySQL(t, s, `INSERT INTO accounts(account,updated_at) VALUES('other',1000)`)
	querySQL(t, s, `INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES('other','user',1,'user',1000)`)
	querySQL(t, s, `INSERT INTO messages(account,peer_type,peer_id,msg_id,date,source,first_seen_at,updated_at) VALUES('other','user',1,1,999999,'update',1000,1000)`)
	querySQL(t, s, `INSERT INTO pending_deletes VALUES('other',10,1900)`)
	querySQL(t, s, `INSERT INTO message_tombstones VALUES('other','channel',1,11,'deleted',1900)`)
	for _, storeNow := range []int64{1999, 2000, 2001, 2100} {
		s.now = func() time.Time { return time.Unix(storeNow, 500000000).UTC() }
		for _, requested := range []time.Time{
			time.Unix(1999, 0).UTC(), time.Unix(2000, 0).UTC(),
			time.Unix(2000, 1).UTC(), time.Unix(2001, 0).UTC(),
			time.Unix(2100, 0).UTC(),
		} {
			assertActivityMatchesMessages(t, s, requested)
		}
	}
	// The surviving media TTL row is a message, not expired content.
	s.now = func() time.Time { return time.Unix(2000, 0).UTC() }
	got := assertActivityMatchesMessages(t, s, time.Unix(2000, 0).UTC())
	if got.LastHour != 21 || got.NewestAny == nil || got.NewestAny.Unix() != 1912 {
		t.Fatalf("visibility/deletion/account isolation = %+v", got)
	}
	querySQL(t, s, `UPDATE peers SET access='lost' WHERE account='primary'`)
	if err := s.SetAuthState(context.Background(), "primary", engine.AuthRevoked, "offline test"); err != nil {
		t.Fatal(err)
	}
	assertActivityMatchesMessages(t, s, time.Unix(2000, 0).UTC())
	querySQL(t, s, `UPDATE messages SET expires_at=0 WHERE account='primary'`)
	if got := assertActivityMatchesMessages(t, s, time.Unix(2000, 0).UTC()); !reflect.DeepEqual(got, index.Activity{}) {
		t.Fatalf("all filtered activity = %+v", got)
	}
	querySQL(t, s, `DELETE FROM messages WHERE account='primary'`)
	if got := assertActivityMatchesMessages(t, s, time.Unix(2000, 0).UTC()); !reflect.DeepEqual(got, index.Activity{}) {
		t.Fatalf("deleted all activity = %+v", got)
	}
}

func TestReadActivityAccountContextAndStorageErrors(t *testing.T) {
	s := queryStore(t)
	now := time.Unix(2000, 0).UTC()
	for _, account := range []engine.AccountID{"", "other"} {
		got, err := s.ReadActivity(context.Background(), account, now)
		if !errors.Is(err, engine.ErrAccountMismatch) || !reflect.DeepEqual(got, index.Activity{}) {
			t.Fatalf("account %q: activity=%+v err=%v", account, got, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, finish := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer finish()
	for _, ctx := range []context.Context{canceled, expired} {
		for _, account := range []engine.AccountID{"primary", "other"} {
			got, err := s.ReadActivity(ctx, account, now)
			_, fallbackErr := s.ReadMessages(ctx, account)
			if !errors.Is(err, ctx.Err()) || !errors.Is(err, fallbackErr) || !reflect.DeepEqual(got, index.Activity{}) {
				t.Fatalf("context: activity=%+v err=%v fallback=%v", got, err, fallbackErr)
			}
		}
	}
	// Cancellation must still reach SQL after the method's initial guard.
	s.reader.SetMaxOpenConns(1)
	conn, err := s.reader.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if got, err := s.ReadActivity(ctx, "primary", now); !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, index.Activity{}) {
		t.Fatalf("blocked query: activity=%+v err=%v", got, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadActivity(context.Background(), "primary", now); err == nil || !reflect.DeepEqual(got, index.Activity{}) {
		t.Fatalf("closed store: activity=%+v err=%v", got, err)
	}
}

func TestReadActivityQueryPlanAndLargeFixture(t *testing.T) {
	s := queryStore(t)
	// Synthetic local-only fixture: 25,000 messages spread across 110 peers.
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<110)
	 INSERT INTO peers(account,peer_type,peer_id,kind,updated_at)
	 SELECT 'primary',CASE WHEN i%2=0 THEN 'channel' ELSE 'user' END,i,
	 CASE WHEN i%2=0 THEN 'channel' ELSE 'user' END,1000 FROM n`)
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<25000)
	 INSERT INTO messages(account,peer_type,peer_id,msg_id,date,out,text,ttl_period,expires_at,source,first_seen_at,updated_at)
	 SELECT 'primary',CASE WHEN ((i-1)%110+1)%2=0 THEN 'channel' ELSE 'user' END,
	 (i-1)%110+1,i,100000+i,i%3=0,'synthetic activity fixture',
	 CASE WHEN i%17=0 THEN 100 ELSE 0 END,
	 CASE WHEN i%19=0 THEN 120000 END,'update',1000,1000 FROM n`)
	querySQL(t, s, `INSERT INTO message_tombstones(account,peer_type,peer_id,msg_id,reason,deleted_at)
	 SELECT account,peer_type,peer_id,msg_id,'deleted',1000 FROM messages WHERE msg_id%97=0`)
	querySQL(t, s, `INSERT INTO pending_deletes(account,msg_id,received_at)
	 SELECT account,msg_id,1000 FROM messages WHERE msg_id%89=0`)
	var messages, peers int
	if err := s.reader.QueryRowContext(context.Background(), `SELECT
	 (SELECT count(*) FROM messages WHERE account='primary'),
	 (SELECT count(*) FROM peers WHERE account='primary')`).Scan(&messages, &peers); err != nil {
		t.Fatal(err)
	}
	if messages != 25000 || peers != 110 {
		t.Fatalf("fixture: messages=%d peers=%d", messages, peers)
	}
	now := time.Unix(122000, 0).UTC()
	s.now = func() time.Time { return now }
	got := assertActivityMatchesMessages(t, s, now)
	if got.LastDay == 0 || got.NewestAny == nil || !got.NewestAny.After(now) {
		t.Fatalf("fixture did not exercise counts and future extrema: %+v", got)
	}
	rows, err := s.reader.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+panelStatusActivityQuery,
		now.Add(-time.Hour).Unix(), now.Unix(), now.Add(-24*time.Hour).Unix(), now.Unix(), "primary", now.Unix(), now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Logf("activity query plan: %s", detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Log rather than assert wall-clock latency: correctness tests must remain
	// stable on loaded/race-instrumented CI. The parent panel measures its SLA.
	var total, slowest time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		activity, err := s.ReadActivity(context.Background(), "primary", now)
		elapsed := time.Since(start)
		if err != nil || !reflect.DeepEqual(activity, got) {
			t.Fatalf("repeat activity=%+v err=%v", activity, err)
		}
		total += elapsed
		if elapsed > slowest {
			slowest = elapsed
		}
	}
	t.Logf("ReadActivity 25000 messages/110 peers: mean=%s slowest=%s (5 calls)", total/5, slowest)
}

var _ interface {
	ReadActivity(context.Context, engine.AccountID, time.Time) (index.Activity, error)
} = (*Store)(nil)
