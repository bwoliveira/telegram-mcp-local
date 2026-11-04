package gotdengine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"time"
)

func (e *Engine) handle(ctx context.Context, raw tg.UpdatesClass) error {
	// Login-token updates are signalled separately. Do not ingest account data
	// before BindSelf has verified the session belongs to this account slot.
	if err := e.require(); err != nil {
		return nil
	}
	b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceUpdate, Received: e.now()}
	var us []tg.UpdateClass
	switch u := raw.(type) {
	case *tg.Updates:
		us = u.Updates
		b.Peers = peerUpserts(u.Users, u.Chats)
		b.CatchUp = u.Seq < 0
	case *tg.UpdatesCombined:
		us = u.Updates
		b.Peers = peerUpserts(u.Users, u.Chats)
		b.CatchUp = u.Seq < 0
	case *tg.UpdateShort:
		us = []tg.UpdateClass{u.Update}
	}
	for _, u := range us {
		switch u := u.(type) {
		case *tg.UpdateNewMessage:
			if u.Pts < 0 || u.PtsCount < 0 {
				b.CatchUp = true
			}
		case *tg.UpdateNewChannelMessage:
			if u.Pts < 0 || u.PtsCount < 0 {
				b.CatchUp = true
			}
		}
	}
	if b.CatchUp {
		b.Source = engine.SourceDifference
	}
	for _, u := range us {
		switch u := u.(type) {
		case *tg.UpdateNewMessage:
			appendMessage(&b, u.Message, false, engine.PeerRef{})
		case *tg.UpdateNewChannelMessage:
			appendMessage(&b, u.Message, false, engine.PeerRef{})
		case *tg.UpdateEditMessage:
			appendMessage(&b, u.Message, true, engine.PeerRef{})
		case *tg.UpdateEditChannelMessage:
			appendMessage(&b, u.Message, true, engine.PeerRef{})
		case *tg.UpdateDeleteMessages:
			b.Deletes = append(b.Deletes, engine.Deletion{IDs: u.Messages})
		case *tg.UpdateDeleteChannelMessages:
			p := engine.PeerRef{Type: engine.PeerChannel, ID: u.ChannelID}
			b.Deletes = append(b.Deletes, engine.Deletion{Peer: &p, IDs: u.Messages})
		case *tg.UpdateMessageReactions:
			key := engine.MessageKey{Account: b.Account, Peer: peerRef(u.Peer), ID: u.MsgID}
			b.Reactions = append(b.Reactions, convertReactions(key, u.Reactions))
		case *tg.UpdateMessagePoll:
			b.Polls = append(b.Polls, convertPollUpdate(u))
		case *tg.UpdateReadHistoryInbox:
			b.Reads = append(b.Reads, engine.ReadUpdate{Peer: peerRef(u.Peer), MaxID: u.MaxID, StillUnread: u.StillUnreadCount})
		case *tg.UpdateReadHistoryOutbox:
			b.Reads = append(b.Reads, engine.ReadUpdate{Peer: peerRef(u.Peer), MaxID: u.MaxID, Outbox: true})
		case *tg.UpdateReadChannelInbox:
			b.Reads = append(b.Reads, engine.ReadUpdate{Peer: engine.PeerRef{Type: engine.PeerChannel, ID: u.ChannelID}, MaxID: u.MaxID, StillUnread: u.StillUnreadCount})
		case *tg.UpdateReadChannelOutbox:
			b.Reads = append(b.Reads, engine.ReadUpdate{Peer: engine.PeerRef{Type: engine.PeerChannel, ID: u.ChannelID}, MaxID: u.MaxID, Outbox: true})
		}
	}
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.appendMissingDialogs(ctx, &b, us); err != nil {
		return err
	}
	_, err := e.applyLocked(ctx, b)
	if err == nil {
		e.requestDialogDiscovery(us)
	}
	return err
}
func (e *Engine) apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	return e.applyLocked(ctx, b)
}

