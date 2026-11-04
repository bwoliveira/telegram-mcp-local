package store

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestStatsExportEffectiveProtection(t *testing.T) {
	for _, tc := range []struct {
		name             string
		messageProtected bool
		peerProtected    bool
	}{
		{name: "unprotected"},
		{name: "message protected", messageProtected: true},
		{name: "peer protected", peerProtected: true},
		{name: "both protected", messageProtected: true, peerProtected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := queryStore(t)
			queryPeer(t, s, "channel", 1, "channel")
			// The same numeric ID in another namespace must not confer protection.
			queryPeer(t, s, "user", 1, "user")
			querySQL(t, s, `UPDATE peers SET protected=1 WHERE peer_type='user'`)
			querySQL(t, s, `UPDATE peers SET protected=? WHERE account='primary' AND peer_type='channel' AND peer_id=1`, tc.peerProtected)
			queryMsg(t, s, "channel", 1, 1, 1000, "preserved caption")
			querySQL(t, s, `UPDATE messages SET protected=?,media_type='photo',media_json='{"type":"photo","filename":"private.jpg","downloadable":true}',media_remote=x'010203'`, tc.messageProtected)

			var storedProtected bool
			if err := s.reader.QueryRowContext(context.Background(), `SELECT protected FROM messages WHERE account='primary' AND peer_type='channel' AND peer_id=1 AND msg_id=1`).Scan(&storedProtected); err != nil {
				t.Fatal(err)
			}
			if storedProtected != tc.messageProtected {
				t.Fatalf("stored message protection = %v, want %v", storedProtected, tc.messageProtected)
			}

			calls := 0
			err := s.StreamExportMessages(context.Background(), "primary", StatsFilter{}, func(m engine.Message) error {
				calls++
				protected := tc.messageProtected || tc.peerProtected
				if m.Protected != protected || m.Text != "preserved caption" {
					t.Fatalf("callback message: protected=%v text=%q, want protected=%v with original text", m.Protected, m.Text, protected)
				}
				if protected {
					if m.Media != nil {
						t.Fatalf("protected media reached callback: %+v", m.Media)
					}
				} else if m.Media == nil || m.Media.Filename != "private.jpg" || !m.Media.Downloadable || m.Media.Remote != nil {
					t.Fatalf("unprotected media not preserved/sanitized: %+v", m.Media)
				}
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("export: calls=%d err=%v", calls, err)
			}
		})
	}
}
