package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

var securitySources = []engine.Source{engine.SourceUpdate, engine.SourceDifference, engine.SourceBackfill, engine.SourceSearch, engine.SourceSend, engine.SourceRefresh}

func securityMessage() engine.Message {
	m := confMessage(engine.PeerRef{Type: engine.PeerUser, ID: 993}, 1, "privatecaptionneedle")
	m.Date = confTime
	revision := confTime.Add(-time.Minute)
	m.EditDate, m.ServerEditDate = &revision, &revision
	m.ContentHash = "original-content"
	m.Media = &engine.Media{Type: engine.MediaPhoto, Filename: "private.jpg", Downloadable: true, Remote: []byte("private-credential")}
	m.Entities = []engine.Entity{{Type: engine.EntityBold, Offset: 0, Length: 7}}
	m.Location = &engine.Location{Latitude: 1, Longitude: 2}
	m.Poll = &engine.Poll{ID: 993, Question: "privatepollneedle"}
	return m
}

func securityBatch(source engine.Source, edit bool, m engine.Message) engine.Batch {
	b := engine.Batch{Account: confAccount, Source: source, Messages: []engine.Message{m}}
	if edit {
		b.Messages, b.Edits = nil, b.Messages
	}
	return b
}

func securityRevision(m *engine.Message, kind string) {
	revision := *m.ServerEditDate
	switch kind {
	case "stale":
		revision = revision.Add(-time.Second)
	case "newer-reaction-only", "newer-content":
		revision = revision.Add(time.Second)
	}
	m.EditDate, m.ServerEditDate = &revision, &revision
	if kind == "newer-content" {
		m.ContentHash, m.Text = "changed-content", "changedcaptionneedle"
	}
}

func checkRestrictiveSecurity(t *testing.T, s *Store, key engine.MessageKey, period int, expiry time.Time, mediaTTL int) {
	t.Helper()
	got := confGet(t, s, key)
	if !got.Protected {
		t.Error("protection was lost or ignored")
	}
	if got.TTLPeriod != period || (expiry.IsZero() && got.ExpiresAt != nil) || (!expiry.IsZero() && (got.ExpiresAt == nil || !got.ExpiresAt.Equal(expiry))) {
		t.Errorf("message TTL lost or ignored: ttl=%d expiry=%v want ttl=%d expiry=%v", got.TTLPeriod, got.ExpiresAt, period, expiry)
	}
	if got.Media == nil || got.Media.TTLSeconds != mediaTTL || got.Media.Downloadable || got.Media.Remote != nil || got.Media.Filename != "" {
		t.Errorf("temporary media was lost or not redacted: %+v", got.Media)
	}
	if got.Text != "[temporary media]" || got.Entities != nil || got.Location != nil || got.Poll != nil {
		t.Errorf("temporary payload was restored: %+v", got)
	}
	if confCount(t, s, `SELECT count(*) FROM messages WHERE media_remote IS NOT NULL`) != 0 {
		t.Error("temporary media retained remote credentials")
	}
}

func TestApplySecurityTightensEveryRevisionAndSource(t *testing.T) {
	for _, source := range securitySources {
		for _, edit := range []bool{false, true} {
			for _, revision := range []string{"stale", "equal", "newer-reaction-only", "newer-content"} {
				t.Run(fmt.Sprintf("%s/edit=%v/%s", source, edit, revision), func(t *testing.T) {
					s := confOpen(t)
					original := securityMessage()
					confApply(t, s, securityBatch(source, false, original))
					incoming := securityMessage()
					securityRevision(&incoming, revision)
					incoming.Protected, incoming.TTLPeriod, incoming.Media.TTLSeconds = true, 120, 45
					expiry := confTime.Add(90 * time.Second) // Earlier than date + TTLPeriod.
					incoming.ExpiresAt = &expiry
					applied := confApply(t, s, securityBatch(source, edit, incoming))
					if edit {
						checkRestrictiveSecurity(t, s, original.Key, 0, time.Time{}, 45)
					} else {
						checkRestrictiveSecurity(t, s, original.Key, 120, expiry, 45)
					}
					got := confGet(t, s, original.Key)
					version := confCount(t, s, "SELECT version FROM messages")
					if revision == "newer-content" {
						confKeys(t, applied.Edited, []engine.MessageKey{original.Key})
						if version != 2 {
							t.Errorf("content version=%d, want 2", version)
						}
					} else {
						confNoEvents(t, applied)
						if version != 1 || got.EditDate == nil || !got.EditDate.Equal(*original.EditDate) {
							t.Errorf("metadata-only update invented an edit: version=%d edited=%v", version, got.EditDate)
						}
					}
					confNoEvents(t, confApply(t, s, securityBatch(source, edit, incoming)))
				})
			}
		}
	}
}

