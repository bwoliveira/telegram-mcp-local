package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Exercise the real SQLite path at the collection cap, including race/checkptr.
// A generous ten-second CPU regression budget replaces neither the cap nor any
// membership/permission assertions in the engine integration tests.
func TestParticipantsCollectionCapPersistenceBudget(t *testing.T) {
	s := confOpen(t)
	p := groupMetadataSnapshot()
	p.Participants = make([]engine.Participant, 10000)
	p.Size = len(p.Participants)
	for i := range p.Participants {
		p.Participants[i] = engine.Participant{Peer: engine.PeerRef{Type: engine.PeerUser, ID: int64(1000 + i)}, Role: "member", AdminRights: uint32(i), BannedRights: uint32(i) << 16, BannedUntil: i, CanEdit: i%2 == 0}
	}
	start := time.Now()
	confApply(t, s, engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{p}})
	elapsed := time.Since(start)
	t.Logf("10000-member SQLite snapshot: %s", elapsed)
	if elapsed > 10*time.Second {
		t.Errorf("participant persistence took %s; budget is 10s", elapsed)
	}
	before := groupWriterChanges(t, s)
	start = time.Now()
	confApply(t, s, engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{p}})
	elapsed = time.Since(start)
	t.Logf("10000-member SQLite replay: %s", elapsed)
	if elapsed > 10*time.Second {
		t.Errorf("participant replay took %s; budget is 10s", elapsed)
	}
	if after := groupWriterChanges(t, s); after != before {
		t.Fatalf("identical full snapshot replay wrote %d rows", after-before)
	}
	got, err := s.ReadGroup(context.Background(), confAccount, p.Group)
	if err != nil {
		t.Fatal(err)
	}
	if got.Partial || len(got.Participants) != len(p.Participants) {
		t.Fatalf("incomplete cache: partial=%v count=%d", got.Partial, len(got.Participants))
	}
	for i, v := range got.Participants {
		want := p.Participants[i]
		if v.Peer != want.Peer || v.Role != want.Role || v.AdminRights != want.AdminRights || v.BannedRights != want.BannedRights || v.BannedUntil != want.BannedUntil || v.CanEdit != want.CanEdit {
			t.Fatalf("member %d lost identity/permissions: got=%+v want=%+v", i, v, want)
		}
	}
}

