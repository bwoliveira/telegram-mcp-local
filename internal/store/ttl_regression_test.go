package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestTTLExistingSameSecondDoesNotAcquireDialogPolicy(t *testing.T) {
	for _, kind := range []string{"edit", "replay", "stale-edit", "explicit-ttl-edit"} {
		t.Run(kind, func(t *testing.T) {
			s := confOpen(t)
			p := engine.PeerRef{Type: engine.PeerUser, ID: 770}
			m := confMessage(p, 1, "originalttlneedle")
			// Persist before activation, but within the same Unix second.
			m.Date = confTime
			previousEdit := confTime.Add(10 * time.Second)
			m.EditDate = &previousEdit
			confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Messages: []engine.Message{m}})
			confApply(t, s, engine.Batch{Account: confAccount, Dialogs: []engine.Dialog{{Peer: p, TTLPeriod: 60}}})
			s.now = func() time.Time { return confTime.Add(61 * time.Second) }

			incoming := m
			if kind != "replay" {
				incoming.Text = "editedttlneedle"
				edit := confTime.Add(61 * time.Second)
				if kind == "stale-edit" {
					edit = confTime.Add(time.Second)
				}
				incoming.EditDate = &edit
			}
			if kind == "explicit-ttl-edit" {
				incoming.TTLPeriod = 1
				expiry := confTime.Add(time.Second)
				incoming.ExpiresAt = &expiry
			}
			var before, after, changes int64
			if err := s.writer.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
				t.Fatal(err)
			}
			a := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{incoming}})
			if len(a.Deleted) != 0 {
				t.Fatalf("existing non-TTL message deleted by later policy: %+v", a)
			}
			got := confGet(t, s, m.Key)
			if got.TTLPeriod != 0 || got.ExpiresAt != nil {
				t.Fatalf("retroactive TTL: period=%d expires=%v", got.TTLPeriod, got.ExpiresAt)
			}
			if n := confCount(t, s, "SELECT count(*) FROM message_tombstones"); n != 0 {
				t.Fatalf("retroactive tombstones=%d", n)
			}
			if kind == "replay" || kind == "stale-edit" {
				confNoEvents(t, a)
				if err := s.writer.QueryRow("SELECT total_changes(),changes()").Scan(&after, &changes); err != nil {
					t.Fatal(err)
				}
				if after != before || changes != 0 || got.Text != m.Text || confCount(t, s, "SELECT version FROM messages WHERE msg_id=1") != 1 {
					t.Fatalf("obsolete copy changed state: delta=%d changes=%d message=%+v", after-before, changes, got)
				}
			} else {
				confKeys(t, a.Edited, []engine.MessageKey{m.Key})
				if got.Text != incoming.Text || confCount(t, s, "SELECT version FROM messages WHERE msg_id=1") != 2 {
					t.Fatalf("new edit not preserved: %+v", got)
				}
			}
		})
	}
}

func TestTTLExistingEditsPreserveOriginalPolicy(t *testing.T) {
	for _, original := range []string{"period", "expiry-only"} {
		for _, incoming := range []string{"omitted", "shorter", "longer", "explicit-expiry"} {
			t.Run(original+"/"+incoming, func(t *testing.T) {
				s := confOpen(t)
				p := engine.PeerRef{Type: engine.PeerChannel, ID: 771}
				m := confMessage(p, 1, "initial")
				m.Date = confTime
				expiry := confTime.Add(120 * time.Second)
				if original == "period" {
					m.TTLPeriod = 120
				} else {
					m.ExpiresAt = &expiry
				}
				confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
				confApply(t, s, engine.Batch{Account: confAccount, Dialogs: []engine.Dialog{{Peer: p, TTLPeriod: 1}}})
				s.now = func() time.Time { return confTime.Add(61 * time.Second) }

				edit := m
				edit.Text = "changed"
				editDate := confTime.Add(61 * time.Second)
				edit.EditDate = &editDate
				// Also prove an edited creation date cannot move the deadline.
				edit.Date = confTime.Add(time.Second)
				edit.TTLPeriod, edit.ExpiresAt = 0, nil
				switch incoming {
				case "shorter":
					edit.TTLPeriod = 1
				case "longer":
					edit.TTLPeriod = 600
				case "explicit-expiry":
					v := confTime.Add(time.Hour)
					edit.TTLPeriod, edit.ExpiresAt = 600, &v
				}
				a := confApply(t, s, engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{edit}})
				confKeys(t, a.Edited, []engine.MessageKey{m.Key})
				got := confGet(t, s, m.Key)
				if got.TTLPeriod != m.TTLPeriod || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expiry) {
					t.Fatalf("edit changed original policy: ttl=%d expires=%v; want ttl=%d expires=%v", got.TTLPeriod, got.ExpiresAt, m.TTLPeriod, expiry)
				}
				s.now = func() time.Time { return expiry }
				confAbsent(t, s, m.Key)
				deleted, err := s.Sweep(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				confKeys(t, deleted.Deleted, []engine.MessageKey{m.Key})
			})
		}
	}
}

