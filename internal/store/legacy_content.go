package store

import (
	"bytes"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// securityContentEqual recognizes a policy-only representation change. The
// adapter's independent identity includes stable attachment IDs and payload,
// but excludes protection-derived downloadability and refreshed credentials.
func securityContentEqual(previous, incoming engine.Message, advancing bool) bool {
	if previous.ContentIdentity != "" && incoming.ContentIdentity != "" &&
		(previous.Media == nil || previous.Media.TTLSeconds == 0) &&
		(incoming.Media == nil || incoming.Media.TTLSeconds == 0) {
		return previous.ContentIdentity == incoming.ContentIdentity
	}
	legacy := previous.ContentHash == ""
	if previous.Media != nil && incoming.Media != nil && previous.Media.TTLSeconds == 0 && incoming.Media.TTLSeconds == 0 && len(previous.Media.Remote) > 0 && !bytes.Equal(previous.Media.Remote, incoming.Media.Remote) {
		// Remote belongs to the adapter: it may contain an ID, access hash,
		// file reference and DC, but the store has no decoder for that opaque
		// encoding. A differing/omitted location cannot prove equal identity,
		// independently of protection or the presence of a legacy hash.
		return false
	}
	if legacy && legacyContentHash(previous) == legacyContentHash(incoming) {
		return true
	}
	if previous.Media == nil || incoming.Media == nil || previous.Media.Type != incoming.Media.Type {
		return false
	}
	oldMedia, newMedia := *previous.Media, *incoming.Media
	if oldMedia.TTLSeconds == 0 && newMedia.TTLSeconds > 0 {
		// An unredacted equal-revision legacy copy must agree before redaction.
		// Otherwise a contradictory caption could erase a valid retained row.
		if (legacy && !advancing) || (incoming.Text != "[ephemeral media]" && incoming.Text != "[temporary media]") {
			newMedia.TTLSeconds = 0
			incoming.Media = &newMedia
			return legacyContentHash(previous) == legacyContentHash(incoming)
		}
		redact := func(m engine.Message) engine.Message {
			m.Media = &engine.Media{Type: m.Media.Type, TTLSeconds: newMedia.TTLSeconds}
			m.Text, m.Entities, m.Location, m.Poll = "[temporary media]", nil, nil, nil
			return m
		}
		return legacyContentHash(redact(previous)) == legacyContentHash(redact(incoming))
	}
	// Downloadable is derived from protection, but public metadata alone does
	// not establish attachment identity. Require the same retained opaque
	// location before ignoring a non-legacy adapter-hash disagreement. Older
	// snapshots without ContentIdentity or Remote cannot prove policy-only
	// equality; retain the conservative fallback instead of masking a swap.
	if !previous.Protected && incoming.Protected && oldMedia.Downloadable && !newMedia.Downloadable && len(oldMedia.Remote) > 0 && bytes.Equal(oldMedia.Remote, newMedia.Remote) {
		return legacyContentHash(previous) == legacyContentHash(incoming)
	}
	return false
}

// Legacy rows retained public attachment metadata but not Telegram attachment
// IDs. Compare only retained evidence; the first equal snapshot establishes the
// adapter hash for future equal-looking replacements. Never infer an ID from
// filenames, counters, credentials or an empty hash.
func legacyContentHash(m engine.Message) string {
	if m.Media != nil {
		media := *m.Media
		media.Downloadable = false // Derived policy, not content.
		media.Remote = nil
		if media.TTLSeconds > 0 {
			// Match the store's redacted representation, not the adapter's
			// differently worded placeholder or discarded payload fields.
			media = engine.Media{Type: media.Type, TTLSeconds: media.TTLSeconds}
			m.Text = "[temporary media]"
			m.Entities, m.Location, m.Poll = nil, nil, nil
		}
		m.Media = &media
	}
	if m.ReplyTo != nil {
		reply := *m.ReplyTo
		reply.Quote = "" // Legacy schema did not retain the quote text.
		m.ReplyTo = &reply
	}
	return engine.MessageContentHash(m, "")
}
