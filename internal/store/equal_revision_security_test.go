package store

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestReviewEqualRevisionSecurity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentHash string
		secure      func(*engine.Message)
		check       func(*testing.T, *Store, engine.Message)
	}{
		{
			name:        "protected cached hash",
			contentHash: "cached-content-hash",
			secure: func(m *engine.Message) {
				m.Protected = true
			},
			check: func(t *testing.T, _ *Store, got engine.Message) {
				if !got.Protected || got.Media == nil || got.Media.Downloadable {
					t.Fatalf("protected metadata not persisted: protected=%v media=%+v", got.Protected, got.Media)
				}
			},
		},
		{
			name: "protected legacy hash",
			secure: func(m *engine.Message) {
				m.Protected = true
			},
			check: func(t *testing.T, _ *Store, got engine.Message) {
				if !got.Protected || got.Media == nil || got.Media.Downloadable {
					t.Fatalf("legacy protected metadata not persisted: protected=%v media=%+v", got.Protected, got.Media)
				}
			},
		},
		{
			name:        "temporary media cached hash",
			contentHash: "cached-content-hash",
			secure: func(m *engine.Message) {
				m.Media.TTLSeconds = 45
			},
			check: func(t *testing.T, s *Store, got engine.Message) {
				if got.Media == nil || got.Media.TTLSeconds != 45 || got.Media.Downloadable || got.Media.Remote != nil {
					t.Fatalf("temporary media metadata not persisted safely: %+v", got.Media)
				}
				if got.Text != "[temporary media]" || got.Entities != nil || got.Location != nil || got.Poll != nil {
					t.Fatalf("temporary payload not redacted: text=%q entities=%+v location=%+v poll=%+v", got.Text, got.Entities, got.Location, got.Poll)
				}
				var remoteIsNull bool
				if err := s.reader.QueryRowContext(context.Background(), `SELECT media_remote IS NULL FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?`, got.Key.Account, got.Key.Peer.Type, got.Key.Peer.ID, got.Key.ID).Scan(&remoteIsNull); err != nil {
					t.Fatal(err)
				}
				if !remoteIsNull {
					t.Fatal("temporary media retained its remote download credential")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := confOpen(t)
			peer := engine.PeerRef{Type: engine.PeerUser, ID: 91}
			m := confMessage(peer, 7, "private caption")
			m.ContentHash = tc.contentHash
			m.Entities = []engine.Entity{{Type: engine.EntityBold, Offset: 0, Length: 7}}
			m.Location = &engine.Location{Latitude: 1, Longitude: 2}
			m.Poll = &engine.Poll{ID: 19, Question: "private question"}
			m.Media = &engine.Media{
				Type:         engine.MediaPhoto,
				MimeType:     "image/jpeg",
				Filename:     "private.jpg",
				Size:         123,
				Downloadable: true,
				Remote:       []byte{1, 2, 3},
			}
			genuineEdit := confTime.Add(-30 * time.Minute)
			m.EditDate = &genuineEdit
			m.ServerEditDate = &genuineEdit
			confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{m}})

			snapshot := confGet(t, s, m.Key)
			laterRevision := genuineEdit
			snapshot.EditDate = &laterRevision
			snapshot.ServerEditDate = &laterRevision
			tc.secure(&snapshot)
			applied := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{snapshot}})
			confNoEvents(t, applied)

			got := confGet(t, s, m.Key)
			if got.EditDate == nil || !got.EditDate.Equal(genuineEdit) {
				t.Fatalf("visible edit date=%v, want last genuine edit %v", got.EditDate, genuineEdit)
			}
			if got.ServerEditDate == nil || !got.ServerEditDate.Equal(laterRevision) {
				t.Fatalf("server edit date=%v, want %v", got.ServerEditDate, laterRevision)
			}
			if got.ContentHash != m.ContentHash {
				t.Fatalf("content hash=%q, want cached hash %q", got.ContentHash, m.ContentHash)
			}
			if version := confCount(t, s, `SELECT version FROM messages WHERE account='store-conformance' AND peer_type='user' AND peer_id=91 AND msg_id=7`); version != 1 {
				t.Fatalf("security-only refresh version=%d, want 1", version)
			}
			tc.check(t, s, got)
		})
	}
}
