package gotdengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestBackfillCreationServiceMarksAccessibleStart(t *testing.T) {
	for _, action := range []tg.MessageActionClass{&tg.MessageActionChatCreate{Title: "synthetic group"}, &tg.MessageActionChannelCreate{Title: "synthetic channel"}} {
		e, s := offlineEngine(t)
		peer := engine.PeerRef{Type: engine.PeerChat, ID: 42}
		e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
			if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
				t.Fatalf("forbidden RPC %T", in)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageService{ID: 1, PeerID: &tg.PeerChat{ChatID: 42}, Date: 1000, Action: action}}}
			return nil
		}))
		r, err := e.Backfill(context.Background(), engine.BackfillRequest{Peer: peer, Rounds: 5})
		if err != nil || !r.Complete || r.OldestID != 1 || r.Rounds != 1 {
			t.Fatalf("creation service did not end history: %+v %v", r, err)
		}
		cur, _, _ := s.Cursor(context.Background(), "primary", peer)
		if !cur.Complete {
			t.Fatal("completion was not durable")
		}
	}
}

func TestBackfillDurableFloodWaitPreventsEarlyRetry(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	now := time.Unix(2000, 0)
	e.cfg.Clock = func() time.Time { return now }
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	until := now.Add(30 * time.Second)
	cursor := engine.Cursor{Peer: peer, OldestID: 10, State: "flood_wait", FloodUntil: &until}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Cursor: &cursor}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{}
		return nil
	}))
	r, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer})
	var fw *engine.FloodWaitError
	if !errors.As(err, &fw) || r.FloodUntil == nil || !r.FloodUntil.Equal(until) || calls != 0 {
		t.Fatalf("durable flood wait bypassed: %+v err=%v calls=%d", r, err, calls)
	}
	now = until
	if _, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer}); err != nil || calls != 1 {
		t.Fatalf("flood wait never released: %v calls=%d", err, calls)
	}
}

func TestBackfillNoAccessIsDurable(t *testing.T) {
	e, s := offlineEngine(t)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		return tgerr.New(400, "CHANNEL_PRIVATE")
	}))
	if _, err := e.Backfill(context.Background(), engine.BackfillRequest{Peer: peer}); !errors.Is(err, engine.ErrNoAccess) {
		t.Fatal(err)
	}
	cur, ok, err := s.Cursor(context.Background(), "primary", peer)
	if err != nil || !ok || cur.State != "no_access" || cur.Complete {
		t.Fatalf("lost access state: %+v ok=%v err=%v", cur, ok, err)
	}
}

func TestBackfillUntilStopsWithoutClaimingExcludedHistory(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r, ok := in.(*tg.MessagesGetHistoryRequest)
		if !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		calls++
		if calls == 1 {
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 99}, Date: 2000, Message: "in range"}, &tg.MessageService{ID: 9, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000}}}
		} else {
			if r.OffsetID != 10 {
				t.Fatalf("excluded service ID skipped: %d", r.OffsetID)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageService{ID: 9, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000}}}
		}
		return nil
	}))
	until := time.Unix(1500, 0)
	r, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2, Rounds: 5, Until: &until})
	if err != nil || calls != 1 || r.Complete || r.OldestID != 10 {
		t.Fatalf("cutoff not respected: %+v calls=%d err=%v", r, calls, err)
	}
	if _, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2, Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	cur, _, _ := s.Cursor(ctx, "primary", peer)
	if cur.OldestID != 9 || cur.OldestDate == nil || !cur.OldestDate.Equal(time.Unix(1000, 0)) {
		t.Fatalf("excluded history did not resume: %+v", cur)
	}
}

func TestBackfillStalledPageFailsWithoutCursorAdvance(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	before := engine.Cursor{Peer: peer, OldestID: 10, NewestID: 20, State: "idle"}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Cursor: &before}); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageService{ID: 10, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000}}}
		return nil
	}))
	if _, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 1, Rounds: 5}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("stalled page accepted: %v", err)
	}
	cur, _, _ := s.Cursor(ctx, "primary", peer)
	if cur.OldestID != 10 || cur.Complete || cur.Fetched != 0 {
		t.Fatalf("stalled cursor committed: %+v", cur)
	}
}

func TestBackfillProductionTTLAndProtectedPayloadReachPersister(t *testing.T) {
	e, s := offlineEngine(t)
	e.cfg.Clock = func() time.Time { return time.Unix(2000, 0) }
	p := &capturePersister{Persister: s}
	e.cfg.Store = p
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{
			&tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 99}, Date: 1990, TTLPeriod: 60, Noforwards: true, Message: "still readable"},
			&tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 99}, Date: 1990, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 1}, TTLSeconds: 10}, Message: "ephemeral caption"},
			&tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Media: &tg.MessageMediaDocument{}},
		}}
		return nil
	}))
	if _, err := e.Backfill(context.Background(), engine.BackfillRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}}); err != nil {
		t.Fatal(err)
	}
	if len(p.batches) != 1 {
		t.Fatal(len(p.batches))
	}
	b := p.batches[0]
	if len(b.Messages) != 2 || len(b.Deletes) != 1 || b.Cursor == nil || b.Cursor.OldestID != 8 {
		t.Fatalf("lost page payload/cursor: %+v", b)
	}
	m := b.Messages[0]
	if !m.Protected || m.Text != "still readable" || m.ExpiresAt == nil || !m.ExpiresAt.Equal(time.Unix(2050, 0)) {
		t.Fatalf("TTL/protection lost: %+v", m)
	}
	m = b.Messages[1]
	if m.Text != "[ephemeral media]" || m.Media == nil || m.Media.Downloadable || len(m.Media.Remote) != 0 {
		t.Fatalf("ephemeral media retained: %+v", m)
	}
}

