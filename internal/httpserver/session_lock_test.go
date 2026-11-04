//go:build linux || darwin || windows

package httpserver

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSessionLockProcessHelper(t *testing.T) {
	path := os.Getenv("TELEGRAM_MCP_SESSION_LOCK_TEST")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Println("waiting")
	if err := lockSessionFile(f); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	if err := unlockSessionFile(f); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLockBlocksOtherProcess(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "lock-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := lockSessionFile(f); err != nil {
		t.Fatal(err)
	}
	defer unlockSessionFile(f)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLockProcessHelper$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "TELEGRAM_MCP_SESSION_LOCK_TEST="+f.Name())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	lines := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	select {
	case line := <-lines:
		if line != "waiting" {
			t.Fatalf("child output = %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not start")
	}
	select {
	case line := <-lines:
		t.Fatalf("child did not block: %q", line)
	case <-time.After(150 * time.Millisecond):
	}
	if err := unlockSessionFile(f); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-lines:
		if line != "locked" {
			t.Fatalf("child output = %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not acquire released lock")
	}
	for range lines {
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLockRejectsClosedFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "lock-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lockSessionFile(f); err == nil {
		t.Fatal("locked closed file")
	}
	if err := unlockSessionFile(f); err == nil {
		t.Fatal("unlocked closed file")
	}
}
