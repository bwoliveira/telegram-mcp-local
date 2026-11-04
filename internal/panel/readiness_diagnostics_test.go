package panel

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"strings"
	"testing"
)

func TestActiveHistoryJobIsNotIdlePartialHistory(t *testing.T) {
	st := engine.Status{State: engine.StateConnected}
	v := statusView{Diagnostics: engine.Diagnostics{Authorized: true, Connected: true}}
	tone, text := statusCoverageText(st, v, &mcp.HistoryJob{})
	if tone != "sync" || strings.Contains(text, "Pronto") {
		t.Fatalf("active history job reported idle: %s %s", tone, text)
	}
}

func TestStatusCoverageDoesNotAnnounceReadyWithUnavailableDiagnostics(t *testing.T) {
	st := engine.Status{State: engine.StateConnected}
	for _, v := range []statusView{{DiagnosticsErr: true}, {Diagnostics: engine.Diagnostics{Authorized: false, Connected: true}}, {Diagnostics: engine.Diagnostics{Authorized: true, Connected: false}}} {
		tone, text := statusCoverageText(st, v, nil)
		if tone == "ok" || strings.Contains(text, "Pronto") {
			t.Fatalf("unavailable diagnostics claim ready: %s %s", tone, text)
		}
	}
}
