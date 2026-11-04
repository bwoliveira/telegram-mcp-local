package gotdengine

import (
	"context"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
)

// SecretsSession binds gotd's opaque bytes to the chosen internal/secrets backend.
// The PoC uses the encrypted file backend explicitly on Linux headless.
type SecretsSession struct{ Store secrets.Store }

func (s SecretsSession) LoadSession(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := s.Store.Get(secrets.Session)
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, engine.ErrNoSession
	}
	return b, err
}
func (s SecretsSession) StoreSession(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Set(secrets.Session, b)
}
func (s SecretsSession) DeleteSession(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Delete(secrets.Session)
}

var _ engine.SessionStore = SecretsSession{}
