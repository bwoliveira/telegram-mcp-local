package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// s16Contexts separates the scheduling window from the bounded SQL lifetime.
// Canceling the load window must not interrupt an admitted Apply or half-read
// snapshot. modernc's BEGIN/ROLLBACK paths can surface raw SQLITE_INTERRUPT,
// not just context.DeadlineExceeded. Drain instead of ignoring either error.
func s16Contexts(parent context.Context, window, drain time.Duration) (context.Context, context.Context, context.CancelFunc, context.CancelFunc) {
	sqlContext, cancelSQL := context.WithTimeout(parent, window+drain)
	scheduling, stopScheduling := context.WithTimeout(sqlContext, window)
	return scheduling, sqlContext, stopScheduling, func() {
		stopScheduling()
		cancelSQL()
	}
}

func TestS16WindowExpiryDrainsInflightSnapshotAndWrite(t *testing.T) {
	s := confOpen(t)
	m := confMessage(engine.PeerRef{Type: engine.PeerUser, ID: 1601}, 1, "s16drainprobe")
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	// The writer is admitted during the load window, but deliberately held until
	// after it closes. The reader holds its first half of a consistent snapshot.
	s.writeMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.writeMu.Unlock()
		}
	}()
	scheduling, sqlContext, stopScheduling, cancel := s16Contexts(context.Background(), time.Minute, 5*time.Second)
	defer cancel()
	tx, err := s.reader.BeginTx(sqlContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var messages int
	if err := tx.QueryRowContext(sqlContext, `SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		edit := confTime.Add(time.Second)
		m.EditDate = &edit
		_, err := s.Apply(sqlContext, engine.Batch{Account: confAccount, Edits: []engine.Message{m}})
		result <- err
	}()
	<-started
	// Close the load window at an exact barrier rather than racing a sleep.
	stopScheduling()
	<-scheduling.Done()
	s.writeMu.Unlock()
	locked = false
	if err := <-result; err != nil {
		t.Errorf("inflight writer was interrupted by scheduling cutoff: %v", err)
	}
	var hits int
	if err := tx.QueryRowContext(sqlContext, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 's16drainprobe'`).Scan(&hits); err != nil {
		t.Fatalf("inflight snapshot was interrupted by scheduling cutoff: %v", err)
	}
	if messages != 1 || hits != messages {
		t.Fatalf("snapshot rows=%d FTS=%d", messages, hits)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestS16SQLContextRemainsBoundedAndParentCancelable(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	scheduling, sqlContext, _, cancel := s16Contexts(parent, time.Hour, time.Second)
	defer cancel()
	cancelParent()
	for _, ctx := range []context.Context{scheduling, sqlContext} {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("parent cancellation was not propagated")
		}
	}
	_, sqlContext, _, cancel = s16Contexts(context.Background(), time.Nanosecond, 10*time.Millisecond)
	defer cancel()
	select {
	case <-sqlContext.Done():
	case <-time.After(time.Second):
		t.Fatal("SQL drain is unbounded")
	}
}