// applyLocked commits a batch while the caller holds commitMu.
func (e *Engine) applyLocked(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	if e.held.Load() {
		return engine.Applied{}, engine.ErrCheckpointHeld
	}
	if b.Received.IsZero() {
		b.Received = e.now()
	}
	applied, err := e.cfg.Store.Apply(ctx, b)
	if err != nil {
		e.held.Store(true)
		select {
		case e.restart <- struct{}{}:
		default:
		}
		return applied, err
	}
	e.mu.Lock()
	e.metrics.Messages += int64(len(applied.Inserted))
	e.metrics.Edits += int64(len(applied.Edited))
	e.metrics.Deletes += int64(len(applied.Deleted))
	e.mu.Unlock()
	for _, k := range applied.Inserted {
		e.messageEvent(ctx, k, engine.EventMessage, b)
	}
	for _, k := range applied.Edited {
		e.messageEvent(ctx, k, engine.EventEdit, b)
	}
	for _, r := range applied.Reactions {
		r := r
		p := r.Key.Peer
		// Counts alone miss reactor-only changes. Even the complete diff can
		// repeat during ABA cycles, so include the durable transition version
		// assigned to this event in the same transaction as the reaction state.
		payload, _ := json.Marshal(r)
		version := fingerprint(fmt.Sprintf("%d|%s", r.Version, payload))
		e.emit(engine.Event{Kind: engine.EventReaction, ID: eventID(e.cfg.Account, engine.EventReaction, p, r.Key.ID, version), Account: e.cfg.Account, Peer: &p, Reaction: &r, ReceivedAt: b.Received, OccurredAt: b.Received, Source: b.Source, CatchUp: b.CatchUp})
	}
	for _, k := range applied.Deleted {
		p := k.Peer
		e.emit(engine.Event{Kind: engine.EventDelete, ID: eventID(e.cfg.Account, engine.EventDelete, p, k.ID, "0"), Account: e.cfg.Account, Peer: &p, Delete: &engine.Deletion{Peer: &p, IDs: []int{k.ID}}, ReceivedAt: b.Received, OccurredAt: b.Received, Source: b.Source, CatchUp: b.CatchUp})
	}
	for _, access := range applied.Access {
		x := access
		p := access.Peer
		version := fingerprint(fmt.Sprintf("%s|%s|%s", access.State, access.Reason, b.Received.UTC().Format(time.RFC3339Nano)))
		e.emit(engine.Event{Kind: engine.EventAccess, ID: eventID(e.cfg.Account, engine.EventAccess, p, 0, version), Account: e.cfg.Account, Access: &x, Peer: &p, ReceivedAt: b.Received, OccurredAt: b.Received, Source: b.Source, CatchUp: b.CatchUp})
	}
	for _, g := range applied.Gaps {
		x := g
		e.emit(engine.Event{Kind: engine.EventCoverage, ID: fingerprint(fmt.Sprint(e.cfg.Account, g.Reason, g.Detected)), Account: e.cfg.Account, Coverage: &x, Peer: g.Peer, ReceivedAt: b.Received, OccurredAt: g.Detected, Source: b.Source})
	}
	return applied, nil
}
func eventID(a engine.AccountID, k engine.EventKind, p engine.PeerRef, id int, version string) string {
	return fingerprint(fmt.Sprintf("%s|%s|%s|%d|%s", a, k, peerText(p), id, version))[:32]
}
func (e *Engine) messageEvent(ctx context.Context, k engine.MessageKey, kind engine.EventKind, b engine.Batch) {
	m, ok, err := e.cfg.Store.Message(ctx, k)
	if err != nil || !ok || (m.ExpiresAt != nil && !m.ExpiresAt.After(e.now())) {
		return
	}
	version := "0"
	at := m.Date
	if kind == engine.EventEdit {
		if m.ServerEditDate != nil {
			at = *m.ServerEditDate
		} else if m.EditDate != nil {
			at = *m.EditDate
		}
		encoded, _ := json.Marshal(m)
		version = fingerprint(string(encoded) + "|" + m.ContentHash + "|" + at.UTC().Format(time.RFC3339Nano))
	}
	p := k.Peer
	e.emit(engine.Event{Kind: kind, ID: eventID(e.cfg.Account, kind, p, k.ID, version), Account: e.cfg.Account, Message: &m, Peer: &p, OccurredAt: at, ReceivedAt: b.Received, Source: b.Source, CatchUp: b.CatchUp})
}
func (e *Engine) Subscribe(ctx context.Context, o engine.SubscribeOptions) <-chan engine.Event {
	n := o.Buffer
	if n <= 0 {
		n = 64
	}
	ch := make(chan engine.Event, n)
	e.mu.Lock()
	e.seq++
	id := e.seq
	e.subscribers[id] = subscriber{ch: ch, kinds: append([]engine.EventKind(nil), o.Kinds...)}
	e.mu.Unlock()
	go func() { <-ctx.Done(); e.mu.Lock(); delete(e.subscribers, id); close(ch); e.mu.Unlock() }()
	return ch
}
func (e *Engine) emit(v engine.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	v.Seq = e.seq
	for _, s := range e.subscribers {
		match := len(s.kinds) == 0
		for _, k := range s.kinds {
			if k == v.Kind {
				match = true
			}
		}
		if !match {
			continue
		}
		select {
		case s.ch <- v:
		default:
			select {
			case <-s.ch:
				e.metrics.DroppedEvents++
			default:
			}
			select {
			case s.ch <- v:
			default:
			}
		}
	}
}
func (e *Engine) gap(p *engine.PeerRef, why engine.GapReason) {
	// Missing state on the very first successful account bind is an initial
	// baseline, not lost updates. Imported/reused account slots are not fresh.
	if p == nil && why == engine.GapStateLost && e.freshAccount.Load() {
		return
	}
	e.mu.Lock()
	ctx := e.active
	e.mu.Unlock()
	if ctx == nil {
		return
	}
	g := engine.GapMark{Peer: p, Reason: why, Detected: e.now()}
	if why == engine.GapStateLost {
		var st engine.UpdatesState
		var found bool
		var err error
		if p == nil {
			st, found, err = e.cfg.Store.LoadUpdatesState(ctx, e.cfg.Account)
		} else if p.Type == engine.PeerChannel {
			st.Pts, found, err = e.cfg.Store.LoadChannelPts(ctx, e.cfg.Account, p.ID)
		}
		if err != nil {
			e.holdCheckpoint()
			return
		}
		if found {
			g.RecoveryAnchor = &st
		}
		// No checkpoint means terminal missing evidence, not a fabricated
		// baseline. Still let gotd attempt getDifference from its adopted head;
		// that can recover future updates but cannot fill this unknown interval.
	}
	b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceDifference, Gaps: []engine.GapMark{g}}
	if why == engine.GapAccessLost && p != nil {
		b.Access = []engine.AccessChange{{Peer: *p, State: engine.AccessLost, Reason: string(why)}}
	}
	applied, err := e.apply(ctx, b)
	if err != nil {
		return
	}
	if why == engine.GapAccessLost {
		// Access and channel-checkpoint removal are one store transaction;
		// never add a second, unchecked drop after the evidence commit.
		return
	}
	// Global state is recovered by the manager's startup/getDifference stream,
	// not by scanning an arbitrary first 100 peers. Only its final durable
	// checkpoint can resolve state_lost; TooLong retains missing-stream evidence.
	if p == nil || why == engine.GapStateLost {
		return
	}
	// Repeated callbacks may be deduplicated by the store. Do not enqueue
	// another bounded head scan unless this callback actually opened a gap.
	if len(applied.Gaps) == 0 {
		return
	}
	go e.recoverGap(ctx, *p, g)
}

