package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // Keep IANA/DST fixtures portable to hosts without zoneinfo.

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func statsFixture(t *testing.T) *Store {
	t.Helper()
	s := queryStore(t)
	queryPeer(t, s, "user", 1, "user")
	queryPeer(t, s, "chat", 1, "group")
	queryPeer(t, s, "channel", 1, "channel")
	queryPeer(t, s, "user", 9, "user")
	querySQL(t, s, `UPDATE peers SET title='Alice' WHERE peer_type='user' AND peer_id=9`)
	queryMsg(t, s, "user", 1, 1, 1000, "first")
	queryMsg(t, s, "user", 1, 2, 1100, "photo")
	queryMsg(t, s, "user", 1, 3, 1200, "outgoing")
	queryMsg(t, s, "chat", 1, 1, 1000, "group")
	queryMsg(t, s, "channel", 1, 1, 1300, "channel")
	querySQL(t, s, `UPDATE messages SET sender_type='user',sender_id=9,sender_name='stale name'`)
	querySQL(t, s, `UPDATE messages SET out=1,media_type='',sender_type=NULL,sender_id=NULL WHERE peer_type='user' AND msg_id=3`)
	querySQL(t, s, `UPDATE messages SET media_type='photo',media_json='{"type":"photo","downloadable":true}',media_remote=x'010203' WHERE peer_type='user' AND msg_id=2`)
	return s
}

func TestStatsMessagesSQLTotalsNamespacesAndNames(t *testing.T) {
	s := statsFixture(t)
	ctx := context.Background()
	r, err := s.StatsMessages(ctx, "primary", StatsFilter{}, "chat", 1)
	if err != nil || r.Total != 5 || r.Groups != 3 || len(r.Buckets) != 1 {
		t.Fatalf("stats: %+v %v", r, err)
	}
	b := r.Buckets[0]
	if b.Key != "user:1" || b.Name != "user" || b.Count != 3 || b.First.Unix() != 1000 || b.Last.Unix() != 1200 {
		t.Fatalf("bucket: %+v", b)
	}
	r, err = s.StatsMessages(ctx, "primary", StatsFilter{}, "sender", 0)
	if err != nil || r.Total != 5 || r.Groups != 2 || len(r.Buckets) != 2 || r.Buckets[0].Key != "user:9" || r.Buckets[0].Name != "Alice" || r.Buckets[0].Count != 4 || r.Buckets[1].Key != "me" || r.Buckets[1].Name != "the account itself" {
		t.Fatalf("senders: %+v %v", r, err)
	}
	querySQL(t, s, `UPDATE messages SET sender_type=NULL,sender_id=NULL WHERE peer_type='channel'`)
	r, err = s.StatsMessages(ctx, "primary", StatsFilter{}, "sender", 50)
	if err != nil || r.Groups != 3 || r.Total != 5 {
		t.Fatalf("anonymous: %+v %v", r, err)
	}
	found := false
	for _, b := range r.Buckets {
		if b.Key == "" && b.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing anonymous sender: %+v", r)
	}
	// Aggregation must not decode arbitrary message JSON, nor return message bodies.
	querySQL(t, s, `UPDATE messages SET media_json='invalid-json'`)
	if _, err = s.StatsMessages(ctx, "primary", StatsFilter{}, "chat", 50); err != nil {
		t.Fatal(err)
	}
}

