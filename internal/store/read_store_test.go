package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestReadStorePeersAndDialogsAreUnboundedSnapshots(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	querySQL(t, s, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1005)
 INSERT INTO peers(account,peer_type,peer_id,kind,title,access_hash,updated_at)
 SELECT 'primary','user',i,CASE WHEN i=1 THEN 'self' WHEN i=2 THEN 'bot' ELSE 'user' END,'name',987654321,1000 FROM n`)
	queryPeer(t, s, "chat", 1, "group")
	queryPeer(t, s, "channel", 1, "supergroup")
	querySQL(t, s, `UPDATE peers SET access='lost',migrated_to_type='channel',migrated_to_id=1 WHERE peer_type='chat'`)
	querySQL(t, s, `INSERT INTO peer_usernames VALUES('primary','chat',1,'alias')`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,unread_mentions,unread_reactions,unread_mark,folder_id,pinned,top_message_id,read_inbox_max_id,read_outbox_max_id,mute_until,ttl_period,refreshed_at)
 SELECT account,peer_type,peer_id,7,2,3,1,1,1,77,50,60,3000,30,1000 FROM peers WHERE peer_type='user'`)
	if err := s.SetAuthState(ctx, "primary", engine.AuthLoggedOut, "offline test"); err != nil {
		t.Fatal(err)
	}
	peers, err := s.ReadPeers(ctx, "primary")
	if err != nil || len(peers) != 1007 {
		t.Fatalf("peers: count=%d err=%v", len(peers), err)
	}
	var index int
	for i, p := range peers {
		if p.Ref.Type == engine.PeerChat {
			index = i
		}
	}
	if peers[index].Access != engine.AccessLost || len(peers[index].Usernames) != 1 || peers[index].MigratedTo == nil {
		t.Fatalf("lost peer: %+v", peers[index])
	}
	peers[index].Usernames[0] = "mutated"
	peers[index].MigratedTo.ID = 999
	again, err := s.ReadPeers(ctx, "primary")
	if err != nil || again[index].Usernames[0] != "alias" || again[index].MigratedTo.ID != 1 {
		t.Fatalf("aliased peers: %+v %v", again[index], err)
	}
	dialogs, err := s.ReadDialogs(ctx, "primary")
	if err != nil || len(dialogs) != 1005 {
		t.Fatalf("dialogs: count=%d err=%v", len(dialogs), err)
	}
	d := dialogs[0]
	if d.UnreadCount != 7 || d.UnreadMentions != 2 || d.UnreadReactions != 3 || !d.UnreadMark || d.FolderID != 1 || !d.Pinned || d.TopMessageID != 77 || d.ReadInboxMaxID != 50 || d.ReadOutboxMaxID != 60 || d.MuteUntil == nil || d.MuteUntil.Unix() != 3000 || d.TTLPeriod != 30 {
		t.Fatalf("persisted dialog: %+v", d)
	}
	*dialogs[0].MuteUntil = time.Time{}
	dialogs, err = s.ReadDialogs(ctx, "primary")
	if err != nil || dialogs[0].MuteUntil.Unix() != 3000 {
		t.Fatalf("aliased dialogs: %+v %v", dialogs[0], err)
	}
	for _, account := range []engine.AccountID{"", "other"} {
		if _, err = s.ReadPeers(ctx, account); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("peers account %q: %v", account, err)
		}
		if _, err = s.ReadDialogs(ctx, account); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("dialogs account %q: %v", account, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.ReadPeers(canceled, "primary"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.ReadDialogs(canceled, "primary"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
