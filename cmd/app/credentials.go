package main

import (
	"errors"

	"github.com/bwoliveira/telegram-mcp-local/internal/daemon"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
)

// credentialsReady runs before creating a production engine: a first-time user
// gets a local form instead of a terminal-only environment-variable error.
func (a *App) credentialsReady() bool {
	_, _, _, err := daemon.PrepareAccountCredentials(a.dataDir, "primary")
	if err == nil {
		return true
	}
	if !errors.Is(err, daemon.ErrCredentialsRequired) {
		a.logger.Error("the Telegram API credentials could not be loaded")
		a.handler.set(startFailure(errors.New("Telegram API credentials could not be loaded; check the environment and vault access"), a.logPath))
		return false
	}
	a.handler.set(panel.Internal(panel.CredentialSetup(func(id, hash string) error {
		if err := daemon.SaveAccountCredentials(a.dataDir, "primary", id, hash); err != nil {
			return errors.New("could not save API credentials")
		}
		a.startDaemon()
		if a.current() == nil {
			return errors.New("could not start gateway")
		}
		return nil
	})))
	return false
}