func TestStatsMessagesAndExportFilterParity(t *testing.T) {
	s := statsFixture(t)
	p := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	cases := []struct {
		name   string
		filter StatsFilter
		count  int64
	}{
		{"all", StatsFilter{}, 5},
		{"typed peer", StatsFilter{Peer: &p}, 3},
		{"bounds", StatsFilter{Since: time.Unix(1100, 0), Until: time.Unix(1300, 0)}, 2},
		{"in", StatsFilter{Direction: "in"}, 4},
		{"out", StatsFilter{Direction: "out"}, 1},
		{"text", StatsFilter{MediaType: "text"}, 4},
		{"any", StatsFilter{MediaType: "any"}, 1},
		{"exact", StatsFilter{MediaType: "photo"}, 1},
		{"literal media", StatsFilter{MediaType: "photo' OR 1=1 --"}, 0},
		{"groups", StatsFilter{ExcludeGroups: true}, 3},
		{"combined", StatsFilter{Peer: &p, ExcludeGroups: true, Direction: "in", MediaType: "any", Since: time.Unix(1100, 0), Until: time.Unix(1200, 0)}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := s.StatsMessages(context.Background(), "primary", tc.filter, "chat", 50)
			if err != nil || r.Total != tc.count {
				t.Fatalf("stats: %+v %v want %d", r, err, tc.count)
			}
			var count int64
			err = s.StreamExportMessages(context.Background(), "primary", tc.filter, func(engine.Message) error { count++; return nil })
			if err != nil || count != tc.count {
				t.Fatalf("export: %d %v want %d", count, err, tc.count)
			}
		})
	}
}

