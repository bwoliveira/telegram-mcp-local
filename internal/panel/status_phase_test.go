package panel

// Opt in with PANEL_PHASE_PERF=1 PANEL_PHASE_OUTPUT=/scratch/evidence.json
// and, optionally, PANEL_PHASE_REAL_DB=/absolute/path/to/isolated-backup.db
// (see requireIsolatedBackup; without it the isolated_real case is skipped).
// No Telegram transport is constructed. Real input is opened read-only and
// VACUUMed into a private temporary database before store.Open can migrate it.
// This version needs real_snapshot_test.go and LightStatus, so it does not
// build on eeda58f. To reproduce the eeda58f baseline, copy the c05b107
// version alone (it builds and runs there; its real-store input has no
// isolation guard, so pass only an isolated backup):
//   git show c05b107:internal/panel/status_phase_test.go > internal/panel/status_phase_test.go
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// These types are an output allowlist, not a marshal of application objects.
// Never include account/peer/message identifiers, paths to real data, titles,
// content, response bodies, client configuration, or backend error strings.
type phaseEvidence struct {
	Schema    int            `json:"schema"`
	Samples   int            `json:"samples"`
	Discovery string         `json:"discovery"`
	Datasets  []phaseDataset `json:"datasets"`
}
type phaseDataset struct {
	Dataset       string             `json:"dataset"`
	Messages      int64              `json:"messages"`
	Conversations int                `json:"conversations"`
	Dialogs       int                `json:"dialogs"`
	CoverageRows  int                `json:"coverage_rows"`
	ActivityMode  string             `json:"activity_mode"`
	Measurements  []phaseMeasurement `json:"measurements"`
}
type phaseMeasurement struct {
	Kind                       string         `json:"kind"`
	Name                       string         `json:"name"`
	N                          int            `json:"n"`
	MedianMS                   float64        `json:"median_ms"`
	P95MS                      float64        `json:"p95_ms"`
	FirstMS                    float64        `json:"first_ms"`
	Over500MS                  int            `json:"over_500ms"`
	Errors                     int            `json:"errors"`
	HTTPStatuses               map[string]int `json:"http_statuses,omitempty"`
	EngineStatusReads          int64          `json:"engine_status_reads"`
	FullMessageReads           int64          `json:"full_message_reads"`
	CoverageRenderedComplete   int            `json:"coverage_rendered_complete,omitempty"`
	CoverageRenderedIncomplete int            `json:"coverage_rendered_incomplete,omitempty"`
}

type phaseStore struct {
	*store.Store
	messageReads atomic.Int64
}

func (s *phaseStore) ReadMessages(ctx context.Context, account engine.AccountID) ([]engine.Message, error) {
	s.messageReads.Add(1)
	return s.Store.ReadMessages(ctx, account)
}

type phaseEngine struct {
	engine.Controller
	store    *phaseStore
	account  engine.AccountID
	calls    atomic.Int64
	failures atomic.Int64
	path     string // private fixture database, for tests seeding extra evidence
}

func (e *phaseEngine) Status() engine.Status {
	e.calls.Add(1)
	status := e.Controller.Status()
	history, updates, err := e.store.ReadStatus(context.Background(), e.account)
	if err != nil {
		e.failures.Add(1)
		status.Updates.Held = true
		return status
	}
	status.History, status.Updates = history, updates
	return status
}
func (e *phaseEngine) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	d, err := e.Controller.Diagnostics(ctx)
	if err == nil {
		d.Connected = e.LightStatus().State == engine.StateConnected
	}
	return d, err
}

