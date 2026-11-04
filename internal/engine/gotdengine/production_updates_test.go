package gotdengine

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// capturePersister observes the production contract before pocstore's deliberately
// more restrictive PoC policy. It is not a replacement production database.
type capturePersister struct {
	engine.Persister
	batches []engine.Batch
	apply   func(context.Context, engine.Batch) (engine.Applied, error)
	message *engine.Message
}

func (p *capturePersister) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	p.batches = append(p.batches, b)
	if p.apply != nil {
		return p.apply(ctx, b)
	}
	return p.Persister.Apply(ctx, b)
}

func (p *capturePersister) Message(ctx context.Context, key engine.MessageKey) (engine.Message, bool, error) {
	if p.message != nil {
		return *p.message, p.message.Key == key, nil
	}
	return p.Persister.Message(ctx, key)
}

func TestProductionUpdatesPreserveProtectedAndAutoDeleteText(t *testing.T) {
	e, s := offlineEngine(t)
	now := time.Unix(2000, 0)
	e.cfg.Clock = func() time.Time { return now }
	p := &capturePersister{Persister: s}
	e.cfg.Store = p
	for _, edit := range []bool{false, true} {
		m := &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1990, TTLPeriod: 60, Noforwards: true, Message: "protected auto-delete text"}
		var u tg.UpdateClass = &tg.UpdateNewMessage{Message: m}
		if edit {
			u = &tg.UpdateEditMessage{Message: m}
		}
		if err := e.handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{u}}); err != nil {
			t.Fatal(err)
		}
		b := p.batches[len(p.batches)-1]
		messages := b.Messages
		if edit {
			messages = b.Edits
		}
		if len(messages) != 1 {
			t.Fatalf("production conversion omitted content: %+v", b)
		}
		got := messages[0]
		if got.Text != m.Message || !got.Protected || got.TTLPeriod != 60 || got.ExpiresAt == nil || !got.ExpiresAt.Equal(time.Unix(2050, 0)) {
			t.Fatalf("lost production policy: %+v", got)
		}
	}
}

func TestProductionEventsRecheckExpirationAfterCommit(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(map[bool]string{false: "insert", true: "edit"}[edit], func(t *testing.T) {
			e, s := offlineEngine(t)
			now := time.Unix(2000, 0)
			e.cfg.Clock = func() time.Time { return now }
			p := &capturePersister{Persister: s}
			p.apply = func(_ context.Context, b engine.Batch) (engine.Applied, error) {
				// The deadline can pass during the commit, even if the store
				// legitimately accepted the message at transaction start.
				now = time.Unix(2001, 0)
				a := engine.Applied{}
				for _, m := range b.Messages {
					a.Inserted = append(a.Inserted, m.Key)
					copy := m
					p.message = &copy
				}
				for _, m := range b.Edits {
					a.Edited = append(a.Edited, m.Key)
					copy := m
					p.message = &copy
				}
				return a, nil
			}
			e.cfg.Store = p
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 2})
			m := &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1991, TTLPeriod: 10, Message: "expired during commit"}
			var u tg.UpdateClass = &tg.UpdateNewMessage{Message: m}
			if edit {
				u = &tg.UpdateEditMessage{Message: m}
			}
			if err := e.handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{u}}); err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-ch:
				t.Fatalf("expired content emitted: %+v", ev)
			default:
			}
		})
	}
}

func TestProductionExpiredMediaBecomesDeletion(t *testing.T) {
	e, s := offlineEngine(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 7}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Text: "previous caption", Date: time.Unix(1000, 0)}}}); err != nil {
		t.Fatal(err)
	}
	p := &capturePersister{Persister: s}
	e.cfg.Store = p
	u := &tg.UpdateEditMessage{Message: &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Media: &tg.MessageMediaPhoto{TTLSeconds: 1}}}
	if err := e.handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{u}}); err != nil {
		t.Fatal(err)
	}
	b := p.batches[0]
	if len(b.Edits) != 0 || len(b.Deletes) != 1 || len(b.Deletes[0].IDs) != 1 || b.Deletes[0].IDs[0] != 7 || b.Deletes[0].Peer == nil || *b.Deletes[0].Peer != key.Peer {
		t.Fatalf("expired media did not become a scoped deletion: %+v", b)
	}
	if _, ok, err := s.Message(ctx, key); err != nil || ok {
		t.Fatalf("expired media survived: ok=%v err=%v", ok, err)
	}
}