func TestStatsLocalTimeCalendarAndDefaultLimit(t *testing.T) {
	s := queryStore(t)
	queryPeer(t, s, "user", 1, "user")
	old := time.Local
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	// Same local day across spring DST; UTC month boundary is still February locally.
	dates := []string{"2024-03-01T00:30:00Z", "2024-03-10T05:30:00Z", "2024-03-11T03:30:00Z", "2024-03-11T04:30:00Z"}
	for i, d := range dates {
		dt, e := time.Parse(time.RFC3339, d)
		if e != nil {
			t.Fatal(e)
		}
		queryMsg(t, s, "user", 1, i+1, int(dt.Unix()), d)
	}
	r, err := s.StatsMessages(context.Background(), "primary", StatsFilter{}, "day", 2)
	if err != nil || r.Total != 4 || r.Groups != 3 || len(r.Buckets) != 2 || r.Buckets[0].Key != "2024-03-11" || r.Buckets[1].Key != "2024-03-10" || r.Buckets[1].Count != 2 {
		t.Fatalf("days: %+v %v", r, err)
	}
	r, err = s.StatsMessages(context.Background(), "primary", StatsFilter{}, "month", 0)
	if err != nil || r.Groups != 2 || len(r.Buckets) != 2 || r.Buckets[0].Key != "2024-03" || r.Buckets[0].Count != 3 || r.Buckets[1].Key != "2024-02" {
		t.Fatalf("months: %+v %v", r, err)
	}
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(2) UNION ALL SELECT i+1 FROM n WHERE i<60) INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) SELECT 'primary','user',i,'user',1000 FROM n`)
	querySQL(t, s, `INSERT INTO messages(account,peer_type,peer_id,msg_id,date,source,first_seen_at,updated_at) SELECT account,peer_type,peer_id,1,1000,'update',1000,1000 FROM peers WHERE peer_id>1`)
	r, err = s.StatsMessages(context.Background(), "primary", StatsFilter{}, "", 0)
	if err != nil || len(r.Buckets) != 50 || r.Groups != 60 || r.Total != 63 {
		t.Fatalf("default limit: %+v %v", r, err)
	}
}

func TestStatsVisibilityAndExportLifecycle(t *testing.T) {
	s := queryStore(t)
	queryPeer(t, s, "user", 1, "user")
	queryPeer(t, s, "channel", 1, "channel")
	for i := 1; i <= 10; i++ {
		queryMsg(t, s, "user", 1, i, 1000+i, "text")
	}
	queryMsg(t, s, "channel", 1, 10, 1010, "channel with pending ID")
	querySQL(t, s, `UPDATE messages SET ttl_period=2000 WHERE peer_type='user' AND msg_id=2`)
	querySQL(t, s, `UPDATE messages SET expires_at=3000 WHERE peer_type='user' AND msg_id=3`)
	querySQL(t, s, `UPDATE messages SET media_ttl=30 WHERE peer_type='user' AND msg_id=4`)
	querySQL(t, s, `UPDATE messages SET expires_at=1999 WHERE peer_type='user' AND msg_id=5`)
	querySQL(t, s, `UPDATE messages SET ttl_period=10 WHERE peer_type='user' AND msg_id=6`)
	querySQL(t, s, `INSERT INTO message_tombstones(account,peer_type,peer_id,msg_id,reason,deleted_at) VALUES('primary','user',1,7,'deleted',1500)`)
	querySQL(t, s, `INSERT INTO pending_deletes(account,msg_id,received_at) VALUES('primary',10,1500)`)
	querySQL(t, s, `UPDATE messages SET protected=1,media_type='photo',media_json='{"type":"photo","filename":"secret.jpg","downloadable":true}',media_remote=x'010203' WHERE peer_type='user' AND msg_id=8`)
	querySQL(t, s, `UPDATE peers SET protected=1,access='lost',access_hash=998877 WHERE peer_type='channel'`)
	querySQL(t, s, `UPDATE messages SET media_type='document',media_json='{"type":"document","filename":"safe.txt","downloadable":true}',media_remote=x'010203' WHERE peer_type='user' AND msg_id=9`)
	r, err := s.StatsMessages(context.Background(), "primary", StatsFilter{}, "chat", 50)
	if err != nil || r.Total != 7 {
		t.Fatalf("visible stats: %+v %v", r, err)
	}
	var messages []engine.Message
	err = s.StreamExportMessages(context.Background(), "primary", StatsFilter{}, func(m engine.Message) error { messages = append(messages, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, m := range messages {
		ids = append(ids, m.Key.ID)
		if m.Media != nil && m.Media.Remote != nil {
			t.Fatal("remote exposed")
		}
		if (m.Key.ID == 8 || m.Key.Peer.Type == engine.PeerChannel) && (!m.Protected || m.Text == "" || m.Media != nil) {
			t.Fatalf("protected: %+v", m)
		}
	}
	if !reflect.DeepEqual(ids, []int{1, 8, 9, 10}) {
		t.Fatalf("export ids: %v", ids)
	}
	raw, err := json.Marshal(messages)
	if err != nil || strings.Contains(string(raw), "998877") || strings.Contains(string(raw), "secret.jpg") || strings.Contains(string(raw), "remote") {
		t.Fatalf("unsafe JSON %s %v", raw, err)
	}
}

func TestStatsStreamOrderNoCapAndFailure(t *testing.T) {
	s := statsFixture(t)
	var got []string
	err := s.StreamExportMessages(context.Background(), "primary", StatsFilter{}, func(m engine.Message) error { got = append(got, string(m.Key.Peer.Type)+":"+m.Text); return nil })
	want := []string{"chat:group", "user:first", "user:photo", "user:outgoing", "channel:channel"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v %v", got, err)
	}
	sentinel := errors.New("sink failed")
	count := 0
	err = s.StreamExportMessages(context.Background(), "primary", StatsFilter{}, func(engine.Message) error { count++; return sentinel })
	if !errors.Is(err, sentinel) || count != 1 {
		t.Fatalf("callback: %d %v", count, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	count = 0
	err = s.StreamExportMessages(ctx, "primary", StatsFilter{}, func(engine.Message) error { count++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatalf("cancel: %d %v", count, err)
	}
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(4) UNION ALL SELECT i+1 FROM n WHERE i<1010) INSERT INTO messages(account,peer_type,peer_id,msg_id,date,source,first_seen_at,updated_at) SELECT 'primary','user',1,i,1400,'update',1000,1000 FROM n`)
	count = 0
	err = s.StreamExportMessages(context.Background(), "primary", StatsFilter{}, func(engine.Message) error { count++; return nil })
	if err != nil || count != 1012 {
		t.Fatalf("uncapped: %d %v", count, err)
	}
	querySQL(t, s, `UPDATE messages SET entities_json='bad-json' WHERE peer_type='chat'`)
	if err = s.StreamExportMessages(context.Background(), "primary", StatsFilter{}, func(engine.Message) error { return nil }); err == nil {
		t.Fatal("expected scanner failure")
	}
}

