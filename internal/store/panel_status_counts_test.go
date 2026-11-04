package store

import (
	"context"
	"database/sql"
	"testing"
)

// Frozen pre-optimization query is the differential oracle and EXPLAIN baseline.
const panelStatusLegacyCountsQuery = `WITH visible AS (SELECT m.peer_type,m.peer_id,m.first_seen_at FROM messages m WHERE ` + localVisibleMessage + `)
 SELECT (SELECT count(*) FROM visible),
 (SELECT count(*) FROM (SELECT peer_type,peer_id FROM dialogs WHERE account=? UNION SELECT peer_type,peer_id FROM visible)),
 (SELECT max(first_seen_at) FROM visible)`

func TestPanelStatusCountsMatchLegacy(t *testing.T) {
	s := queryStore(t)
	check := func(t *testing.T) {
		t.Helper()
		var messages int64
		var conversations int
		var last sql.NullInt64
		if err := s.reader.QueryRow(panelStatusLegacyCountsQuery, "primary", 2000, 2000, "primary").Scan(&messages, &conversations, &last); err != nil {
			t.Fatal(err)
		}
		history, _, err := s.ReadStatus(context.Background(), "primary")
		if err != nil {
			t.Fatal(err)
		}
		if history.Messages != messages || history.Conversations != conversations || (last.Valid && history.LastAt.Unix() != last.Int64) || (!last.Valid && !history.LastAt.IsZero()) {
			t.Fatalf("history=%+v legacy=(%d,%d,%+v)", history, messages, conversations, last)
		}
	}
	t.Run("empty", check)
	queryPeer(t, s, "user", 1, "user")
	queryPeer(t, s, "channel", 1, "channel")
	queryPeer(t, s, "user", 2, "user")
	for id := 1; id <= 8; id++ {
		queryMsg(t, s, "user", 1, id, 1000+id, "synthetic")
	}
	queryMsg(t, s, "channel", 1, 1, 1000, "synthetic channel")
	querySQL(t, s, `UPDATE messages SET first_seen_at=1400+msg_id`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,refreshed_at) VALUES('primary','user',1,1),('primary','user',2,1)`)
	t.Run("typed peers and dialog only", check)
	querySQL(t, s, `UPDATE messages SET expires_at=2000 WHERE peer_type='user' AND msg_id=1`)
	querySQL(t, s, `UPDATE messages SET ttl_period=10 WHERE peer_type='user' AND msg_id=2`)
	querySQL(t, s, `INSERT INTO pending_deletes(account,msg_id,received_at) VALUES('primary',3,2000)`)
	querySQL(t, s, `INSERT INTO message_tombstones(account,peer_type,peer_id,msg_id,deleted_at,reason) VALUES('primary','user',1,4,2000,'deleted')`)
	t.Run("visibility before janitor", check)
	querySQL(t, s, `DELETE FROM messages`)
	t.Run("dialogs without messages", check)
}
