package store

import (
	"context"
	"testing"
)

func TestPanelMediaTTLTranscriptDoesNotInfluenceSearch(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "user", 1, "user")
	queryMsg(t, s, "user", 1, 1, 1901, "caption")
	queryMsg(t, s, "user", 1, 2, 1902, "caption")
	querySQL(t, s, `UPDATE messages SET media_type='voice'`)
	querySQL(t, s, `INSERT INTO transcripts(row_id,text,raw_text,source,created_at) SELECT row_id,CASE WHEN msg_id=1 THEN 'visibleonly' ELSE 'secretonly' END,'raw original','manual',1000 FROM messages`)
	querySQL(t, s, `UPDATE messages SET media_ttl=1 WHERE msg_id=2`)
	for _, tc := range []struct {
		query string
		count int
		id    string
	}{
		{"secretonly", 0, ""}, {"caption NOT secretonly", 2, "2"}, {"caption AND secretonly", 0, ""}, {"visibleonly", 1, "1"},
	} {
		page, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: tc.query})
		if err != nil || len(page.Messages) != tc.count || (tc.count > 0 && page.Messages[0].ID != tc.id) {
			t.Errorf("query %q: %+v %v", tc.query, page, err)
		}
	}
	page, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "caption"})
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("caption: %+v %v", page, err)
	}
	hidden, visible := page.Messages[0], page.Messages[1]
	if hidden.Media != "" || hidden.Transcript != "" || hidden.Corrected || visible.Media != "audio" || visible.Transcript != "visibleonly" || !visible.Corrected {
		t.Fatalf("inherited transcript/media presentation: %+v", page)
	}
}