func phaseFixture(t *testing.T, source string) (*Panel, http.Handler, *phaseEngine) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	account := engine.AccountID("primary")
	if source != "" {
		// Accept any isolated backup the caller names, never a live
		// application database or anything inside a data directory.
		requireIsolatedBackup(t, source)
		db, err := sql.Open("sqlite", "file:"+source+"?mode=ro")
		if err != nil {
			t.Fatal("open isolated snapshot failed")
		}
		_, err = db.ExecContext(context.Background(), "VACUUM INTO ?", path)
		_ = db.Close()
		if err != nil {
			t.Fatal("copy isolated snapshot failed")
		}
		// Choose the populated account only in memory. Nothing identifying
		// that account is persisted in evidence or printed on failure.
		db, err = sql.Open("sqlite", path)
		if err != nil {
			t.Fatal("open private copy failed")
		}
		err = db.QueryRow("SELECT account FROM messages GROUP BY account ORDER BY COUNT(*) DESC LIMIT 1").Scan(&account)
		_ = db.Close()
		if err != nil {
			t.Fatal("private snapshot has no populated account")
		}
	}
	st, err := store.Open(context.Background(), path, account)
	if err != nil {
		t.Fatal("open fixture store failed")
	}
	t.Cleanup(func() { _ = st.Close() })
	if source == "" {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal("open synthetic fixture failed")
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
				_ = db.Close()
				t.Fatal("seed synthetic fixture failed")
			}
		}
		_ = db.Close()
		history, _, err := st.ReadStatus(context.Background(), account)
		if err != nil || history.Messages != 25000 || history.Conversations != 110 {
			t.Fatal("synthetic fixture totals differ")
		}
	}
	ctrl, err := fake.New(engine.Config{Account: account}) // no Store: do not seed fake messages into the DB
	if err != nil {
		t.Fatal("create offline controller failed")
	}
	rs := &phaseStore{Store: st}
	eng := &phaseEngine{Controller: ctrl, store: rs, account: account, path: path}
	settings, err := state.Open(filepath.Join(dir, "settings"))
	if err != nil {
		t.Fatal("open isolated settings failed")
	}
	t.Cleanup(func() { _ = settings.Close() })
	// Exercise completed setup pages rather than measuring a wizard redirect.
	if err := settings.SetSetting(context.Background(), setupSetting, "done"); err != nil {
		t.Fatal("set fixture setup state failed")
	}
	p := &Panel{Engine: eng, ReadStore: rs, AccountID: account, State: settings, Endpoint: func() string { return "http://127.0.0.1:47831/mcp" }}
	p.Server = mcp.New(mcp.Config{Engine: eng, ReadStore: rs, State: settings})
	mux := http.NewServeMux()
	p.Register(mux)
	return p, Internal(mux), eng
}

// Support both localCoverage signatures without rewriting either revision.
func phaseCoverage(p *Panel, ctx context.Context, status engine.Status) ([]conversationCoverage, error) {
	if reader, ok := any(p).(interface {
		localCoverage(context.Context, engine.Status) ([]conversationCoverage, error)
	}); ok {
		return reader.localCoverage(ctx, status)
	}
	if reader, ok := any(p).(interface {
		localCoverage(context.Context) ([]conversationCoverage, error)
	}); ok {
		return reader.localCoverage(ctx)
	}
	return nil, fmt.Errorf("unsupported coverage seam")
}

// Direct activity projection: aggregate when supported, otherwise the exact
// baseline readMessages calculation. Health's separate coverage query remains
// in the Health phase, so this phase isolates activity alone in both revisions.
func phaseReadActivity(ctx context.Context, st *phaseStore, account engine.AccountID, now time.Time) error {
	if reader, ok := any(st).(interface {
		ReadActivity(context.Context, engine.AccountID, time.Time) (index.Activity, error)
	}); ok {
		_, err := reader.ReadActivity(ctx, account, now)
		return err
	}
	rows, err := st.ReadMessages(ctx, account)
	if err != nil {
		return err
	}
	var activity index.Activity
	for _, m := range rows {
		if m.ExpiresAt != nil && !m.ExpiresAt.After(now) {
			continue
		}
		if m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(now) {
			continue
		}
		date := m.Date
		if activity.Oldest == nil || date.Before(*activity.Oldest) {
			activity.Oldest = &date
		}
		if activity.NewestAny == nil || date.After(*activity.NewestAny) {
			activity.NewestAny = &date
		}
		if !m.Out && (activity.NewestIncoming == nil || date.After(*activity.NewestIncoming)) {
			activity.NewestIncoming = &date
		}
		if !date.Before(now.Add(-time.Hour)) && !date.After(now) {
			activity.LastHour++
		}
		if !date.Before(now.Add(-24*time.Hour)) && !date.After(now) {
			activity.LastDay++
		}
	}
	// Keep the projected result live without serializing message-derived data.
	phaseActivitySink = activity
	return nil
}

