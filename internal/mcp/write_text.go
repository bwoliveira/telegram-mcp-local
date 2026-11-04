package mcp

import (
	"context"
	"strings"
	"unicode"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Member matching and label selection are adapted from resolve_member and
// mention_entities in BrOrlandi/telegram-cli (MIT, Copyright Bruno Orlandi).
// The engine creates mention_name entities and computes their UTF-16 offsets;
// selecting explicit labels here preserves the original first-name/prefix rules
// without depending on a particular engine's default label placement.
func (s *Server) textMentions(ctx context.Context, chat engine.PeerRef, text string, refs []string) (string, []engine.Mention, map[string]any) {
	var users []engine.Peer
	seen := make(map[engine.PeerRef]bool)
	for _, ref := range refs {
		var user engine.Peer
		if ref == "" || ref != strings.TrimSpace(ref) {
			return "", nil, toolError("mention must be a non-empty user reference without surrounding whitespace")
		}
		if chat.Type != engine.PeerUser && !strings.HasPrefix(ref, "@") && !strings.Contains(ref, ":") {
			group, err := s.eng.RefreshGroup(ctx, engine.GroupRequest{Group: chat, Limit: 200})
			if err != nil {
				return "", nil, engineError(err)
			}
			// A truncated participant list cannot prove a name is unique.
			if group.Partial {
				return "", nil, toolError("member list is partial; use @username or user:ID for mentions")
			}
			var matches []engine.Participant
			target := foldMemberName(ref)
			for _, member := range group.Participants {
				first, _, _ := strings.Cut(member.Name, " ")
				for _, name := range []string{member.Name, first, member.Username} {
					n := foldMemberName(name)
					if n != "" && strings.HasPrefix(n, target) {
						matches = append(matches, member)
						break
					}
				}
			}
			if len(matches) == 0 {
				return "", nil, toolError("no member matches %q in this chat; use @username or user:ID", ref)
			}
			if len(matches) != 1 {
				return "", nil, toolError("ambiguous member %q; use @username or user:ID", ref)
			}
			ref = peerID(matches[0].Peer)
		}
		p, fail := s.recipient(ctx, ref)
		if fail != nil {
			return "", nil, fail
		}
		if p.Type != engine.PeerUser {
			return "", nil, toolError("mentions must identify users")
		}
		candidates, err := s.eng.Resolve(ctx, engine.ResolveRequest{Query: peerID(p), Network: true, Limit: 2})
		if err != nil {
			return "", nil, engineError(err)
		}
		if len(candidates) != 1 || candidates[0].Peer.Ref != p {
			return "", nil, toolError("mention user could not be resolved uniquely")
		}
		user = candidates[0].Peer
		if !seen[p] {
			users = append(users, user)
			seen[p] = true
		}
	}
	var prefix []string
	labels := func(u engine.Peer) []string {
		var out []string
		if u.Username != "" {
			out = append(out, "@"+u.Username)
		}
		if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
			out = append(out, name)
		}
		if u.FirstName != "" {
			out = append(out, u.FirstName)
		}
		return out
	}
	for _, user := range users {
		names := labels(user)
		if len(names) == 0 {
			return "", nil, toolError("mention user has no display name or username")
		}
		found := false
		for _, name := range names {
			if strings.Contains(text, name) {
				found = true
				break
			}
		}
		if !found {
			prefix = append(prefix, names[0])
		}
	}
	if len(prefix) > 0 {
		text = strings.Join(prefix, " ") + " " + text
	}
	var mentions []engine.Mention
	for _, user := range users {
		for _, name := range labels(user) {
			if strings.Contains(text, name) {
				mentions = append(mentions, engine.Mention{User: user.Ref, Text: name})
				break
			}
		}
	}
	return text, mentions, nil
}

func foldMemberName(s string) string {
	decomposed := norm.NFKD.String(s)
	return strings.TrimSpace(cases.Fold().String(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, decomposed)))
}
