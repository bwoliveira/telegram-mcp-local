package panel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusReportsKnownLocalDepthWithoutClaimingFullHistory(t *testing.T) {
	_, h, _ := phaseFixture(t, "")
	response := panelRequest(t, h, "GET", "/api/panel/status", "")
	if response.Code != 200 {
		t.Fatalf("status: HTTP %d", response.Code)
	}
	var snapshot struct{ HTML string }
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot.HTML, `data-local-history-depth`) ||
		!strings.Contains(snapshot.HTML, "Esse limite não comprova histórico completo no Telegram.") {
		t.Fatal("known retained boundary must be visible and distinct from Telegram completeness")
	}
}
