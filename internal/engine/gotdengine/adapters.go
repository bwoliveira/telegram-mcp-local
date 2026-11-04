package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/updates"
)

type stateAdapter struct {
	p   engine.Persister
	acc engine.AccountID
	e   *Engine
}

func (s stateAdapter) GetState(ctx context.Context, _ int64) (updates.State, bool, error) {
	st, ok, err := s.p.LoadUpdatesState(ctx, s.acc)
	if err == nil && !ok && !s.e.freshAccount.Load() {
		// Adopting getState's current head is not a replay from the lost
		// baseline. Persist stream-loss evidence BEFORE gotd can save that
		// head, so a later restart cannot turn an empty diff into false proof.
		_, err = s.e.apply(ctx, engine.Batch{Account: s.acc, Source: engine.SourceDifference, Gaps: []engine.GapMark{
			{Reason: engine.GapStateLost, Detected: s.e.now()},
			{Reason: engine.GapDifferenceTooLong, Detected: s.e.now()},
		}})
	}
	return updates.State{Pts: st.Pts, Qts: st.Qts, Date: st.Date, Seq: st.Seq}, ok, err
}
func (s stateAdapter) SetState(ctx context.Context, _ int64, st updates.State) error {
	return s.saveState(ctx, s.acc, engine.UpdatesState{Pts: st.Pts, Qts: st.Qts, Date: st.Date, Seq: st.Seq})
}
func (s stateAdapter) SetPts(ctx context.Context, _ int64, v int) error {
	return s.patchState(ctx, s.acc, engine.UpdatesPatch{Pts: &v})
}
func (s stateAdapter) SetQts(ctx context.Context, _ int64, v int) error {
	return s.patchState(ctx, s.acc, engine.UpdatesPatch{Qts: &v})
}
func (s stateAdapter) SetDate(ctx context.Context, _ int64, v int) error {
	return s.patchState(ctx, s.acc, engine.UpdatesPatch{Date: &v})
}
func (s stateAdapter) SetSeq(ctx context.Context, _ int64, v int) error {
	return s.patchState(ctx, s.acc, engine.UpdatesPatch{Seq: &v})
}
func (s stateAdapter) SetDateSeq(ctx context.Context, _ int64, d, q int) error {
	return s.patchState(ctx, s.acc, engine.UpdatesPatch{Date: &d, Seq: &q})
}
func (s stateAdapter) GetChannelPts(ctx context.Context, _ int64, ch int64) (int, bool, error) {
	return s.p.LoadChannelPts(ctx, s.acc, ch)
}
func (s stateAdapter) SetChannelPts(ctx context.Context, _ int64, ch int64, pts int) error {
	return s.saveChannel(ctx, s.acc, ch, pts)
}
func (s stateAdapter) ForEachChannels(ctx context.Context, _ int64, f func(context.Context, int64, int) error) error {
	return s.p.ForEachChannelPts(ctx, s.acc, func(ch int64, pts int) error { return f(ctx, ch, pts) })
}
func (s stateAdapter) SetChannelAccessHash(ctx context.Context, _ int64, ch, h int64) error {
	_, err := s.e.apply(ctx, engine.Batch{Account: s.acc, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel, ID: ch}}, AccessHash: h}}})
	return err
}
func (s stateAdapter) GetChannelAccessHash(ctx context.Context, _ int64, ch int64) (int64, bool, error) {
	h, found, err := s.p.AccessHash(ctx, s.acc, engine.PeerRef{Type: engine.PeerChannel, ID: ch})
	if err != nil || found {
		return h, found, err
	}
	return s.e.recoverChannelHash(ctx, ch)
}
func (s stateAdapter) SetUserAccessHash(ctx context.Context, _ int64, u, h int64) error {
	_, err := s.e.apply(ctx, engine.Batch{Account: s.acc, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: u}}, AccessHash: h}}})
	return err
}
func (s stateAdapter) GetUserAccessHash(ctx context.Context, _ int64, u int64) (int64, bool, error) {
	return s.p.AccessHash(ctx, s.acc, engine.PeerRef{Type: engine.PeerUser, ID: u})
}

type sessAdapter struct{ s engine.SessionStore }