func TestTTLExpiredExistingReplayAndStaleEditDoNotWrite(t *testing.T) {
	for _, kind := range []string{"replay", "stale-edit"} {
		t.Run(kind, func(t *testing.T) {
			s := confOpen(t)
			p := engine.PeerRef{Type: engine.PeerUser, ID: 772}
			m := confMessage(p, 1, "expiring")
			m.Date, m.TTLPeriod = confTime, 60
			editDate := confTime.Add(10 * time.Second)
			m.EditDate = &editDate
			confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
			s.now = func() time.Time { return confTime.Add(61 * time.Second) }
			if kind == "stale-edit" {
				older := confTime.Add(time.Second)
				m.EditDate, m.Text, m.TTLPeriod = &older, "stale", 1
			}
			var before, after, changes int64
			if err := s.writer.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
				t.Fatal(err)
			}
			a := confApply(t, s, engine.Batch{Account: confAccount, Edits: []engine.Message{m}})
			confNoEvents(t, a)
			if err := s.writer.QueryRow("SELECT total_changes(),changes()").Scan(&after, &changes); err != nil {
				t.Fatal(err)
			}
			if before != after || changes != 0 || confCount(t, s, "SELECT version FROM messages") != 1 || confCount(t, s, "SELECT count(*) FROM message_tombstones") != 0 {
				t.Fatalf("obsolete expired copy mutated store: delta=%d changes=%d", after-before, changes)
			}
			confAbsent(t, s, m.Key) // Read filtering still hides it before sweep.
			deleted, err := s.Sweep(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			confKeys(t, deleted.Deleted, []engine.MessageKey{m.Key})
		})
	}
}

func TestTTLExpiredExistingNewEditCannotExtendDeadline(t *testing.T) {
	s := confOpen(t)
	p := engine.PeerRef{Type: engine.PeerUser, ID: 773}
	m := confMessage(p, 1, "expiring")
	m.Date, m.TTLPeriod = confTime, 60
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	s.now = func() time.Time { return confTime.Add(61 * time.Second) }
	editDate, later := confTime.Add(61*time.Second), confTime.Add(time.Hour)
	m.EditDate, m.TTLPeriod, m.ExpiresAt = &editDate, 3600, &later
	a := confApply(t, s, engine.Batch{Account: confAccount, Edits: []engine.Message{m}})
	confKeys(t, a.Deleted, []engine.MessageKey{m.Key})
	if a.Skipped != 1 || len(a.Edited) != 0 || confCount(t, s, "SELECT count(*) FROM messages") != 0 || confCount(t, s, "SELECT count(*) FROM message_tombstones WHERE reason='ttl'") != 1 {
		t.Fatalf("edit extended expired message: %+v", a)
	}
	confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}}))
	confAbsent(t, s, m.Key)
}

