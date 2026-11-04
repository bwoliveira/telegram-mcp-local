package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/platform"
)

type installNoNetwork struct{ calls atomic.Int32 }

func (n *installNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.calls.Add(1)
	return nil, errors.New("external network forbidden in P41")
}

// P41 exercises the same session/CSRF POST and canonical progress API as the
// install button. Only the installer is injected; status still inspects actual
// local fixtures. No sidecar/model download or Telegram connection is allowed.
func TestIntegrationP41LocalASRInstallOffline(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_SIDECARS", "")
	blocked := &installNoNetwork{}
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: blocked}
	t.Cleanup(func() { http.DefaultClient = oldClient })
	var calls atomic.Int32
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	b := newSettingsPanelConfigured(t, nil, func(dir string) *localasr.Engine {
		return localasr.NewWithInstaller(dir, func(ctx context.Context, data string, progress func(string, int64, int64)) error {
			calls.Add(1)
			defer close(finished)
			progress("offline fixture", 1, 2)
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			for _, name := range []string{"whisper-cli", "ffmpeg"} {
				if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), platform.ExeName(name)), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					return err
				}
			}
			models := filepath.Join(data, "models")
			if err := os.MkdirAll(models, 0700); err != nil {
				return err
			}
			model, err := os.Create(filepath.Join(models, localasr.ModelName))
			if err != nil {
				return err
			}
			// Sparse, inert fixture: no model bytes are downloaded or executed.
			err = model.Truncate(574041195)
			closeErr := model.Close()
			if err != nil {
				return err
			}
			progress("offline fixture", 2, 2)
			return closeErr
		})
	})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		if calls.Load() > 0 {
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Error("installer did not finish")
			}
		}
	})
	const jsonType = "application/json"
	if code, _, _ := b.page("POST", "/api/asr/install", jsonType, `{}`, false); code != http.StatusUnauthorized {
		t.Fatalf("install without session: %d", code)
	}
	b.login()
	if code, _, body := b.page("GET", "/configuracoes", "", "", false); code != http.StatusOK || !strings.Contains(body, "data-asr-install") {
		t.Fatalf("install button missing: %d %s", code, body)
	}
	if code, _, _ := b.page("POST", "/api/asr/install", jsonType, `{}`, false); code != http.StatusForbidden {
		t.Fatalf("install without CSRF: %d", code)
	}
	if calls.Load() != 0 {
		t.Fatal("merely reading settings or a refused request started installation")
	}
	if code, _, body := b.page("POST", "/api/asr/install", jsonType, `{}`, true); code != http.StatusOK {
		t.Fatalf("start install: %d %s", code, body)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("installer was not invoked")
	}
	code, _, body := b.page("GET", "/api/transcription", "", "", false)
	var status localasr.Status
	if code != http.StatusOK || json.Unmarshal([]byte(body), &status) != nil || status.Ready || status.Install.State != "running" || status.Install.Downloaded != 1 || status.Install.Total != 2 {
		t.Fatalf("running progress: %d %s", code, body)
	}
	if code, _, body := b.page("POST", "/api/asr/install", jsonType, `{}`, true); code != http.StatusOK {
		t.Fatalf("idempotent install while running: %d %s", code, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent install calls: %d", calls.Load())
	}
	unblock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _, body = b.page("GET", "/api/transcription", "", "", false)
		if code != http.StatusOK || json.Unmarshal([]byte(body), &status) != nil {
			t.Fatalf("poll: %d %s", code, body)
		}
		if status.Install.State == "done" {
			break
		}
		if status.Install.State == "error" || time.Now().After(deadline) {
			t.Fatalf("installation did not complete: %s", body)
		}
		time.Sleep(time.Millisecond)
	}
	if !status.Ready || status.Whisper != "" || status.FFmpeg != "" || status.Model != localasr.ModelName || strings.Contains(body, b.p.DataDir) {
		t.Fatalf("ready status or path redaction: %s", body)
	}
	if code, _, body := b.page("GET", "/configuracoes", "", "", false); code != http.StatusOK || strings.Contains(body, "data-asr-install") {
		t.Fatalf("installed page still offers installation: %d %s", code, body)
	}
	if calls.Load() != 1 || blocked.calls.Load() != 0 {
		t.Fatalf("installer calls=%d, external HTTP calls=%d", calls.Load(), blocked.calls.Load())
	}
}
