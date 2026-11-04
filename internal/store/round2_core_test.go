package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

func TestRound2AccessLossRespectsNamespace(t *testing.T) {
	ctx := context.Background()
	s := confOpen(t)
	ch := engine.PeerRef{Type: engine.PeerChannel, ID: 765}
	chat := engine.PeerRef{Type: engine.PeerChat, ID: 765}
	confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ch}, AccessHash: 42}}})
	if err := s.SaveChannelPts(ctx, confAccount, ch.ID, 321); err != nil {
		t.Fatal(err)
	}
	confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: chat, State: engine.AccessLost}}})
	pts, ok, err := s.LoadChannelPts(ctx, confAccount, ch.ID)
	t.Logf("after access loss of chat:765, channel:765 checkpoint pts=%d found=%v err=%v", pts, ok, err)
	if err != nil || !ok || pts != 321 {
		t.Fatal("unrelated channel checkpoint deleted")
	}
}

func TestRound2ParticipantsReplayZeroWrites(t *testing.T) {
	s := confOpen(t)
	ctx := context.Background()
	group := engine.PeerRef{Type: engine.PeerChat, ID: 900}
	b := engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{{Group: group, At: confTime, Size: 1, Participants: []engine.Participant{{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 80}, Name: "Member", Role: "member"}}}}}
	confApply(t, s, b)
	var before, after int64
	if err := s.writer.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return confTime.Add(time.Minute) }
	ap := confApply(t, s, b)
	if err := s.writer.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	t.Logf("identical participants replay changes=%d Applied=%+v", after-before, ap)
	if after != before {
		t.Fatal("identical batch replay changes persisted rows")
	}
}

func TestRound2AccessLossCursorCannotRestart(t *testing.T) {
	s := confOpen(t)
	ctx := context.Background()
	p := engine.PeerRef{Type: engine.PeerChannel, ID: 901}
	confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: p, State: engine.AccessLost}}, Cursor: &engine.Cursor{Peer: p, State: "running", OldestID: 10, NewestID: 20}})
	c, ok, err := s.Cursor(ctx, confAccount, p)
	t.Logf("lost peer cursor state=%s exists=%v err=%v", c.State, ok, err)
	if err != nil || !ok || c.State != "no_access" {
		t.Fatal("page cursor bypasses access lifecycle")
	}
}