func (a sessAdapter) LoadSession(ctx context.Context) ([]byte, error) {
	b, err := a.s.LoadSession(ctx)
	if errors.Is(err, engine.ErrNoSession) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return expandSession(b)
}
func (a sessAdapter) StoreSession(ctx context.Context, b []byte) error {
	compact, err := compactSession(b)
	if err != nil {
		return err
	}
	return a.s.StoreSession(ctx, compact)
}

var (
	_ updates.StateStorage        = stateAdapter{}
	_ updates.ChannelAccessHasher = stateAdapter{}
	_ updates.UserAccessHasher    = stateAdapter{}
	_ session.Storage             = sessAdapter{}
)

func (s stateAdapter) saveState(ctx context.Context, acc engine.AccountID, st engine.UpdatesState) error {
	s.e.commitMu.Lock()
	defer s.e.commitMu.Unlock()
	if s.e.held.Load() {
		return engine.ErrCheckpointHeld
	}
	if proof := s.e.globalProof; proof != nil && proof.state == st {
		if err := s.commitProof(ctx, acc, proof); err != nil {
			return err
		}
		s.e.globalProof = nil
		return nil
	}
	if err := s.p.SaveUpdatesState(ctx, acc, st); err != nil {
		s.e.holdCheckpoint()
		return err
	}
	return nil
}
func (s stateAdapter) patchState(ctx context.Context, acc engine.AccountID, p engine.UpdatesPatch) error {
	s.e.commitMu.Lock()
	defer s.e.commitMu.Unlock()
	if s.e.held.Load() {
		return engine.ErrCheckpointHeld
	}
	if proof := s.e.globalProof; proof != nil && p.Date != nil && p.Seq != nil {
		st, found, err := s.p.LoadUpdatesState(ctx, acc)
		if err != nil {
			s.e.holdCheckpoint()
			return err
		}
		if p.Pts != nil {
			st.Pts = *p.Pts
		}
		if p.Qts != nil {
			st.Qts = *p.Qts
		}
		st.Date, st.Seq = *p.Date, *p.Seq
		if found && st == proof.state {
			if err := s.commitProof(ctx, acc, proof); err != nil {
				return err
			}
			s.e.globalProof = nil
			return nil
		}
	}
	if err := s.p.PatchUpdatesState(ctx, acc, p); err != nil {
		s.e.holdCheckpoint()
		return err
	}
	return nil
}
func (s stateAdapter) saveChannel(ctx context.Context, acc engine.AccountID, ch int64, pts int) error {
	s.e.commitMu.Lock()
	defer s.e.commitMu.Unlock()
	if s.e.held.Load() {
		return engine.ErrCheckpointHeld
	}
	if proof := s.e.channelProofs[ch]; proof != nil && proof.state.Pts == pts {
		if err := s.commitProof(ctx, acc, proof); err != nil {
			return err
		}
		delete(s.e.channelProofs, ch)
		return nil
	}
	if err := s.p.SaveChannelPts(ctx, acc, ch, pts); err != nil {
		s.e.holdCheckpoint()
		return err
	}
	return nil
}

// A persister without the atomic proof contract can save checkpoints, but cannot
// turn them into recovery evidence. This conservative fallback also preserves
// checkpoint-failure wrappers' semantics.
func (s stateAdapter) commitProof(ctx context.Context, acc engine.AccountID, p *differenceProof) error {
	store, ok := s.p.(differenceCheckpointStore)
	if !ok {
		var err error
		if p.channel == 0 {
			err = s.p.SaveUpdatesState(ctx, acc, p.state)
		} else {
			err = s.p.SaveChannelPts(ctx, acc, p.channel, p.state.Pts)
		}
		if err != nil {
			s.e.holdCheckpoint()
			return err
		}
		// Readiness may advance after a durable final response, but absent
		// the atomic contract NO pending gap may acquire recovery evidence.
		return s.e.finishDifferenceProof(ctx, p, engine.Applied{})
	}
	ap, err := store.SaveDifferenceCheckpoint(ctx, acc, engine.DifferenceCheckpoint{From: p.from, State: p.state, Channel: p.channel, Gaps: p.gaps, Final: p.final})
	if err != nil {
		s.e.holdCheckpoint()
		return err
	}
	return s.e.finishDifferenceProof(ctx, p, ap)
}
