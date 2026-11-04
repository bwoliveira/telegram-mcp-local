package gotdengine

import "github.com/gotd/td/tg"

// withoutActionReactionReplay copies only the response envelope and edit
// snapshots. UpdateMessageReactions is stateless (no PTS); edit updates must
// retain their native PTS/PTSCount/SEQ for manager ordering and gap recovery.
// No input object is mutated. Independently received server updates are not
// filtered: only the response already committed by finishMessageAction is used.
func withoutActionReactionReplay(raw tg.UpdatesClass) tg.UpdatesClass {
	filter := func(us []tg.UpdateClass) []tg.UpdateClass {
		out := make([]tg.UpdateClass, 0, len(us))
		for _, u := range us {
			switch v := u.(type) {
			case *tg.UpdateMessageReactions:
				continue
			case *tg.UpdateEditMessage:
				c := *v
				c.Message = withoutMessageReactions(v.Message)
				u = &c
			case *tg.UpdateEditChannelMessage:
				c := *v
				c.Message = withoutMessageReactions(v.Message)
				u = &c
			}
			out = append(out, u)
		}
		return out
	}
	switch u := raw.(type) {
	case *tg.Updates:
		c := *u
		c.Updates = filter(u.Updates)
		return &c
	case *tg.UpdatesCombined:
		c := *u
		c.Updates = filter(u.Updates)
		return &c
	case *tg.UpdateShort:
		us := filter([]tg.UpdateClass{u.Update})
		if len(us) == 0 {
			return &tg.Updates{Date: u.Date}
		}
		c := *u
		c.Update = us[0]
		return &c
	default:
		return raw
	}
}

func withoutMessageReactions(raw tg.MessageClass) tg.MessageClass {
	m, ok := raw.(*tg.Message)
	if !ok {
		return raw
	}
	c := *m
	// The optional flag must be cleared as well as the value.
	c.Flags &^= 1 << 20
	c.Reactions = tg.MessageReactions{}
	return &c
}
