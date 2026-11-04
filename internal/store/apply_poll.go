package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// applyPoll merges result-only updates into every local copy of this poll.
// It never creates messages, resets TTL, or revives tombstones. Existing JSON
// columns are used so upgraded databases have exactly the same behavior.
func applyPoll(ctx context.Context, tx *sql.Tx, a engine.AccountID, u engine.PollUpdate, now int64) error {
	if u.ID == 0 {
		return engine.ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, `SELECT row_id,poll_json,text FROM messages WHERE account=? AND media_ttl=0 AND (expires_at IS NULL OR expires_at>?) AND json_valid(poll_json) AND CAST(json_extract(poll_json,'$.id') AS TEXT)=CAST(? AS TEXT)`, a, now, u.ID)
	if err != nil {
		return err
	}
	type change struct {
		row       int64
		raw, text string
	}
	var changes []change
	for rows.Next() {
		var row int64
		var raw, text string
		if err = rows.Scan(&row, &raw, &text); err != nil {
			rows.Close()
			return err
		}
		var p engine.Poll
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			rows.Close()
			return err
		}
		oldQuestion := p.Question
		if u.Definition != nil {
			d := *u.Definition
			d.Options = append([]engine.PollOption(nil), d.Options...)
			d.Partial = p.Partial
			if d.ID != u.ID {
				rows.Close()
				return engine.ErrInvalid
			}
			d.TotalVoters = p.TotalVoters
			for i := range d.Options {
				for _, old := range p.Options {
					if d.Options[i].Key == old.Key {
						d.Options[i].Voters = old.Voters
						d.Options[i].Chosen = old.Chosen
						d.Options[i].Correct = old.Correct
					}
				}
			}
			p = d
		}
		if u.TotalVoters != nil {
			p.TotalVoters = *u.TotalVoters
		}
		if u.Results != nil || u.TotalVoters != nil {
			p.Partial = u.Min || (p.TotalVoters > 0 && len(u.Results) == 0)
			for i := range p.Options {
				p.Options[i].Voters = 0
				if !u.Min {
					p.Options[i].Chosen = false
					p.Options[i].Correct = false
				}
				for _, r := range u.Results {
					if r.Key == p.Options[i].Key {
						p.Options[i].Voters = r.Voters
						if !u.Min {
							p.Options[i].Chosen = r.Chosen
							p.Options[i].Correct = r.Correct
						}
					}
				}
			}
		}
		if text == "" || text == oldQuestion {
			text = p.Question
		}
		next := encode(p)
		if next != raw {
			changes = append(changes, change{row: row, raw: next, text: text})
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err = tx.ExecContext(ctx, `UPDATE messages SET poll_json=?,text=?,updated_at=? WHERE row_id=?`, c.raw, c.text, now, c.row); err != nil {
			return err
		}
	}
	return nil
}
