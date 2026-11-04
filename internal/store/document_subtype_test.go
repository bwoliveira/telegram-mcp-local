package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestDocumentSubtypeSameRevisionRepair(t *testing.T) {
	for _, kind := range []engine.MediaType{engine.MediaVideo, engine.MediaVideoNote, engine.MediaAudio, engine.MediaVoice, engine.MediaSticker, engine.MediaAnimation} {
		t.Run(string(kind), func(t *testing.T) {
			s := queryStore(t)
			ctx := context.Background()
			expiry := time.Unix(5000, 0)
			key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
			original := engine.Message{Key: key, Date: time.Unix(1000, 0), Text: "original caption", TTLPeriod: 4000, ExpiresAt: &expiry, Protected: true, Media: &engine.Media{Type: engine.MediaDocument, Filename: "keep.bin", MimeType: "application/octet-stream", Remote: []byte("opaque"), Downloadable: false}, Reactions: []engine.ReactionCount{{Emoji: "👍", Count: 2}}}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceUpdate, Messages: []engine.Message{original}}); err != nil {
				t.Fatal(err)
			}
			before := documentSnapshot(t, s)
			refreshed := original
			refreshed.Text = "must not replace content"
			refreshed.TTLPeriod = 99999
			refreshed.ExpiresAt = nil
			refreshed.Protected = false
			refreshed.Media = &engine.Media{Type: kind, Filename: "replace.mp4", Remote: []byte("replace"), Downloadable: true}
			refreshed.Reactions = []engine.ReactionCount{{Emoji: "👎", Count: 9}}
			for n := 0; n < 2; n++ {
				applied, err := s.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceRefresh, Messages: []engine.Message{refreshed}})
				if err != nil {
					t.Fatal(err)
				}
				if len(applied.Inserted) != 0 || len(applied.Edited) != 0 || len(applied.Deleted) != 0 || len(applied.Versions) != 0 || applied.Skipped != 1 {
					t.Fatalf("metadata repair emitted content event: %+v", applied)
				}
				got, ok, err := s.Message(ctx, key)
				if err != nil || !ok || got.Media.Type != kind {
					t.Fatalf("same revision repair: ok=%v type=%v err=%v; want %s", ok, got.Media, err, kind)
				}
				after := documentSnapshot(t, s)
				// The repair changes classification only, not content, version, timestamps,
				// lifecycle, credentials, reactions, provenance or download policy.
				delete(before, "media_type")
				delete(after, "media_type")
				var mediaBefore, mediaAfter map[string]any
				if err := json.Unmarshal([]byte(before["media_json"].(string)), &mediaBefore); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(after["media_json"].(string)), &mediaAfter); err != nil {
					t.Fatal(err)
				}
				delete(mediaBefore, "type")
				delete(mediaAfter, "type")
				if !reflect.DeepEqual(mediaBefore, mediaAfter) {
					t.Fatalf("media payload changed: before=%v after=%v", mediaBefore, mediaAfter)
				}
				delete(before, "media_json")
				delete(after, "media_json")
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("durable metadata changed: before=%v after=%v", before, after)
				}
				before = documentSnapshot(t, s)
			}
		})
	}
}

func documentSnapshot(t *testing.T, s *Store) map[string]any {
	t.Helper()
	rows, err := s.reader.Query(`SELECT * FROM messages WHERE msg_id=8`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("missing fixture")
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for i, c := range cols {
		out[c] = vals[i]
	}
	return out
}

func TestDocumentSubtypeRepairSafety(t *testing.T) {
	for _, policy := range []string{"stale-edit", "tombstone", "expired", "old-temporary", "incoming-temporary", "non-document", "no-downgrade", "no-subtype-overwrite", "unknown"} {
		t.Run(policy, func(t *testing.T) {
			s := queryStore(t)
			ctx := context.Background()
			key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
			old := engine.Message{Key: key, Date: time.Unix(1000, 0), Text: "keep", Media: &engine.Media{Type: engine.MediaDocument}}
			if policy == "stale-edit" {
				v := time.Unix(1500, 0)
				old.EditDate = &v
			}
			if policy == "old-temporary" {
				old.Media.TTLSeconds = 30
			}
			if policy == "non-document" {
				old.Media.Type = engine.MediaPhoto
			}
			if policy == "no-downgrade" || policy == "no-subtype-overwrite" {
				old.Media.Type = engine.MediaVoice
			}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{old}}); err != nil {
				t.Fatal(err)
			}
			if policy == "expired" {
				querySQL(t, s, `UPDATE messages SET expires_at=1900`)
			}
			if policy == "tombstone" {
				if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Deletes: []engine.Deletion{{Peer: &key.Peer, IDs: []int{8}}}}); err != nil {
					t.Fatal(err)
				}
			}
			incoming := engine.Message{Key: key, Date: old.Date, Text: "replace", Media: &engine.Media{Type: engine.MediaVideo}}
			if policy == "incoming-temporary" {
				incoming.Media.TTLSeconds = 30
			}
			if policy == "no-downgrade" || policy == "unknown" {
				incoming.Media.Type = engine.MediaDocument
				incoming.Media.MimeType = "video/mp4"
				incoming.Media.Filename = "movie.mp4"
			}
			var before map[string]any
			if policy != "tombstone" {
				before = documentSnapshot(t, s)
			}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceRefresh, Messages: []engine.Message{incoming}}); err != nil {
				t.Fatal(err)
			}
			if policy == "tombstone" {
				var n int
				if err := s.reader.QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("resurrected tombstone: n=%d err=%v", n, err)
				}
				return
			}
			after := documentSnapshot(t, s)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("unsafe repair changed %s row", policy)
			}
		})
	}
}
