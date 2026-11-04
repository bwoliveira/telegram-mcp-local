package gotdengine

import (
	"reflect"
	"testing"

	"github.com/gotd/td/tg"
)

func TestActionReactionProjectionPreservesOrderingAndInput(t *testing.T) {
	for _, envelope := range []string{"updates", "combined", "short"} {
		for _, channel := range []bool{false, true} {
			t.Run(envelope+"/"+map[bool]string{true: "channel", false: "user"}[channel], func(t *testing.T) {
				m := &tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 10}, Date: 1000, Message: "Synthetic content"}
				m.SetEditDate(1100)
				m.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}})
				var edit tg.UpdateClass = &tg.UpdateEditMessage{Message: m, Pts: 20, PtsCount: 1}
				if channel {
					edit = &tg.UpdateEditChannelMessage{Message: m, Pts: 20, PtsCount: 1}
				}
				original := actionEnvelope(envelope, edit, &tg.UpdateMessageReactions{Peer: m.PeerID, MsgID: m.ID})
				got := withoutActionReactionReplay(original)
				var us []tg.UpdateClass
				switch v := got.(type) {
				case *tg.Updates:
					us = v.Updates
					before := original.(*tg.Updates)
					if v.Seq != before.Seq || v.Date != before.Date {
						t.Fatal("changed ordering")
					}
				case *tg.UpdatesCombined:
					us = v.Updates
					before := original.(*tg.UpdatesCombined)
					if v.Seq != before.Seq || v.SeqStart != before.SeqStart || v.Date != before.Date {
						t.Fatal("changed ordering")
					}
				case *tg.UpdateShort:
					us = []tg.UpdateClass{v.Update}
					if v.Date != original.(*tg.UpdateShort).Date {
						t.Fatal("changed date")
					}
				}
				if len(us) != 1 {
					t.Fatalf("projection %+v", us)
				}
				var projected *tg.Message
				switch v := us[0].(type) {
				case *tg.UpdateEditMessage:
					if v.Pts != 20 || v.PtsCount != 1 {
						t.Fatal("lost PTS")
					}
					projected = v.Message.(*tg.Message)
				case *tg.UpdateEditChannelMessage:
					if v.Pts != 20 || v.PtsCount != 1 {
						t.Fatal("lost channel PTS")
					}
					projected = v.Message.(*tg.Message)
				}
				if _, has := projected.GetReactions(); has {
					t.Fatal("reaction replay retained")
				}
				if _, has := m.GetReactions(); !has || len(m.Reactions.Results) != 1 {
					t.Fatal("mutated original")
				}
				restored := *projected
				restored.SetReactions(m.Reactions)
				if !reflect.DeepEqual(&restored, m) {
					t.Fatal("changed other message fields")
				}
			})
		}
	}
}
