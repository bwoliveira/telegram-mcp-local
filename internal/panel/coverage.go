package panel

import (
	"context"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"sort"
	"time"
)

type conversationCoverage struct {
	Name, ID string
	Complete bool
	Oldest   *time.Time
}

// Optional extension preserves small test/in-memory adapters. The production
// SQLite store always uses the grouped, single-statement implementation.
type conversationCoverageStore interface {
	ReadConversationCoverage(context.Context, engine.AccountID) ([]store.ConversationCoverage, error)
}

func (p *Panel) localCoverage(ctx context.Context, status engine.Status) ([]conversationCoverage, error) {
	if reader, ok := p.ReadStore.(conversationCoverageStore); ok {
		rows, err := reader.ReadConversationCoverage(ctx, p.AccountID)
		if err != nil {
			return nil, err
		}
		out := make([]conversationCoverage, 0, len(rows))
		for _, row := range rows {
			out = append(out, conversationCoverage{row.Name, fmt.Sprintf("%s:%d", row.Peer.Type, row.Peer.ID), row.Complete && !status.Updates.Held, row.Oldest})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	dialogs, err := p.ReadStore.ReadDialogs(ctx, p.AccountID)
	if err != nil {
		return nil, err
	}
	peers, err := p.ReadStore.ReadPeers(ctx, p.AccountID)
	if err != nil {
		return nil, err
	}
	names := map[engine.PeerRef]string{}
	for _, p := range peers {
		name := p.Title
		if name == "" {
			name = p.FirstName + " " + p.LastName
		}
		names[p.Ref] = name
	}
	out := make([]conversationCoverage, 0, len(dialogs))
	for _, d := range dialogs {
		complete, oldest, err := p.ReadStore.ReadCoverage(ctx, p.AccountID, &d.Peer)
		if err != nil {
			return nil, err
		}
		if status.Updates.Held {
			complete = false
		}
		out = append(out, conversationCoverage{names[d.Peer], fmt.Sprintf("%s:%d", d.Peer.Type, d.Peer.ID), complete, oldest})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