func TestTTLUnknownEditsDoNotInferDialogPolicy(t *testing.T) {
	for _, editedDate := range []bool{false, true} {
		for _, inMessages := range []bool{false, true} {
			if inMessages && !editedDate {
				continue
			} // this would be a genuine creation
			t.Run(fmt.Sprintf("date=%v/messages=%v", editedDate, inMessages), func(t *testing.T) {
				s := confOpen(t)
				p := engine.PeerRef{Type: engine.PeerUser, ID: 775}
				confApply(t, s, engine.Batch{Account: confAccount, Dialogs: []engine.Dialog{{Peer: p, TTLPeriod: 60}}})
				s.now = func() time.Time { return confTime.Add(61 * time.Second) }
				m := confMessage(p, 1, "unknowneditttlneedle")
				m.Date = confTime
				if editedDate {
					edit := confTime.Add(61 * time.Second)
					m.EditDate = &edit
				}
				b := engine.Batch{Account: confAccount, Source: engine.SourceUpdate, Edits: []engine.Message{m}}
				if inMessages {
					b.Messages, b.Edits = b.Edits, nil
				}
				a := confApply(t, s, b)
				got := confGet(t, s, m.Key)
				if len(a.Deleted) != 0 || got.TTLPeriod != 0 || got.ExpiresAt != nil {
					t.Fatalf("unknown edit acquired newer dialog TTL: %+v %+v", a, got)
				}
			})
		}
	}
}

func TestTTLNewMessagePoliciesStillApply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		source   engine.Source
		age      time.Duration
		ttl      int
		wantTTL  int
		wantSkip bool
	}{
		{"live-same-second", engine.SourceUpdate, 0, 0, 60, false},
		{"send-same-second", engine.SourceSend, 0, 0, 60, false},
		{"live-expired", engine.SourceUpdate, 0, 0, 60, true},
		{"send-expired", engine.SourceSend, 0, 0, 60, true},
		{"live-before-policy", engine.SourceUpdate, -time.Second, 0, 0, false},
		{"history-same-second", engine.SourceBackfill, 0, 0, 0, false},
		{"difference-same-second", engine.SourceDifference, 0, 0, 0, false},
		{"search-same-second", engine.SourceSearch, 0, 0, 0, false},
		{"explicit-over-dialog", engine.SourceUpdate, 0, 120, 120, false},
		{"explicit-history", engine.SourceBackfill, 0, 120, 120, false},
		{"explicit-expired", engine.SourceDifference, -120 * time.Second, 60, 60, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := confOpen(t)
			p := engine.PeerRef{Type: engine.PeerUser, ID: 774}
			confApply(t, s, engine.Batch{Account: confAccount, Dialogs: []engine.Dialog{{Peer: p, TTLPeriod: 60}}})
			m := confMessage(p, 1, "new")
			m.Date, m.TTLPeriod = confTime.Add(tc.age), tc.ttl
			if tc.name == "live-expired" || tc.name == "send-expired" {
				s.now = func() time.Time { return confTime.Add(60 * time.Second) }
			}
			a := confApply(t, s, engine.Batch{Account: confAccount, Source: tc.source, Messages: []engine.Message{m}})
			if tc.wantSkip {
				confAbsent(t, s, m.Key)
				confKeys(t, a.Deleted, []engine.MessageKey{m.Key})
				if a.Skipped != 1 || confCount(t, s, "SELECT count(*) FROM message_tombstones WHERE reason='ttl'") != 1 {
					t.Fatalf("expired new message not discarded: %+v", a)
				}
				return
			}
			confKeys(t, a.Inserted, []engine.MessageKey{m.Key})
			got := confGet(t, s, m.Key)
			if got.TTLPeriod != tc.wantTTL || (tc.wantTTL == 0 && got.ExpiresAt != nil) || (tc.wantTTL > 0 && (got.ExpiresAt == nil || !got.ExpiresAt.Equal(m.Date.Add(time.Duration(tc.wantTTL)*time.Second)))) {
				t.Fatalf("new message policy incorrect: ttl=%d expiry=%v wantTTL=%d", got.TTLPeriod, got.ExpiresAt, tc.wantTTL)
			}
		})
	}
}
