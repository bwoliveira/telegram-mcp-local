package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// The existing single-peer read is the semantic oracle; never derive expected
// coverage from the new query's predicates alone.
func assertConversationCoverage(t *testing.T, s *Store) []ConversationCoverage {
	t.Helper()
	ctx := context.Background()
	rows, err := s.ReadConversationCoverage(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	dialogs, err := s.ReadDialogs(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	peers, err := s.ReadPeers(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	names := map[engine.PeerRef]string{}
	for _, p := range peers {
		name := p.Title
		if name == "" {
			name = p.FirstName + " " + p.LastName
		}
		names[p.Ref] = name
	}
	if len(rows) != len(dialogs) {
		t.Fatalf("batch rows=%d dialogs=%d", len(rows), len(dialogs))
	}
	byPeer := map[engine.PeerRef]ConversationCoverage{}
	for _, r := range rows {
		if _, ok := byPeer[r.Peer]; ok {
			t.Fatal("duplicate peer")
		}
		byPeer[r.Peer] = r
	}
	for _, d := range dialogs {
		complete, oldest, err := s.ReadCoverage(ctx, "primary", &d.Peer)
		if err != nil {
			t.Fatal(err)
		}
		want := ConversationCoverage{Peer: d.Peer, Name: names[d.Peer], Complete: complete, Oldest: oldest}
		if got, ok := byPeer[d.Peer]; !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("batch=%+v oracle=%+v present=%v", got, want, ok)
		}
	}
	return rows
}

func TestReadConversationCoverageDifferential(t *testing.T) {
	s := queryStore(t)
	if rows := assertConversationCoverage(t, s); len(rows) != 0 {
		t.Fatal("empty store is nonempty")
	}
	for _, p := range []struct{ typ, kind string }{{"user", "user"}, {"chat", "group"}, {"channel", "channel"}} {
		queryPeer(t, s, p.typ, 1, p.kind)
	}
	querySQL(t, s, `UPDATE peers SET first_name='First',last_name='Last' WHERE peer_type='user'`)
	querySQL(t, s, `UPDATE peers SET title='Group title' WHERE peer_type='chat'`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,unread_mark,refreshed_at) SELECT account,peer_type,peer_id,0,0,1 FROM peers`)
	assertConversationCoverage(t, s) // missing cursors
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) SELECT account,peer_type,peer_id,CASE peer_type WHEN 'user' THEN 0 WHEN 'chat' THEN 1000 ELSE 2000 END,1,1000 FROM peers`)
	rows := assertConversationCoverage(t, s)
	for _, row := range rows {
		if !row.Complete || row.Oldest == nil {
			t.Fatal("complete proof/date lost")
		}
	}
	for _, condition := range []string{"complete=0", "state='running'", "state='error'", "state='no_access'", "state='flood_wait'", "last_error='failed'", "flood_until=3000", "oldest_date=NULL"} {
		querySQL(t, s, `UPDATE backfill_cursors SET `+condition+` WHERE peer_type='channel'`)
		assertConversationCoverage(t, s)
		querySQL(t, s, `UPDATE backfill_cursors SET complete=1,state='idle',last_error='',flood_until=NULL,oldest_date=2000 WHERE peer_type='channel'`)
	}
	for _, access := range []string{"left", "kicked", "banned", "lost", "deleted"} {
		querySQL(t, s, `UPDATE peers SET access=? WHERE peer_type='channel'`, access)
		assertConversationCoverage(t, s)
	}
	querySQL(t, s, `UPDATE peers SET access='ok'`)
	// Unrelated peer cache entries/gaps must not change a dialog's coverage.
	queryPeer(t, s, "channel", 99, "channel")
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('primary','channel',99,'access_lost',1000)`)
	assertConversationCoverage(t, s)
	for _, reason := range []string{"access_lost", "channel_too_long", "state_lost"} {
		querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('primary','channel',1,?,1000)`, reason)
		assertConversationCoverage(t, s)
	}
	querySQL(t, s, `UPDATE gaps SET filled_at=1800`)
	assertConversationCoverage(t, s)
	querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES('primary','difference_too_long',1800)`)
	for _, r := range assertConversationCoverage(t, s) {
		if r.Complete {
			t.Fatal("global gap ignored")
		}
	}
	querySQL(t, s, `UPDATE gaps SET filled_at=1900`)
	querySQL(t, s, `INSERT INTO updates_state(account,pts,qts,date,seq,held,held_reason,updated_at) VALUES('primary',1,0,1000,1,1,'test hold',1000)`)
	for _, r := range assertConversationCoverage(t, s) {
		if r.Complete {
			t.Fatal("durable hold ignored")
		}
	}
	querySQL(t, s, `UPDATE updates_state SET held=0,held_reason=''`)
	assertConversationCoverage(t, s)
	// Other account proof/gaps cannot contaminate this account.
	querySQL(t, s, `INSERT INTO accounts(account,updated_at) VALUES('other',1000)`)
	querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES('other','state_lost',1000)`)
	querySQL(t, s, `INSERT INTO updates_state(account,pts,qts,date,seq,held,held_reason,updated_at) VALUES('other',1,0,1000,1,1,'test hold',1000)`)
	rows = assertConversationCoverage(t, s)
	for _, r := range rows {
		if !r.Complete {
			t.Fatal("other account evidence leaked")
		}
	}
	// Returned pointers must not alias future reads or one another.
	*rows[0].Oldest = time.Time{}
	assertConversationCoverage(t, s)
}

