package mcp

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

var peerUsername = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// resolvePeer is the single human-facing conversation resolver. Reserved aliases
// always identify Saved Messages; message-key parsers remain strictly typed.
// Local snapshots are exhaustive so a truncated search cannot hide ambiguity.
func (s *Server) resolvePeer(ctx context.Context, input string) (engine.PeerRef, error) {
	input = strings.TrimSpace(input)
	if err := ctx.Err(); err != nil {
		return engine.PeerRef{}, err
	}
	if input == "" {
		return engine.PeerRef{}, fmt.Errorf("chat is required: %w", engine.ErrInvalid)
	}
	// Canonical identities need no engine lookup, including while disconnected.
	if strings.HasPrefix(input, "user:") || strings.HasPrefix(input, "chat:") || strings.HasPrefix(input, "channel:") {
		return parsePeer(input)
	}
	if s.eng == nil {
		return engine.PeerRef{}, engine.ErrNotAuthorized
	}
	switch strings.ToLower(input) {
	case "me", "self", "saved":
		a, err := s.eng.Account(ctx)
		if err != nil {
			return engine.PeerRef{}, err
		}
		if a.SelfID <= 0 {
			return engine.PeerRef{}, engine.ErrPeerNotFound
		}
		return engine.PeerRef{Type: engine.PeerUser, ID: a.SelfID}, nil
	}
	query := input
	lower := strings.ToLower(query)
	if strings.HasPrefix(lower, "t.me/") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		link := query
		if strings.HasPrefix(lower, "t.me/") {
			link = "https://" + query
		}
		u, err := url.Parse(link)
		if err != nil || !strings.EqualFold(u.Host, "t.me") || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return engine.PeerRef{}, engine.ErrInvalid
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) > 0 && parts[0] == "s" {
			parts = parts[1:]
		}
		if len(parts) == 0 || len(parts) > 2 || !peerUsername.MatchString(parts[0]) {
			return engine.PeerRef{}, engine.ErrInvalid
		}
		if len(parts) == 2 {
			if id, err := strconv.ParseInt(parts[1], 10, 64); err != nil || id <= 0 {
				return engine.PeerRef{}, engine.ErrInvalid
			}
		}
		query = "@" + parts[0]
	}
	username := strings.HasPrefix(query, "@")
	if username && !peerUsername.MatchString(strings.TrimPrefix(query, "@")) {
		return engine.PeerRef{}, engine.ErrInvalid
	}
	var peers []engine.Peer
	local := s.readStore
	if local == nil {
		local, _ = s.eng.(ReadStore)
	}
	if local != nil {
		a, err := s.eng.Account(ctx)
		if err != nil {
			return engine.PeerRef{}, err
		}
		peers, err = local.ReadPeers(ctx, a.ID)
		if err != nil {
			return engine.PeerRef{}, err
		}
	} else {
		candidates, err := s.eng.Resolve(ctx, engine.ResolveRequest{Query: query, Limit: 1000})
		if err != nil {
			return engine.PeerRef{}, err
		}
		for _, c := range candidates {
			peers = append(peers, c.Peer)
		}
	}
	var exact, partial []engine.Peer
	numeric, numericErr := strconv.ParseInt(query, 10, 64)
	for _, p := range peers {
		if username {
			name := strings.TrimPrefix(query, "@")
			match := strings.EqualFold(name, strings.TrimPrefix(p.Username, "@"))
			for _, alias := range p.Usernames {
				match = match || strings.EqualFold(name, strings.TrimPrefix(alias, "@"))
			}
			if match {
				exact = append(exact, p)
			}
		} else if numericErr == nil {
			if numeric > 0 && p.Ref.ID == numeric {
				exact = append(exact, p)
			}
		} else {
			name := peopleName(p)
			if strings.EqualFold(name, query) {
				exact = append(exact, p)
			} else if strings.Contains(strings.ToLower(name), strings.ToLower(query)) {
				partial = append(partial, p)
			}
		}
	}
	if len(exact) == 0 {
		exact = partial
	}
	// Only explicit usernames may require remote discovery. Never turn a missing
	// title or a reserved alias into a network lookup.
	if len(exact) == 0 && username {
		candidates, err := s.eng.Resolve(ctx, engine.ResolveRequest{Query: query, Network: true, Limit: 1000})
		if err != nil {
			return engine.PeerRef{}, err
		}
		for _, c := range candidates {
			exact = append(exact, c.Peer)
		}
	}
	seen := map[engine.PeerRef]bool{}
	unique := exact[:0]
	for _, p := range exact {
		if !seen[p.Ref] {
			seen[p.Ref] = true
			unique = append(unique, p)
		}
	}
	if len(unique) == 1 {
		return unique[0].Ref, nil
	}
	if len(unique) == 0 {
		return engine.PeerRef{}, fmt.Errorf("no chat matches %q: %w", input, engine.ErrPeerNotFound)
	}
	sort.Slice(unique, func(i, j int) bool { return peerID(unique[i].Ref) < peerID(unique[j].Ref) })
	names := make([]string, 0, len(unique))
	candidates := make([]engine.Candidate, 0, len(unique))
	for _, p := range unique {
		names = append(names, fmt.Sprintf("%q (%s)", peopleName(p), peerID(p.Ref)))
		candidates = append(candidates, engine.Candidate{Peer: p, Exact: true, Source: "store"})
	}
	return engine.PeerRef{}, fmt.Errorf("ambiguous chat %q; use a typed ID: %s: %w", input, strings.Join(names, ", "), &engine.AmbiguousError{Query: input, Candidates: candidates})
}
