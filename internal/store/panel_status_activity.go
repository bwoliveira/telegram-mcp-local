package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
)

// One aggregate pass avoids projecting, decoding and sorting message content.
// Future dates still participate in the extrema, but not in recent counts.
const panelStatusActivityQuery = `SELECT
 MAX(CASE WHEN m.out=0 THEN m.date END), MAX(m.date), MIN(m.date),
 COUNT(CASE WHEN m.date>=? AND m.date<=? THEN 1 END),
 COUNT(CASE WHEN m.date>=? AND m.date<=? THEN 1 END)
 FROM messages m WHERE ` + localVisibleMessage

// ReadActivity computes the MCP activity fallback without materializing local
// messages. It applies ReadMessages visibility at the store's clock as well as
// the fallback's expiry check at the requested clock; neither may revive TTLs.
// This is local cached activity only, not a claim about history coverage.
func (s *Store) ReadActivity(ctx context.Context, account engine.AccountID, now time.Time) (index.Activity, error) {
	var activity index.Activity
	if err := s.checkReadAccount(ctx, account); err != nil {
		return activity, err
	}
	visibleAt := s.now().Unix()
	if requested := now.Unix(); requested > visibleAt {
		visibleAt = requested
	}
	// Whole-second message dates must be at or above the exact lower bound.
	// Unix truncates fractional seconds downward, so round these bounds up.
	hour, day := now.Add(-time.Hour).Unix(), now.Add(-24*time.Hour).Unix()
	if now.Nanosecond() != 0 {
		hour++
		day++
	}
	var incoming, newest, oldest sql.NullInt64
	err := s.reader.QueryRowContext(ctx, panelStatusActivityQuery,
		hour, now.Unix(), day, now.Unix(), account, visibleAt, visibleAt,
	).Scan(&incoming, &newest, &oldest, &activity.LastHour, &activity.LastDay)
	if err != nil {
		return index.Activity{}, err
	}
	activity.NewestIncoming = queryTime(incoming.Int64, incoming.Valid)
	activity.NewestAny = queryTime(newest.Int64, newest.Valid)
	activity.Oldest = queryTime(oldest.Int64, oldest.Valid)
	return activity, nil
}