func TestStatsAPIGuards(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	for _, account := range []engine.AccountID{"", "other"} {
		if _, err := s.StatsMessages(ctx, account, StatsFilter{}, "chat", 1); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("stats account: %v", err)
		}
		if err := s.StreamExportMessages(ctx, account, StatsFilter{}, func(engine.Message) error { return nil }); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("export account: %v", err)
		}
		if _, err := s.StatsCoverage(ctx, account, nil); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("coverage account: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.StatsMessages(canceled, "primary", StatsFilter{}, "chat", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.StreamExportMessages(canceled, "primary", StatsFilter{}, func(engine.Message) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.StatsCoverage(canceled, "primary", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, f := range []StatsFilter{{Peer: &engine.PeerRef{}}, {Peer: &engine.PeerRef{Type: "bogus", ID: 1}}, {Direction: "incoming"}, {Since: time.Unix(20, 0), Until: time.Unix(10, 0)}, {Since: time.Unix(20, 0), Until: time.Unix(20, 0)}} {
		if _, err := s.StatsMessages(ctx, "primary", f, "chat", 1); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("stats filter %+v: %v", f, err)
		}
		if err := s.StreamExportMessages(ctx, "primary", f, func(engine.Message) error { return nil }); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("export filter %+v: %v", f, err)
		}
	}
	for _, n := range []int{-1, 1001} {
		if _, err := s.StatsMessages(ctx, "primary", StatsFilter{}, "chat", n); !errors.Is(err, engine.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := s.StatsMessages(ctx, "primary", StatsFilter{}, "sql injection", 1); !errors.Is(err, engine.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.StreamExportMessages(ctx, "primary", StatsFilter{}, nil); !errors.Is(err, engine.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.StatsCoverage(ctx, "primary", &engine.PeerRef{}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatal(err)
	}
	r, err := s.StatsMessages(ctx, "primary", StatsFilter{}, "chat", 1)
	if err != nil || r.Buckets == nil || len(r.Buckets) != 0 || r.Total != 0 || r.Groups != 0 {
		t.Fatalf("empty: %+v %v", r, err)
	}
}

func TestStatsCoverageRichConservativeAndScoped(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	p := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	queryPeer(t, s, "user", 1, "user")
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) VALUES('primary','user',1,500,1,1000)`)
	c, err := s.StatsCoverage(ctx, "primary", &p)
	if err != nil || !c.Complete || c.HistorySince == nil || c.HistorySince.Unix() != 500 || len(c.Cursors) != 1 || c.Gaps == nil {
		t.Fatalf("complete: %+v %v", c, err)
	}
	queryPeer(t, s, "channel", 2, "channel")
	c, err = s.StatsCoverage(ctx, "primary", nil)
	if err != nil || c.Complete || c.HistorySince != nil {
		t.Fatalf("unknown global peer: %+v %v", c, err)
	}
	querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES('primary','difference_too_long',1200)`)
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('primary','channel',2,'access_lost',1300)`)
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at,filled_at) VALUES('primary','user',1,'state_lost',1400,1500)`)
	c, err = s.StatsCoverage(ctx, "primary", &p)
	if err != nil || c.Complete || len(c.Gaps) != 1 || c.Gaps[0].Peer != nil || c.Gaps[0].Reason != "difference_too_long" {
		t.Fatalf("scoped gaps: %+v %v", c, err)
	}
	c, err = s.StatsCoverage(ctx, "primary", nil)
	if err != nil || len(c.Gaps) != 2 {
		t.Fatalf("global gaps: %+v %v", c, err)
	}
	querySQL(t, s, `UPDATE gaps SET filled_at=1800`)
	querySQL(t, s, `UPDATE backfill_cursors SET state='running'`)
	c, err = s.StatsCoverage(ctx, "primary", &p)
	if err != nil || c.Complete {
		t.Fatalf("running cursor: %+v %v", c, err)
	}
	querySQL(t, s, `UPDATE backfill_cursors SET state='idle'`)
	querySQL(t, s, `INSERT INTO updates_state(account,pts,qts,date,seq,held,held_reason,updated_at) VALUES('primary',1,0,1000,1,1,'test',1000)`)
	c, err = s.StatsCoverage(ctx, "primary", &p)
	if err != nil || c.Complete {
		t.Fatalf("held checkpoint: %+v %v", c, err)
	}
}
