package mcp

import (
	"context"
	"sort"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// conversationCoverage lists actual indexed conversations, not all cached peers.
// A recent message is activity evidence only; completeness comes from the
// store's cursor/gap/checkpoint proof and is never inferred from message counts.
func (s *Server) conversationCoverage(ctx context.Context, account engine.AccountID, checkpointSafe bool) ([]map[string]any, error) {
	dialogs, err := s.readStore.ReadDialogs(ctx, account)
	if err != nil {
		return nil, err
	}
	messages, err := s.readStore.ReadMessages(ctx, account)
	if err != nil {
		return nil, err
	}
	counts := map[engine.PeerRef]int{}
	latest := map[engine.PeerRef]time.Time{}
	for _, d := range dialogs {
		counts[d.Peer] = 0
	}
	for _, m := range messages {
		if m.Key.Account != account {
			return nil, engine.ErrAccountMismatch
		}
		if m.ExpiresAt != nil && !m.ExpiresAt.After(s.clock()) {
			continue
		}
		if m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(s.clock()) {
			continue
		}
		counts[m.Key.Peer]++
		if m.Date.After(latest[m.Key.Peer]) {
			latest[m.Key.Peer] = m.Date
		}
	}
	peers := make([]engine.PeerRef, 0, len(counts))
	for peer := range counts {
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(i, j int) bool { return peerID(peers[i]) < peerID(peers[j]) })
	rows := make([]map[string]any, 0, len(peers))
	for _, peer := range peers {
		complete, oldest, err := s.readStore.ReadCoverage(ctx, account, &peer)
		if err != nil {
			return nil, err
		}
		row := map[string]any{"chat_id": peerID(peer), "messages": counts[peer], "coverage_complete": complete && checkpointSafe}
		if oldest != nil {
			row["history_since"] = oldest
		}
		if last := latest[peer]; !last.IsZero() {
			row["last_message_at"] = last
		}
		rows = append(rows, row)
	}
	return rows, nil
}
