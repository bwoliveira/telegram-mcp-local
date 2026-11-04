package daemon

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

type confirmationController struct {
	engine.Controller
	got   bool
	calls int
}

func (c *confirmationController) DeleteConfirmed(_ context.Context, _ engine.DeleteRequest, confirmed bool) (engine.WriteResult, error) {
	c.got = confirmed
	c.calls++
	return engine.WriteResult{}, nil
}
func TestManagedControllerPreservesExplicitDeleteConfirmation(t *testing.T) {
	ctrl, err := fake.New(engine.Config{Account: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	spy := &confirmationController{Controller: ctrl}
	m := &managedController{current: spy}
	for _, confirmed := range []bool{false, true, false} {
		if _, err := m.DeleteConfirmed(context.Background(), engine.DeleteRequest{Revoke: true}, confirmed); err != nil {
			t.Fatal(err)
		}
		if spy.got != confirmed {
			t.Fatal("confirmation was changed by wrapper")
		}
	}
	if spy.calls != 3 {
		t.Fatal(spy.calls)
	}
}
