package gotdengine

import "github.com/bwoliveira/telegram-mcp-local/internal/engine"

// LightStatus returns only the in-memory lifecycle snapshot. Unlike Status it
// does not enrich history or checkpoints from persistence or contact Telegram.
// Panel callers combine this with their independently cached local-store data.
func (e *Engine) LightStatus() engine.Status {
	e.mu.Lock()
	s := e.status
	e.mu.Unlock()
	s.Updates.Held = s.Updates.Held || e.held.Load() || e.recovering.Load()
	return s
}
