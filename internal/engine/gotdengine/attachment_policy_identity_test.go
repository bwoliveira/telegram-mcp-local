package gotdengine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/tg"
)

// Full adapter snapshots omit Remote. Equal public labels must neither invent
// a protection-only edit nor hide a genuinely different Telegram attachment.
func TestAttachmentPolicyIdentitySQLite(t *testing.T) {
	for _, kind := range []string{"document", "photo"} {
		for _, change := range []string{"protection", "replacement", "credentials", "caption", "quote"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "identity.db")
				open := func() *store.Store {
					db, err := store.Open(ctx, path, "primary", store.WithClock(func() time.Time { return time.Unix(3000, 0) }))
					if err != nil {
						t.Fatal(err)
					}
					return db
				}
				db := open()
				raw := &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, Message: "Synthetic attachment", ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 7, QuoteText: "Original quote"}}
				setMedia := func(id, access int64, reference string) {
					if kind == "document" {
						raw.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: id, AccessHash: access, DCID: 2, FileReference: []byte(reference), MimeType: "application/pdf", Size: 123, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "same.pdf"}}}})
					} else {
						raw.SetMedia(&tg.MessageMediaPhoto{Photo: &tg.Photo{ID: id, AccessHash: access, DCID: 2, FileReference: []byte(reference), Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", Size: 123, W: 10, H: 10}}}})
					}
				}
				setMedia(401, 402, "synthetic-reference-A")
				raw.SetEditDate(1100)
				initial, ok := convertMessage("primary", raw)
				if !ok {
					t.Fatal("initial conversion rejected")
				}
				if len(initial.ContentIdentity) != 64 || !initial.Media.Downloadable {
					t.Fatal("missing canonical identity or invalid attachment fixture")
				}
				if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceUpdate, Messages: []engine.Message{initial}}); err != nil {
					t.Fatal(err)
				}
				// The comparison must use durable identity, not a process-local cache.
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db = open()
				defer db.Close()
				raw.Noforwards = true
				raw.SetEditDate(1200)
				switch change {
				case "replacement":
					setMedia(501, 502, "synthetic-reference-B")
				case "credentials":
					setMedia(401, 502, "synthetic-reference-B")
				case "caption":
					raw.Message = "A genuinely edited caption"
				case "quote":
					raw.ReplyTo = &tg.MessageReplyHeader{ReplyToMsgID: 7, QuoteText: "A genuinely edited quote"}
				}
				incoming, ok := convertMessage("primary", raw)
				if !ok {
					t.Fatal("incoming conversion rejected")
				}
				if len(incoming.Media.Remote) != 0 {
					t.Fatal("adapter retained download credentials")
				}
				encoded, err := json.Marshal(incoming)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), incoming.ContentIdentity) || strings.Contains(string(encoded), "synthetic-reference") || strings.Contains(string(encoded), "content_identity") {
					t.Fatal("identity or credentials escaped message JSON")
				}
				batch := engine.Batch{Account: "primary", Source: engine.SourceRefresh, Messages: []engine.Message{incoming}}
				applied, err := db.Apply(ctx, batch)
				if err != nil {
					t.Fatal(err)
				}
				wantEdits := 0
				if change == "replacement" || change == "caption" || change == "quote" {
					wantEdits = 1
				}
				if len(applied.Edited) != wantEdits {
					t.Fatalf("edits=%d want=%d: %+v", len(applied.Edited), wantEdits, applied)
				}
				got, found, err := db.Message(ctx, initial.Key)
				if err != nil || !found {
					t.Fatalf("read found=%t err=%v", found, err)
				}
				if got.ContentIdentity != incoming.ContentIdentity {
					t.Fatal("canonical identity was not persisted/read back")
				}
				if !got.Protected || got.Media.Downloadable || got.ServerEditDate == nil || got.ServerEditDate.Unix() != 1200 {
					t.Fatalf("policy/order not retained: %+v", got)
				}
				wantEdit := int64(1100)
				if wantEdits > 0 {
					wantEdit = 1200
				}
				if got.EditDate == nil || got.EditDate.Unix() != wantEdit {
					t.Fatalf("visible edit=%v want unix %d", got.EditDate, wantEdit)
				}
				replay, err := db.Apply(ctx, batch)
				if err != nil || len(replay.Edited) != 0 {
					t.Fatalf("replay emitted edit: %+v err=%v", replay, err)
				}
			})
		}
	}
}
