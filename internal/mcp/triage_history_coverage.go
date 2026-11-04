package mcp

import (
	"context"
	"errors"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// HistoryPeerCoverage is independent of BackfillResult.Complete: reaching the
// accessible start cannot prove that update gaps or held checkpoints are closed.
// The ReadStore owns that proof. Unknown or failed coverage is always partial.
type HistoryPeerCoverage struct {
	ChatID       string     `json:"chat_id"`
	Complete     bool       `json:"coverage_complete"`
	Partial      bool       `json:"partial"`
	HistorySince *time.Time `json:"history_since,omitempty"`
	Error        string     `json:"error,omitempty"`
}

func (s *Server) telegramHistoryCoverage(ctx context.Context, job HistoryJob) []HistoryPeerCoverage {
	out := make([]HistoryPeerCoverage, 0, len(job.Chats))
	var account engine.Account
	var accountErr error
	if s.eng == nil {
		accountErr = errors.New("Telegram engine is not configured")
	} else {
		account, accountErr = s.eng.Account(ctx)
	}
	if accountErr == nil && account.ID == "" {
		accountErr = engine.ErrAccountMismatch
	}
	for _, chat := range job.Chats {
		row := HistoryPeerCoverage{ChatID: chat, Partial: true}
		peer, err := parsePeer(chat)
		if err == nil {
			err = accountErr
		}
		if err == nil && s.readStore == nil {
			err = errors.New("local Telegram coverage requires ReadStore")
		}
		if err == nil {
			row.Complete, row.HistorySince, err = s.readStore.ReadCoverage(ctx, account.ID, &peer)
		}
		if err != nil {
			row.Complete = false
			row.HistorySince = nil
			row.Error = err.Error()
		}
		row.Partial = !row.Complete
		out = append(out, row)
	}
	return out
}

// telegramSyncHistory is the handler to install in place of syncHistory. It
// reports a scheduled bounded job, not a claim that history has already synced.
func (s *Server) telegramSyncHistory(ctx context.Context, a arguments) map[string]any {
	job, started, err := s.requestTelegramHistory(ctx, a.ChatJID, a.Count, a.Rounds, a.Chats)
	if err != nil {
		return engineError(err)
	}
	return textResult(map[string]any{
		"started": started, "job": job, "coverage": s.telegramHistoryCoverage(ctx, job),
		"next": "follow the bounded backfill in telegram_status; reaching accessible history start does not close open gaps or recover deleted, secret or inaccessible history",
	}, false)
}
