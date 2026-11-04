package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
)

// Check is one verdict of the health report.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, info, warn or fail
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Health is the answer to "is this working?": one overall verdict and the
// checks it was made from, each with what to do when it is not ok.
// Activity describes retained local Telegram messages when a store is configured;
// the inherited index fallback never verifies Telegram reception or coverage.
type Health struct {
	Status   string                 `json:"status"`
	Summary  string                 `json:"summary"`
	Checks   []Check                `json:"checks"`
	Activity index.Activity         `json:"activity"`
	History  engine.HistoryProgress `json:"history"`
	At       time.Time              `json:"checked_at"`
}

// healthInputs is everything the verdict depends on, gathered first so the
// judgement itself is a pure function that can be tested.
type healthInputs struct {
	Now              time.Time
	Uptime           time.Duration
	Diagnostics      engine.Diagnostics
	DiagnosticsErr   error
	Sync             engine.Status
	Activity         index.Activity
	ActivityErr      error
	Gaps             []index.Gap
	History          *HistoryJob
	MaxSilence       time.Duration
	Store            bool
	CoverageComplete bool
	CoverageErr      error
}

const defaultMaxSilence = 6 * time.Hour

var errHealthEngineUnavailable = errors.New("engine is not configured")
var errHealthIndexUnavailable = errors.New("inherited index is not configured")

// Health runs read-only engine and local-store checks. Existing JSON fields
// are kept for the panel and HTTP callers. maxSilence annotates retained incoming
// activity; neither recent activity nor engine readiness proves full coverage.
func (s *Server) Health(ctx context.Context, maxSilence time.Duration) Health {
	health, _, _, _ := s.HealthSnapshot(ctx, maxSilence)
	return health
}

// HealthSnapshot returns the same health verdict together with the local status
// and diagnostics it evaluated. Panel consumers must not independently reload
// those inputs, which would repeat account-wide aggregates within one request.
func (s *Server) HealthSnapshot(ctx context.Context, maxSilence time.Duration) (Health, engine.Status, engine.Diagnostics, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	now := time.Now()
	if s.clock != nil {
		now = s.clock()
	}
	in := healthInputs{Now: now, Uptime: now.Sub(s.started), History: s.historyStatus(), MaxSilence: maxSilence}
	if s.eng == nil {
		in.DiagnosticsErr = errHealthEngineUnavailable
	} else {
		in.Sync, in.CoverageErr = s.localStatus(ctx)
		in.Diagnostics, in.DiagnosticsErr = s.eng.Diagnostics(ctx)
	}
	if s.readStore != nil && s.eng != nil {
		in.Store = true
		in.Activity, in.CoverageComplete, in.ActivityErr = s.readActivity(ctx, now)
		in.CoverageErr = errors.Join(in.CoverageErr, in.ActivityErr)
	} else if s.index == nil {
		in.ActivityErr = errHealthIndexUnavailable
	} else {
		in.Activity, in.ActivityErr = s.index.Activity(ctx, now)
		in.Gaps, _ = s.index.Gaps(ctx, gapThreshold, 7*24*time.Hour, 20)
	}
	return evaluate(in), in.Sync, in.Diagnostics, in.DiagnosticsErr
}

