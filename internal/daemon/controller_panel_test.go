package daemon

import (
	"reflect"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

type panelLightController struct {
	engine.Controller
	snapshot engine.Status
}

func (c panelLightController) LightStatus() engine.Status { return c.snapshot }
func (c panelLightController) Status() engine.Status {
	panic("optional LightStatus must not fall back to heavy Status")
}

type panelStatusOnlyController struct {
	engine.Controller
	snapshot engine.Status
}

func (c panelStatusOnlyController) Status() engine.Status { return c.snapshot }

func TestManagedControllerLightStatusFollowsCurrentGeneration(t *testing.T) {
	first := engine.Status{State: engine.StateConnected, Mode: engine.ModeReadOnly}
	second := engine.Status{State: engine.StatePaused, Mode: engine.ModeReadWrite}
	m := &managedController{current: panelLightController{snapshot: first}}
	reader, ok := any(m).(interface{ LightStatus() engine.Status })
	if !ok {
		t.Fatal("managed controller does not forward optional LightStatus")
	}
	if got := reader.LightStatus(); !reflect.DeepEqual(got, first) {
		t.Fatalf("LightStatus() = %+v, want %+v", got, first)
	}
	m.mu.Lock()
	m.current = panelLightController{snapshot: second}
	m.mu.Unlock()
	if got := reader.LightStatus(); !reflect.DeepEqual(got, second) {
		t.Fatalf("LightStatus() after replacement = %+v, want %+v", got, second)
	}
}

func TestManagedControllerLightStatusFallsBackForLegacyController(t *testing.T) {
	want := engine.Status{State: engine.StateStarting, Mode: engine.ModeReadOnly}
	m := &managedController{current: panelStatusOnlyController{snapshot: want}}
	reader, ok := any(m).(interface{ LightStatus() engine.Status })
	if !ok {
		t.Fatal("managed controller does not expose LightStatus")
	}
	if got := reader.LightStatus(); !reflect.DeepEqual(got, want) {
		t.Fatalf("LightStatus() = %+v, want %+v", got, want)
	}
}
