package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
)

func TestAccessLossInterruptsAnchoredChannelReplay(t *testing.T) {
	s := confOpen(t)
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	anchor := engine.UpdatesState{Pts: 1}
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at,recovery_anchor,recovery_cursor) VALUES(?,?,?,'state_lost',100,?,?)`, confAccount, peer.Type, peer.ID, encode(anchor), encode(anchor))
	before, err := s.PendingGaps(ctx, confAccount, &peer)
	if err != nil || len(before) != 1 {
		t.Fatalf("pending: %+v %v", before, err)
	}
	_, err = s.Apply(ctx, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLeft}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, restored := range []bool{false, true} {
		if restored {
			_, err = s.Apply(ctx, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessOK}}})
			if err != nil {
				t.Fatal(err)
			}
		}
		_, status, err := s.ReadStatus(ctx, confAccount)
		if err != nil || status.OpenGaps != 0 || status.TerminalGaps != 2 {
			t.Fatalf("access loss must stop replay, including after restoration: %+v %v", status, err)
		}
		ap, err := s.SaveDifferenceCheckpoint(ctx, confAccount, engine.DifferenceCheckpoint{Channel: peer.ID, From: anchor, State: anchor, Final: true, Gaps: before})
		if err != nil || len(ap.Gaps) != 0 {
			t.Fatalf("stale proof after access loss must not fill retained evidence: %+v %v", ap, err)
		}
	}
}
