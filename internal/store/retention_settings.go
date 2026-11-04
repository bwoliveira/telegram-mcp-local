package store

import (
	"context"
	"strconv"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// RetentionSettings supplies live account-local retention settings. It is read
// at every cleanup, so an explicit zero is distinct from an unset setting.
// Its lifetime must cover the Store (the daemon closes State after Store).
type RetentionSettings interface {
	Setting(context.Context, string) (string, error)
}

func WithRetentionSettings(settings RetentionSettings) Option {
	return func(s *Store) { s.retentionSettings = settings }
}

func (s *Store) retentionPolicy(ctx context.Context) (Retention, error) {
	policy := Retention{MediaDays: 30, ExportDays: 7}
	if s.retentionSettings == nil {
		return policy, nil
	}
	for _, setting := range []struct {
		key  string
		days *int
	}{{"media_retention_days", &policy.MediaDays}, {"export_retention_days", &policy.ExportDays}} {
		value, err := s.retentionSettings.Setting(ctx, setting.key)
		if err != nil {
			return Retention{}, err
		}
		if value == "" {
			continue
		}
		days, err := strconv.Atoi(value)
		if err != nil || days < 0 {
			return Retention{}, engine.ErrInvalid
		}
		*setting.days = days
	}
	return policy, nil
}
