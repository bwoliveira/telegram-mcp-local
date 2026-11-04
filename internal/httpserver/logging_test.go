package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestRedactorPreservesKnownDiagnosticsAndSafeAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(Redact(slog.NewJSONHandler(&output, nil)))

	logger.Error("account engine stopped",
		"state", "reconnecting",
		"code", "AUTH_TOKEN_EXPIRED",
		"duration", 1500*time.Millisecond,
	)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "account engine stopped" {
		t.Fatalf("known diagnostic was discarded: %v", record["msg"])
	}
	if record["state"] != "reconnecting" {
		t.Fatalf("safe state was discarded: %v", record["state"])
	}
	if record["code"] != "AUTH_TOKEN_EXPIRED" {
		t.Fatalf("safe RPC code was discarded: %v", record["code"])
	}
	if record["duration"] != float64(1500*time.Millisecond) {
		t.Fatalf("safe duration was discarded: %v", record["duration"])
	}
}

func TestRedactorPreservesOnlyKnownStaticMessages(t *testing.T) {
	known := []string{
		"the MCP port is taken; the engine runs, the MCP waits for another port",
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
		"Telegram transport diagnostic",
	}
	for _, message := range known {
		t.Run(message, func(t *testing.T) {
			var output bytes.Buffer
			slog.New(Redact(slog.NewJSONHandler(&output, nil))).Warn(message)
			var record map[string]any
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["msg"] != message {
				t.Fatalf("known static message was discarded: %q", record["msg"])
			}
		})
	}

	const secret = "SYNTHETIC_DYNAMIC_LOG_MESSAGE"
	var output bytes.Buffer
	slog.New(Redact(slog.NewJSONHandler(&output, nil))).Error(secret)
	if strings.Contains(output.String(), secret) {
		t.Fatal("unrecognized message leaked")
	}
	if !strings.Contains(output.String(), `"msg":"daemon event"`) {
		t.Fatalf("missing safe fallback: %s", output.String())
	}
}

func TestRedactorAllowsContractedStateRPCCodeAndDurationShapes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(Redact(slog.NewJSONHandler(&output, nil)))
	logger.LogAttrs(t.Context(), slog.LevelWarn, "Telegram transport diagnostic",
		slog.String("state", "qr"),
		slog.String("rpc_code", "AUTH_TOKEN_EXPIRED"),
		slog.String("err_msg", "AUTH_TOKEN_EXPIRED"),
		slog.Duration("elapsed", 2*time.Second),
		slog.Duration("waited", 3*time.Second),
		slog.Int64("duration_ms", 4000),
	)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"state":       "qr",
		"rpc_code":    "AUTH_TOKEN_EXPIRED",
		"err_msg":     "AUTH_TOKEN_EXPIRED",
		"elapsed":     float64(2 * time.Second),
		"waited":      float64(3 * time.Second),
		"duration_ms": float64(4000),
	}
	for key, value := range want {
		if record[key] != value {
			t.Errorf("%s = %v, want %v", key, record[key], value)
		}
	}
}

func TestRedactorExtractsOnlyAllowlistedCodeFromTypedRPCError(t *testing.T) {
	const methodSecret = "SYNTHETIC_RPC_METHOD_SECRET"
	var output bytes.Buffer
	logger := slog.New(Redact(slog.NewJSONHandler(&output, nil)))
	logger.Error("account engine stopped", "error", &engine.RPCError{
		Code:   401,
		Type:   "AUTH_TOKEN_EXPIRED",
		Method: methodSecret,
	})

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["code"] != "AUTH_TOKEN_EXPIRED" {
		t.Fatalf("safe typed RPC code was discarded: %v", record)
	}
	if _, ok := record["error"]; ok {
		t.Fatalf("raw RPC error was logged: %v", record)
	}
	if strings.Contains(output.String(), methodSecret) {
		t.Fatalf("RPC method leaked: %s", output.String())
	}
}

func TestRedactorDropsTelegramIdentifiersAndExternalErrorDetails(t *testing.T) {
	const (
		chatID     = int64(4242424242)
		messageID  = int64(3131313131)
		privateDir = "/home/example/private/session"
		token      = "SYNTHETIC_PRIVATE_TOKEN"
		message    = "SYNTHETIC_PRIVATE_MESSAGE_TEXT"
	)
	var output bytes.Buffer
	logger := slog.New(Redact(slog.NewJSONHandler(&output, nil)))
	logger.Error("account engine stopped",
		"chat_id", chatID,
		"msg_id", messageID,
		"error", errors.New(privateDir+" "+token+" "+message),
	)

	logs := output.String()
	for _, private := range []string{
		strconv.FormatInt(chatID, 10),
		strconv.FormatInt(messageID, 10),
		privateDir,
		token,
		message,
	} {
		if strings.Contains(logs, private) {
			t.Fatalf("private Telegram data leaked: %s", logs)
		}
	}
	if !strings.Contains(logs, `"msg":"account engine stopped"`) {
		t.Fatalf("redaction removed the useful diagnostic: %s", logs)
	}
}

func TestRedactorDropsSecretsEvenFromKnownDiagnosticsAndSafeLookingKeys(t *testing.T) {
	const marker = "SYNTHETIC_SECRET_IN_LOG_ATTR"
	var output bytes.Buffer
	logger := slog.New(Redact(slog.NewJSONHandler(&output, nil))).
		With("session", marker).
		WithGroup(marker)
	logger.Error("account engine stopped",
		"count", 1,
		"token", marker,
		"phone", marker,
		"code", marker,
		"rpc_code", marker,
		"err_msg", marker,
		"password", marker,
		"text", marker,
		"body", marker,
		"caption", marker,
		"transcript", marker,
		"qr_url", marker,
		"api_hash", marker,
		"auth_key", marker,
		"authorization", marker,
		"cookie", marker,
		"csrf", marker,
		"bootstrap", marker,
		"webhook_secret", marker,
		"access_hash", marker,
		"file_reference", marker,
		"state", marker,
		"duration", marker,
		"error", errors.New("wrapped "+marker),
	)

	logs := output.String()
	if strings.Contains(logs, marker) {
		t.Fatalf("secret leaked: %s", logs)
	}
	if !strings.Contains(logs, `"msg":"account engine stopped"`) || !strings.Contains(logs, `"count":1`) {
		t.Fatalf("redaction removed the useful diagnostic too: %s", logs)
	}
}
