package mcp

import (
	"context"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
)

func (s *Server) readActivity(ctx context.Context, now time.Time) (index.Activity, bool, error) {
	var activity index.Activity
	acc, err := s.eng.Account(ctx)
	if err != nil {
		return activity, false, err
	}
	// Persistent stores can aggregate safe activity without decoding every
	// retained message. Keep the unpaginated fallback for fake/spike stores.
	if reader, ok := s.readStore.(interface {
		ReadActivity(context.Context, engine.AccountID, time.Time) (index.Activity, error)
	}); ok {
		activity, err = reader.ReadActivity(ctx, acc.ID, now)
		if err != nil {
			return activity, false, err
		}
		complete, _, err := s.readStore.ReadCoverage(ctx, acc.ID, nil)
		return activity, complete, err
	}
	rows, err := s.readStore.ReadMessages(ctx, acc.ID)
	if err != nil {
		return activity, false, err
	}
	for _, m := range rows {
		if m.ExpiresAt != nil && !m.ExpiresAt.After(now) {
			continue
		}
		if m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(now) {
			continue
		}
		date := m.Date
		if activity.Oldest == nil || date.Before(*activity.Oldest) {
			activity.Oldest = &date
		}
		if activity.NewestAny == nil || date.After(*activity.NewestAny) {
			activity.NewestAny = &date
		}
		if !m.Out && (activity.NewestIncoming == nil || date.After(*activity.NewestIncoming)) {
			activity.NewestIncoming = &date
		}
		if !date.Before(now.Add(-time.Hour)) && !date.After(now) {
			activity.LastHour++
		}
		if !date.Before(now.Add(-24*time.Hour)) && !date.After(now) {
			activity.LastDay++
		}
	}
	complete, _, err := s.readStore.ReadCoverage(ctx, acc.ID, nil)
	return activity, complete, err
}
