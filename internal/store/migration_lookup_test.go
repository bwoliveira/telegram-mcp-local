package store

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestReadMigrationPeersIncludesPeersOnlyAndScopesCandidates(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	// A basic group without a dialog must survive candidate selection, including
	// its already-persisted link (used to avoid reprobing the destination).
	queryPeer(t, s, "chat", 71, "group")
	querySQL(t, s, `UPDATE peers SET migrated_to_type='channel',migrated_to_id=81 WHERE peer_type='chat'`)
	for id := 81; id <= 86; id++ {
		queryPeer(t, s, "channel", id, "supergroup")
	}
	querySQL(t, s, `UPDATE peers SET access='ok',access_hash=999 WHERE peer_type='channel';
 UPDATE peers SET kind='channel' WHERE peer_id=82;
 UPDATE peers SET access='left' WHERE peer_id=83;
 UPDATE peers SET hash_min=1 WHERE peer_id=84;
 UPDATE peers SET access_hash=NULL WHERE peer_id=85;
 INSERT INTO dialogs(account,peer_type,peer_id,refreshed_at) SELECT account,peer_type,peer_id,1000 FROM peers WHERE peer_type='channel' AND peer_id<>86`)
	// Rows in another account, even when the peer IDs coincide, cannot qualify
	// as a dialog or leak into this account's candidate snapshot.
	querySQL(t, s, `INSERT INTO accounts(account,updated_at) VALUES('other',1000)`)
	querySQL(t, s, `INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES('other','chat',999,'group',1000);
 INSERT INTO peers(account,peer_type,peer_id,kind,access_hash,updated_at) VALUES('other','channel',86,'supergroup',999,1000);
 INSERT INTO dialogs(account,peer_type,peer_id,refreshed_at) VALUES('other','channel',86,1000)`)
	peers, err := s.ReadMigrationPeers(ctx, "primary")
	if err != nil || len(peers) != 2 {
		t.Fatalf("candidate snapshot: %+v %v", peers, err)
	}
	seen := map[engine.PeerRef]engine.Peer{}
	for _, p := range peers {
		seen[p.Ref] = p
	}
	old := seen[engine.PeerRef{Type: engine.PeerChat, ID: 71}]
	if old.MigratedTo == nil || old.MigratedTo.ID != 81 {
		t.Fatalf("peers-only link missing: %+v", old)
	}
	if _, ok := seen[engine.PeerRef{Type: engine.PeerChannel, ID: 81}]; !ok {
		t.Fatal("eligible indexed destination missing")
	}
	for _, account := range []engine.AccountID{"", "other"} {
		if _, err := s.ReadMigrationPeers(ctx, account); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatalf("account %q: %v", account, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ReadMigrationPeers(canceled, "primary"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