func TestReadConversationCoverageAccountContextAndStorageErrors(t *testing.T) {
	s := queryStore(t)
	for _, account := range []engine.AccountID{"", "other"} {
		if rows, err := s.ReadConversationCoverage(context.Background(), account); !errors.Is(err, engine.ErrAccountMismatch) || rows != nil {
			t.Fatalf("account guard: rows=%v err=%v", rows, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, account := range []engine.AccountID{"primary", "other"} {
		if rows, err := s.ReadConversationCoverage(ctx, account); !errors.Is(err, context.Canceled) || rows != nil {
			t.Fatalf("context guard: rows=%v err=%v", rows, err)
		}
	}
	s.reader.SetMaxOpenConns(1)
	conn, err := s.reader.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if rows, err := s.ReadConversationCoverage(ctx, "primary"); !errors.Is(err, context.DeadlineExceeded) || rows != nil {
		t.Fatalf("blocked query: rows=%v err=%v", rows, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ReadConversationCoverage(context.Background(), "primary"); err == nil || rows != nil {
		t.Fatalf("closed store: rows=%v err=%v", rows, err)
	}
}

func TestReadConversationCoverageGroupedQueryPlan(t *testing.T) {
	s := queryStore(t)
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<110) INSERT INTO peers(account,peer_type,peer_id,kind,access,updated_at) SELECT 'primary','user',i,'user','ok',1 FROM n`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,unread_mark,refreshed_at) SELECT account,peer_type,peer_id,0,0,1 FROM peers`)
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) SELECT account,peer_type,peer_id,1,1,1000 FROM peers`)
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) SELECT account,peer_type,peer_id,'state_lost',1000 FROM peers WHERE peer_id%2=0`)
	got := assertConversationCoverage(t, s)
	if len(got) != 110 {
		t.Fatalf("rows=%d", len(got))
	}
	rows, err := s.reader.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+panelStatusCoverageQuery, "primary")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var grouped bool
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Logf("coverage plan: %s", detail)
		if strings.Contains(detail, "CORRELATED") {
			t.Errorf("per-peer correlated query: %s", detail)
		}
		if strings.Contains(detail, "MATERIALIZE peer_gaps") {
			grouped = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !grouped {
		t.Fatal("peer gaps not materialized once")
	}
}
