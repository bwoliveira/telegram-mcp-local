package mcp

import (
	"context"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func (s *Server) groupPeer(ctx context.Context, a arguments) (engine.PeerRef, map[string]any) {
	p, err := s.resolvePeer(ctx, a.GroupJID)
	if err != nil {
		return p, toolError("group_id: %v", err)
	}
	if p.Type == engine.PeerUser {
		return p, toolError("group_id must identify a chat or channel")
	}
	return p, nil
}
func (s *Server) manageParticipants(ctx context.Context, a arguments) map[string]any {
	p, fail := s.groupPeer(ctx, a)
	if fail != nil {
		return fail
	}
	if !map[string]bool{"add": true, "remove": true, "ban": true, "promote": true, "demote": true}[a.Action] {
		return toolError("action must be add, remove, ban, promote or demote")
	}
	if len(a.Participants) == 0 || len(a.Participants) > 50 {
		return toolError("participants must list one to fifty users")
	}
	var users []engine.PeerRef
	seen := make(map[engine.PeerRef]bool)
	for _, v := range a.Participants {
		u, fail := s.recipient(ctx, v)
		if fail != nil {
			return fail
		}
		if u.Type != engine.PeerUser {
			return toolError("participants must identify users")
		}
		if seen[u] {
			return toolError("participants must not contain duplicate users")
		}
		seen[u] = true
		users = append(users, u)
	}
	return s.writeRequest(ctx, a, engine.ParticipantsRequest{Group: p, Action: engine.ParticipantsAction(a.Action), Users: users})
}
func (s *Server) updateGroup(ctx context.Context, a arguments) map[string]any {
	p, fail := s.groupPeer(ctx, a)
	if fail != nil {
		return fail
	}
	var title *string
	if strings.TrimSpace(a.Name) != "" {
		title = &a.Name
	}
	if title == nil && a.Description == nil {
		return toolError("give a new name, description or both")
	}
	return s.writeRequest(ctx, a, engine.UpdateGroupRequest{Group: p, Title: title, About: a.Description})
}
func (s *Server) groupInviteLink(ctx context.Context, a arguments) map[string]any {
	p, fail := s.groupPeer(ctx, a)
	if fail != nil {
		return fail
	}
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	if a.Reset || a.Revoke {
		return s.writeRequest(ctx, a, engine.InviteLinkRequest{Group: p, Revoke: true})
	}
	result, err := s.eng.InviteLink(ctx, engine.InviteLinkRequest{Group: p, Revoke: false, WriteOptions: engine.WriteOptions{DryRun: true}})
	if err != nil {
		return engineError(err)
	}
	return textResult(result, false)
}
func (s *Server) leaveGroup(ctx context.Context, a arguments) map[string]any {
	p, fail := s.groupPeer(ctx, a)
	if fail != nil {
		return fail
	}
	return s.writeRequest(ctx, a, engine.LeaveRequest{Group: p})
}
