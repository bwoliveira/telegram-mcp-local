package panel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

type failedDurableStatusStore struct{ *statusFixtureStore }

func (s failedDurableStatusStore) ReadStatus(context.Context, engine.AccountID) (engine.HistoryProgress, engine.UpdatesStatus, error) {
	return engine.HistoryProgress{}, engine.UpdatesStatus{}, errors.New("private SQL diagnostic")
}

func TestPanelStatusDoesNotClaimHealthyCheckpointAfterAggregateFailure(t *testing.T) {
	p, h, eng := newStatusFixture(t)
	reader := failedDurableStatusStore{p.ReadStore.(*statusFixtureStore)}
	p.ReadStore = reader
	p.Server = mcp.New(mcp.Config{Engine: eng, ReadStore: reader, State: p.State})
	response := fixtureGET(t, h, "/api/panel/status")
	if response.Code != 200 {
		t.Fatalf("status: HTTP %d", response.Code)
	}
	var snapshot struct{ HTML string }
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Sem lacunas conhecidas", "Histórico completo", "private SQL diagnostic"} {
		if strings.Contains(snapshot.HTML, forbidden) {
			t.Errorf("failed aggregate exposed %q", forbidden)
		}
	}
	if !strings.Contains(snapshot.HTML, "Histórico parcial") {
		t.Fatal("failed checkpoint must make coverage partial")
	}
}
