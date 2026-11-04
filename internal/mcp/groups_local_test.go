package mcp

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

type groupWithoutSnapshot struct{ ReadStore }

func (g groupWithoutSnapshot) ReadGroup(context.Context, engine.AccountID, engine.PeerRef) (engine.Group, error) {
	return engine.Group{}, engine.ErrPeerNotFound
}

func TestKnownGroupWithoutFullSnapshotReturnsExplicitPartial(t *testing.T) {
	f := peopleFake(t)
	s := New(Config{Engine: f, ReadStore: groupWithoutSnapshot{f}})
	result := peopleCall(t, s, "get_group", map[string]any{"group_id": "chat:1001"})
	if result["isError"] == true {
		t.Fatalf("known group rejected because snapshot absent: %v", toolBody(t, result))
	}
	body := toolBody(t, result)
	if body["partial"] != true || body["snapshot_available"] != false || body["note"] == nil {
		t.Fatalf("missing explicit snapshot limitation: %v", body)
	}
}
