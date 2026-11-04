package index

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// Names provides local compatibility names for typed Telegram peers. It reads
// only aliases and chat titles from the supplied index, never an account session.
// The Telegram engine and store remain responsible for resolving human input.
type Names struct {
	index *sql.DB

	mu     sync.Mutex
	loaded time.Time
	byPeer map[string]string
}

const namesTTL = 45 * time.Second

func openNames(index *sql.DB) *Names { return &Names{index: index} }

// peerType recognizes canonical positive Telegram IDs without rewriting them.
func peerType(peer string) string {
	kind, id, ok := strings.Cut(peer, ":")
	if !ok || (kind != "user" && kind != "chat" && kind != "channel") || id == "" || id[0] == '0' {
		return ""
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return ""
	}
	return kind
}

// IsCollectivePeer reports whether peer is a canonical Telegram chat or channel.
// The compatibility schema cannot distinguish supergroups from broadcast channels,
// so both channel kinds are collective conversations, never direct messages.
func IsCollectivePeer(peer string) bool {
	kind := peerType(peer)
	return kind == "chat" || kind == "channel"
}

// IsIdentifier reports whether a stored name is an identifier or number rather
// than a display name. Placeholder peer IDs are not useful contact names.
func IsIdentifier(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "@") || peerType(s) != "" {
		return true
	}
	for _, r := range s {
		if (r < '0' || r > '9') && !strings.ContainsRune("+-() ", r) {
			return false
		}
	}
	return true
}

func (n *Names) refresh(ctx context.Context) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if time.Since(n.loaded) < namesTTL || ctx.Err() != nil {
		return
	}
	byPeer := map[string]string{}
	scan := func(query string) {
		if n.index == nil {
			return
		}
		rows, err := n.index.QueryContext(ctx, query)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var peer, name string
			if rows.Scan(&peer, &name) != nil {
				continue
			}
			peer, name = strings.TrimSpace(peer), strings.TrimSpace(name)
			if peerType(peer) == "" || IsIdentifier(name) {
				continue
			}
			if _, ok := byPeer[peer]; !ok {
				byPeer[peer] = name
			}
		}
	}
	// Explicit user aliases take precedence over cached chat titles.
	scan(`SELECT jid, alias FROM contact_aliases`)
	scan(`SELECT jid, COALESCE(name,'') FROM chats`)
	if ctx.Err() != nil {
		return
	}
	n.byPeer, n.loaded = byPeer, time.Now()
}

// Canonical preserves typed peer identity. It does not infer a peer from a
// display name, numeric ID, username, phone number or another peer type.
func (n *Names) Canonical(_ context.Context, peer string) string {
	return strings.TrimSpace(peer)
}

// Name returns a cached display name or a Telegram-specific fallback.
func (n *Names) Name(ctx context.Context, peer string) string {
	if name := n.Known(ctx, peer); name != "" {
		return name
	}
	return formatPeer(strings.TrimSpace(peer))
}

// Known is Name without the fallback: empty when no local name is known.
func (n *Names) Known(ctx context.Context, peer string) string {
	n.refresh(ctx)
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.byPeer[strings.TrimSpace(peer)]
}

// Self is retained for callers; this compatibility index has no account identity.
func (n *Names) Self(context.Context) string { return "" }

func formatPeer(peer string) string {
	switch peerType(peer) {
	case "chat":
		return unnamedGroup
	case "channel":
		return channel
	default:
		return peer
	}
}

// Fallback names remain stable across tool and webhook languages. The panel
// translates them with ShownName.
var (
	unnamedGroup = i18n.N("Grupo sem nome")
	channel      = i18n.N("Canal do Telegram")
)

// ShownName localizes fallback chat names for the panel.
func ShownName(name string) string {
	switch name {
	case unnamedGroup, channel:
		return i18n.T(name)
	}
	return name
}

// Contacts lists named user peers, optionally filtered by name or typed ID.
// Numeric peer IDs are not phone numbers. Equal names retain distinct peers.
func (n *Names) Contacts(ctx context.Context, search string, limit int) []Contact {
	if limit <= 0 {
		return nil
	}
	n.refresh(ctx)
	n.mu.Lock()
	defer n.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(search))
	var out []Contact
	for peer, name := range n.byPeer {
		if peerType(peer) != "user" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(name), q) && !strings.Contains(peer, q) {
			continue
		}
		out = append(out, Contact{JID: peer, Name: name})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a == b {
			return out[i].JID < out[j].JID
		}
		return a < b
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Same compares identities, never display names or untyped numeric IDs.
func (n *Names) Same(ctx context.Context, a, b string) bool {
	return n.Canonical(ctx, a) == n.Canonical(ctx, b)
}

// Own preserves the compatibility API without reading account session files.
// Account identity must be obtained from the Telegram engine instead.
func (n *Names) Own(context.Context) (primary, alternate string) { return "", "" }