func TestParticipantsPeerMergeMatchesGenericUpsert(t *testing.T) {
	actual, reference := confOpen(t), confOpen(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 80}
	migrated := engine.PeerRef{Type: engine.PeerChannel, ID: 99}
	seed := engine.PeerUpsert{Peer: engine.Peer{Ref: ref, Kind: engine.KindBot, Title: "Équipe", FirstName: "Óld", LastName: "Náme", Username: "old_name", Usernames: []string{"alias"}, Phone: "123", IsContact: true, Forum: true, Protected: true, Participants: 42, Access: engine.AccessBanned, MigratedTo: &migrated}, AccessHash: 12345, HashMin: true}
	for _, s := range []*Store{actual, reference} {
		confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{seed}})
	}
	p := groupMetadataSnapshot()
	p.Participants = []engine.Participant{
		{Peer: ref, Name: "Néw", Username: "@new_name", Role: "admin"},
		{Peer: engine.PeerRef{Type: engine.PeerChat, ID: 81}, Name: "Chát", Username: "chat_name", Role: "member"},
		{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 82}, Role: "member"},
	}
	for i := 0; i < 198; i++ {
		p.Participants = append(p.Participants, engine.Participant{Peer: engine.PeerRef{Type: engine.PeerUser, ID: int64(1000 + i)}, Name: fmt.Sprintf("Mémber %d", i), Username: fmt.Sprintf("member_%d", i), Role: "member"})
	}
	for round := 0; round < 5; round++ {
		clock := confTime.Add(time.Duration(round+1) * time.Minute)
		actual.now = func() time.Time { return clock }
		p.At = confTime.Add(time.Duration(round) * time.Second)
		if round == 1 {
			p.Participants[0].Name, p.Participants[0].Username = "", ""
		}
		if round == 2 {
			p.Participants[0].Username = "NEW_NAME"
		}
		if round == 3 {
			for _, s := range []*Store{actual, reference} {
				if _, err := s.writer.ExecContext(ctx, `DELETE FROM peer_usernames WHERE account=? AND peer_id=? AND username=?`, confAccount, int64(1097), "member_97"); err != nil {
					t.Fatal(err)
				}
			}
		}
		confApply(t, actual, engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{p}})
		tx, err := reference.writer.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range p.Participants {
			if err := upsertPeer(ctx, tx, confAccount, engine.PeerUpsert{Peer: engine.Peer{Ref: v.Peer, FirstName: v.Name, Username: v.Username}}, clock.Unix()); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		for _, v := range p.Participants {
			query := `SELECT json_array(kind,title,first_name,last_name,username,phone,search_name,is_contact,forum,protected,participants,access,access_hash,hash_min,migrated_to_type,migrated_to_id,updated_at) FROM peers WHERE account=? AND peer_type=? AND peer_id=?`
			var got, want string
			if err := actual.reader.QueryRowContext(ctx, query, confAccount, v.Peer.Type, v.Peer.ID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if err := reference.reader.QueryRowContext(ctx, query, confAccount, v.Peer.Type, v.Peer.ID).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("round %d peer %v: got %s want %s", round, v.Peer, got, want)
			}
			aliases := func(s *Store) []string {
				rows, err := s.reader.QueryContext(ctx, `SELECT username FROM peer_usernames WHERE account=? AND peer_type=? AND peer_id=? ORDER BY username`, confAccount, v.Peer.Type, v.Peer.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				var names []string
				for rows.Next() {
					var n string
					if err := rows.Scan(&n); err != nil {
						t.Fatal(err)
					}
					names = append(names, n)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return names
			}
			if got, want := aliases(actual), aliases(reference); !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d aliases: %v want %v", round, got, want)
			}
		}
	}
}

func TestParticipantsBulkFailureRollsBack(t *testing.T) {
	for _, mode := range []string{"invalid_peer", "duplicate_peer", "before_commit"} {
		t.Run(mode, func(t *testing.T) {
			armed := false
			injected := errors.New("participant transaction failure")
			s := confOpen(t, FaultAt(func(stage string) error {
				if armed && stage == "apply_before_commit" {
					return injected
				}
				return nil
			}))
			p := groupMetadataSnapshot()
			confApply(t, s, engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{p}})
			before, err := s.ReadGroup(context.Background(), confAccount, p.Group)
			if err != nil {
				t.Fatal(err)
			}
			p.At = p.At.Add(time.Second)
			p.About = "must roll back"
			p.Participants[0].Name, p.Participants[0].Username = "must roll back", "rollback_alias"
			for i := 0; i < 201; i++ {
				p.Participants = append(p.Participants, engine.Participant{Peer: engine.PeerRef{Type: engine.PeerUser, ID: int64(1234 + i)}, Name: "must roll back", Username: fmt.Sprintf("rollback_%d", i)})
			}
			switch mode {
			case "invalid_peer":
				p.Participants = append(p.Participants, engine.Participant{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 0}})
			case "duplicate_peer":
				p.Participants = append(p.Participants, p.Participants[0])
			case "before_commit":
				armed = true
			}
			if _, err := s.Apply(context.Background(), engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{p}}); err == nil {
				t.Fatal("expected transaction failure")
			}
			after, err := s.ReadGroup(context.Background(), confAccount, p.Group)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed snapshot mutated complete group")
			}
			if n := confCount(t, s, `SELECT count(*) FROM peers WHERE account=? AND peer_id=1234`, confAccount); n != 0 {
				t.Fatal(fmt.Sprintf("failed transaction retained %d member peers", n))
			}
		})
	}
}
