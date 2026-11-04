package gotdengine

import (
	"context"
	"errors"
	"fmt"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// Group previews use the same persisted-resolution and single-use approval
// boundary as send.go. No group preview performs an RPC.
func (e *Engine) groupWriteStart(ctx context.Context, opts engine.WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !opts.DryRun && e.Mode() == engine.ModeReadOnly {
		return engine.ErrReadOnly
	}
	return nil
}

func (e *Engine) groupTarget(ctx context.Context, ref engine.PeerRef) (engine.Peer, tg.InputPeerClass, error) {
	if ref.Type != engine.PeerChat && ref.Type != engine.PeerChannel {
		return engine.Peer{}, nil, engine.ErrInvalid
	}
	p, ip, err := e.writePeer(ctx, ref)
	if err != nil {
		return p, nil, err
	}
	if (ref.Type == engine.PeerChat && p.Kind != engine.KindGroup) ||
		(ref.Type == engine.PeerChannel && p.Kind != engine.KindSupergroup && p.Kind != engine.KindChannel) || p.MigratedTo != nil {
		// Never silently write into the destination of a migrated basic group.
		return p, nil, engine.ErrInvalid
	}
	return p, ip, nil
}

func groupInputChannel(ip tg.InputPeerClass) *tg.InputChannel {
	v := ip.(*tg.InputPeerChannel) // groupTarget validated the peer type.
	return &tg.InputChannel{ChannelID: v.ChannelID, AccessHash: v.AccessHash}
}

func groupInputUser(ip tg.InputPeerClass) (tg.InputUserClass, error) {
	switch v := ip.(type) {
	case *tg.InputPeerSelf:
		return &tg.InputUserSelf{}, nil
	case *tg.InputPeerUser:
		return &tg.InputUser{UserID: v.UserID, AccessHash: v.AccessHash}, nil
	default:
		return nil, engine.ErrInvalid
	}
}

// groupPeerIdentity excludes display, counters and read metadata. Inputs bind
// persisted access hashes separately; a name change does not change the RPC.
func groupPeerIdentity(p engine.Peer) engine.Peer {
	return engine.Peer{Ref: p.Ref, Kind: p.Kind, Access: p.Access, MigratedTo: p.MigratedTo}
}

type groupMembership struct {
	Known, Present            bool
	Role                      string
	AdminRights, BannedRights uint32
	BannedUntil               int
	CanEdit                   bool
}

func groupMemberState(g *engine.Group, ref engine.PeerRef) groupMembership {
	if g == nil {
		return groupMembership{}
	}
	for _, member := range g.Participants {
		if member.Peer == ref {
			return groupMembership{Known: true, Present: true, Role: member.Role, AdminRights: member.AdminRights, BannedRights: member.BannedRights, BannedUntil: member.BannedUntil, CanEdit: member.CanEdit}
		}
	}
	return groupMembership{Known: !g.Partial}
}

func (e *Engine) groupSnapshot(ctx context.Context, ref engine.PeerRef) (*engine.Group, error) {
	reader, ok := e.cfg.Store.(interface {
		ReadGroup(context.Context, engine.AccountID, engine.PeerRef) (engine.Group, error)
	})
	if !ok {
		return nil, nil
	}
	g, err := reader.ReadGroup(ctx, e.cfg.Account, ref)
	if errors.Is(err, engine.ErrPeerNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

type groupUpdateIntent struct{ Title, About *string }

func (e *Engine) groupApprove(ctx context.Context, opts engine.WriteOptions, p *engine.Preview, ip tg.InputPeerClass, intent any) (bool, error) {
	a, err := e.Account(ctx)
	if err != nil {
		return false, err
	}
	g, err := e.groupSnapshot(ctx, p.Target.Ref)
	if err != nil {
		return false, err
	}
	// Bind only state this operation acts on. Snapshot age, participant counts,
	// other members and presentation metadata never alter the approved RPC.
	state := struct {
		Known, AmAdmin, Owner bool
		Self                  groupMembership
		Users                 []groupMembership
		OldTitle, OldAbout    *string
		AboutKnown            bool
	}{Self: groupMemberState(g, engine.PeerRef{Type: engine.PeerUser, ID: a.SelfID})}
	if g != nil {
		state.Known, state.AmAdmin = true, g.AmAdmin
		state.Owner = g.Owner != nil && *g.Owner == (engine.PeerRef{Type: engine.PeerUser, ID: a.SelfID})
	}
	for _, user := range p.Users {
		state.Users = append(state.Users, groupMemberState(g, user.Ref))
	}
	if update, ok := intent.(groupUpdateIntent); ok {
		if update.Title != nil {
			title := p.Target.Title
			state.OldTitle = &title
		}
		if update.About != nil && g != nil {
			state.OldAbout, state.AboutKnown = &g.About, true
		}
	}
	p.Fingerprint, err = canonicalWriteFingerprint(struct {
		Action  string
		Account engine.AccountID
		SelfID  int64
		Target  engine.Peer
		Input   tg.InputPeerClass
		Intent  any
		State   any
		Key     string
	}{p.Action, e.cfg.Account, a.SelfID, groupPeerIdentity(p.Target), ip, intent, state, opts.IdempotencyKey})
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !opts.DryRun {
		if err := e.require(); err != nil {
			return false, err
		}
	}
	return e.checkWritePreview(opts, p)
}

// Recheck between RPCs: a multi-user or title+about operation is not an atomic
// Telegram transaction and must stop if cancellation or mode changes intervene.
func (e *Engine) groupCanRPC(ctx context.Context) error {
	if err := e.groupWriteStart(ctx, engine.WriteOptions{}); err != nil {
		return err
	}
	return e.require()
}

func groupRPCError(err error, method string) error {
	err = rpcError(err)
	var r *engine.RPCError
	if errors.As(err, &r) {
		r.Method = method
		switch r.Type {
		case "CHAT_ADMIN_REQUIRED", "RIGHT_FORBIDDEN", "CHAT_WRITE_FORBIDDEN", "USER_ADMIN_INVALID", "CHAT_ADMIN_INVITE_REQUIRED":
			r.Kind = engine.ErrPermission
		case "CHANNEL_PRIVATE", "CHAT_FORBIDDEN", "USER_BANNED_IN_CHANNEL":
			r.Kind = engine.ErrNoAccess
		}
	}
	var f *engine.FloodWaitError
	if errors.As(err, &f) {
		f.Method = method
	}
	return err
}

// groupReconciliationError distinguishes local ingestion failure from a refused
// RPC. In particular, never continue a multi-user moderation batch after this.
// Unwrap preserves errors.Is/As for the actual store or context error.
type groupReconciliationError struct{ error }

func (e *groupReconciliationError) Unwrap() error { return e.error }

func (e *Engine) groupUpdates(ctx context.Context, updates tg.UpdatesClass) error {
	if updates == nil {
		return &groupReconciliationError{engine.ErrInvalid}
	}
	// The manager is the sole authoritative update/PTS ingestion path. Do not
	// fabricate participants, title, about, or a local leave from RPC acceptance.
	if err := e.manager.Handle(ctx, updates); err != nil {
		return &groupReconciliationError{err}
	}
	return nil
}

// Keep acceptance evidence even when a later field/user RPC fails. Local
// reconciliation uses the common accepted-write warning; a remote partial
// refusal must not be mislabeled as a local failure.
func groupWriteError(result engine.WriteResult, err error) (engine.WriteResult, error) {
	if err == nil || result.Affected == 0 {
		return result, err
	}
	var local *groupReconciliationError
	if errors.As(err, &local) {
		return acceptedWriteError(result, err)
	}
	const warning = "Telegram accepted part of the write; inspect affected and per_user; do not blindly retry"
	if result.Note != "" {
		result.Note += "; "
	}
	result.Note += warning
	return result, fmt.Errorf("%s: %w", warning, err)
}

const groupAcceptedNote = "accepted by Telegram; local group state follows server updates"
