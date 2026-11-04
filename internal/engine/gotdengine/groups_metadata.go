package gotdengine

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

func (e *Engine) UpdateGroup(ctx context.Context, r engine.UpdateGroupRequest) (result engine.WriteResult, retErr error) {
	defer func() { result, retErr = groupWriteError(result, retErr) }()
	if err := e.groupWriteStart(ctx, r.WriteOptions); err != nil {
		return result, err
	}
	if r.Title == nil && r.About == nil {
		return result, engine.ErrInvalid
	}
	if r.Title != nil && (!utf8.ValidString(*r.Title) || strings.TrimSpace(*r.Title) == "" || writeUTF16Len(*r.Title) > 128) {
		return result, engine.ErrInvalid
	}
	if r.About != nil && (!utf8.ValidString(*r.About) || writeUTF16Len(*r.About) > 255) {
		return result, engine.ErrInvalid
	}
	target, ip, err := e.groupTarget(ctx, r.Group)
	if err != nil {
		return result, err
	}
	p := &engine.Preview{Action: "update_group", Target: target, Reversible: true}
	if r.Title != nil {
		p.Text = "title: " + *r.Title
	}
	if r.About != nil {
		if r.Title != nil {
			p.Text += "\n"
		}
		p.Text += "about: " + *r.About
	}
	if r.Title != nil && r.About != nil {
		p.Warnings = append(p.Warnings, "title and description require separate RPCs; partial acceptance is possible")
	}
	dry, err := e.groupApprove(ctx, r.WriteOptions, p, ip, groupUpdateIntent{r.Title, r.About})
	if err != nil {
		return result, err
	}
	result.Preview = p
	if dry {
		result.DryRun = true
		return result, nil
	}
	result.Note = "operation may be partially accepted; affected counts accepted fields; local state follows server updates"
	if r.Title != nil {
		if err := e.groupCanRPC(ctx); err != nil {
			return result, err
		}
		var got tg.UpdatesClass
		var err error
		method := "messages.editChatTitle"
		if r.Group.Type == engine.PeerChat {
			got, err = e.api.MessagesEditChatTitle(ctx, &tg.MessagesEditChatTitleRequest{ChatID: r.Group.ID, Title: *r.Title})
		} else {
			method = "channels.editTitle"
			got, err = e.api.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{Channel: groupInputChannel(ip), Title: *r.Title})
		}
		if err != nil {
			return result, groupRPCError(err, method)
		}
		result.Affected++
		// Done records RPC acceptance, independently of local ingestion. With
		// an about field still pending, only part of the request was accepted.
		result.Done = r.About == nil
		if err := e.groupUpdates(ctx, got); err != nil {
			return result, err
		}
	}
	if r.About != nil {
		if err := e.groupCanRPC(ctx); err != nil {
			return result, err
		}
		ok, err := e.api.MessagesEditChatAbout(ctx, &tg.MessagesEditChatAboutRequest{Peer: ip, About: *r.About})
		if err != nil {
			return result, groupRPCError(err, "messages.editChatAbout")
		}
		if !ok {
			return result, engine.ErrPermission
		}
		result.Affected++
	}
	result.Done = true
	result.Note = groupAcceptedNote
	return result, nil
}

