package pocstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestCheckpointSurvivesReopenAndIsAccountScoped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "store.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := engine.UpdatesState{Pts: 12, Qts: 3, Date: 42, Seq: 8}
	if err := s.SaveUpdatesState(ctx, "primary", st); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, ok, err := s.LoadUpdatesState(ctx, "primary")
	if err != nil || !ok || got != st {
		t.Fatalf("state = %+v, %v, %v", got, ok, err)
	}
	_, ok, err = s.LoadUpdatesState(ctx, "other")
	if err != nil || ok {
		t.Fatalf("other account: %v %v", ok, err)
	}
}