var phaseActivitySink index.Activity

func phaseMeasure(eng *phaseEngine, kind, name string, run func() (int, error)) phaseMeasurement {
	const n = 5
	m := phaseMeasurement{Kind: kind, Name: name, N: n}
	if kind == "route" {
		m.HTTPStatuses = map[string]int{}
	}
	beforeCalls, beforeRows := eng.calls.Load(), eng.store.messageReads.Load()
	beforeFailures := eng.failures.Load()
	var samples []float64
	for i := 0; i < n; i++ {
		start := time.Now()
		status, err := run()
		elapsed := time.Since(start)
		ms := float64(elapsed) / float64(time.Millisecond)
		samples = append(samples, ms)
		if i == 0 {
			m.FirstMS = ms
		}
		if elapsed > 500*time.Millisecond {
			m.Over500MS++
		}
		if err != nil {
			m.Errors++
		}
		if kind == "route" {
			m.HTTPStatuses[strconv.Itoa(status)]++
		}
	}
	sort.Float64s(samples)
	m.MedianMS, m.P95MS = samples[n/2], samples[int(math.Ceil(.95*float64(n)))-1]
	m.EngineStatusReads = eng.calls.Load() - beforeCalls
	m.FullMessageReads = eng.store.messageReads.Load() - beforeRows
	m.Errors += int(eng.failures.Load() - beforeFailures)
	return m
}

