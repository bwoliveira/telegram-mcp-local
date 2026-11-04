package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestFactoryAndReadOnlyPreview(t *testing.T) {
	var factory engine.Factory = fake.New
	controller, err := factory(engine.Config{Account: "test"})
	if err != nil {
		t.Fatal(err)
	}
	f := controller.(*fake.Controller)
	if f.Mode() != engine.ModeReadOnly {
		t.Fatal("new fake must be read-only")
	}
	req := engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "hello"}
	if _, err := f.SendText(context.Background(), req); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("write: %v", err)
	}
	req.DryRun = true
	result, err := f.SendText(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Done || result.Preview == nil || result.Preview.Fingerprint == "" || len(result.Preview.Warnings) == 0 {
		t.Fatalf("preview: %+v", result)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("preview or blocked call recorded a write")
	}
}
