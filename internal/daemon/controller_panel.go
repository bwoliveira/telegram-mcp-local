package daemon

import "github.com/bwoliveira/telegram-mcp-local/internal/engine"

// LightStatus preserves the optional, persistence-free status capability across
// managed controller generations. Older controllers retain their Status behavior.
func (m *managedController) LightStatus() engine.Status {
	c := m.controller()
	if light, ok := c.(interface{ LightStatus() engine.Status }); ok {
		return light.LightStatus()
	}
	return c.Status()
}
