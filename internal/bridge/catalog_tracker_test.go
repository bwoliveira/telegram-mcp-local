package bridge

import (
	"fmt"
	"strings"
	"testing"
)

func TestCatalogTrackerIgnoresFailedUnrelatedAndLateResponses(t *testing.T) {
	v1, v2 := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64)
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	response := func(version string) []byte {
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"_meta":{"catalogVersion":%q}}}`, version))
	}
	var tracker catalogTracker
	if tracker.observe(request, response(v1), 1) {
		t.Fatal("first catalog must establish a baseline, not notify")
	}
	for _, tc := range []struct {
		name      string
		req, resp []byte
	}{
		{"transport failure", request, []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"not open"}}`)},
		{"error with metadata", request, []byte(fmt.Sprintf(`{"id":1,"error":{"code":-32000},"result":{"_meta":{"catalogVersion":%q}}}`, v2))},
		{"legacy daemon", request, []byte(`{"id":1,"result":{}}`)},
		{"not JSON", request, []byte(`not JSON`)},
		{"invalid version", request, response("sha256:" + strings.Repeat("z", 64))},
		{"release version not catalog", request, response("release-2")},
		{"unrelated tool result", []byte(`{"id":1,"method":"tools/call"}`), response(v2)},
		{"notification", []byte(`{"method":"notifications/initialized"}`), response(v2)},
		{"mismatched id", []byte(`{"id":2,"method":"ping"}`), response(v2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tracker.observe(tc.req, tc.resp, 99) {
				t.Fatal("unusable response notified a catalog change")
			}
			if tracker.version != v1 || tracker.sequence != 1 {
				t.Fatalf("unusable response overwrote baseline: %+v", tracker)
			}
		})
	}
	if !tracker.observe(request, response(v2), 3) {
		t.Fatal("successful reconnect with a changed hash must notify")
	}
	if tracker.observe(request, response(v1), 2) {
		t.Fatal("late response from old daemon reverted the observed catalog")
	}
	if tracker.observe(request, response(v2), 4) {
		t.Fatal("unchanged catalog notified again")
	}
	if !tracker.observe(request, response(v1), 5) {
		t.Fatal("a real downgrade at a newer sequence must invalidate too")
	}
}
