package pocstore

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Metrics returns counts only: no message text, phone numbers, hashes or tokens.
func (s *Store) Metrics(ctx context.Context, acc engine.AccountID) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT kind,count(*) FROM poc_records WHERE account=? GROUP BY kind", acc)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var count int64
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return nil, err
		}
		out[kind] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	gaps, err := records(ctx, s.db, acc, "gap")
	if err != nil {
		return nil, err
	}
	for _, r := range gaps {
		var g engine.GapMark
		if err := json.Unmarshal(r.body, &g); err != nil {
			return nil, err
		}
		if g.Filled == nil {
			out["open_gaps"]++
		}
	}
	return out, nil
}
