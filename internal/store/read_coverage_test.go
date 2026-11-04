package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestReadStoreGroupRequiresStoredSnapshot(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerChannel, ID: 1}
	if _, err := s.ReadGroup(ctx, "primary", ref); !errors.Is(err, engine.ErrPeerNotFound) {
		t.Fatalf("missing peer: %v", err)
	}
	queryPeer(t, s, "channel", 1, "supergroup")
	if _, err := s.ReadGroup(ctx, "primary", ref); !errors.Is(err, engine.ErrPeerNotFound) {
		t.Fatalf("missing snapshot: %v", err)
	}
	querySQL(t, s, `INSERT INTO groups_full(account,peer_type,peer_id,about,owner_id,created_at,linked_type,linked_id,size,partial,am_admin,snapshot_at) VALUES('primary','channel',1,'about',9,800,'chat',2,1005,1,1,900)`)
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1005)
 INSERT INTO peers(account,peer_type,peer_id,kind,first_name,username,updated_at) SELECT 'primary','user',i,'user','person','alias',1000 FROM n`)
	querySQL(t, s, `INSERT INTO participants(account,group_type,group_id,user_type,user_id,role,joined_at,updated_at) SELECT 'primary','channel',1,peer_type,peer_id,'member',700,1000 FROM peers WHERE peer_type='user'`)
	querySQL(t, s, `UPDATE peers SET access='lost',access_hash=987654321 WHERE peer_type='channel'`)
	if err := s.SetAuthState(ctx, "primary", engine.AuthRevoked, "fixture"); err != nil {
		t.Fatal(err)
	}
	g, err := s.ReadGroup(ctx, "primary", ref)
	if err != nil || g.Peer.Access != engine.AccessLost || !g.Partial || g.SnapshotAt.Unix() != 900 || len(g.Participants) != 1005 || g.About != "about" || g.Owner == nil || g.Owner.ID != 9 || g.CreatedAt == nil || g.CreatedAt.Unix() != 800 || g.LinkedChat == nil || g.LinkedChat.Type != engine.PeerChat || g.LinkedChat.ID != 2 || !g.AmAdmin || g.Size != 1005 || g.Participants[0].Name != "person" || g.Participants[0].Username != "alias" || g.Participants[0].JoinedAt.Unix() != 700 {
		t.Fatalf("snapshot: %+v %v", g, err)
	}
	g.Participants[0].Name = "mutated"
	*g.Participants[0].JoinedAt = time.Time{}
	g.Owner.ID = 999
	g.LinkedChat.ID = 999
	*g.CreatedAt = time.Time{}
	again, err := s.ReadGroup(ctx, "primary", ref)
	if err != nil || again.Participants[0].Name != "person" || again.Participants[0].JoinedAt.Unix() != 700 || again.Owner.ID != 9 || again.LinkedChat.ID != 2 || again.CreatedAt.Unix() != 800 || again.SnapshotAt.Unix() != 900 {
		t.Fatalf("alias or refreshed timestamp: %+v %v", again, err)
	}
	for _, p := range []engine.PeerRef{{}, {Type: engine.PeerUser, ID: 1}, {Type: engine.PeerChannel, ID: -1}, {Type: "unknown", ID: 1}} {
		if _, err := s.ReadGroup(ctx, "primary", p); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid group %v: %v", p, err)
		}
	}
	for _, a := range []engine.AccountID{"", "other"} {
		if _, err := s.ReadGroup(ctx, a, ref); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ReadGroup(canceled, "primary", ref); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func readCoverageAssert(t *testing.T, s *Store, p *engine.PeerRef, wantComplete bool, wantOldest int64) {
	t.Helper()
	complete, oldest, err := s.ReadCoverage(context.Background(), "primary", p)
	if err != nil || complete != wantComplete || (wantOldest == 0 && oldest != nil) || (wantOldest != 0 && (oldest == nil || oldest.Unix() != wantOldest)) {
		t.Fatalf("coverage(%v): complete=%v oldest=%v err=%v; want %v %d", p, complete, oldest, err, wantComplete, wantOldest)
	}
	if oldest != nil {
		*oldest = time.Time{}
	}
}

func TestReadStoreCoverageUsesConservativeIntersection(t *testing.T) {
	s := queryStore(t)
	user := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	channel := engine.PeerRef{Type: engine.PeerChannel, ID: 1}
	readCoverageAssert(t, s, nil, false, 0)
	readCoverageAssert(t, s, &user, false, 0)
	queryPeer(t, s, "user", 1, "user")
	readCoverageAssert(t, s, nil, false, 0)
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) VALUES('primary','user',1,500,1,1000)`)
	readCoverageAssert(t, s, &user, true, 500)
	readCoverageAssert(t, s, nil, true, 500)
	queryPeer(t, s, "channel", 1, "channel")
	// A seen peer with an unknown cursor invalidates a global oldest bound.
	readCoverageAssert(t, s, &user, true, 500)
	readCoverageAssert(t, s, nil, false, 0)
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) VALUES('primary','channel',1,1500,1,1000)`)
	readCoverageAssert(t, s, &channel, true, 1500)
	readCoverageAssert(t, s, nil, true, 1500)
	readCoverageAssert(t, s, &user, true, 500)
	querySQL(t, s, `UPDATE backfill_cursors SET oldest_date=NULL WHERE peer_type='channel'`)
	// Complete empty history may have no known date; never invent one.
	readCoverageAssert(t, s, nil, true, 0)
	querySQL(t, s, `UPDATE backfill_cursors SET oldest_date=1500 WHERE peer_type='channel'`)
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('primary','channel',1,'channel_too_long',1000)`)
	readCoverageAssert(t, s, nil, false, 1500)
	readCoverageAssert(t, s, &user, true, 500)
	readCoverageAssert(t, s, &channel, false, 1500)
	querySQL(t, s, `UPDATE gaps SET filled_at=1800`)
	readCoverageAssert(t, s, nil, true, 1500)
	querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES('primary','difference_too_long',1800)`)
	readCoverageAssert(t, s, &user, false, 500)
	readCoverageAssert(t, s, nil, false, 1500)
	querySQL(t, s, `UPDATE gaps SET filled_at=1900 WHERE filled_at IS NULL`)
	querySQL(t, s, `INSERT INTO updates_state(account,pts,qts,date,seq,held,held_reason,updated_at) VALUES('primary',1,0,1000,1,1,'test hold',1000)`)
	readCoverageAssert(t, s, &user, false, 500)
	readCoverageAssert(t, s, nil, false, 1500)
	querySQL(t, s, `UPDATE updates_state SET held=0,held_reason=''`)
	readCoverageAssert(t, s, nil, true, 1500)
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('primary','channel',99,'state_lost',1900)`)
	readCoverageAssert(t, s, nil, false, 0)
	readCoverageAssert(t, s, &channel, true, 1500)
}

