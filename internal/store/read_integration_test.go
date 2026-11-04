package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

var _ interface {
	ReadPeers(context.Context, engine.AccountID) ([]engine.Peer, error)
	ReadDialogs(context.Context, engine.AccountID) ([]engine.Dialog, error)
	ReadMessages(context.Context, engine.AccountID) ([]engine.Message, error)
	ReadGroup(context.Context, engine.AccountID, engine.PeerRef) (engine.Group, error)
	ReadCoverage(context.Context, engine.AccountID, *engine.PeerRef) (bool, *time.Time, error)
} = (*Store)(nil)

func TestReadStoreAllQueriesScopeEveryJoinToAccount(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	for _, acc := range []string{"primary", "other"} {
		for _, p := range []struct{ typ, kind string }{{"user", "user"}, {"chat", "group"}} {
			querySQL(t, s, `INSERT INTO peers(account,peer_type,peer_id,kind,title,updated_at) VALUES(?,?,1,?,?,1000)`, acc, p.typ, p.kind, acc)
		}
		querySQL(t, s, `INSERT INTO peer_usernames VALUES(?,'user',1,?)`, acc, acc)
		querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,refreshed_at) VALUES(?,'chat',1,7,1000)`, acc)
		querySQL(t, s, `INSERT INTO messages(account,peer_type,peer_id,msg_id,date,text,source,first_seen_at,updated_at) VALUES(?,'chat',1,1,1000,?,'update',1000,1000)`, acc, acc+" needle")
		querySQL(t, s, `INSERT INTO transcripts(row_id,text,source,created_at) SELECT row_id,?,'manual',1000 FROM messages WHERE account=?`, acc+" spoken", acc)
		querySQL(t, s, `INSERT INTO groups_full(account,peer_type,peer_id,about,size,snapshot_at) VALUES(?,'chat',1,?,1,1000)`, acc, acc)
		querySQL(t, s, `INSERT INTO participants(account,group_type,group_id,user_type,user_id,role,updated_at) VALUES(?,'chat',1,'user',1,'member',1000)`, acc)
		querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_date,complete,updated_at) SELECT account,peer_type,peer_id,1100,1,1000 FROM peers WHERE account=?`, acc)
	}
	querySQL(t, s, `UPDATE peers SET title='other-only' WHERE account='other'`)
	querySQL(t, s, `UPDATE dialogs SET unread_count=999 WHERE account='other'`)
	querySQL(t, s, `UPDATE backfill_cursors SET oldest_date=1900 WHERE account='other'`)
	querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES('other','state_lost',1000)`)
	querySQL(t, s, `INSERT INTO updates_state(account,pts,qts,date,seq,held,updated_at) VALUES('other',1,0,1000,1,1,1000)`)
	querySQL(t, s, `INSERT INTO message_tombstones VALUES('other','chat',1,1,'deleted',1000)`)
	querySQL(t, s, `INSERT INTO pending_deletes VALUES('other',1,1000)`)
	var before int64
	if err := s.writer.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	peers, err := s.ReadPeers(ctx, "primary")
	if err != nil || len(peers) != 2 {
		t.Fatalf("peers: %+v %v", peers, err)
	}
	for _, p := range peers {
		if p.Title != "primary" || (p.Ref.Type == engine.PeerUser && (len(p.Usernames) != 1 || p.Usernames[0] != "primary")) {
			t.Fatalf("peer join: %+v", p)
		}
	}
	dialogs, err := s.ReadDialogs(ctx, "primary")
	if err != nil || len(dialogs) != 1 || dialogs[0].UnreadCount != 7 {
		t.Fatalf("dialogs: %+v %v", dialogs, err)
	}
	for _, read := range []func(context.Context, engine.AccountID) ([]engine.Message, error){s.ReadMessages, func(ctx context.Context, a engine.AccountID) ([]engine.Message, error) {
		return s.SearchLocal(ctx, a, "needle OR spoken")
	}} {
		msgs, err := read(ctx, "primary")
		if err != nil || len(msgs) != 1 || msgs[0].Text != "primary needle" || msgs[0].ChatName != "primary" || msgs[0].Key.Account != "primary" {
			t.Fatalf("messages: %+v %v", msgs, err)
		}
	}
	ref := engine.PeerRef{Type: engine.PeerChat, ID: 1}
	group, err := s.ReadGroup(ctx, "primary", ref)
	if err != nil || group.About != "primary" || group.Peer.Title != "primary" || len(group.Participants) != 1 || group.Participants[0].Name != "primary" {
		t.Fatalf("group join: %+v %v", group, err)
	}
	tr, ok, err := s.ReadTranscript(ctx, engine.MessageKey{Account: "primary", Peer: ref, ID: 1})
	if err != nil || !ok || tr.Text != "primary spoken" {
		t.Fatalf("transcript join: %+v %v %v", tr, ok, err)
	}
	readCoverageAssert(t, s, nil, true, 1100)
	readCoverageAssert(t, s, &ref, true, 1100)
	var after int64
	if err := s.writer.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("reads wrote data: %d -> %d", before, after)
	}
}

func TestReadStoreApplyEditsDeletesAndTranscriptReplacement(t *testing.T) {
	s := confOpen(t)
	ctx := context.Background()
	user := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	channel := engine.PeerRef{Type: engine.PeerChannel, ID: 10}
	first := confMessage(user, 7, "originaltoken")
	second := confMessage(channel, 7, "originaltoken")
	confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{first, second}})
	if err := s.SaveTranscript(ctx, first.Key, Transcript{Text: "firstspoken", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTranscript(ctx, first.Key, Transcript{Text: "secondspoken", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if hits, err := s.SearchLocal(ctx, confAccount, "firstspoken"); err != nil || len(hits) != 0 {
		t.Fatalf("stale transcript FTS: %+v %v", hits, err)
	}
	if hits, err := s.SearchLocal(ctx, confAccount, "secondspoken"); err != nil || len(hits) != 1 || hits[0].Key != first.Key {
		t.Fatalf("replacement FTS: %+v %v", hits, err)
	}
	edit := first
	edit.Text = "editedtoken"
	at := confTime
	edit.EditDate = &at
	confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{edit}})
	if hits, err := s.SearchLocal(ctx, confAccount, "originaltoken"); err != nil || len(hits) != 1 || hits[0].Key != second.Key {
		t.Fatalf("edit FTS: %+v %v", hits, err)
	}
	confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Deletes: []engine.Deletion{{Peer: &user, IDs: []int{7}}}})
	confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceBackfill, Messages: []engine.Message{first}})
	if hits, err := s.SearchLocal(ctx, confAccount, "editedtoken OR secondspoken"); err != nil || len(hits) != 0 {
		t.Fatalf("resurrection FTS: %+v %v", hits, err)
	}
	if _, ok, err := s.ReadTranscript(ctx, first.Key); err != nil || ok {
		t.Fatalf("deleted transcript: %v %v", ok, err)
	}
	if msgs, err := s.ReadMessages(ctx, confAccount); err != nil || len(msgs) != 1 || msgs[0].Key != second.Key {
		t.Fatalf("namespaced delete: %+v %v", msgs, err)
	}
}

func TestReadStoreConcurrentApplyKeepsGroupSnapshotCoherent(t *testing.T) {
	s := confOpen(t)
	ctx := context.Background()
	group := engine.PeerRef{Type: engine.PeerChat, ID: 55}
	member := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	batch := func(i int) engine.Batch {
		value := fmt.Sprintf("snapshot-%d", i)
		return engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Participants: []engine.ParticipantsSnapshot{{Group: group, About: value, Size: 1, At: confTime.Add(time.Duration(i) * time.Second), Participants: []engine.Participant{{Peer: member, Name: value, Role: "member"}}}}, Messages: []engine.Message{confMessage(group, 1, value)}}
	}
	confApply(t, s, batch(0))
	errors := make(chan error, 8)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 60; i++ {
			if _, err := s.Apply(ctx, batch(i)); err != nil {
				errors <- err
				return
			}
		}
	}()
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				g, err := s.ReadGroup(ctx, confAccount, group)
				if err != nil {
					errors <- err
					return
				}
				if len(g.Participants) != 1 || g.Participants[0].Name != g.About {
					errors <- fmt.Errorf("torn group: %+v", g)
					return
				}
				if _, err := s.ReadPeers(ctx, confAccount); err != nil {
					errors <- err
					return
				}
				if _, err := s.ReadDialogs(ctx, confAccount); err != nil {
					errors <- err
					return
				}
				if _, err := s.ReadMessages(ctx, confAccount); err != nil {
					errors <- err
					return
				}
				if _, err := s.SearchLocal(ctx, confAccount, "snapshot"); err != nil {
					errors <- err
					return
				}
				if _, _, err := s.ReadCoverage(ctx, confAccount, nil); err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
