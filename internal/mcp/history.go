package mcp

import (
	"context"
	"errors"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"time"
)

// HistoryJob reports bounded engine backfills, not a phone/CLI sync.
type HistoryJob struct {
	StartedAt  time.Time               `json:"started_at"`
	FinishedAt *time.Time              `json:"finished_at,omitempty"`
	Chats      []string                `json:"chats"`
	Count      int                     `json:"count"`
	Rounds     int                     `json:"rounds"`
	Done       int                     `json:"chats_done"`
	Added      int                     `json:"messages_added"`
	Current    string                  `json:"current_chat,omitempty"`
	Failures   map[string]any          `json:"failures,omitempty"`
	Results    []engine.BackfillResult `json:"results,omitempty"`
}

func (s *Server) syncHistory(ctx context.Context, a arguments) map[string]any {
	job, started, err := s.RequestHistory(ctx, a.ChatJID, a.Count, a.Rounds, a.Chats)
	if err != nil {
		return toolError("%v", err)
	}
	return textResult(map[string]any{"started": started, "job": job, "next": "follow the backfill in telegram_status; per-chat completeness and FloodWait are reported, not assumed"}, false)
}
func (s *Server) RequestHistory(ctx context.Context, chat string, count, rounds, chats int) (HistoryJob, bool, error) {
	if s.readStore != nil {
		return s.requestTelegramHistory(ctx, chat, count, rounds, chats)
	}
	if s.eng == nil {
		return HistoryJob{}, false, errors.New("not implemented yet: Telegram engine is not configured")
	}
	var targets []string
	if chat != "" {
		p, err := s.resolvePeer(ctx, chat)
		if err != nil {
			return HistoryJob{}, false, err
		}
		targets = []string{peerID(p)}
	} else {
		if s.index == nil {
			return HistoryJob{}, false, errors.New("not implemented yet: selecting recent Telegram chats needs the Telegram store adapter; supply chat_id")
		}
		if chats <= 0 {
			chats = 10
		}
		var err error
		targets, err = s.index.ActiveChats(ctx, min(chats, 50))
		if err != nil {
			return HistoryJob{}, false, err
		}
	}
	if len(targets) == 0 {
		return HistoryJob{}, false, errors.New("no local chats are available; supply a typed Telegram chat_id")
	}
	for _, target := range targets {
		if _, err := parsePeer(target); err != nil {
			return HistoryJob{}, false, fmt.Errorf("not implemented yet: inherited local chat %q is not a Telegram peer; supply chat_id", target)
		}
	}
	if count <= 0 {
		count = 50
	}
	count = min(count, 100)
	rounds = min(max(rounds, 1), 20)
	s.historyMu.Lock()
	if s.history != nil && s.history.FinishedAt == nil {
		out := cloneHistory(s.history)
		s.historyMu.Unlock()
		return out, false, nil
	}
	job := &HistoryJob{StartedAt: time.Now().UTC(), Chats: targets, Count: count, Rounds: rounds, Failures: map[string]any{}}
	s.history = job
	out := cloneHistory(job)
	s.historyMu.Unlock()
	go s.runHistory(job)
	return out, true, nil
}
func (s *Server) runHistory(job *HistoryJob) {
	for _, chat := range job.Chats {
		s.historyMu.Lock()
		job.Current = chat
		s.historyMu.Unlock()
		p, _ := parsePeer(chat)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(job.Rounds)*3*time.Minute)
		out, err := s.eng.Backfill(ctx, engine.BackfillRequest{Peer: p, Count: job.Count, Rounds: job.Rounds})
		cancel()
		s.historyMu.Lock()
		job.Done++
		job.Added += out.Added
		job.Results = append(job.Results, out)
		if err != nil {
			job.Failures[chat] = err.Error()
		}
		s.historyMu.Unlock()
	}
	s.historyMu.Lock()
	now := time.Now().UTC()
	job.FinishedAt = &now
	job.Current = ""
	s.historyMu.Unlock()
}
func cloneHistory(job *HistoryJob) HistoryJob {
	return cloneTelegramHistory(job)
}
func (s *Server) HistoryStatus() *HistoryJob { return s.historyStatus() }
func (s *Server) historyStatus() *HistoryJob {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	if s.history == nil {
		return nil
	}
	out := cloneHistory(s.history)
	return &out
}
