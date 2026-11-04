package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAppLoggerRedactsStartupError(t *testing.T) {
	const privatePath = "/home/example/private/startup"
	var output bytes.Buffer

	appLogger(&output).Error("the app could not start", "error", errors.New(privatePath))

	logs := output.String()
	if strings.Contains(logs, privatePath) {
		t.Fatalf("private startup path leaked: %s", logs)
	}
	if !strings.Contains(logs, "the app could not start") {
		t.Fatalf("redaction removed the useful diagnostic: %s", logs)
	}
}

func TestOpenLogRedactsExternalErrorAndPrivateAttributes(t *testing.T) {
	const (
		privatePath = "/home/example/private/session"
		token       = "SYNTHETIC_PRIVATE_TOKEN"
		message     = "SYNTHETIC_PRIVATE_MESSAGE_TEXT"
	)

	logger, file, err := openLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logger.Warn("config.json could not be read; using the defaults",
		"error", errors.New(privatePath+" "+token+" "+message),
		"data", privatePath,
		"token", token,
		"text", message,
	)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	logs, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{privatePath, token, message} {
		if strings.Contains(string(logs), private) {
			t.Fatalf("private app data leaked: %s", logs)
		}
	}
	if !strings.Contains(string(logs), "config.json could not be read; using the defaults") {
		t.Fatalf("redaction removed the useful diagnostic: %s", logs)
	}
}
