package service

import (
	"os"
	"testing"
)

type missingLaunchdJob struct{}

func (missingLaunchdJob) Error() string { return "job not loaded" }
func (missingLaunchdJob) ExitCode() int { return 113 }

func TestLaunchdLifecycleHandlesAlreadyStoppedAndStarted(t *testing.T) {
	m, _, _ := newTestManager(t)
	m.GOOS = "darwin"
	loaded := false
	bootstrapCount, bootoutCount := 0, 0
	m.Runner = func(name string, args ...string) ([]byte, error) {
		if name != "launchctl" {
			t.Fatalf("unexpected executable %s", name)
		}
		switch args[0] {
		case "print":
			if !loaded {
				return nil, missingLaunchdJob{}
			}
			return []byte("state = running\n"), nil
		case "bootstrap":
			if loaded {
				t.Fatal("bootstrapped an already-loaded job")
			}
			loaded = true
			bootstrapCount++
		case "bootout":
			if !loaded {
				t.Fatal("booted out an already-stopped job")
			}
			loaded = false
			bootoutCount++
		default:
			t.Fatalf("unexpected verb %s", args[0])
		}
		return nil, nil
	}
	path, err := m.Install(validSpec())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := m.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("uninstall after stop preserved plist")
	}
	if _, err := m.Install(validSpec()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(validSpec()); err != nil {
		t.Fatal("reinstall after stop failed:", err)
	}
	if bootstrapCount != 3 || bootoutCount != 2 {
		t.Fatalf("bootstrap=%d bootout=%d", bootstrapCount, bootoutCount)
	}
}
