package gotdengine

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// ResolveContacts checks identities without changing Telegram contacts. In
// particular, phone lookup must never fall back to contacts.importContacts:
// an undiscoverable phone is not permission to add it to the address book.
func (e *Engine) ResolveContacts(ctx context.Context, queries []string) ([]engine.ContactResolution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.require(); err != nil {
		return nil, err
	}
	out := make([]engine.ContactResolution, 0, len(queries))
	for _, original := range queries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		query, kind, valid := contactsQuery(original)
		result := engine.ContactResolution{Query: original, Reason: "not_found"}
		if !valid {
			result.Reason = "invalid"
			out = append(out, result)
			continue
		}

		// Resolve's store search includes fuzzy matches and labels every
		// candidate Exact. Check the identity ourselves. A full page may
		// conceal duplicate exact names, so it cannot prove uniqueness.
		const lookupLimit = 1000 // maximum supported by the production store
		candidates, err := e.Resolve(ctx, engine.ResolveRequest{Query: query, Limit: lookupLimit})
		if err != nil {
			return nil, err
		}
		var exact []engine.Peer
		for _, c := range candidates {
			if contactsExact(c.Peer, query, kind) {
				exact = append(exact, c.Peer)
			}
		}
		if len(exact) == 1 && len(candidates) < lookupLimit {
			result.Found, result.Peer, result.Reason = true, &exact[0], ""
			out = append(out, result)
			continue
		}
		if len(exact) > 1 || (kind != "username" && kind != "phone") {
			out = append(out, result)
			continue
		}

		if err := e.require(); err != nil {
			return nil, err
		}
		var resolved *tg.ContactsResolvedPeer
		if kind == "phone" {
			resolved, err = e.api.ContactsResolvePhone(ctx, query)
		} else {
			resolved, err = e.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: strings.TrimPrefix(query, "@")})
		}
		if err != nil {
			if reason := contactsFailureReason(err); reason != "" {
				result.Reason = reason
				out = append(out, result)
				continue
			}
			return nil, rpcError(err)
		}
		// Persist every learned peer and hash before exposing the resolved
		// identity. Returning the stored peer also preserves enriched metadata.
		if _, err := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceRefresh, Peers: peerUpserts(resolved.Users, resolved.Chats)}); err != nil {
			return nil, err
		}
		ref := peerRef(resolved.Peer)
		if ref.ID > 0 {
			p, ok, err := e.cfg.Store.LookupPeer(ctx, e.cfg.Account, ref)
			if err != nil {
				return nil, err
			}
			if ok {
				result.Found, result.Peer, result.Reason = true, &p, ""
			}
		}
		out = append(out, result)
	}
	return out, nil
}

// contactsQuery accepts explicit usernames/links and international phone
// numbers for network lookup. Unadorned names and typed peers are store-only.
func contactsQuery(original string) (query, kind string, valid bool) {
	query = strings.TrimSpace(original)
	if query == "" {
		return query, "", false
	}
	if strings.HasPrefix(query, "t.me/") || strings.Contains(query, "://") {
		link := query
		if strings.HasPrefix(link, "t.me/") {
			link = "https://" + link
		}
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host != "t.me" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return query, "", false
		}
		query = "@" + strings.TrimPrefix(u.Path, "/")
	}
	if strings.HasPrefix(query, "@") {
		name := strings.TrimPrefix(query, "@")
		if len(name) == 0 || len(name) > 32 {
			return query, "username", false
		}
		for i, c := range name {
			letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
			if !letter && (i == 0 || c != '_' && (c < '0' || c > '9')) {
				return query, "username", false
			}
		}
		return query, "username", true
	}
	if strings.HasPrefix(query, "+") {
		digits := strings.TrimPrefix(query, "+")
		return query, "phone", len(digits) > 0 && len(digits) <= 15 && digits[0] != '0' && contactsDigits(digits)
	}
	if strings.Contains(query, ":") {
		parts := strings.Split(query, ":")
		if len(parts) != 2 || (parts[0] != "user" && parts[0] != "chat" && parts[0] != "channel") || !contactsDigits(parts[1]) {
			return query, "peer", false
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		return query, "peer", err == nil && id > 0
	}
	return query, "name", true
}

func contactsDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func contactsExact(p engine.Peer, query, kind string) bool {
	switch kind {
	case "peer":
		parts := strings.SplitN(query, ":", 2)
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		return p.Ref.Type == engine.PeerType(parts[0]) && p.Ref.ID == id
	case "phone":
		return p.Phone != "" && strings.TrimPrefix(p.Phone, "+") == strings.TrimPrefix(query, "+")
	case "username":
		name := strings.TrimPrefix(query, "@")
		for _, candidate := range append([]string{p.Username}, p.Usernames...) {
			if candidate != "" && strings.EqualFold(candidate, name) {
				return true
			}
		}
		return false
	default:
		return query == "me" || strings.EqualFold(title(p), query) || p.Username != "" && strings.EqualFold(p.Username, query)
	}
}

func contactsFailureReason(err error) string {
	var rpc *tgerr.Error
	if !errors.As(err, &rpc) {
		return ""
	}
	switch rpc.Type {
	case "USERNAME_INVALID", "PHONE_NUMBER_INVALID":
		return "invalid"
	case "USERNAME_NOT_OCCUPIED", "PHONE_NOT_OCCUPIED":
		return "not_found"
	case "USER_PRIVACY_RESTRICTED", "USER_NOT_MUTUAL_CONTACT", "PHONE_PRIVACY_RESTRICTED":
		return "privacy"
	default:
		return ""
	}
}
