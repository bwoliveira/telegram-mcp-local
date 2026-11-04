package gotdengine

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"

	gotdlog "github.com/gotd/log"
)

func TestTransportLoggerUsesConfiguredHandlerWithoutPayloads(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil)).With("account", "offline-e2")
	transport := transportLogger(logger)
	transport.Log(context.Background(), gotdlog.LevelDebug, "sensitive-debug")
	transport.Log(context.Background(), gotdlog.LevelInfo, "sensitive-info")
	if output.Len() != 0 {
		t.Fatal("gotd debug/info reached the application logger")
	}
	transport.Log(context.Background(), gotdlog.LevelWarn, "sensitive-message", gotdlog.Any("error", errors.New("sensitive-error")), gotdlog.String("body", "sensitive-body"))
	transport.Log(context.Background(), gotdlog.LevelError, "sensitive-error-message", gotdlog.String("password", "sensitive-password"))
	logs := output.String()
	if strings.Contains(logs, "sensitive-") {
		t.Fatal("transport payload leaked through configured logger")
	}
	if !strings.Contains(logs, `"account":"offline-e2"`) || !strings.Contains(logs, `"level":"WARN"`) || !strings.Contains(logs, `"level":"ERROR"`) {
		t.Fatalf("configured handler/context or severity lost: %s", logs)
	}
}

func TestTransportLoggerKeepsSafeDiagnostics(t *testing.T) {
	var output bytes.Buffer
	transport := transportLogger(slog.New(slog.NewJSONHandler(&output, nil)))
	transport.Log(context.Background(), gotdlog.LevelWarn, "Ping failed", gotdlog.Error(tgerr.New(400, "AUTH_TOKEN_EXPIRED")), gotdlog.Duration("elapsed", time.Second), gotdlog.String("token", "sensitive-token"))
	logs := output.String()
	for _, want := range []string{`"msg":"Ping failed"`, `"rpc_code":"AUTH_TOKEN_EXPIRED"`, `"elapsed":1000000000`} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing %s in %s", want, logs)
		}
	}
	if strings.Contains(logs, "sensitive-token") {
		t.Fatal("token leaked")
	}
}

func TestTransportLoggerHonorsConfiguredLevelAndNilIsSilent(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelError}))
	transportLogger(logger).Log(context.Background(), gotdlog.LevelWarn, "suppressed")
	if output.Len() != 0 {
		t.Fatal("configured handler level ignored")
	}
	transportLogger(logger).Log(context.Background(), gotdlog.LevelError, "sanitized")
	if output.Len() == 0 {
		t.Fatal("configured error handler bypassed")
	}
	transportLogger(nil).Log(context.Background(), gotdlog.LevelWarn, "never use global logger or stderr")
}
