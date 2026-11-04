package panel

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The conversation endpoint pages by the store's (date, id) cursor so the
// panel's refresh can read "everything from the oldest shown on" exactly.
func TestConversationHTTPCursorPagination(t *testing.T) {
	_, h := conversationFixture(t)
	type page struct {
		Messages []struct {
			ID     string `json:"id"`
			Cursor string `json:"cursor"`
		} `json:"messages"`
		HasMore bool `json:"has_more"`
	}
	read := func(target string) page {
		t.Helper()
		w := conversationRequest(h, "GET", target, "", true)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", target, w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ids := func(p page) (out []string) {
		for _, m := range p.Messages {
			out = append(out, m.ID+"@"+m.Cursor)
		}
		return out
	}
	// Oldest first within the page, as the offset form returns it.
	if got := ids(read("/api/chats/user:1?limit=2&before=1005:5")); len(got) != 2 || got[0] != "3@1003:3" || got[1] != "4@1004:4" {
		t.Fatalf("before page: %v", got)
	}
	if p := read("/api/chats/user:1?limit=10&since=1004:4"); len(p.Messages) != 2 || p.HasMore || p.Messages[0].ID != "4" {
		t.Fatalf("since page: %v", ids(p))
	}
	if p := read("/api/chats/user:1?limit=1&since=1002:2&before=1004:4&q=needle"); len(p.Messages) != 1 || !p.HasMore || p.Messages[0].ID != "3" {
		t.Fatalf("bounded search page: %v", ids(p))
	}
	for _, bad := range []string{
		"/api/chats/user:1?before=abc", "/api/chats/user:1?before=1005:0", "/api/chats/user:1?since=01:1",
		"/api/chats/user:1?before=1005:5&offset=0", "/api/chats/user:1?since=1:1&offset=2",
		"/api/chats/user:1?before=1005:5&before=1004:4", "/api/chats/user:1?since=",
		"/api/search?q=needle&before=1005:5",
	} {
		if w := conversationRequest(h, "GET", bad, "", true); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: HTTP %d", bad, w.Code)
		}
	}
}
