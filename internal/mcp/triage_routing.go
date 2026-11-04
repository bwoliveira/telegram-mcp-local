package mcp

import "context"

// triageAnalysisHandlers bypasses only the inherited index/state preconditions.
// Every handler below uses the account-scoped Telegram store instead.
func (s *Server) triageAnalysisHandlers() map[string]func(context.Context, arguments) map[string]any {
	return map[string]func(context.Context, arguments) map[string]any{
		"list_unanswered": s.triageUnanswered,
		"list_mentions":   s.triageMentions,
		"mark_handled":    s.triageMarkHandled,
		"snooze_chat":     s.triageSnooze,
		"message_stats":   s.statsMessages,
		"export_messages": s.exportStoreMessages,
		"sync_history":    s.telegramSyncHistory,
	}
}
