//go:build unix

package fake_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestMediaFIFO(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.SendMedia(context.Background(), engine.SendMediaRequest{Peer: fake.PrivatePeer, Path: path, Kind: engine.MediaDocument, WriteOptions: engine.WriteOptions{DryRun: true}})
		done <- e
	}()
	select {
	case e := <-done:
		if !errors.Is(e, engine.ErrInvalid) {
			t.Fatalf("FIFO: %v", e)
		}
	case <-time.After(time.Second): // unblock a broken implementation so the test does not leave a goroutine
		writer, e := os.OpenFile(path, os.O_WRONLY, 0600)
		if e == nil {
			_ = writer.Close()
		}
		<-done
		t.Fatal("media preview blocked opening FIFO")
	}
}
