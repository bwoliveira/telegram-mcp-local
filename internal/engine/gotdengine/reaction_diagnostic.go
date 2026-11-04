package gotdengine

import (
	"fmt"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// reactionRPCShape exposes constructor and presence metadata through the existing
// bridge receipt, without weakening log redaction or starting another session.
// Never include content, IDs, emoji, hashes, or arbitrary Stringer values here.
func reactionRPCShape(raw tg.UpdatesClass, key engine.MessageKey) string {
	var updates []tg.UpdateClass
	envelope := "other"
	switch v := raw.(type) {
	case *tg.Updates:
		envelope, updates = "updates", v.Updates
	case *tg.UpdatesCombined:
		envelope, updates = "combined", v.Updates
	case *tg.UpdateShort:
		envelope, updates = "short", []tg.UpdateClass{v.Update}
	}
	snapshot := func(r tg.MessageReactions) string {
		chosen := 0
		for _, c := range r.Results {
			if _, ok := c.GetChosenOrder(); ok {
				chosen++
			}
		}
		_, recent := r.GetRecentReactions()
		return fmt.Sprintf("min=%t,results=%d,chosen=%d,recent_present=%t,recent=%d", r.Min, len(r.Results), chosen, recent, len(r.RecentReactions))
	}
	message := func(kind string, raw tg.MessageClass) string {
		m, ok := raw.(*tg.Message)
		if !ok {
			return kind + "(other)"
		}
		r, has := m.GetReactions()
		return fmt.Sprintf("%s(target=%t,present=%t,%s)", kind, m.ID == key.ID && peerRef(m.PeerID) == key.Peer, has, snapshot(r))
	}
	parts := make([]string, 0, min(len(updates), 16))
	for i, u := range updates {
		if i == 16 {
			parts = append(parts, "truncated")
			break
		}
		switch v := u.(type) {
		case *tg.UpdateMessageReactions:
			parts = append(parts, fmt.Sprintf("reaction(target=%t,%s)", v.MsgID == key.ID && peerRef(v.Peer) == key.Peer, snapshot(v.Reactions)))
		case *tg.UpdateEditMessage:
			parts = append(parts, message("edit", v.Message))
		case *tg.UpdateEditChannelMessage:
			parts = append(parts, message("channel_edit", v.Message))
		default:
			parts = append(parts, "other")
		}
	}
	return fmt.Sprintf("%s(count=%d)[%s]", envelope, len(updates), strings.Join(parts, ";"))
}
