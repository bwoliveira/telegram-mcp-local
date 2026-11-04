package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// telegramHistoryTargets selects typed Telegram dialogs, never inherited index
// chat identifiers. Message dates, not IDs across independent peers, determine
// recency. Dialogs without retained messages remain eligible in stable ID order.
func (s *Server) telegramHistoryTargets(ctx context.Context, chat string, chats int) (engine.AccountID, []string, error) {
	if s.eng == nil {
		return "", nil, errors.New("Telegram engine is not configured")
	}
	account, err := s.eng.Account(ctx)
	if err != nil {
		return "", nil, err
	}
	if account.ID == "" {
		return "", nil, engine.ErrAccountMismatch
	}
	if chat != "" {
		peer, err := s.resolvePeer(ctx, chat)
		if err != nil {
			return "", nil, err
		}
		return account.ID, []string{peerID(peer)}, nil
	}
	if s.readStore == nil {
		return "", nil, errors.New("selecting recent Telegram dialogs requires the local ReadStore; supply chat_id")
	}
	dialogs, err := s.readStore.ReadDialogs(ctx, account.ID)
	if err != nil {
		return "", nil, err
	}
	messages, err := s.readStore.ReadMessages(ctx, account.ID)
	if err != nil {
		return "", nil, err
	}
	latest := map[engine.PeerRef]time.Time{}
	for _, m := range messages {
		if m.Key.Account != account.ID {
			continue
		}
		if m.Date.After(latest[m.Key.Peer]) {
			latest[m.Key.Peer] = m.Date
		}
	}
	peers := make([]engine.PeerRef, 0, len(dialogs))
	seen := map[engine.PeerRef]bool{}
	for _, d := range dialogs {
		if _, err := parsePeer(peerID(d.Peer)); err != nil {
			return "", nil, err
		}
		if !seen[d.Peer] {
			peers = append(peers, d.Peer)
			seen[d.Peer] = true
		}
	}
	sort.Slice(peers, func(i, j int) bool {
		a, b := latest[peers[i]], latest[peers[j]]
		if !a.Equal(b) {
			return a.After(b)
		}
		return peerID(peers[i]) < peerID(peers[j])
	})
	if chats <= 0 {
		chats = 10
	}
	peers = peers[:min(len(peers), min(chats, 50))]
	targets := make([]string, 0, len(peers))
	for _, p := range peers {
		targets = append(targets, peerID(p))
	}
	if len(targets) == 0 {
		return "", nil, errors.New("no local Telegram dialogs are available; supply a typed chat_id")
	}
	return account.ID, targets, nil
}

// requestTelegramHistory is the Telegram-native replacement for RequestHistory.
// Its worker reads the durable engine cursor on every one-page Backfill call;
// no process-local offset or old index ActiveChats result is used.
func (s *Server) requestTelegramHistory(ctx context.Context, chat string, count, rounds, chats int) (HistoryJob, bool, error) {
	account, targets, err := s.telegramHistoryTargets(ctx, chat, chats)
	if err != nil {
		return HistoryJob{}, false, err
	}
	if count <= 0 {
		count = 50
	}
	count = min(count, 100)
	rounds = min(max(rounds, 1), 20)
	s.historyMu.Lock()
	if s.history != nil && s.history.FinishedAt == nil {
		out := cloneTelegramHistory(s.history)
		s.historyMu.Unlock()
		return out, false, nil
	}
	if s.history != nil {
		for _, previous := range s.history.Results {
			if previous.FloodUntil != nil && previous.FloodUntil.After(s.clock()) {
				out := cloneTelegramHistory(s.history)
				s.historyMu.Unlock()
				return out, false, &engine.FloodWaitError{Method: "messages.getHistory", Until: *previous.FloodUntil}
			}
		}
	}
	job := &HistoryJob{StartedAt: s.clock().UTC(), Chats: targets, Count: count, Rounds: rounds, Failures: map[string]any{}}
	s.history = job
	out := cloneTelegramHistory(job)
	s.historyMu.Unlock()
	go s.runTelegramHistory(account, job)
	return out, true, nil
}

func (s *Server) runTelegramHistory(account engine.AccountID, job *HistoryJob) {
	for targetIndex, chat := range job.Chats {
		peer, err := parsePeer(chat)
		result := engine.BackfillResult{Peer: peer}
		s.historyMu.Lock()
		job.Current = chat
		slot := len(job.Results)
		job.Results = append(job.Results, result)
		s.historyMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(job.Rounds)*3*time.Minute)
		for n := 0; err == nil && n < job.Rounds; n++ {
			var current engine.Account
			current, err = s.eng.Account(ctx)
			if err != nil {
				break
			}
			if current.ID != account {
				err = engine.ErrAccountMismatch
				break
			}
			if n == 0 {
				result, err = s.telegramHistoryProgress(ctx, account, peer)
				if err != nil {
					break
				}
				s.historyMu.Lock()
				job.Results[slot] = result
				s.historyMu.Unlock()
				if result.FloodUntil != nil && result.FloodUntil.After(s.clock()) {
					err = &engine.FloodWaitError{Method: "messages.getHistory", Until: *result.FloodUntil}
					break
				}
			}
			var page engine.BackfillResult
			page, err = s.eng.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: job.Count, Rounds: 1})
			result.Fetched += page.Fetched
			result.Added += page.Added
			result.Rounds += page.Rounds
			if err == nil {
				result.Complete = page.Complete
			}
			if page.OldestID > 0 {
				result.OldestID = page.OldestID
				result.OldestDate = page.OldestDate
			}
			if err == nil || page.FloodUntil != nil {
				result.FloodUntil = page.FloodUntil
			}
			var flood *engine.FloodWaitError
			if errors.As(err, &flood) {
				until := flood.Until
				result.FloodUntil = &until
			}
			s.historyMu.Lock()
			job.Added += page.Added
			job.Results[slot] = result
			s.historyMu.Unlock()
			if err != nil || page.Complete {
				break
			}
			// A backend that cannot demonstrate a committed page must not be looped
			// twenty times. Empty successful pages normally carry Complete=true.
			if page.Rounds == 0 {
				err = fmt.Errorf("history backfill made no progress: %w", engine.ErrInvalid)
				break
			}
		}
		cancel()
		s.historyMu.Lock()
		job.Done++
		if err != nil {
			failure := map[string]any{"error": err.Error()}
			var flood *engine.FloodWaitError
			if errors.As(err, &flood) {
				failure["flood_wait_until"] = flood.Until
			}
			if errors.Is(err, engine.ErrNoAccess) {
				failure["no_access"] = true
			}
			job.Failures[chat] = failure
		}
		s.historyMu.Unlock()
		var flood *engine.FloodWaitError
		if errors.As(err, &flood) {
			// getHistory rate limits are not isolated by peer. Do not probe
			// another conversation while this account/method is waiting.
			s.historyMu.Lock()
			for _, pending := range job.Chats[targetIndex+1:] {
				job.Failures[pending] = map[string]any{"error": "not attempted: Telegram history rate limit", "deferred": true, "flood_wait_until": flood.Until}
			}
			s.historyMu.Unlock()
			break
		}
	}
	s.historyMu.Lock()
	now := s.clock().UTC()
	job.Current = ""
	job.FinishedAt = &now
	s.historyMu.Unlock()
}
