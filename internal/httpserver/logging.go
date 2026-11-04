package httpserver

import (
	"context"
	"log/slog"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// RedactingHandler is the daemon's log boundary. Message templates must be
// static; unstructured messages are intentionally suppressed. Attribute values
// are allowlisted so fmt.Stringer, error and foreign payloads cannot leak data.
// The engine's production transport must apply the same policy to its logger.
type RedactingHandler struct{ next slog.Handler }

func Redact(next slog.Handler) slog.Handler {
	if _, ok := next.(*RedactingHandler); ok {
		return next
	}
	return &RedactingHandler{next: next}
}
func (h *RedactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	message := "daemon event"
	if safeMessage(r.Message) {
		message = r.Message
	}
	clean := slog.NewRecord(r.Time, r.Level, message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if v, ok := safeAttr(a); ok {
			clean.AddAttrs(v)
		}
		return true
	})
	return h.next.Handle(ctx, clean)
}
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if v, ok := safeAttr(a); ok {
			out = append(out, v)
		}
	}
	return &RedactingHandler{next: h.next.WithAttrs(out)}
}
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	// Group names are developer-provided, but may accidentally be dynamic.
	return &RedactingHandler{next: h.next.WithGroup("event")}
}

func safeMessage(message string) bool {
	switch message {
	case "the MCP port is taken; the engine runs, the MCP waits for another port",
		"serving MCP",
		"the MCP listener stopped",
		"MCP moved to another port",
		"account controller stopped",
		"account janitor stopped",
		"account engine stopped",
		"could not load the webhooks",
		"rejected request with foreign Host",
		"rejected cross-origin request",
		"HTTP handler panic",
		"Telegram transport diagnostic", "Telegram login ended", "Telegram QR refreshed", "Telegram QR retry", "Telegram QR migrating", "Telegram session revoked",
		"Ping failed", "Ping failed, connection considered dead", "Failed to ACK",
		"Permanent connection error, will not reconnect", "Got error on self",
		"Error while handling message", "Local clock needs synchronization",
		"Slow write: waited for exchange lock", "Slow write: transport send took too long",
		"config.json could not be read; using the defaults", "starting",
		"opened at login: staying in the tray", "the data folder could not be prepared",
		"the HTTP credential could not be loaded", "the gateway did not start",
		"could not register the app to open at login", "the app did not leave the Dock as it started",
		"stopping the gateway", "stopped", "notification not sent",
		"restarting into the new version", "autostart could not be changed",
		"the Telegram API credentials could not be loaded", "the app stopped", "the app could not start",
		"Telegram logout failed; erasing anyway", "app data could not be erased":
		return true
	// The updater's development-mode warnings (internal/updater/devfeed.go).
	// They are static and must survive redaction to stay loud.
	case "WARNING: DEVELOPMENT MODE: TELEGRAM_MCP_UPDATES_API replaces GitHub Releases and updates are verified with TELEGRAM_MCP_UPDATES_PUBLIC_KEY, a test key, NOT the release key. Never use this in production.",
		"WARNING: DEVELOPMENT MODE: TELEGRAM_MCP_UPDATES_API replaces GitHub Releases; updates are verified with the embedded release key. Never use this in production.",
		"WARNING: TELEGRAM_MCP_UPDATES_API or TELEGRAM_MCP_UPDATES_PUBLIC_KEY is set but refused: a test update feed is accepted only when TELEGRAM_MCP_DEV=1; update checks are disabled until it is unset",
		"WARNING: TELEGRAM_MCP_UPDATES_API or TELEGRAM_MCP_UPDATES_PUBLIC_KEY is set but refused: release builds never accept a test update feed; update checks are disabled until it is unset",
		"WARNING: DEVELOPMENT MODE: the test update feed is invalid and was refused; update checks are disabled until it is unset":
		return true
	default:
		return false
	}
}

func safeState(state string) bool {
	switch state {
	case "starting", "not_authorized", "connecting", "connected", "reconnecting",
		"resyncing", "paused", "logged_out", "revoked", "error", "stopped",
		"idle", "qr", "code", "password", "syncing", "done", "cancelled":
		return true
	default:
		return false
	}
}

func safeRPCCode(code string) bool {
	switch code {
	case "AUTH_TOKEN_EXPIRED", "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "AUTH_KEY_DUPLICATED", "SESSION_PASSWORD_NEEDED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN", "DC_ID_INVALID", "AUTH_TOKEN_INVALID", "AUTH_TOKEN_ALREADY_ACCEPTED":
		return true
	default:
		return false
	}
}

func safeAttr(a slog.Attr) (slog.Attr, bool) {
	key := strings.ToLower(a.Key)
	if key == "error" && a.Value.Kind() == slog.KindAny {
		// Inspect only the concrete contract type. Do not call Error, Unwrap or
		// errors.As: arbitrary error implementations may expose secret payloads.
		if rpc, ok := a.Value.Any().(*engine.RPCError); ok && rpc != nil && safeRPCCode(rpc.Type) {
			return slog.String("code", rpc.Type), true
		}
	}
	if key == "state" && a.Value.Kind() == slog.KindString && safeState(a.Value.String()) {
		return a, true
	}
	if (key == "code" || key == "rpc_code" || key == "err_msg") && a.Value.Kind() == slog.KindString && safeRPCCode(a.Value.String()) {
		return a, true
	}
	switch key {
	case "duration", "elapsed", "waited", "took", "backoff", "time_difference":
		if a.Value.Kind() == slog.KindDuration {
			return a, true
		}
	}
	for _, word := range []string{"text", "body", "caption", "transcript", "code", "password", "token", "secret", "session", "hash", "qr", "cookie", "authorization", "file_reference"} {
		if strings.Contains(key, word) {
			return slog.Attr{}, false
		}
	}
	if a.Value.Kind() == slog.KindGroup {
		var out []slog.Attr
		for _, v := range a.Value.Group() {
			if clean, ok := safeAttr(v); ok {
				out = append(out, clean)
			}
		}
		return slog.Attr{Key: "event", Value: slog.GroupValue(out...)}, len(out) > 0
	}
	// No Resolve call: arbitrary LogValuers/Stringers may themselves expose a
	// secret, panic or log. Secret values cannot sneak in under a safe key.
	switch key {
	case "port", "count", "attempt", "duration_ms", "dc_id":
		switch a.Value.Kind() {
		case slog.KindInt64, slog.KindUint64, slog.KindBool, slog.KindDuration:
			return a, true
		}
	case "stack":
		if pcs, ok := a.Value.Any().([]uintptr); ok {
			frames := runtime.CallersFrames(pcs)
			var out strings.Builder
			for {
				f, more := frames.Next()
				out.WriteString(f.Function + " " + f.File + ":" + strconv.Itoa(f.Line) + "\n")
				if !more {
					break
				}
			}
			return slog.String("stack", out.String()), true
		}
	case "panic_type", "error_type":
		// The caller supplies a reflect.Type, never an error instance.
		if t, ok := a.Value.Any().(reflect.Type); ok {
			return slog.String(key, t.String()), true
		}
	}
	return slog.Attr{}, false
}
