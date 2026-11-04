package mcp

import (
	"context"
	"errors"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func (s *Server) status(ctx context.Context, _ arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint := s.base() + "/mcp"
	report := map[string]any{
		"gateway": map[string]any{"version": Version, "endpoint": endpoint},
		"version": Version, "endpoint": endpoint,
		"mode": engine.Mode(""), "ready": false, "logged_out": false,
		"error": false, "paused": false, "secret_chats": "unsupported",
	}
	// ready is engine/session readiness, never a promise of complete history.
	report["ready_note"] = "ready describes an authorized, connected engine with no held checkpoint or recoverable update gaps; terminal coverage gaps and partial history remain informational and do not verify complete Telegram history"
	// Preserve the panel's account label/slot metadata.
	report["account"] = s.Account(ctx)

	problems := []string{}
	var sync engine.Status
	var statusErr error
	if s.eng == nil {
		report["error"] = true
		problems = append(problems, "engine is not configured")
	} else {
		sync, statusErr = s.localStatus(ctx)
		report["sync"] = sync
		report["mode"] = sync.Mode
		report["logged_out"] = sync.State == engine.StateLoggedOut || sync.State == engine.StateRevoked || sync.State == engine.StateNotAuthorized
		report["paused"] = sync.State == engine.StatePaused
		report["error"] = sync.State == engine.StateError || sync.State == engine.StateStopped || sync.State == engine.StateRevoked
		if diagnostics, err := s.eng.Diagnostics(ctx); err != nil {
			// Backend errors may contain sensitive implementation details. Expose
			// only known session/connection reasons, never an arbitrary error.
			switch {
			case errors.Is(err, engine.ErrRevoked):
				report["logged_out"], report["error"] = true, true
				problems = append(problems, "the Telegram session was revoked outside this app; log in again from the app's account panel")
			case errors.Is(err, engine.ErrNotAuthorized):
				report["logged_out"] = true
				problems = append(problems, "Telegram is not authorized; log in from the app's account panel")
			case errors.Is(err, engine.ErrPaused):
				report["paused"] = true
				problems = append(problems, "the engine is disconnected; reconnect from the app's account panel")
			default:
				report["error"] = true
				problems = append(problems, "engine diagnostics are unavailable; check the engine connection in the app")
			}
		} else {
			report["engine"] = diagnostics
			report["ready"] = sync.State == engine.StateConnected && diagnostics.Authorized && diagnostics.Connected && !sync.Updates.Held && sync.Updates.OpenGaps == 0 && !diagnostics.LocalUpdates.Held && diagnostics.LocalUpdates.OpenGaps == 0
			if diagnostics.LocalUpdates.Held || diagnostics.LocalUpdates.OpenGaps > 0 {
				problems = append(problems, "engine diagnostics report incomplete update coverage: the checkpoint is held or update gaps remain")
			}
			if !diagnostics.Authorized {
				problems = append(problems, "the engine reports no authorized Telegram session; log in from the app's account panel")
			}
			if sync.State == engine.StateConnected && !diagnostics.Connected {
				problems = append(problems, "engine status and diagnostics disagree about the connection")
			}
		}

		switch sync.State {
		case engine.StateNotAuthorized:
			problems = append(problems, "Telegram is not authorized; log in from the app's account panel")
		case engine.StateLoggedOut:
			problems = append(problems, "the Telegram account is logged out; log in again from the app's account panel")
		case engine.StateRevoked:
			problems = append(problems, "the Telegram session was revoked outside this app; log in again from the app's account panel")
		case engine.StateError, engine.StateStopped:
			problems = append(problems, "the engine is not running; check its configuration and connection in the app")
		case engine.StatePaused:
			report["note"] = "the engine is disconnected; session and history are kept; reconnect from the app's account panel"
		case engine.StateResyncing:
			report["note"] = "the engine is replaying updates; coverage is incomplete until replay and backfill finish"
		case engine.StateStarting, engine.StateConnecting, engine.StateReconnecting:
			report["note"] = "the engine is establishing its connection; check status again shortly"
		case engine.StateConnected:
		default:
			report["error"] = true
			problems = append(problems, "the engine reported an unknown connection state")
		}
		if sync.Updates.Held || sync.Updates.OpenGaps > 0 {
			problems = append(problems, "engine update coverage is incomplete: the checkpoint is held or update gaps remain")
		}
		if statusErr != nil {
			report["ready"], report["error"] = false, true
			problems = append(problems, "Telegram local store status is unavailable")
		}
	}

	// ReadCoverage(peer) scopes terminal evidence to that peer. Aggregate gap
	// counters must not invalidate unrelated conversations. Held/actionable
	// update state remains a conservative guard for all reads.
	scopedCoverageSafe := statusErr == nil && !sync.Updates.Held && sync.Updates.OpenGaps == 0
	coverageSafe := scopedCoverageSafe && sync.Updates.CoverageGaps == 0 && sync.Updates.TerminalGaps == 0 && sync.Updates.InaccessibleGaps == 0
	if diagnostics, ok := report["engine"].(engine.Diagnostics); ok {
		scopedCoverageSafe = scopedCoverageSafe && !diagnostics.LocalUpdates.Held && diagnostics.LocalUpdates.OpenGaps == 0
		coverageSafe = coverageSafe && !diagnostics.LocalUpdates.Held && diagnostics.LocalUpdates.OpenGaps == 0 && diagnostics.LocalUpdates.CoverageGaps == 0 && diagnostics.LocalUpdates.TerminalGaps == 0 && diagnostics.LocalUpdates.InaccessibleGaps == 0
	} else {
		coverageSafe = false
		scopedCoverageSafe = false
	}
	// The legacy fallback is never evidence of Telegram coverage.
	report["history_state"] = "unknown"
	report["information"] = []string{}
	report["index_verified"] = false
	report["index_note"] = "inherited local index only; Telegram receiving and history coverage are not verified"
	if s.readStore != nil && s.eng != nil {
		report["index_note"] = "Telegram local store coverage; not real-account verification when using a fake engine"
		acc, err := s.eng.Account(ctx)
		if err != nil {
			problems = append(problems, "local store account identity unavailable")
		} else {
			if rows, err := s.conversationCoverage(ctx, acc.ID, scopedCoverageSafe); err != nil {
				problems = append(problems, "Telegram per-conversation coverage is unavailable")
			} else {
				report["conversation_coverage"] = rows
			}
			complete, oldest, err := s.readStore.ReadCoverage(ctx, acc.ID, nil)
			if err != nil {
				problems = append(problems, "Telegram local history coverage is unavailable")
			} else {
				complete = complete && coverageSafe
				coverage := map[string]any{"coverage_complete": complete, "history_since": oldest}
				if statusErr == nil {
					coverage["messages"], coverage["chats"] = sync.History.Messages, sync.History.Conversations
					coverage["last_synced_at"] = sync.History.LastAt
				}
				report["index"] = coverage
				report["history_state"] = "complete"
				if !complete {
					report["history_state"] = "partial"
					report["information"] = []string{"Telegram local history coverage is incomplete or unknown; terminal gap causes are in sync.updates.gap_causes; readiness is not history completeness"}
				}
			}
		}
	} else if s.index == nil {
		problems = append(problems, "inherited local index is not configured; Telegram history coverage is unavailable")
	} else {
		if coverage, err := s.index.Coverage(ctx); err == nil {
			report["index"] = coverage
		} else {
			problems = append(problems, "inherited local index coverage is unavailable; it does not verify Telegram history")
		}
		if gaps, err := s.index.Gaps(ctx, gapThreshold, 30*24*time.Hour, 10); err != nil {
			problems = append(problems, "inherited local index silent-window query is unavailable")
		} else if len(gaps) > 0 {
			report["unknown_windows"] = gaps
			report["unknown_windows_note"] = "silent windows in the inherited local index; not confirmed Telegram gaps and not evidence of complete history or a stopped engine"
		}
	}

	if job := s.historyStatus(); job != nil {
		report["history_request"] = job
		report["history_coverage"] = s.telegramHistoryCoverage(ctx, *job)
	}
	transcription := map[string]any{"mode": "on request only: transcribe_audio"}
	if s.asr != nil {
		st := s.asr.Status()
		transcription["local"] = map[string]any{"ready": st.Ready, "supported": st.Supported, "missing": st.Missing, "accel": st.Accel, "engine": "whisper.cpp large-v3-turbo"}
	}
	if s.state != nil {
		if total, corrected, err := s.state.TranscriptCount(ctx); err == nil {
			transcription["transcripts"] = total
			transcription["corrected_from_context"] = corrected
		}
	}
	report["transcription"] = transcription
	webhooks := map[string]any{"setup": "in the app, Configurações › Webhooks: " + s.base() + "/configuracoes#webhooks",
		"documentation": s.base() + "/webhooks/documentacao",
		"what":          "configured webhooks deliver engine events to the user's script; delivery support and Telegram event coverage must be verified separately; these tools only answer when asked"}
	if s.state != nil {
		if hooks, err := s.state.Webhooks(ctx); err == nil {
			enabled := 0
			for _, h := range hooks {
				if h.Enabled {
					enabled++
				}
			}
			webhooks["configured"], webhooks["enabled"] = len(hooks), enabled
		}
	}
	report["webhooks"] = webhooks
	report["sync_state"] = "unavailable"
	if report["error"] == true {
		report["sync_state"] = "error"
	} else if report["paused"] == true {
		report["sync_state"] = "paused"
	} else if report["logged_out"] == true {
		report["sync_state"] = "logged_out"
	} else if report["ready"] == true {
		report["sync_state"] = "ready"
	} else if sync.State == engine.StateResyncing || sync.State == engine.StateStarting || sync.State == engine.StateConnecting || sync.State == engine.StateReconnecting || (sync.State == engine.StateConnected && (sync.Updates.Held || sync.Updates.OpenGaps > 0)) {
		report["sync_state"] = "syncing"
	}
	if job := s.historyStatus(); sync.History.Running != nil || (job != nil && job.FinishedAt == nil) {
		report["history_state"] = "syncing"
	}
	report["problems"] = problems
	return textResult(report, false)
}

// localStatus overlays durable totals without requiring a change to ReadStore
// or Persister. Fake/spike stores retain their existing status implementation.
// Memory-only checkpoint holds and gaps remain conservative evidence even if
// the persisted snapshot was captured just before an update failure.
func (s *Server) localStatus(ctx context.Context) (engine.Status, error) {
	var sync engine.Status
	if light, ok := s.eng.(interface{ LightStatus() engine.Status }); ok {
		sync = light.LightStatus()
	} else {
		sync = s.eng.Status()
	}
	reader, ok := s.readStore.(interface {
		ReadStatus(context.Context, engine.AccountID) (engine.HistoryProgress, engine.UpdatesStatus, error)
	})
	if !ok {
		return sync, nil
	}
	account, err := s.eng.Account(ctx)
	if err != nil {
		sync.Updates.Held = true
		sync.LastError = "local store status is unavailable"
		return sync, err
	}
	history, updates, err := reader.ReadStatus(ctx, account.ID)
	if err != nil {
		// LightStatus deliberately has no durable checkpoint evidence. Match
		// enriched Status's conservative failure semantics without a retry:
		// an unreadable checkpoint can never be presented as ready/complete.
		sync.Updates.Held = true
		sync.LastError = "local store status is unavailable"
		return sync, err
	}
	updates.Held = updates.Held || sync.Updates.Held
	updates.OpenGaps = max(updates.OpenGaps, sync.Updates.OpenGaps)
	sync.History, sync.Updates = history, updates
	return sync, nil
}