func (e *Engine) InviteLink(ctx context.Context, r engine.InviteLinkRequest) (engine.InviteLinkResult, error) {
	var result engine.InviteLinkResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if r.Revoke {
		if err := e.groupWriteStart(ctx, r.WriteOptions); err != nil {
			return result, err
		}
	} else if err := e.require(); err != nil {
		return result, err
	}
	target, ip, err := e.groupTarget(ctx, r.Group)
	if err != nil {
		return result, err
	}
	if !r.Revoke {
		// Reading an existing primary link must NEVER export/create/revoke one,
		// even when none is present. DryRun is deliberately ignored for this read.
		var got *tg.MessagesChatFull
		var err error
		method := "messages.getFullChat"
		if r.Group.Type == engine.PeerChat {
			got, err = e.api.MessagesGetFullChat(ctx, r.Group.ID)
		} else {
			method = "channels.getFullChannel"
			got, err = e.api.ChannelsGetFullChannel(ctx, groupInputChannel(ip))
		}
		if err != nil {
			if saveErr := e.persistMetadataAccessLoss(ctx, r.Group, err); saveErr != nil {
				return result, saveErr
			}
			return result, groupRPCError(err, method)
		}
		if got == nil {
			return result, engine.ErrInvalid
		}
		var invite tg.ExportedChatInviteClass
		switch full := got.FullChat.(type) {
		case *tg.ChatFull:
			invite = full.ExportedInvite
		case *tg.ChannelFull:
			invite = full.ExportedInvite
		default:
			return result, engine.ErrInvalid
		}
		groupInviteFields(&result, invite)
		if result.Link == "" {
			result.Note = "no primary invite link is available; no link was created"
		}
		return result, nil
	}
	p := &engine.Preview{Action: "invite_link", Target: target, Reversible: false, Warnings: []string{"revokes the previous permanent invite link and creates a replacement; the old link cannot be restored"}}
	dry, err := e.groupApprove(ctx, r.WriteOptions, p, ip, struct{ Revoke bool }{true})
	if err != nil {
		return result, err
	}
	result.Preview = p
	if dry {
		result.DryRun = true
		return result, nil
	}
	if err := e.groupCanRPC(ctx); err != nil {
		return result, err
	}
	// This legacy flag is Telegram's export-and-revoke-primary operation, the
	// operation requested by Revoke in the engine contract (PLANO 4.3).
	got, err := e.api.MessagesExportChatInvite(ctx, &tg.MessagesExportChatInviteRequest{Peer: ip, LegacyRevokePermanent: true})
	if err != nil {
		return result, groupRPCError(err, "messages.exportChatInvite")
	}
	groupInviteFields(&result, got)
	result.Revoked = true // refers to the old primary link, not the returned replacement.
	result.Done = true
	result.Affected = 1
	result.Note = "primary invite link replaced by Telegram; the old link is revoked"
	return result, nil
}

func groupInviteFields(result *engine.InviteLinkResult, invite tg.ExportedChatInviteClass) {
	v, ok := invite.(*tg.ChatInviteExported)
	if !ok || v == nil {
		return
	}
	result.Link = v.Link
	result.Revoked = v.Revoked
	result.RequiresApproval = v.RequestNeeded
	if v.ExpireDate > 0 {
		expires := time.Unix(int64(v.ExpireDate), 0)
		result.ExpiresAt = &expires
	}
}

func (e *Engine) Leave(ctx context.Context, r engine.LeaveRequest) (engine.WriteResult, error) {
	var result engine.WriteResult
	if err := e.groupWriteStart(ctx, r.WriteOptions); err != nil {
		return result, err
	}
	target, ip, err := e.groupTarget(ctx, r.Group)
	if err != nil {
		return result, err
	}
	p := &engine.Preview{Action: "leave", Target: target, Reversible: false, Warnings: []string{"leaves this group or channel; rejoining may require an invitation and is not guaranteed"}}
	dry, err := e.groupApprove(ctx, r.WriteOptions, p, ip, struct{ Leave bool }{true})
	if err != nil {
		return result, err
	}
	result.Preview = p
	if dry {
		result.DryRun = true
		return result, nil
	}
	if err := e.groupCanRPC(ctx); err != nil {
		return result, err
	}
	var got tg.UpdatesClass
	method := "messages.deleteChatUser"
	if r.Group.Type == engine.PeerChat {
		got, err = e.api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{ChatID: r.Group.ID, UserID: &tg.InputUserSelf{}, RevokeHistory: false})
	} else {
		method = "channels.leaveChannel"
		got, err = e.api.ChannelsLeaveChannel(ctx, groupInputChannel(ip))
	}
	if err != nil {
		return result, groupRPCError(err, method)
	}
	result.Affected = 1
	result.Done = true
	result.Note = groupAcceptedNote
	if err := e.groupUpdates(ctx, got); err != nil {
		return groupWriteError(result, err)
	}
	return result, nil
}
