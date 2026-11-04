package localasr

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/sidecar"
)

func TestStatusDoesNotPublishDoneAfterStaleToolProbe(t *testing.T) {
	e := NewWithInstaller(t.TempDir(), func(context.Context, string, func(string, int64, int64)) error { return nil })
	e.setInstall(func(i *Install) { i.State = "running" })
	old := systemTools
	t.Cleanup(func() { systemTools = old })
	// Complete the installer precisely during probing. This probe began before
	// completion and must not be combined with a newer done snapshot.
	systemTools = func() sidecar.Tools {
		e.setInstall(func(i *Install) { i.State = "done" })
		return sidecar.Tools{}
	}
	status := e.Status()
	if status.Install.State == "done" {
		t.Fatal("published done with pre-completion component discovery")
	}
}
