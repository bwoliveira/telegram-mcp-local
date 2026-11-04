package store

import (
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestContentIdentityAuthoritativeComparison(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		previous := securityMessage()
		previous.ContentIdentity = "stable-canonical-content-A"
		if legacy {
			previous.ContentHash = ""
		}
		incoming := previous
		media := *previous.Media
		incoming.Media = &media
		incoming.Media.Downloadable = false
		incoming.Protected = true
		incoming.ContentHash = "changed-protection-dependent-hash"
		if !securityContentEqual(previous, incoming, true) {
			t.Fatal("explicit stable identity did not prove a protection-only change")
		}
		incoming.ContentIdentity = "stable-canonical-content-B"
		if securityContentEqual(previous, incoming, true) {
			t.Fatal("legacy public metadata overrode an explicit identity mismatch")
		}
	}
}

func TestContentIdentityHydratesAtEqualRevision(t *testing.T) {
	s := confOpen(t)
	original := securityMessage()
	original.ContentIdentity = "stable-canonical-content"
	confApply(t, s, securityBatch(engine.SourceUpdate, false, original))
	if _, err := s.writer.Exec(`UPDATE messages SET content_identity=''`); err != nil {
		t.Fatal(err)
	}
	confNoEvents(t, confApply(t, s, securityBatch(engine.SourceRefresh, false, original)))
	if got := confGet(t, s, original.Key); got.ContentIdentity != original.ContentIdentity {
		t.Fatal("matching equal-revision snapshot did not establish durable identity")
	}
	incoming := original
	media := *original.Media
	incoming.Media = &media
	incoming.Media.Downloadable = false
	incoming.Protected = true
	incoming.ContentHash = "protection-dependent-hash"
	securityRevision(&incoming, "newer-reaction-only")
	confNoEvents(t, confApply(t, s, securityBatch(engine.SourceRefresh, false, incoming)))
	if got := confGet(t, s, original.Key); !got.Protected || got.Media.Downloadable || confCount(t, s, "SELECT version FROM messages") != 1 {
		t.Fatalf("hydrated protection-only refresh changed content version/policy: %+v", got)
	}
}

func TestReviewProtectionAttachmentIdentitySwapMustEmitEdit(t *testing.T) {
	s := confOpen(t)
	original := securityMessage()
	original.Media.Type = engine.MediaDocument
	original.Media.Filename = "same.pdf"
	original.Media.MimeType = "application/pdf"
	original.Media.Size = 123
	original.Media.Remote = []byte("attachment-A")
	original.ContentHash = "identity-A"
	confApply(t, s, securityBatch(engine.SourceUpdate, false, original))

	incoming := original
	newer := original.ServerEditDate.Add(time.Second)
	incoming.EditDate, incoming.ServerEditDate = &newer, &newer
	incoming.Protected = true
	incoming.Media = &engine.Media{
		Type: engine.MediaDocument, Filename: "same.pdf", MimeType: "application/pdf",
		Size: 123, Downloadable: false, Remote: []byte("attachment-B"),
	}
	incoming.ContentHash = "identity-B"
	applied := confApply(t, s, securityBatch(engine.SourceRefresh, false, incoming))
	got := confGet(t, s, original.Key)
	if !got.Protected || got.Media == nil || got.Media.Downloadable {
		t.Fatalf("protection was not tightened: protected=%v media=%+v", got.Protected, got.Media)
	}
	if len(applied.Edited) != 1 {
		t.Fatalf("attachment identity changed but edit was masked: applied=%+v version=%d", applied, confCount(t, s, "SELECT version FROM messages"))
	}
	if got.ContentHash != incoming.ContentHash || got.ServerEditDate == nil || !got.ServerEditDate.Equal(newer) || got.EditDate == nil || !got.EditDate.Equal(newer) || confCount(t, s, "SELECT version FROM messages") != 2 {
		t.Fatalf("replacement was not persisted as a genuine edit: message=%+v", got)
	}
	confNoEvents(t, confApply(t, s, securityBatch(engine.SourceRefresh, false, incoming)))
}

func TestProtectionCannotProveAttachmentIdentityFromPublicMetadata(t *testing.T) {
	// The real adapter does not retain Remote credentials. A changed adapter
	// hash is not evidence of a policy-only change, even with identical labels.
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary replacement", true: "protected replacement"}[protected], func(t *testing.T) {
			s := confOpen(t)
			original := securityMessage()
			original.Media.Remote = nil
			confApply(t, s, securityBatch(engine.SourceUpdate, false, original))
			incoming := original
			media := *original.Media
			incoming.Media = &media
			incoming.Protected = protected
			incoming.Media.Downloadable = !protected
			incoming.ContentHash = "replacement-identity"
			securityRevision(&incoming, "newer-reaction-only")
			applied := confApply(t, s, securityBatch(engine.SourceRefresh, false, incoming))
			if len(applied.Edited) != 1 || confCount(t, s, "SELECT version FROM messages") != 2 {
				t.Fatalf("unknown identity was treated as equal public metadata: %+v", applied)
			}
		})
	}
}

func TestProtectionRetainedOpaqueAttachmentIdentity(t *testing.T) {
	for _, protected := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			for _, remote := range []string{"attachment-A", "attachment-B", ""} {
				t.Run(map[bool]string{false: "ordinary", true: "protected"}[protected]+"/"+map[bool]string{false: "cached", true: "legacy"}[legacy]+"/"+remote, func(t *testing.T) {
					previous := securityMessage()
					previous.Media.Remote = []byte("attachment-A")
					if legacy {
						previous.ContentHash = ""
					}
					incoming := previous
					media := *previous.Media
					incoming.Media = &media
					incoming.Media.Remote = []byte(remote)
					incoming.Media.Downloadable = !protected
					incoming.Protected = protected
					incoming.ContentHash = "different-adapter-hash"
					wantEqual := remote == "attachment-A" && (legacy || protected)
					if equal := securityContentEqual(previous, incoming, true); equal != wantEqual {
						t.Fatalf("securityContentEqual=%v, want %v", equal, wantEqual)
					}
				})
			}
		}
	}
}
