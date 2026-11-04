package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestToolsListCatalogCacheHint(t *testing.T) {
	s := New(Config{})
	var listed struct {
		Result struct {
			Tools      []any             `json:"tools"`
			TTLMS      int               `json:"ttlMs"`
			CacheScope string            `json:"cacheScope"`
			Meta       map[string]string `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Result.TTLMS != 600000 {
		t.Fatalf("tools/list result.ttlMs = %d, want SEP-2549 ten-minute hint 600000", listed.Result.TTLMS)
	}
	if listed.Result.CacheScope != "public" {
		t.Fatalf("catalog cache scope = %q, want public (definitions contain no account data)", listed.Result.CacheScope)
	}
	canonical, err := json.Marshal(listed.Result.Tools)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
	if got := listed.Result.Meta["catalogVersion"]; got != want {
		t.Fatalf("catalogVersion = %q, want content hash %q", got, want)
	}
	// The same catalog has the same identity across server instances; neither
	// runtime state nor the release version is a cache invalidation signal.
	other := New(Config{})
	if got := string(other.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))); got != string(s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))) {
		t.Fatalf("identical catalogs changed across server instances: %s", got)
	}
}

func TestInitializeAndPingExposeCatalogVersion(t *testing.T) {
	s := New(Config{})
	var version string
	for _, method := range []string{"initialize", "ping", "tools/list"} {
		var response struct {
			Result struct {
				Meta         map[string]string `json:"_meta"`
				Capabilities struct {
					Tools struct {
						ListChanged bool `json:"listChanged"`
					} `json:"tools"`
				} `json:"capabilities"`
			} `json:"result"`
		}
		body := s.Handle(context.Background(), []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q}`, method)))
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		got := response.Result.Meta["catalogVersion"]
		if got == "" {
			t.Fatalf("%s does not expose catalogVersion: %s", method, body)
		}
		if version != "" && got != version {
			t.Fatalf("%s catalogVersion = %s, want %s", method, got, version)
		}
		version = got
		if method == "initialize" && !response.Result.Capabilities.Tools.ListChanged {
			t.Fatalf("initialize must advertise tools.listChanged: %s", body)
		}
	}
}

func TestCatalogVersionTracksCompleteDefinitions(t *testing.T) {
	const original = `[{"name":"health","description":"Old description","inputSchema":{"type":"object","properties":{"limit":{"type":"integer"}},"required":["limit"]},"annotations":{"readOnlyHint":true}}]`
	decode := func(raw string) []any {
		t.Helper()
		var tools []any
		if err := json.Unmarshal([]byte(raw), &tools); err != nil {
			t.Fatal(err)
		}
		return tools
	}
	want := catalogVersion(decode(original))
	// Object-key insertion order and JSON whitespace are not catalog changes.
	reordered := `[{"annotations":{"readOnlyHint":true}, "inputSchema":{"required":["limit"], "properties":{"limit":{"type":"integer"}},"type":"object"},"description":"Old description","name":"health"}]`
	if got := catalogVersion(decode(reordered)); got != want {
		t.Fatalf("object order changed catalog version: %s != %s", got, want)
	}
	for _, tc := range []struct{ name, before, after string }{
		{"name", `"health"`, `"new_health"`},
		{"description", `"Old description"`, `"New description"`},
		{"schema", `"integer"`, `"string"`},
		{"required", `"required":["limit"]`, `"required":[]`},
		{"annotations", `"readOnlyHint":true`, `"readOnlyHint":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(original, tc.before, tc.after, 1)
			if changed == original {
				t.Fatal("fixture did not change")
			}
			if got := catalogVersion(decode(changed)); got == want {
				t.Fatalf("%s change did not invalidate catalog version", tc.name)
			}
		})
	}
	if got := catalogVersion(append(decode(original), map[string]any{"name": "new_tool"})); got == want {
		t.Fatal("adding a tool did not invalidate catalog version")
	}
	if got := catalogVersion([]any{}); got == want {
		t.Fatal("removing tools did not invalidate catalog version")
	}
}