func evaluate(in healthInputs) Health {
	if in.MaxSilence <= 0 {
		in.MaxSilence = defaultMaxSilence
	}
	h := Health{At: in.Now.UTC(), Activity: in.Activity, History: in.Sync.History}
	add := func(name, status, detail, fix string) {
		h.Checks = append(h.Checks, Check{Name: name, Status: status, Detail: detail, Fix: fix})
	}

	add("daemon", "ok", fmt.Sprintf("answering, version %s, up for %s", Version, round(in.Uptime)), "")

	// Preserve the paired check name for panel callers, but use engine session
	// semantics. Revocation/state always wins over a stale diagnostics snapshot.
	switch {
	case in.Sync.State == engine.StateRevoked || errors.Is(in.DiagnosticsErr, engine.ErrRevoked):
		add("paired", "fail", "the Telegram session was revoked outside this app", "log in again from the app's account panel")
	case in.Sync.State == engine.StateLoggedOut:
		add("paired", "fail", "the Telegram account is logged out", "log in from the app's account panel")
	case in.Sync.State == engine.StateNotAuthorized || errors.Is(in.DiagnosticsErr, engine.ErrNotAuthorized):
		add("paired", "fail", "the Telegram account is not authorized", "log in from the app's account panel")
	case errors.Is(in.DiagnosticsErr, engine.ErrPaused):
		add("paired", "warn", "session authorization cannot be checked while disconnected", "reconnect from the app's account panel")
	case in.DiagnosticsErr != nil:
		// Do not echo arbitrary backend errors into a health response.
		add("engine", "fail", "engine diagnostics are unavailable", "check the engine configuration and connection in the app")
	case !in.Diagnostics.Authorized:
		add("paired", "fail", "the engine reports no authorized Telegram session", "log in from the app's account panel")
	default:
		add("paired", "ok", "the engine reports an authorized Telegram session", "")
	}

	sync := in.Sync
	for_ := round(in.Now.Sub(sync.Since))
	switch sync.State {
	case engine.StateConnected:
		if in.DiagnosticsErr == nil && !in.Diagnostics.Connected {
			add("sync", "warn", "engine status and diagnostics disagree about the connection", "check the connection again in the app")
		} else {
			add("sync", "ok", "engine reports connected for "+for_, "")
		}
	case engine.StatePaused:
		add("sync", "warn", "the engine is disconnected; the session and local history are kept", "reconnect from the app's account panel")
	case engine.StateStarting, engine.StateConnecting:
		add("sync", "warn", "engine is starting or connecting, for "+for_, "wait briefly and check the engine connection in the app")
	case engine.StateReconnecting:
		add("sync", "warn", "engine is reconnecting, for "+for_, "check the internet connection; the engine retries automatically")
	case engine.StateResyncing:
		add("sync", "warn", "engine is replaying updates, for "+for_, "wait for replay to finish; check telegram_status for checkpoint and gap details")
	case engine.StateNotAuthorized, engine.StateLoggedOut, engine.StateRevoked:
		add("sync", "fail", "engine is waiting for an authorized Telegram session", "log in from the app's account panel")
	default:
		add("sync", "fail", "engine is not connected", "check the engine status and configuration in the app")
	}
	held, openGaps := sync.Updates.Held, sync.Updates.OpenGaps
	if in.DiagnosticsErr == nil {
		// Status and Diagnostics may straddle an update. Neither snapshot can
		// dismiss a held checkpoint or gap observed by the other.
		held = held || in.Diagnostics.LocalUpdates.Held
		openGaps = max(openGaps, in.Diagnostics.LocalUpdates.OpenGaps)
	}
	if held || openGaps > 0 {
		add("updates", "warn", fmt.Sprintf("engine checkpoint held: %t; open update gaps: %d", held, openGaps),
			"check telegram_status and wait for engine replay or backfill; coverage is incomplete")
	}

	a := in.Activity
	if in.Store {
		switch {
		case in.ActivityErr != nil:
			add("receiving", "warn", "Telegram local activity is unavailable", "check the local store")
		case a.NewestIncoming == nil:
			add("receiving", "info", "no retained incoming Telegram messages; absence is not proof of failure", "check sync and history coverage")
		case in.Now.Sub(*a.NewestIncoming) > in.MaxSilence:
			add("receiving", "info", "last retained incoming Telegram message exceeds the silence tolerance; not proof of engine failure", "check sync and expected account activity")
		default:
			add("receiving", "ok", "recent incoming Telegram messages are retained locally; this is not proof of complete coverage", "")
		}
		if in.CoverageErr != nil {
			add("coverage", "warn", "Telegram local history coverage is unavailable", "check the local store")
		} else if !in.CoverageComplete || held || openGaps > 0 || sync.Updates.CoverageGaps > 0 || sync.Updates.TerminalGaps > 0 || sync.Updates.InaccessibleGaps > 0 {
			add("coverage", "info", "Telegram local history coverage is incomplete or unknown; this does not mean synchronization is running", "inspect per-conversation coverage and gap causes; backfill only downloads accessible history")
		} else {
			add("coverage", "ok", "local store declares complete history coverage", "")
		}
	} else {
		fix := "Telegram receiving must be checked against the Telegram store and engine updates after their integration"
		switch {
		case in.ActivityErr != nil:
			add("receiving", "warn", "inherited local index is unavailable; Telegram receiving is not verified", fix)
		case a.NewestIncoming == nil:
			add("receiving", "warn", "inherited local index has no incoming messages; Telegram receiving is not verified", fix)
		default:
			age := in.Now.Sub(*a.NewestIncoming)
			detail := fmt.Sprintf("inherited local index: last incoming message %s ago; %d messages in the last hour, %d in 24 hours; Telegram receiving is not verified",
				round(age), a.LastHour, a.LastDay)
			if age > in.MaxSilence {
				detail += "; older than the configured silence tolerance (not proof of an engine failure)"
			}
			add("receiving", "warn", detail, fix)
		}

		detail := "inherited local index does not verify Telegram history coverage"
		if len(in.Gaps) > 0 {
			detail += fmt.Sprintf("; %d silent window(s) in the last 7 days are local heuristics, not confirmed Telegram gaps", len(in.Gaps))
		}
		add("coverage", "warn", detail, "use Telegram store backfill cursors and engine update gaps after store integration")
	}

	if job := in.History; job != nil && len(job.Failures) > 0 {
		add("history_request", "warn", fmt.Sprintf("the last sync_history request failed for %d of %d conversations", len(job.Failures), len(job.Chats)),
			"telegram_status shows the history request; check engine access, connection and rate limits")
	}

	h.Status = "ok"
	var failing, warning []string
	for _, c := range h.Checks {
		switch c.Status {
		case "fail":
			failing = append(failing, c.Name)
		case "warn":
			warning = append(warning, c.Name)
		}
	}
	switch {
	case len(failing) > 0:
		h.Status = "fail"
		h.Summary = fmt.Sprintf("not working: %v", failing)
	case len(warning) > 0:
		h.Status = "warn"
		h.Summary = fmt.Sprintf("engine checks completed with warnings: %v", warning)
	default:
		h.Summary = "engine checks passed"
	}
	return h
}

func round(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

func (s *Server) health(ctx context.Context, a arguments) map[string]any {
	if a.MaxSilenceHours < 0 {
		return toolError("max_silence_hours must not be negative")
	}
	h := s.Health(ctx, time.Duration(a.MaxSilenceHours*float64(time.Hour)))
	return textResult(h, false)
}
