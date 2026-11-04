package panel

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// Model the production controller's Status seam: every call reads durable
// history totals. A plain fake would hide the per-conversation SQL regression.
type statusFixtureEngine struct {
	engine.Controller
	store *store.Store
	calls atomic.Int64
}

func (e *statusFixtureEngine) Status() engine.Status {
	e.calls.Add(1)
	status := e.Controller.Status()
	history, updates, err := e.store.ReadStatus(context.Background(), "primary")
	if err != nil {
		status.Updates.Held = true
		return status
	}
	status.History, status.Updates = history, updates
	return status
}

func (e *statusFixtureEngine) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	diagnostics, err := e.Controller.Diagnostics(ctx)
	if err != nil {
		return diagnostics, err
	}
	// Diagnostics uses only the in-memory lifecycle, not durable enrichment.
	diagnostics.Connected = e.LightStatus().State == engine.StateConnected
	return diagnostics, nil
}

// Catch accidental fallback to all-message projection in page health checks.
type statusFixtureStore struct {
	*store.Store
	messageReads       atomic.Int64
	activityReads      atomic.Int64
	coverageReads      atomic.Int64
	batchCoverageReads atomic.Int64
	statusReads        atomic.Int64
}

func (s *statusFixtureStore) ReadStatus(ctx context.Context, account engine.AccountID) (engine.HistoryProgress, engine.UpdatesStatus, error) {
	s.statusReads.Add(1)
	return s.Store.ReadStatus(ctx, account)
}

func (s *statusFixtureStore) ReadCoverage(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) (bool, *time.Time, error) {
	if peer != nil {
		s.coverageReads.Add(1)
	}
	return s.Store.ReadCoverage(ctx, account, peer)
}
func (s *statusFixtureStore) ReadConversationCoverage(ctx context.Context, account engine.AccountID) ([]store.ConversationCoverage, error) {
	s.batchCoverageReads.Add(1)
	return s.Store.ReadConversationCoverage(ctx, account)
}

func (s *statusFixtureStore) ReadMessages(ctx context.Context, account engine.AccountID) ([]engine.Message, error) {
	s.messageReads.Add(1)
	return s.Store.ReadMessages(ctx, account)
}
func (s *statusFixtureStore) ReadActivity(ctx context.Context, account engine.AccountID, now time.Time) (index.Activity, error) {
	s.activityReads.Add(1)
	return s.Store.ReadActivity(ctx, account, now)
}

// newStatusFixture is entirely offline. Only synthetic content enters the
// temporary SQLite database; no session storage or Telegram transport exists.
func newStatusFixture(tb testing.TB) (*Panel, http.Handler, *statusFixtureEngine) {
	tb.Helper()
	dir := tb.TempDir()
	path := filepath.Join(dir, "store.db")
	st, err := store.Open(context.Background(), path, "primary")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		tb.Fatal(err)
	}
	now := time.Now().Unix()
	statements := []string{
		`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<110) INSERT INTO peers(account,peer_type,peer_id,kind,title,access,updated_at) SELECT 'primary','user',i,'user',printf('Synthetic %03d',i),'ok',1 FROM n`,
		`INSERT INTO dialogs(account,peer_type,peer_id,unread_count,unread_mark,refreshed_at) SELECT account,peer_type,peer_id,0,0,1 FROM peers`,
		`INSERT INTO backfill_cursors(account,peer_type,peer_id,complete,state,oldest_date,updated_at) SELECT account,peer_type,peer_id,1,'idle',1,1 FROM peers`,
		fmt.Sprintf(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<25000) INSERT INTO messages(account,peer_type,peer_id,msg_id,date,out,text,source,first_seen_at,updated_at) SELECT 'primary','user',1+(i-1)%%110,i,%d-i,i%%2,'Synthetic performance fixture','updates',%d,%d FROM n`, now, now, now),
	}
	for _, statement := range statements {
		if _, err = db.Exec(statement); err != nil {
			db.Close()
			tb.Fatal(err)
		}
	}
	db.Close()
	history, _, err := st.ReadStatus(context.Background(), "primary")
	if err != nil || history.Messages != 25000 || history.Conversations != 110 {
		tb.Fatalf("fixture totals: %+v %v", history, err)
	}
	ctrl, err := fake.New(engine.Config{})
	if err != nil {
		tb.Fatal(err)
	}
	eng := &statusFixtureEngine{Controller: ctrl, store: st}
	settings, err := state.Open(filepath.Join(dir, "settings"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = settings.Close() })
	readStore := &statusFixtureStore{Store: st}
	p := &Panel{Engine: eng, ReadStore: readStore, AccountID: "primary", State: settings, Endpoint: func() string { return "http://127.0.0.1:47831/mcp" }}
	p.Server = mcp.New(mcp.Config{Engine: eng, ReadStore: readStore, State: settings})
	mux := http.NewServeMux()
	p.Register(mux)
	return p, Internal(mux), eng
}

func fixtureGET(tb testing.TB, handler http.Handler, path string) *httptest.ResponseRecorder {
	tb.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47831"+path, nil))
	if w.Code != http.StatusOK {
		tb.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	return w
}

func TestPanelStatusPerformance(t *testing.T) {
	p, handler, eng := newStatusFixture(t)
	readStore := p.ReadStore.(*statusFixtureStore)
	for _, path := range []string{"/status", "/conta", "/api/panel/status", "/api/status"} {
		before := eng.calls.Load()
		start := time.Now()
		response := fixtureGET(t, handler, path)
		elapsed := time.Since(start)
		calls := eng.calls.Load() - before
		t.Logf("GET %s: %s, engine.Status calls=%d, bytes=%d", path, elapsed, calls, response.Body.Len())
		if path == "/api/panel/status" && strings.Count(response.Body.String(), "Synthetic ") != 110 {
			t.Fatal("coverage did not render all 110 chats")
		}
		if calls > 4 {
			t.Errorf("GET %s: %d engine.Status calls; must not scale with chats", path, calls)
		}
		// Enforce wall time explicitly on an unloaded non-race run. Structural
		// regressions remain unconditional; race/parallel CI adds scheduling cost.
		if os.Getenv("PANEL_PERF_ENFORCE") == "1" && elapsed >= 300*time.Millisecond {
			t.Errorf("GET %s: %s exceeds 300ms fixture budget", path, elapsed)
		}
	}
	if readStore.messageReads.Load() != 0 || readStore.activityReads.Load() != 1 {
		t.Fatalf("page health materialized messages: messageReads=%d aggregateReads=%d", readStore.messageReads.Load(), readStore.activityReads.Load())
	}
	if readStore.coverageReads.Load() != 0 || readStore.batchCoverageReads.Load() != 1 {
		t.Fatalf("coverage path: per-peer=%d batched=%d; want 0 and 1", readStore.coverageReads.Load(), readStore.batchCoverageReads.Load())
	}
	// Reuse the captured held state, rather than consulting Engine per chat.
	// A nil Engine makes any accidental status lookup fail immediately.
	p.Engine = nil
	coverage, err := p.localCoverage(context.Background(), engine.Status{Updates: engine.UpdatesStatus{Held: true}})
	if err != nil || len(coverage) != 110 {
		t.Fatalf("held coverage: %d %v", len(coverage), err)
	}
	for _, conversation := range coverage {
		if conversation.Complete {
			t.Fatal("held checkpoint reported complete coverage")
		}
	}
}

func BenchmarkPanelStatus25000(b *testing.B) {
	_, handler, _ := newStatusFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fixtureGET(b, handler, "/status")
	}
}