func TestReadStoreCoverageRejectsUncertainCursorOrAccess(t *testing.T) {
	for _, condition := range []string{"complete=0", "state='running'", "state='error'", "state='no_access'", "state='flood_wait'", "last_error='failed'", "flood_until=3000"} {
		t.Run(condition, func(t *testing.T) {
			s := queryStore(t)
			queryPeer(t, s, "channel", 1, "channel")
			querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) VALUES('primary','channel',1,1000,1,1000)`)
			querySQL(t, s, `UPDATE backfill_cursors SET `+condition)
			p := engine.PeerRef{Type: engine.PeerChannel, ID: 1}
			readCoverageAssert(t, s, &p, false, 1000)
			readCoverageAssert(t, s, nil, false, 1000)
		})
	}
	s := queryStore(t)
	queryPeer(t, s, "channel", 1, "channel")
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) VALUES('primary','channel',1,1000,1,1000)`)
	p := engine.PeerRef{Type: engine.PeerChannel, ID: 1}
	for _, access := range []string{"left", "kicked", "banned", "lost", "deleted"} {
		querySQL(t, s, `UPDATE peers SET access=?`, access)
		readCoverageAssert(t, s, &p, false, 1000)
		readCoverageAssert(t, s, nil, false, 1000)
	}
	for _, a := range []engine.AccountID{"", "other"} {
		if _, _, err := s.ReadCoverage(context.Background(), a, nil); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatal(err)
		}
	}
	for _, invalid := range []engine.PeerRef{{}, {Type: "unknown", ID: 1}, {Type: engine.PeerUser, ID: -1}} {
		if _, _, err := s.ReadCoverage(context.Background(), "primary", &invalid); !errors.Is(err, engine.ErrInvalid) {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.ReadCoverage(canceled, "primary", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
