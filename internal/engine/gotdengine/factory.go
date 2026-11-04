package gotdengine

import "github.com/bwoliveira/telegram-mcp-local/internal/engine"

// Factory registers the production controller with daemon.Config.Factory. The
// caller owns per-account Persister and SessionStore lifetimes and must inject
// credentials before calling it. No network or session loading occurs here.
// Missing dependencies fail closed; pocstore is never selected automatically.
func Factory(cfg engine.Config) (engine.Controller, error) {
	controller, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return controller, nil
}

var _ engine.Factory = Factory
