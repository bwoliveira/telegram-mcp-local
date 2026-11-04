package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestTriageForeignKeyCascadeAndAccountScope(t *testing.T) {
	ctx := context.Background()
	s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	triageSeed(t, s, peer)
	at := time.Unix(1730000000, 42)
	if err := s.MarkTriageHandled(ctx, "primary", peer, "primary note", at); err != nil {
		t.Fatal(err)
	}
	// Simulate a foreign namespace at the SQL boundary: every triage query must
	// still explicitly scope by account, not just rely on one-file-per-account.
	for _, query := range []string{
		`INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES('secondary','user',1,'user',1)`,
		`INSERT INTO triage(account,peer_type,peer_id,handled_at,note) VALUES('secondary','user',1,42,'secondary note')`,
	} {
		if _, err := s.writer.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	marks, err := s.ReadTriageMarks(ctx, "primary")
	if err != nil || len(marks) != 1 || marks[peer].Note != "primary note" {
		t.Fatalf("account-scoped read = %v, %v", marks, err)
	}
	if err := s.SnoozeTriage(ctx, "primary", peer, " ", at, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	marks, err = s.ReadTriageMarks(ctx, "primary")
	if err != nil || marks[peer].Note != " " {
		t.Fatalf("whitespace note must replace, not retain = %v, %v", marks, err)
	}
	if err := s.ClearTriageMark(ctx, "primary", peer); err != nil {
		t.Fatal(err)
	}
	var note string
	if err := s.reader.QueryRow(`SELECT note FROM triage WHERE account='secondary'`).Scan(&note); err != nil || note != "secondary note" {
		t.Fatalf("other account touched = %q, %v", note, err)
	}
	if err := s.MarkTriageHandled(ctx, "primary", peer, "", at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `DELETE FROM peers WHERE account='primary' AND peer_type='user' AND peer_id=1`); err != nil {
		t.Fatal(err)
	}
	marks, err = s.ReadTriageMarks(ctx, "primary")
	if err != nil || len(marks) != 0 {
		t.Fatalf("peer-delete cascade = %v, %v", marks, err)
	}
}

func TestTriageReadAndWriteErrorsPropagate(t *testing.T) {
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	at := time.Unix(1730000000, 0)
	t.Run("scan", func(t *testing.T) {
		s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
		triageSeed(t, s, peer)
		if err := s.MarkTriageHandled(ctx, "primary", peer, "", at); err != nil {
			t.Fatal(err)
		}
		if _, err := s.writer.Exec(`UPDATE triage SET handled_at='not an integer'`); err != nil {
			t.Fatal(err)
		}
		if marks, err := s.ReadTriageMarks(ctx, "primary"); err == nil || marks != nil {
			t.Fatalf("corrupt timestamp silently accepted: %v, %v", marks, err)
		}
	})
	t.Run("reader query", func(t *testing.T) {
		s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
		if err := s.reader.Close(); err != nil {
			t.Fatal(err)
		}
		if marks, err := s.ReadTriageMarks(ctx, "primary"); err == nil || marks != nil {
			t.Fatalf("closed reader = %v, %v", marks, err)
		}
	})
	t.Run("writer and lazy create", func(t *testing.T) {
		s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
		triageSeed(t, s, peer)
		if err := s.writer.Close(); err != nil {
			t.Fatal(err)
		}
		if marks, err := s.ReadTriageMarks(ctx, "primary"); err == nil || marks != nil {
			t.Fatalf("closed lazy writer = %v, %v", marks, err)
		}
		for _, err := range []error{
			s.MarkTriageHandled(ctx, "primary", peer, "", at),
			s.SnoozeTriage(ctx, "primary", peer, "", at, at),
			s.ClearTriageMark(ctx, "primary", peer),
		} {
			if err == nil {
				t.Fatal("closed writer error swallowed")
			}
		}
	})
	t.Run("mutation", func(t *testing.T) {
		s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
		triageSeed(t, s, peer)
		if _, err := s.ReadTriageMarks(ctx, "primary"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.writer.Exec(`CREATE TRIGGER triage_deny_insert BEFORE INSERT ON triage BEGIN SELECT RAISE(FAIL,'denied'); END`); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkTriageHandled(ctx, "primary", peer, "", at); err == nil {
			t.Fatal("insert failure swallowed")
		}
		if err := s.SnoozeTriage(ctx, "primary", peer, "", at, at); err == nil {
			t.Fatal("snooze failure swallowed")
		}
		if _, err := s.writer.Exec(`DROP TRIGGER triage_deny_insert`); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkTriageHandled(ctx, "primary", peer, "", at); err != nil {
			t.Fatal(err)
		}
		if _, err := s.writer.Exec(`CREATE TRIGGER triage_deny_delete BEFORE DELETE ON triage BEGIN SELECT RAISE(FAIL,'denied'); END`); err != nil {
			t.Fatal(err)
		}
		if err := s.ClearTriageMark(ctx, "primary", peer); err == nil {
			t.Fatal("delete failure swallowed")
		}
	})
}

func TestTriageConcurrentLazyReadWriteAndApplyNoBusy(t *testing.T) {
	ctx := context.Background()
	s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	triageSeed(t, s, peer)
	at := time.Unix(1730000000, 123)
	start := make(chan struct{})
	errs := make(chan error, 24)
	var wg sync.WaitGroup
	for worker := 0; worker < 24; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for pass := 0; pass < 12; pass++ {
				var err error
				switch worker % 6 {
				case 0:
					err = s.MarkTriageHandled(ctx, "primary", peer, "handled", at)
				case 1:
					err = s.SnoozeTriage(ctx, "primary", peer, "snoozed", at, at.Add(time.Hour))
				case 2:
					err = s.ClearTriageMark(ctx, "primary", peer)
				case 3, 4:
					var marks map[engine.PeerRef]TriageMark
					marks, err = s.ReadTriageMarks(ctx, "primary")
					if err == nil {
						for _, mark := range marks {
							if mark.HandledAt != nil && (mark.SnoozedAt != nil || mark.SnoozeUntil != nil) {
								err = fmt.Errorf("non-atomic mark: %+v", mark)
							}
						}
					}
				case 5:
					_, err = s.Apply(ctx, engine.Batch{Account: "primary"})
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent operation: %v", err)
	}
	if err := s.SnoozeTriage(ctx, "primary", peer, "final", at, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	marks, err := s.ReadTriageMarks(ctx, "primary")
	if err != nil || len(marks) != 1 || marks[peer].Note != "final" || marks[peer].HandledAt != nil || marks[peer].SnoozedAt == nil {
		t.Fatalf("final durable state = %v, %v", marks, err)
	}
}
