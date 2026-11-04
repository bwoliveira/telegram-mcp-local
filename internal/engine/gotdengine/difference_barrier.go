package gotdengine

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// durableDifference is a response barrier, not a PTS inferred from individual
// updates. gotd v0.163.0 queues channelDifference.OtherUpdates to another worker
// and saves channel PTS BEFORE that worker dispatches them. A handler-error latch
// alone cannot protect that window. Commit the entire authoritative difference
// before handing its response to the manager: no worker can see its new PTS yet.
// Leave the response intact for gotd's ordering/access-hash bookkeeping. Its
// later handler delivery is an idempotent replay (and emits no duplicate events).
func (e *Engine) durableDifference(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		var scope *engine.PeerRef
		switch r := in.(type) {
		case *tg.UpdatesGetDifferenceRequest:
		case *tg.UpdatesGetChannelDifferenceRequest:
			if ch, ok := r.Channel.(*tg.InputChannel); ok {
				scope = &engine.PeerRef{Type: engine.PeerChannel, ID: ch.ChannelID}
				// gotd's channel worker caches its starting hash. A later authoritative
				// refresh must be used on every retry without mutating the shared request.
				if h, found, err := e.cfg.Store.AccessHash(ctx, e.cfg.Account, *scope); err != nil {
					return err
				} else if found && h != ch.AccessHash {
					copy := *r
					copy.Channel = &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: h}
					in = &copy
				}
			}
		default:
			return next.Invoke(ctx, in, out)
		}
		// Snapshot BEFORE the RPC: a final response must not close a gap detected
		// after that response's recovery interval began.
		gaps, err := e.recoveryGaps(ctx, scope)
		if err != nil {
			e.holdCheckpoint()
			return err
		}
		if err := next.Invoke(ctx, in, out); err != nil {
			if scope == nil || !tgerr.Is(err, "CHANNEL_PRIVATE") {
				return err
			}
			r := in.(*tg.UpdatesGetChannelDifferenceRequest)
			old, ok := r.Channel.(*tg.InputChannel)
			if !ok {
				return err
			}
			h, found, repairErr := e.recoverChannelHash(ctx, scope.ID)
			if repairErr != nil {
				return repairErr
			}
			if !found || h == old.AccessHash {
				return err
			}
			retry := *r
			retry.Channel = &tg.InputChannel{ChannelID: scope.ID, AccessHash: h}
			if err := next.Invoke(ctx, &retry, out); err != nil {
				return err
			}
		}
		var other []tg.UpdateClass
		var messages []tg.MessageClass
		var users []tg.UserClass
		var chats []tg.ChatClass
		var proof *differenceProof
		channel := scope != nil
		switch r := in.(type) {
		case *tg.UpdatesGetChannelDifferenceRequest:
			diff, ok := out.(*tg.UpdatesChannelDifferenceBox)
			if !ok {
				return engine.ErrInvalid
			}
			switch d := diff.ChannelDifference.(type) {
			case *tg.UpdatesChannelDifference:
				other, messages, users, chats = d.OtherUpdates, d.NewMessages, d.Users, d.Chats
				if scope != nil {
					proof = &differenceProof{gaps: gaps, channel: scope.ID, final: d.Final, state: engine.UpdatesState{Pts: d.Pts}}
				}
			case *tg.UpdatesChannelDifferenceEmpty:
				if scope != nil {
					proof = &differenceProof{gaps: gaps, channel: scope.ID, final: d.Final, state: engine.UpdatesState{Pts: d.Pts}}
				}
			case *tg.UpdatesChannelDifferenceTooLong:
				// gotd advances the channel PTS without dispatching this available
				// history snapshot. Ingest it here, still without claiming that
				// the missing update stream (edits/deletes) was recovered.
				messages, users, chats = d.Messages, d.Users, d.Chats
				if _, err := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceDifference, Gaps: []engine.GapMark{{Peer: scope, Reason: engine.GapChannelTooLong, Detected: e.now()}}}); err != nil {
					return err
				}
			}
		case *tg.UpdatesGetDifferenceRequest:
			diff, ok := out.(*tg.UpdatesDifferenceBox)
			if !ok {
				return engine.ErrInvalid
			}
			switch d := diff.Difference.(type) {
			case *tg.UpdatesDifference:
				other, messages, users, chats = d.OtherUpdates, d.NewMessages, d.Users, d.Chats
				proof = &differenceProof{gaps: gaps, final: true, state: engine.UpdatesState{Pts: d.State.Pts, Qts: d.State.Qts, Date: d.State.Date, Seq: d.State.Seq}}
			case *tg.UpdatesDifferenceSlice:
				other, messages, users, chats = d.OtherUpdates, d.NewMessages, d.Users, d.Chats
				proof = &differenceProof{gaps: gaps, state: engine.UpdatesState{Pts: d.IntermediateState.Pts, Qts: d.IntermediateState.Qts, Date: d.IntermediateState.Date, Seq: d.IntermediateState.Seq}}
			case *tg.UpdatesDifferenceEmpty:
				proof = &differenceProof{gaps: gaps, final: true, state: engine.UpdatesState{Pts: r.Pts, Qts: r.Qts, Date: d.Date, Seq: d.Seq}}
			case *tg.UpdatesDifferenceTooLong:
				// gotd saves the replacement PTS before invoking OnTooLong.
				// Persist the lost-stream warning before exposing that reset.
				if _, err := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceDifference, Gaps: []engine.GapMark{{Reason: engine.GapDifferenceTooLong, Detected: e.now()}}}); err != nil {
					return err
				}
			}
		}
		if err := e.require(); err != nil {
			e.holdCheckpoint()
			return err
		}
		if e.held.Load() {
			return engine.ErrCheckpointHeld
		}
		if len(other)+len(messages)+len(users)+len(chats) > 0 {
			us := append([]tg.UpdateClass(nil), other...)
			for _, m := range messages {
				if channel {
					us = append(us, &tg.UpdateNewChannelMessage{Message: m, Pts: -1, PtsCount: -1})
				} else {
					us = append(us, &tg.UpdateNewMessage{Message: m, Pts: -1, PtsCount: -1})
				}
			}
			if err := e.handle(ctx, &tg.Updates{Updates: us, Users: users, Chats: chats, Seq: -1}); err != nil {
				return err
			}
		}
		if proof != nil {
			switch r := in.(type) {
			case *tg.UpdatesGetDifferenceRequest:
				proof.from = engine.UpdatesState{Pts: r.Pts, Qts: r.Qts, Date: r.Date}
			case *tg.UpdatesGetChannelDifferenceRequest:
				proof.from = engine.UpdatesState{Pts: r.Pts}
			}
			e.stageDifferenceProof(proof)
		}
		return nil
	}
}
