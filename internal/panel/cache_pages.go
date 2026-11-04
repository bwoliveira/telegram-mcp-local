package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// shellSnapshot is deliberately independent of Account, Login, Diagnostics,
// SQL history and Telegram. The mode is live, never restored from telemetry.
func (p *Panel) shellSnapshot() snapshot {
	s := snapshot{Sync: p.lightStatus()}
	s.ReadOnly = s.Sync.Mode != engine.ModeReadWrite
	if h, at, fresh := p.cache.health.peek(); fresh {
		s.Tone = tone(h.Health.Checks, s.Sync)
		s.SnapshotReady = true
		s.SnapshotAt = at
	}
	return s
}

// cachedClientInfo never waits behind a running CLI probe. Those probes hold
// the legacy cache mutex for their entire subprocess lifetime.
func (p *Panel) cachedClientInfo(c *codeCache) clientInfo {
	if !c.mu.TryLock() {
		return clientInfo{}
	}
	defer c.mu.Unlock()
	if c.endpoint != p.endpoint() || time.Since(c.at) >= 30*time.Second {
		return clientInfo{}
	}
	return c.info
}

// cheapSetup prepares local instructions, but never spawns discovery commands.
func (p *Panel) cheapSetup(ctx context.Context) setup {
	snippet, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{ServerName: p.desktopEntry()}}, "", "  ")
	cursor, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{ServerName: p.cursorEntry()}}, "", "  ")
	return setup{Endpoint: p.endpoint(), Command: "claude " + shellJoin(p.codeArgs()), JSON: string(snippet), DesktopPath: DesktopConfigPath(), AgentPrompt: agentPrompt(p.endpoint(), string(snippet)), CodexCommand: "codex " + shellJoin(p.codexArgs()), CodexTOML: p.codexTOML(), CodexPath: CodexConfigPath(), CursorJSON: string(cursor), CursorPath: CursorConfigPath(), HermesYAML: p.hermesYAML(), HermesEnv: p.hermesEnv(), HermesPath: HermesConfigPath(), Desktop: p.desktopInfo(), Code: p.cachedClientInfo(&p.code), Codex: p.cachedClientInfo(&p.codex), Cursor: p.cursorInfo()}
}

func (p *Panel) apiPanelClients(r *http.Request) (any, error) {
	ctx, end, err := p.snapshotContext(r.Context())
	if err != nil {
		return nil, err
	}
	defer end()
	// Send only the three discovery flags the browser needs, never another
	// client's endpoint, executable arguments, tokens or configuration details.
	type flags struct {
		Found      bool `json:"found"`
		Configured bool `json:"configured"`
		Stale      bool `json:"stale"`
	}
	selectFlags := func(info clientInfo) flags { return flags{info.Found, info.Configured, info.Stale} }
	code := selectFlags(p.codeInfo(ctx, false))
	codex := selectFlags(p.codexInfo(ctx, false))
	return map[string]flags{"claude-code": code, "codex": codex}, ctx.Err()
}
func (p *Panel) freshTelemetry(ctx context.Context) (snapshot, error) {
	h, err := p.cache.health.get(ctx, p.loadHealth)
	if err != nil {
		return snapshot{}, err
	}
	s := p.snapshot(ctx)
	// Lifecycle, authorization and mode always come from the cheap live read.
	s.Sync.History = h.Sync.History
	held := s.Sync.Updates.Held
	s.Sync.Updates = h.Sync.Updates
	s.Sync.Updates.Held = s.Sync.Updates.Held || held
	s.Health, s.Activity = h.Health, h.Health.Activity
	s.Checks = nil
	s.readChecks()
	s.SnapshotReady = true
	_, s.SnapshotAt, _ = p.cache.health.peek()
	return s, nil
}
func (p *Panel) apiPanelHealth(r *http.Request) (any, error) {
	ctx, cancel, contextErr := p.snapshotContext(r.Context())
	if contextErr != nil {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	defer cancel()
	p.lightStatus()
	version := p.cache.health.version()
	s, err := p.freshTelemetry(ctx)
	if err != nil {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	if p.cache.health.version() != version {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	return map[string]any{"fresh": true, "checked_at": s.SnapshotAt, "messages": s.Sync.History.Messages, "conversations": s.Sync.History.Conversations, "tone": s.Tone}, nil
}
func (p *Panel) apiPanelStatus(r *http.Request) (any, error) {
	ctx, cancel, contextErr := p.snapshotContext(r.Context())
	if contextErr != nil {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	defer cancel()
	p.lightStatus()
	version := p.cache.health.version()
	s, err := p.freshTelemetry(ctx)
	if err != nil {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	health, _, healthFresh := p.cache.health.peek()
	if !healthFresh {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	v, err := p.cache.details.get(ctx, func(ctx context.Context) (statusView, error) {
		var v statusView
		if p.cache.health.version() != version {
			return v, errSnapshotInvalidated
		}
		v.Diagnostics = health.Diagnostics
		v.DiagnosticsErr = health.DiagnosticsErr != nil
		if p.ReadStore != nil {
			v.ConversationCoverage, err = p.localCoverage(ctx, s.Sync)
			v.CoverageUnavailable = err != nil
		}
		if p.cache.health.version() != version {
			return statusView{}, errSnapshotInvalidated
		}
		return v, ctx.Err()
	})
	if err != nil {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	v.CoverageTone, v.CoverageText = statusCoverageText(s.Sync, v, s.History)
	v.Recent = recentEvents(s.Sync.Recent, 8)
	if t := s.Sync.FloodWaitUntil; t != nil && t.After(time.Now()) {
		v.FloodWait = t
	}
	var body bytes.Buffer
	data := statusPage{p.layout(r, i18n.N("Status"), "status", s), s, v, true}
	if err := p.pages.ExecuteTemplate(&body, "statuscontent", data); err != nil {
		return nil, err
	}
	if p.cache.health.version() != version {
		return nil, userError{i18n.T("Não foi possível atualizar o status agora.")}
	}
	return map[string]any{"html": body.String(), "fresh": true, "checked_at": s.SnapshotAt, "tone": s.Tone}, nil
}
