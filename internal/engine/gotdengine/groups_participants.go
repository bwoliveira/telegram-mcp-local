package gotdengine

import (
	"context"
	"errors"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

type groupUser struct {
	Peer  engine.Peer
	Input tg.InputPeerClass
	User  tg.InputUserClass
}

func (e *Engine) ManageParticipants(ctx context.Context, r engine.ParticipantsRequest) (result engine.ParticipantsResult, retErr error) {
	if err := e.groupWriteStart(ctx, r.WriteOptions); err != nil {
		return result, err
	}
	switch r.Action {
	case engine.ParticipantsAdd, engine.ParticipantsRemove, engine.ParticipantsBan, engine.ParticipantsPromote, engine.ParticipantsDemote:
	default:
		return result, engine.ErrInvalid
	}
	if len(r.Users) == 0 {
		return result, engine.ErrInvalid
	}
	target, ip, err := e.groupTarget(ctx, r.Group)
	if err != nil {
		return result, err
	}
	p := &engine.Preview{Action: "participants", Target: target, Text: string(r.Action), Reversible: r.Action != engine.ParticipantsBan && r.Action != engine.ParticipantsRemove}
	var users []groupUser
	seen := make(map[engine.PeerRef]bool)
	for _, ref := range r.Users {
		if ref.Type != engine.PeerUser || seen[ref] {
			return result, engine.ErrInvalid
		}
		seen[ref] = true
		peer, input, err := e.writePeer(ctx, ref)
		if err != nil {
			return result, err
		}
		user, err := groupInputUser(input)
		if err != nil {
			return result, err
		}
		users = append(users, groupUser{Peer: peer, Input: input, User: user})
		p.Users = append(p.Users, peer)
	}
	if r.Action == engine.ParticipantsPromote && r.Group.Type == engine.PeerChannel {
		p.Warnings = append(p.Warnings, "promotes with minimal administrator status (admin log/member visibility and slow-mode bypass); grants no posting, deletion, ban, invite, or add-admin rights")
	}
	if r.Action == engine.ParticipantsBan {
		p.Warnings = append(p.Warnings, "banning participants is an irreversible moderation action; removed access cannot be restored automatically")
	}
	if r.Action == engine.ParticipantsBan && r.Group.Type == engine.PeerChat {
		p.Warnings = append(p.Warnings, "basic groups support removal only; this does not permanently prevent rejoining")
	}
	if r.Action == engine.ParticipantsRemove && r.Group.Type == engine.PeerChannel {
		p.Warnings = append(p.Warnings, "removes by banning then unbanning; if the second RPC fails the user remains banned")
	}
	// The RPC users retain their resolved snapshots for presentation. Hash only
	// identity/access and inputs; membership permissions are bound by groupApprove.
	approvedUsers := make([]groupUser, len(users))
	for i, user := range users {
		approvedUsers[i] = user
		approvedUsers[i].Peer = groupPeerIdentity(user.Peer)
	}
	dry, err := e.groupApprove(ctx, r.WriteOptions, p, ip, struct {
		Action engine.ParticipantsAction
		Users  []groupUser
	}{r.Action, approvedUsers})
	if err != nil {
		return result, err
	}
	result.Preview = p
	if dry {
		result.DryRun = true
		return result, nil
	}
	result.PerUser = make(map[string]string)
	completed := 0
	defer func() {
		// Completion means every requested user's RPC sequence was accepted,
		// not that local reconciliation succeeded. A kick needs both RPCs.
		result.Done = completed == len(users)
		result.WriteResult, retErr = groupWriteError(result.WriteResult, retErr)
	}()
	var failures []error
	for _, u := range users {
		if err := e.groupCanRPC(ctx); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		changed, complete, err := e.groupParticipantRPC(ctx, r.Group, ip, r.Action, u)
		if changed {
			result.Affected++
		}
		if complete {
			completed++
		}
		key := "user:" + itoa(u.Peer.Ref.ID)
		if err == nil {
			result.PerUser[key] = "ok"
			continue
		}
		result.PerUser[key] = err.Error()
		failures = append(failures, err)
		var flood *engine.FloodWaitError
		var local *groupReconciliationError
		if errors.As(err, &local) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, engine.ErrPaused) || errors.Is(err, engine.ErrRevoked) || errors.Is(err, engine.ErrNotAuthorized) || errors.Is(err, engine.ErrReadOnly) || errors.As(err, &flood) {
			result.Note = "operation stopped; inspect per_user and affected for partial acceptance"
			return result, errors.Join(failures...)
		}
	}
	result.Note = groupAcceptedNote
	if completed != len(users) {
		result.Note = "not all participants were updated; inspect per_user and affected for partial acceptance"
	}
	return result, errors.Join(failures...)
}

