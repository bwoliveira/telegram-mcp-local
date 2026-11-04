package mcp

import (
	"encoding/json"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// The poll preview must disclose the visibility and mode the engine resolves,
// so the approval binding covers them even though they are not MCP inputs.
func TestCanonicalPollParametersDiscloseEffectiveDefaults(t *testing.T) {
	public := false
	anonymous := true
	base := engine.SendPollRequest{Peer: engine.PeerRef{Type: "chat", ID: 20}, Question: "Q?", Options: []string{"A", "B"}}
	encode := func(r engine.SendPollRequest) (map[string]any, string) {
		t.Helper()
		_, p, err := canonicalWriteParameters("primary", r)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return p, string(b)
	}
	def, defJSON := encode(base)
	if def["anonymous"] != true || def["quiz"] != false {
		t.Fatalf("default poll preview = %v, want anonymous=true quiz=false", def)
	}
	explicit := base
	explicit.Anonymous = &anonymous
	if _, got := encode(explicit); got != defJSON {
		t.Fatalf("explicit anonymous binding %s differs from the default %s", got, defJSON)
	}
	visible := base
	visible.Anonymous = &public
	p, got := encode(visible)
	if p["anonymous"] != false || got == defJSON {
		t.Fatalf("public-voter poll binding %s is not distinguished from the anonymous default %s", got, defJSON)
	}
}
