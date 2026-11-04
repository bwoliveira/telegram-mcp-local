package store

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Telegram dates have one-second resolution: within one conversation the
// panel pages by the complete (date, msg_id) key, so rows arriving or leaving
// between two pages neither repeat nor skip a row, even when dates tie.
func TestPanelConversationCursorPagesTotalOrder(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "user", 1, "user")
	for i := 1; i <= 120; i++ {
		date := 1000
		if i > 100 {
			date = 1001 // a later second after a long run of equal dates
		}
		queryMsg(t, s, "user", 1, i, date, "tied")
	}
	peer := panelTestPeer(engine.PeerUser, 1)
	var got []PanelMessage
	var before *PanelCursor
	for pages := 0; ; pages++ {
		page, err := s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Limit: 25, Before: before})
		if err != nil || pages > 10 {
			t.Fatalf("cursor page %d: %v", pages, err)
		}
		got = append(got, page.Messages...)
		if pages == 0 {
			// Between two pages: one arrival in a tied second, one deletion
			// of a row not yet read, one of a row already read.
			queryMsg(t, s, "user", 1, 500, 1001, "arrival")
			querySQL(t, s, `DELETE FROM messages WHERE peer_type='user' AND peer_id=1 AND msg_id IN (50,110)`)
		}
		if !page.HasMore {
			break
		}
		last := page.Messages[len(page.Messages)-1]
		cursor, err := ParsePanelCursor(last.Cursor)
		if err != nil || cursor != (PanelCursor{Date: last.Date.Unix(), ID: mustAtoi(t, last.ID)}) {
			t.Fatalf("cursor %q of %s: %v", last.Cursor, last.ID, err)
		}
		before = &cursor
	}
	seen := map[string]bool{}
	for i, m := range got {
		if seen[m.ID] {
			t.Fatalf("message %s repeated", m.ID)
		}
		seen[m.ID] = true
		if i > 0 {
			prev := got[i-1]
			if prev.Date.Before(m.Date) || (prev.Date.Equal(m.Date) && mustAtoi(t, prev.ID) <= mustAtoi(t, m.ID)) {
				t.Fatalf("order broken at %s after %s", m.ID, prev.ID)
			}
		}
	}
	// 120 rows read: the arrival is newer than page one and the deleted row
	// 110 had been read already; only 50, deleted before its page, is absent.
	if len(got) != 119 || seen["50"] || !seen["110"] || seen["500"] || !seen["1"] {
		t.Fatalf("cursor paging skipped or repeated rows: %d rows", len(got))
	}

	// since is inclusive and bounds the same order from below.
	page, err := s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Limit: 100, Since: &PanelCursor{Date: 1000, ID: 98}})
	if err != nil || len(page.Messages) != 23 || page.HasMore || page.Messages[0].ID != "500" || page.Messages[1].ID != "120" || page.Messages[22].ID != "98" {
		t.Fatalf("since window: %d %v", len(page.Messages), err)
	}
	page, err = s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Limit: 4, Since: &PanelCursor{Date: 1000, ID: 98}, Before: &PanelCursor{Date: 1001, ID: 103}})
	if err != nil || len(page.Messages) != 4 || !page.HasMore || page.Messages[0].ID != "102" || page.Messages[3].ID != "99" {
		t.Fatalf("bounded window: %+v %v", page.Messages, err)
	}
	// The cursor also bounds a text search inside the conversation.
	page, err = s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Query: "tied", Limit: 3, Before: &PanelCursor{Date: 1000, ID: 3}})
	if err != nil || len(page.Messages) != 2 || page.HasMore || page.Messages[0].ID != "2" {
		t.Fatalf("search below cursor: %+v %v", page.Messages, err)
	}
}

func TestPanelCursorValidation(t *testing.T) {
	for _, bad := range []string{"", "1", "1:", ":1", "1:0", "-1:1", "01:1", "1:01", "1:x", "1:1:1", " 1:1", "1:9223372036854775808"} {
		if _, err := ParsePanelCursor(bad); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("cursor %q accepted", bad)
		}
	}
	if c, err := ParsePanelCursor("1000:42"); err != nil || c != (PanelCursor{Date: 1000, ID: 42}) || c.String() != "1000:42" {
		t.Fatalf("canonical cursor: %+v %v", c, err)
	}
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "user", 1, "user")
	queryMsg(t, s, "user", 1, 1, 1000, "needle")
	peer := panelTestPeer(engine.PeerUser, 1)
	cursor := &PanelCursor{Date: 1000, ID: 2}
	// A cursor orders one conversation only; it never combines with offsets.
	if _, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "needle", Before: cursor}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("cross-conversation cursor: %v", err)
	}
	if _, err := s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Offset: 1, Since: cursor}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("cursor with offset: %v", err)
	}
	page, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "needle"})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Cursor != "" {
		t.Fatalf("cross-conversation results carry no cursor: %+v %v", page.Messages, err)
	}
	page, err = s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Cursor != "1000:1" {
		t.Fatalf("conversation cursor: %+v %v", page.Messages, err)
	}
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	c, err := ParsePanelCursor("0:" + s)
	if err != nil {
		t.Fatalf("message id %q", s)
	}
	return c.ID
}
