package gotdengine

import (
	"context"
	gotdlog "github.com/gotd/log"
	"github.com/gotd/td/tgerr"
	"log/slog"
	"reflect"
)

// Only audited static transport messages and typed metadata cross this boundary.
// Never format errors or payloads: they may contain authentication material.
type transportLog struct{ logger *slog.Logger }

func transportLogger(logger *slog.Logger) gotdlog.Logger { return transportLog{logger} }
func (l transportLog) Enabled(ctx context.Context, level gotdlog.Level) bool {
	return l.logger != nil && level >= gotdlog.LevelWarn && l.logger.Enabled(ctx, slog.Level(level))
}
func (l transportLog) Log(ctx context.Context, level gotdlog.Level, message string, attrs ...gotdlog.Attr) {
	if !l.Enabled(ctx, level) {
		return
	}
	switch message {
	case "Ping failed", "Ping failed, connection considered dead", "Failed to ACK", "Permanent connection error, will not reconnect", "Got error on self", "Error while handling message", "Local clock needs synchronization", "Slow write: waited for exchange lock", "Slow write: transport send took too long":
	default:
		message = "Telegram transport diagnostic"
	}
	var safe []slog.Attr
	for _, a := range attrs {
		if a.Value.Kind() == gotdlog.KindDuration {
			switch a.Key {
			case "elapsed", "backoff", "waited", "took", "time_difference":
				safe = append(safe, slog.Duration(a.Key, a.Value.Duration()))
			}
		}
		if a.Key == "error" && a.Value.Kind() == gotdlog.KindError {
			err := a.Value.Error()
			safe = append(safe, slog.Any("error_type", reflect.TypeOf(err)))
			if rpc, ok := err.(*tgerr.Error); ok && rpc != nil {
				switch rpc.Type {
				case "AUTH_TOKEN_EXPIRED", "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "AUTH_KEY_DUPLICATED", "SESSION_PASSWORD_NEEDED":
					safe = append(safe, slog.String("rpc_code", rpc.Type))
				}
			}
		}
	}
	l.logger.LogAttrs(ctx, slog.Level(level), message, safe...)
}
