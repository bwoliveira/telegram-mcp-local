package store

import (
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestApplySecurityLegacyMetadataOnlyDoesNotInventEdit(t *testing.T) {
	for _, policy := range []string{"protected", "message-ttl", "temporary-media"} {
		t.Run(policy, func(t *testing.T) {
			s := confOpen(t)
			original := securityMessage()
			original.ContentHash = ""
			confApply(t, s, securityBatch(engine.SourceSend, false, original))
			incoming := securityMessage()
			incoming.ContentHash = ""
			securityRevision(&incoming, "newer-reaction-only")
			switch policy {
			case "protected":
				incoming.Protected = true
			case "message-ttl":
				incoming.TTLPeriod = 120
			case "temporary-media":
				incoming.Media.TTLSeconds = 45
			}
			applied := confApply(t, s, securityBatch(engine.SourceRefresh, true, incoming))
			confNoEvents(t, applied)
			got := confGet(t, s, original.Key)
			if got.EditDate == nil || !got.EditDate.Equal(*original.EditDate) || confCount(t, s, "SELECT version FROM messages") != 1 {
				t.Error("legacy security metadata invented a visible edit")
			}
		})
	}
}

func TestApplySecurityOnlyRetainsReactionEvents(t *testing.T) {
	for _, revision := range []string{"stale", "equal", "newer-reaction-only"} {
		t.Run(revision, func(t *testing.T) {
			s := confOpen(t)
			original := securityMessage()
			original.Reactions = []engine.ReactionCount{{Emoji: "👍", Count: 1}}
			confApply(t, s, securityBatch(engine.SourceUpdate, false, original))
			incoming := securityMessage()
			securityRevision(&incoming, revision)
			incoming.Protected = true
			incoming.TTLPeriod = 120
			incoming.Reactions = []engine.ReactionCount{{Emoji: "👍", Count: 2}}
			applied := confApply(t, s, securityBatch(engine.SourceUpdate, true, incoming))
			want := 1
			if revision != "stale" {
				want = 2
			}
			got := confGet(t, s, original.Key)
			if !got.Protected || got.TTLPeriod != 0 || len(got.Reactions) != 1 || got.Reactions[0].Count != want {
				t.Errorf("security/reaction state=%+v", got)
			}
			if len(applied.Edited)+len(applied.Inserted)+len(applied.Versions) != 0 {
				t.Errorf("invented content event: %+v", applied)
			}
			if (revision == "stale" && len(applied.Reactions) != 0) || (revision != "stale" && len(applied.Reactions) != 1) {
				t.Errorf("incorrect reaction events: %+v", applied)
			}
		})
	}
}

func TestApplySecurityPeerProtectionAndReplayAreMonotonic(t *testing.T) {
	s := confOpen(t)
	original := securityMessage()
	confApply(t, s, securityBatch(engine.SourceSend, false, original))
	incoming := securityMessage()
	incoming.TTLPeriod = 120
	incoming.Media.TTLSeconds = 45
	batch := securityBatch(engine.SourceRefresh, true, incoming)
	batch.Peers = []engine.PeerUpsert{{Peer: engine.Peer{Ref: original.Key.Peer, Protected: true}}}
	applied := confApply(t, s, batch)
	confNoEvents(t, applied)
	checkRestrictiveSecurity(t, s, original.Key, 0, time.Time{}, 45)
	var before, after int64
	if err := s.writer.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	confNoEvents(t, confApply(t, s, batch))
	if err := s.writer.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("security replay performed writes: delta=%d", after-before)
	}
}
