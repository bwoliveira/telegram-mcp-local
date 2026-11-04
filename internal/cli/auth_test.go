package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/daemon"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

type authOutputFunc func([]byte) (int, error)

func (f authOutputFunc) Write(p []byte) (int, error) { return f(p) }

func TestQRAuthPrivateLocalPageUsesDaemon(t *testing.T) {
	var controller *fake.Controller
	var logs bytes.Buffer
	d, err := daemon.Start(daemon.Config{Port: 0, RequirePort: true, Token: "synthetic-qr-bearer", DataDir: t.TempDir(), StoreDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(&logs, nil)), Factory: func(cfg engine.Config) (engine.Controller, error) {
		c, err := fake.New(cfg)
		if err == nil {
			err = c.LogOut(context.Background())
		}
		if c != nil {
			controller = c.(*fake.Controller)
		}
		return c, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	var path string
	var output bytes.Buffer
	out := authOutputFunc(func(p []byte) (int, error) {
		output.Write(p)
		if strings.HasPrefix(string(p), "QR page (private; open locally): ") {
			path = strings.TrimSpace(strings.TrimPrefix(string(p), "QR page (private; open locally): "))
			for target, dir := range map[string]bool{path: false, filepath.Dir(path): true} {
				if err := service.CheckPrivateDataPath(target, dir); err != nil {
					t.Fatalf("private artifact mode: %v", err)
				}
			}
			html, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(html, []byte("data:image/png;base64,")) || !bytes.Contains(html, []byte("http-equiv=\"refresh\"")) {
				t.Fatal("missing local QR or refresh")
			}
			for _, marker := range []string{"synthetic-qr-bearer", "tg://", "http://", "https://", "<script"} {
				if bytes.Contains(html, []byte(marker)) {
					t.Fatal("unsafe local page content")
				}
			}
			if err := controller.CompleteLogin(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		return len(p), nil
	})
	err = runAuth(context.Background(), config{Port: d.Port(), Token: "synthetic-qr-bearer"}, []string{"--qr"}, func(string, bool) (string, error) { t.Fatal("QR unexpectedly prompted"); return "", io.EOF }, out)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || !strings.Contains(output.String(), "authenticated") {
		t.Fatal("QR page/success not reported")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("QR artifacts not removed")
	}
	for _, marker := range []string{"synthetic-qr-bearer", "tg://login", "c3ludGhldGlj"} {
		if strings.Contains(output.String()+logs.String(), marker) {
			t.Fatal("QR secret exposed in output/logs")
		}
	}
}

func TestTerminalAuthUsesDaemonController(t *testing.T) {
	for _, args := range [][]string{nil, {"--phone"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) { testTerminalAuthUsesDaemonController(t, args) })
	}
}

func testTerminalAuthUsesDaemonController(t *testing.T, args []string) {
	var controller engine.Controller
	var logs bytes.Buffer
	d, err := daemon.Start(daemon.Config{Port: 0, RequirePort: true, Token: "synthetic-auth-bearer", DataDir: t.TempDir(), StoreDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(&logs, nil)), Factory: func(cfg engine.Config) (engine.Controller, error) {
		c, err := fake.New(cfg)
		if err == nil {
			err = c.LogOut(context.Background())
		}
		controller = c
		return c, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	var output bytes.Buffer
	inputs := []string{"+5511912345678", "synthetic-code-marker", "synthetic-password-marker"}
	calls := 0
	err = runAuth(context.Background(), config{Port: d.Port(), Token: "synthetic-auth-bearer"}, args, func(prompt string, secret bool) (string, error) {
		if !secret {
			t.Error("credential prompt allowed echo")
		}
		if calls >= len(inputs) {
			t.Fatal("too many prompts")
		}
		v := inputs[calls]
		calls++
		return v, nil
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || controller.Login().State != engine.LoginDone {
		t.Fatalf("calls=%d state=%s", calls, controller.Login().State)
	}
	for _, marker := range inputs {
		if strings.Contains(output.String()+logs.String(), marker) {
			t.Error("auth secret exposed")
		}
	}
	if !strings.Contains(output.String(), "authenticated") {
		t.Fatalf("missing success: %s", &output)
	}
}

func TestAuthRejectsCredentialArguments(t *testing.T) {
	var output bytes.Buffer
	err := runAuth(context.Background(), config{}, []string{"--password", "synthetic-secret-marker"}, func(string, bool) (string, error) { t.Error("read input for invalid arguments"); return "", io.EOF }, &output)
	if err == nil || strings.Contains(err.Error(), "synthetic-secret-marker") {
		t.Fatalf("unsafe arguments error: %v", err)
	}
}

func TestAuthInputErrorsAreRedacted(t *testing.T) {
	err := runAuth(context.Background(), config{}, nil, func(string, bool) (string, error) { return "", errors.New("synthetic-input-marker") }, io.Discard)
	if err == nil || strings.Contains(err.Error(), "synthetic-input-marker") {
		t.Fatalf("unsafe input error: %v", err)
	}
}