func TestBackfillServiceOnlyPageAdvancesDurableCursor(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r, ok := in.(*tg.MessagesGetHistoryRequest)
		if !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		calls++
		switch calls {
		case 1:
			if r.OffsetID != 0 {
				t.Fatal(r.OffsetID)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageService{ID: 20, PeerID: &tg.PeerUser{UserID: 99}, Date: 2000}, &tg.MessageService{ID: 19, PeerID: &tg.PeerUser{UserID: 99}, Date: 1900}}}
		case 2:
			if r.OffsetID != 19 {
				t.Fatalf("service page replayed instead of advancing: %d", r.OffsetID)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 18, PeerID: &tg.PeerUser{UserID: 99}, Date: 1800, Message: "older"}}}
		case 3:
			if r.OffsetID != 18 {
				t.Fatal(r.OffsetID)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{}
		default:
			t.Fatalf("unexpected page %d", calls)
		}
		return nil
	}))
	first, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2, Rounds: 1})
	if err != nil {
		t.Fatal(err)
	}
	cur, ok, err := s.Cursor(ctx, "primary", peer)
	if err != nil || !ok || cur.OldestID != 19 || cur.NewestID != 20 || cur.OldestDate == nil || !cur.OldestDate.Equal(time.Unix(1900, 0)) || first.OldestID != 19 || cur.Fetched != 2 {
		t.Fatalf("service cursor: %+v result=%+v ok=%v err=%v", cur, first, ok, err)
	}
	second, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2, Rounds: 2})
	if err != nil || second.Added != 1 || !second.Complete {
		t.Fatalf("resume: %+v %v", second, err)
	}
	cur, _, _ = s.Cursor(ctx, "primary", peer)
	if cur.State != "idle" || cur.Fetched != 3 || cur.NewestID != 20 {
		t.Fatalf("final cursor: %+v", cur)
	}
}

func TestBackfillCursorLoadFailureNeverIssuesRPC(t *testing.T) {
	e, s := offlineEngine(t)
	e.cfg.Store = cursorLoadFailure{Persister: s}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return errors.New("forbidden") }))
	_, err := e.Backfill(context.Background(), engine.BackfillRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}})
	if err == nil || calls != 0 {
		t.Fatalf("ignored cursor failure: %v calls=%d", err, calls)
	}
}

type cursorLoadFailure struct{ engine.Persister }

func (p cursorLoadFailure) Cursor(context.Context, engine.AccountID, engine.PeerRef) (engine.Cursor, bool, error) {
	return engine.Cursor{}, false, errors.New("synthetic cursor failure")
}

func TestBackfillFailedPageCannotCommitCursorOrEvents(t *testing.T) {
	e, s := offlineEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	before := engine.Cursor{Peer: peer, OldestID: 10, NewestID: 20, State: "idle"}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Cursor: &before}); err != nil {
		t.Fatal(err)
	}
	p := &capturePersister{Persister: s, apply: func(context.Context, engine.Batch) (engine.Applied, error) {
		return engine.Applied{}, errors.New("synthetic transaction failure")
	}}
	e.cfg.Store = p
	ch := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 2})
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "not committed"}}}
		return nil
	}))
	if _, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2}); err == nil {
		t.Fatal("failed page accepted")
	}
	cur, _, err := s.Cursor(ctx, "primary", peer)
	if err != nil || cur.OldestID != before.OldestID || cur.NewestID != before.NewestID {
		t.Fatalf("cursor escaped transaction: %+v %v", cur, err)
	}
	if len(p.batches) != 1 || len(p.batches[0].Messages) != 1 || p.batches[0].Cursor == nil || p.batches[0].Cursor.OldestID != 9 {
		t.Fatalf("page/cursor not atomic: %+v", p.batches)
	}
	select {
	case ev := <-ch:
		t.Fatalf("event before commit: %+v", ev)
	default:
	}
}

func TestBackfillEmptyMessageUsesRequestedCompositePeer(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	other := engine.PeerRef{Type: engine.PeerChat, ID: 42}
	var messages []engine.Message
	for _, p := range []engine.PeerRef{peer, other} {
		messages = append(messages, engine.Message{Key: engine.MessageKey{Account: "primary", Peer: p, ID: 7}, Date: time.Unix(1000, 0), Text: "old"})
	}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: messages}); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageEmpty{ID: 7}}}
		return nil
	}))
	if _, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 2}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []engine.PeerRef{peer, other} {
		_, ok, err := s.Message(ctx, engine.MessageKey{Account: "primary", Peer: p, ID: 7})
		if err != nil || ok != (p == other) {
			t.Fatalf("wrong empty-message scope: peer=%+v ok=%v err=%v", p, ok, err)
		}
	}
	cur, _, _ := s.Cursor(ctx, "primary", peer)
	if cur.OldestID != 7 {
		t.Fatalf("empty ID lost: %+v", cur)
	}
}
