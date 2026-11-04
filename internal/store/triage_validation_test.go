package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestTriageValidationAndCanceledContext(t *testing.T) {
	s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	triageSeed(t, s, peer)
	at := time.Unix(1730000000, 123).UTC()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		account engine.AccountID
		peer    engine.PeerRef
		want    error
	}{
		{"empty account", context.Background(), "", peer, engine.ErrAccountMismatch},
		{"other account", context.Background(), "secondary", peer, engine.ErrAccountMismatch},
		{"missing type", context.Background(), "primary", engine.PeerRef{ID: 1}, engine.ErrInvalid},
		{"bad type", context.Background(), "primary", engine.PeerRef{Type: "USER", ID: 1}, engine.ErrInvalid},
		{"zero id", context.Background(), "primary", engine.PeerRef{Type: engine.PeerUser}, engine.ErrInvalid},
		{"negative id", context.Background(), "primary", engine.PeerRef{Type: engine.PeerUser, ID: -1}, engine.ErrInvalid},
		{"missing peer", context.Background(), "primary", engine.PeerRef{Type: engine.PeerUser, ID: 2}, engine.ErrPeerNotFound},
		{"wrong namespace", context.Background(), "primary", engine.PeerRef{Type: engine.PeerChat, ID: 1}, engine.ErrPeerNotFound},
		{"canceled", canceled, "primary", peer, context.Canceled},
		{"deadline", deadline, "primary", peer, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []struct {
				name string
				run  func() error
			}{
				{"handled", func() error { return s.MarkTriageHandled(tc.ctx, tc.account, tc.peer, "", at) }},
				{"snooze", func() error { return s.SnoozeTriage(tc.ctx, tc.account, tc.peer, "", at, at.Add(time.Hour)) }},
				{"clear", func() error { return s.ClearTriageMark(tc.ctx, tc.account, tc.peer) }},
			} {
				if err := op.run(); !errors.Is(err, tc.want) {
					t.Errorf("%s = %v, want %v", op.name, err, tc.want)
				}
			}
			if tc.account != "primary" || tc.ctx.Err() != nil {
				marks, err := s.ReadTriageMarks(tc.ctx, tc.account)
				if marks != nil || !errors.Is(err, tc.want) {
					t.Errorf("read = %v, %v, want %v", marks, err, tc.want)
				}
			}
		})
	}
	var count int
	if err := s.reader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='triage'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid calls created lazy table: %d, %v", count, err)
	}
}

func TestTriageNilContext(t *testing.T) {
	s := triageOpen(t, filepath.Join(t.TempDir(), "store.db"), "primary")
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	triageSeed(t, s, peer)
	at := time.Unix(1730000000, 0)
	if _, err := s.ReadTriageMarks(nil, "primary"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("nil read context = %v", err)
	}
	for _, err := range []error{
		s.MarkTriageHandled(nil, "primary", peer, "", at),
		s.SnoozeTriage(nil, "primary", peer, "", at, at),
		s.ClearTriageMark(nil, "primary", peer),
	} {
		if !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("nil write context = %v", err)
		}
	}
}
