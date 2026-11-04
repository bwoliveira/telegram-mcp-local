package mcp

import "github.com/bwoliveira/telegram-mcp-local/internal/engine"

// cloneTelegramHistory keeps polling callers from mutating a worker's cursor
// boundaries or structured failures. Install it behind cloneHistory as well.
func cloneTelegramHistory(job *HistoryJob) HistoryJob {
	out := *job
	out.Chats = append([]string(nil), job.Chats...)
	out.Results = append([]engine.BackfillResult(nil), job.Results...)
	if job.FinishedAt != nil {
		stamp := *job.FinishedAt
		out.FinishedAt = &stamp
	}
	for i := range out.Results {
		if out.Results[i].OldestDate != nil {
			stamp := *out.Results[i].OldestDate
			out.Results[i].OldestDate = &stamp
		}
		if out.Results[i].FloodUntil != nil {
			stamp := *out.Results[i].FloodUntil
			out.Results[i].FloodUntil = &stamp
		}
	}
	out.Failures = make(map[string]any, len(job.Failures))
	for chat, failure := range job.Failures {
		if fields, ok := failure.(map[string]any); ok {
			copy := make(map[string]any, len(fields))
			for k, v := range fields {
				copy[k] = v
			}
			out.Failures[chat] = copy
		} else {
			out.Failures[chat] = failure
		}
	}
	return out
}