func TestPanelStatusPhases(t *testing.T) {
	if os.Getenv("PANEL_PHASE_PERF") != "1" {
		t.Skip("opt-in offline performance evidence")
	}
	output := os.Getenv("PANEL_PHASE_OUTPUT")
	if output == "" {
		t.Fatal("PANEL_PHASE_OUTPUT is required")
	}
	evidence := phaseEvidence{Schema: 1, Samples: 5, Discovery: "unchanged PATH and HOME; real client discovery; cold cache explicitly reset; no hidden warmups"}
	inputs := []struct{ name, source string }{{"synthetic_25000", ""}, {"isolated_real", os.Getenv("PANEL_PHASE_REAL_DB")}}
	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			skipWithoutBackup(t, input.name == "isolated_real", input.source, "PANEL_PHASE_REAL_DB")
			p, handler, eng := phaseFixture(t, input.source)
			ctx := context.Background()
			history, _, err := eng.store.ReadStatus(ctx, eng.account)
			if err != nil {
				t.Fatal("read fixture totals failed")
			}
			dialogs, err := eng.store.ReadDialogs(ctx, eng.account)
			if err != nil {
				t.Fatal("read fixture dialogs failed")
			}
			d := phaseDataset{Dataset: input.name, Messages: history.Messages, Conversations: history.Conversations, Dialogs: len(dialogs), ActivityMode: "baseline_readMessages_projection"}
			if _, ok := any(eng.store).(interface {
				ReadActivity(context.Context, engine.AccountID, time.Time) (index.Activity, error)
			}); ok {
				d.ActivityMode = "store_ReadActivity"
			}
			status := eng.Status()
			phases := []struct {
				name string
				run  func() (int, error)
			}{
				{"snapshot", func() (int, error) { _ = p.snapshot(ctx); return 0, nil }},
				{"coverage", func() (int, error) {
					rows, err := phaseCoverage(p, ctx, status)
					d.CoverageRows = len(rows)
					if err == nil && len(rows) != len(dialogs) {
						return 0, fmt.Errorf("coverage row count differs")
					}
					return 0, err
				}},
				{"health", func() (int, error) { _ = p.Server.Health(ctx, 0); return 0, nil }},
				{"readActivity", func() (int, error) { return 0, phaseReadActivity(ctx, eng.store, eng.account, time.Now()) }},
				{"readStatus", func() (int, error) { _, _, err := eng.store.ReadStatus(ctx, eng.account); return 0, err }},
				// Actual store-backed Status cost if pulse omitted Health. This is
				// a diagnostic candidate, NOT a compatible replacement: receiving,
				// diagnostics and coverage warnings would be lost without a
				// separately refreshed, explicitly stale health result.
				{"pulseStatusOnlyCandidate", func() (int, error) {
					status := eng.Status()
					_ = syncKey(status)
					_ = settling(status)
					return 0, nil
				}},
			}
			for _, phase := range phases {
				d.Measurements = append(d.Measurements, phaseMeasure(eng, "phase", phase.name, phase.run))
			}
			routes := []struct{ label, path string }{}
			for _, path := range []string{"/", "/conta", "/status", "/funcoes", "/receitas", "/ajuda", "/configuracoes", "/conectar", "/api/pulse", "/api/state", "/api/chats"} {
				routes = append(routes, struct{ label, path string }{path, path})
			}
			if len(dialogs) > 0 {
				peer := dialogs[0].Peer // strictly in memory; never persist the actual route
				routes = append(routes, struct{ label, path string }{"/api/chats/{peer}", fmt.Sprintf("/api/chats/%s:%d?limit=50", peer.Type, peer.ID)})
			}
			for _, route := range routes {
				complete, incomplete := 0, 0
				measurement := phaseMeasure(eng, "route", route.label, func() (int, error) {
					r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47831"+route.path, nil)
					r.Header.Set("Origin", "http://127.0.0.1:47831")
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					// Bodies are private process memory only, never logged, written,
					// or used as the error message even for a non-200 response.
					if route.label == "/status" {
						// A timeout still returns HTTP 200 in the baseline. Count
						// rendered coverage rows, never mistake it for success.
						body := w.Body.String()
						_, tail, found := strings.Cut(body, "data-conversation-coverage>")
						list, _, closed := strings.Cut(tail, "</ul>")
						if (found && closed && strings.Count(list, "<li class=\"row\">") == len(dialogs)) || (!found && len(dialogs) == 0) {
							complete++
						} else {
							incomplete++
						}
					}
					return w.Code, nil
				})
				measurement.CoverageRenderedComplete, measurement.CoverageRenderedIncomplete = complete, incomplete
				d.Measurements = append(d.Measurements, measurement)
			}
			// Measure expensive client discovery separately; routes above retain
			// natural cold/warm behavior rather than faking executable results.
			d.Measurements = append(d.Measurements, phaseMeasure(eng, "phase", "clientDiscoveryCold", func() (int, error) {
				p.code.at, p.codex.at = time.Time{}, time.Time{}
				_ = p.setup(ctx)
				return 0, nil
			}))
			d.Measurements = append(d.Measurements, phaseMeasure(eng, "phase", "clientDiscoveryWarm", func() (int, error) { _ = p.setup(ctx); return 0, nil }))
			evidence.Datasets = append(evidence.Datasets, d)
		})
	}
	body, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal("encode allowlisted evidence failed")
	}
	if err := os.WriteFile(output, append(body, '\n'), 0600); err != nil {
		t.Fatal("write allowlisted evidence failed")
	}
	for _, d := range evidence.Datasets {
		for _, m := range d.Measurements {
			t.Logf("%s %s %s n=%d median=%.3fms p95=%.3fms >500ms=%d errors=%d HTTP=%v", d.Dataset, m.Kind, m.Name, m.N, m.MedianMS, m.P95MS, m.Over500MS, m.Errors, m.HTTPStatuses)
		}
	}
}