func TestApplySecurityNeverWeakensOnPartialCopies(t *testing.T) {
	for _, source := range securitySources {
		for _, edit := range []bool{false, true} {
			for _, revision := range []string{"stale", "equal", "newer-reaction-only", "newer-content"} {
				t.Run(fmt.Sprintf("%s/edit=%v/%s", source, edit, revision), func(t *testing.T) {
					s := confOpen(t)
					original := securityMessage()
					original.Protected, original.TTLPeriod, original.Media.TTLSeconds = true, 120, 45
					expiry := confTime.Add(90 * time.Second)
					original.ExpiresAt = &expiry
					confApply(t, s, securityBatch(source, false, original))
					incoming := securityMessage()
					securityRevision(&incoming, revision)
					incoming.Date = confTime.Add(time.Hour) // Never rebase the original deadline.
					incoming.TTLPeriod = 600
					later := confTime.Add(2 * time.Hour)
					incoming.ExpiresAt = &later
					confApply(t, s, securityBatch(source, edit, incoming))
					checkRestrictiveSecurity(t, s, original.Key, 120, expiry, 45)
					incoming.Media.TTLSeconds = 15 // A shorter observation cannot replace the acquired timer.
					confApply(t, s, securityBatch(source, edit, incoming))
					checkRestrictiveSecurity(t, s, original.Key, 120, expiry, 45)
					incoming.Media = nil // Omission is not proof that temporary media disappeared.
					confApply(t, s, securityBatch(source, edit, incoming))
					checkRestrictiveSecurity(t, s, original.Key, 120, expiry, 45)
				})
			}
		}
	}
}

func TestApplyMessageTTLPreservesOriginalPolicyAndDate(t *testing.T) {
	for _, revision := range []string{"stale", "equal", "newer-reaction-only", "newer-content"} {
		for _, policy := range []string{"period", "expiry-only", "both"} {
			t.Run(revision+"/"+policy, func(t *testing.T) {
				s := confOpen(t)
				original := securityMessage()
				original.TTLPeriod = 300
				confApply(t, s, securityBatch(engine.SourceUpdate, false, original))
				incoming := securityMessage()
				incoming.Date = confTime.Add(time.Hour)
				securityRevision(&incoming, revision)
				wantExpiry := confTime.Add(300 * time.Second)
				wantTTL := 300
				if policy != "expiry-only" {
					incoming.TTLPeriod = 120
				} else {
					wantTTL = 300
				}
				if policy != "period" {
					earlier := confTime.Add(120 * time.Second)
					incoming.ExpiresAt = &earlier
				}
				applied := confApply(t, s, securityBatch(engine.SourceRefresh, true, incoming))
				got := confGet(t, s, original.Key)
				if got.TTLPeriod != wantTTL || got.ExpiresAt == nil || !got.ExpiresAt.Equal(wantExpiry) {
					t.Errorf("original message policy changed/rebased: ttl=%d expiry=%v want ttl=%d expiry=%v", got.TTLPeriod, got.ExpiresAt, wantTTL, wantExpiry)
				}
				if revision != "newer-content" {
					confNoEvents(t, applied)
				}
			})
		}
	}
}

func TestApplySecurityExpiryCannotResurrectAcrossSources(t *testing.T) {
	for _, source := range securitySources {
		for _, revision := range []string{"stale", "equal", "newer-reaction-only", "newer-content"} {
			t.Run(string(source)+"/"+revision, func(t *testing.T) {
				s := confOpen(t)
				original := securityMessage()
				confApply(t, s, securityBatch(source, false, original))
				incoming := securityMessage()
				securityRevision(&incoming, revision)
				expiry := confTime.Add(-time.Second)
				incoming.ExpiresAt = &expiry
				// Only a full snapshot can reveal previously unknown message TTL.
				applied := confApply(t, s, securityBatch(source, false, incoming))
				confAbsent(t, s, original.Key)
				confKeys(t, applied.Deleted, []engine.MessageKey{original.Key})
				if confCount(t, s, "SELECT count(*) FROM messages") != 0 || confCount(t, s, "SELECT count(*) FROM message_tombstones WHERE reason='ttl'") != 1 {
					t.Error("expired evidence not deleted/tombstoned")
				}
				replay := securityMessage()
				later := confTime.Add(time.Hour)
				replay.EditDate, replay.ServerEditDate = &later, &later
				confNoEvents(t, confApply(t, s, securityBatch(source, false, replay)))
				confAbsent(t, s, original.Key)
			})
		}
	}
}

func TestApplyTemporarySecurityPurgesExistingDerivedContent(t *testing.T) {
	s := confOpen(t)
	original := securityMessage()
	confApply(t, s, securityBatch(engine.SourceSend, false, original))
	if err := s.SaveTranscript(t.Context(), original.Key, Transcript{Text: "privatetranscriptneedle", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	path := "media/private-cache.jpg"
	confApply(t, s, engine.Batch{Account: confAccount, Downloads: []engine.DownloadRecord{{Key: original.Key, Path: path, At: confTime}}})
	incoming := securityMessage()
	incoming.Media.TTLSeconds = 45
	applied := confApply(t, s, securityBatch(engine.SourceRefresh, true, incoming))
	confNoEvents(t, applied)
	if confCount(t, s, "SELECT count(*) FROM media_files") != 0 || confCount(t, s, "SELECT count(*) FROM transcripts") != 0 {
		t.Error("temporary media retained cached file/transcript")
	}
	if confCount(t, s, "SELECT count(*) FROM files_to_unlink") != 1 {
		t.Error("cached temporary media not queued for unlink")
	}
	if confCount(t, s, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'privatecaptionneedle OR privatetranscriptneedle'`) != 0 {
		t.Error("temporary payload remains searchable")
	}
}
