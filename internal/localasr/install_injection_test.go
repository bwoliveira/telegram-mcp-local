package localasr_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
)

func TestInjectedInstallerExplicitOnlyAndRetry(t *testing.T) {
	var calls atomic.Int32
	dir := t.TempDir()
	e := localasr.NewWithInstaller(dir, func(ctx context.Context, data string, progress func(string, int64, int64)) error {
		if data != dir {
			return errors.New("wrong installation directory")
		}
		progress("offline step", 3, 5)
		if calls.Add(1) == 1 {
			return errors.New("offline fixture failure")
		}
		return nil
	})
	_ = e.Status()
	if _, err := e.Transcribe(context.Background(), "not-present.ogg", "", ""); err == nil {
		t.Fatal("transcription unexpectedly ready")
	}
	if calls.Load() != 0 {
		t.Fatal("status or transcription implicitly started installation")
	}
	wait := func(want string) localasr.Status {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s := e.Status()
			if s.Install.State == want {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatalf("install state=%s, want %s", s.Install.State, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := e.StartInstall(); err != nil {
		t.Fatal(err)
	}
	s := wait("error")
	if s.Install.Error != "offline fixture failure" || s.Install.Step != "offline step" || s.Install.Downloaded != 3 || s.Install.Total != 5 {
		t.Fatalf("failed install progress: %+v", s.Install)
	}
	if err := e.StartInstall(); err != nil {
		t.Fatal(err)
	}
	s = wait("done")
	if calls.Load() != 2 || s.Ready {
		t.Fatalf("retry calls=%d, status=%+v; installer must not fabricate readiness", calls.Load(), s)
	}
}

func TestInjectedInstallerReceivesInstallNowContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	e := localasr.NewWithInstaller(t.TempDir(), func(got context.Context, _ string, progress func(string, int64, int64)) error {
		calls.Add(1)
		progress("cancelled", 0, 1)
		return got.Err()
	})
	other := localasr.NewWithInstaller(t.TempDir(), func(context.Context, string, func(string, int64, int64)) error {
		t.Error("another engine's installer was called")
		return nil
	})
	if err := e.InstallNow(ctx, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("InstallNow: %v", err)
	}
	if calls.Load() != 1 || other.Status().Install.State != "idle" {
		t.Fatal("installation dependency is not per-engine")
	}
}