// Bounded best-effort head refresh after a truncated channel difference. Seeing
// the previous message ID is NOT proof that missed edits/deletes were replayed;
// keep the update gap open even when this scan reaches the old high-water mark.
func (e *Engine) recoverGap(ctx context.Context, p engine.PeerRef, g engine.GapMark) {
	select {
	case e.backfill <- struct{}{}:
		defer func() { <-e.backfill }()
	case <-ctx.Done():
		return
	}
	cursor, _, err := e.cfg.Store.Cursor(ctx, e.cfg.Account, p)
	if err != nil {
		return
	}
	boundary := cursor.NewestID
	ip, err := e.inputPeer(ctx, p)
	if err != nil {
		return
	}
	offset := 0
	for n := 0; n < 10; n++ {
		got, err := e.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ip, OffsetID: offset, Limit: 100})
		if err != nil {
			return
		}
		raw, users, chats := messagePage(got)
		b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceDifference, CatchUp: true, Peers: peerUpserts(users, chats)}
		reached := len(raw) == 0
		for _, m := range raw {
			if m.GetID() <= boundary && boundary > 0 {
				reached = true
			}
			if offset == 0 || m.GetID() < offset {
				offset = m.GetID()
			}
			appendMessage(&b, m, false, p)
		}
		if _, err := e.apply(ctx, b); err != nil {
			return
		}
		if reached {
			return
		}
	}
}