func groupMissingInvitee(got *tg.MessagesInvitedUsers, id int64, method string) error {
	for _, m := range got.MissingInvitees {
		if m.UserID != id {
			continue
		}
		typ := "USER_PRIVACY_RESTRICTED"
		if m.PremiumWouldAllowInvite {
			typ = "PREMIUM_ACCOUNT_REQUIRED"
		}
		// Never silently export a link or message someone as a privacy workaround.
		return &engine.RPCError{Type: typ, Method: method, Kind: engine.ErrPermission}
	}
	return nil
}

// changed records any accepted mutation for Affected. complete separately
// records acceptance of the entire requested operation (both ban and unban
// for channel removal), even if ingestion of its final response fails.
func (e *Engine) groupParticipantRPC(ctx context.Context, ref engine.PeerRef, ip tg.InputPeerClass, action engine.ParticipantsAction, u groupUser) (changed, complete bool, retErr error) {
	if action == engine.ParticipantsAdd {
		var got *tg.MessagesInvitedUsers
		var err error
		method := "messages.addChatUser"
		if ref.Type == engine.PeerChat {
			got, err = e.api.MessagesAddChatUser(ctx, &tg.MessagesAddChatUserRequest{ChatID: ref.ID, UserID: u.User, FwdLimit: 0})
		} else {
			method = "channels.inviteToChannel"
			got, err = e.api.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: groupInputChannel(ip), Users: []tg.InputUserClass{u.User}})
		}
		if err != nil {
			return false, false, groupRPCError(err, method)
		}
		if got == nil {
			return false, false, engine.ErrInvalid
		}
		missing := groupMissingInvitee(got, u.Peer.Ref.ID, method)
		if err := e.groupUpdates(ctx, got.Updates); err != nil {
			return missing == nil, missing == nil, errors.Join(missing, err)
		}
		return missing == nil, missing == nil, missing
	}
	if ref.Type == engine.PeerChat {
		switch action {
		case engine.ParticipantsRemove, engine.ParticipantsBan:
			got, err := e.api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{ChatID: ref.ID, UserID: u.User, RevokeHistory: false})
			if err != nil {
				return false, false, groupRPCError(err, "messages.deleteChatUser")
			}
			return true, true, e.groupUpdates(ctx, got)
		case engine.ParticipantsPromote, engine.ParticipantsDemote:
			ok, err := e.api.MessagesEditChatAdmin(ctx, &tg.MessagesEditChatAdminRequest{ChatID: ref.ID, UserID: u.User, IsAdmin: action == engine.ParticipantsPromote})
			if err != nil {
				return false, false, groupRPCError(err, "messages.editChatAdmin")
			}
			if !ok {
				return false, false, engine.ErrPermission
			}
			return true, true, nil
		}
	}
	channel := groupInputChannel(ip)
	switch action {
	case engine.ParticipantsRemove, engine.ParticipantsBan:
		got, err := e.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: channel, Participant: u.Input, BannedRights: tg.ChatBannedRights{ViewMessages: true, UntilDate: 0}})
		if err != nil {
			return false, false, groupRPCError(err, "channels.editBanned")
		}
		if err := e.groupUpdates(ctx, got); err != nil {
			return true, action == engine.ParticipantsBan, err
		}
		if action == engine.ParticipantsRemove {
			if err := e.groupCanRPC(ctx); err != nil {
				return true, false, err
			}
			got, err = e.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: channel, Participant: u.Input, BannedRights: tg.ChatBannedRights{}})
			if err != nil {
				return true, false, groupRPCError(err, "channels.editBanned")
			}
			if err := e.groupUpdates(ctx, got); err != nil {
				return true, true, err
			}
		}
		return true, true, nil
	case engine.ParticipantsPromote, engine.ParticipantsDemote:
		// The public contract has no rights argument. Use least privilege rather
		// than implicitly grant broad rights or authority to promote more admins.
		rights := tg.ChatAdminRights{Other: action == engine.ParticipantsPromote}
		got, err := e.api.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: channel, UserID: u.User, AdminRights: rights})
		if err != nil {
			return false, false, groupRPCError(err, "channels.editAdmin")
		}
		return true, true, e.groupUpdates(ctx, got)
	}
	return false, false, engine.ErrInvalid
}
