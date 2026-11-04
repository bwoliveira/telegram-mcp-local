package panel

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// The synthetic/read-only-copy fixture mirrors gotd's separation between its
// in-memory lifecycle and its durable Status enrichment.
func (e *phaseEngine) LightStatus() engine.Status         { return e.Controller.Status() }
func (e *statusFixtureEngine) LightStatus() engine.Status { return e.Controller.Status() }

func TestCachedPageBudgetsOnStoreFixtures(t *testing.T) {
	cases := []struct{ name, path string }{{"synthetic", ""}, {"isolated-real-copy", os.Getenv("PANEL_CACHE_REAL_DB")}}
	for _, dataset := range cases {
		t.Run(dataset.name, func(t *testing.T) {
			skipWithoutBackup(t, dataset.name == "isolated-real-copy", dataset.path, "PANEL_CACHE_REAL_DB")
			p, h, eng := phaseFixture(t, dataset.path)
			for _, path := range []string{"/", "/ajuda", "/funcoes", "/receitas", "/conectar", "/configuracoes", "/conta", "/status"} {
				for _, sample := range []string{"cold", "warm"} {
					before := eng.calls.Load()
					start := time.Now()
					response := panelRequest(t, h, "GET", path, "")
					elapsed := time.Since(start)
					if response.Code != 200 {
						t.Fatalf("%s %s: HTTP %d", path, sample, response.Code)
					}
					budget := 150 * time.Millisecond
					if path == "/status" {
						budget = 300 * time.Millisecond
					}
					if elapsed >= budget {
						t.Fatalf("%s %s %s exceeds %s", path, sample, elapsed, budget)
					}
					if eng.calls.Load() != before {
						t.Fatalf("%s %s read durable status", path, sample)
					}
					t.Logf("%s %s %.3fms status_reads=0", path, sample, float64(elapsed.Microseconds())/1000)
				}
			}
			start := time.Now()
			response := panelRequest(t, h, "GET", "/api/panel/status", "")
			cold := time.Since(start)
			if response.Code != 200 {
				t.Fatalf("async HTTP %d", response.Code)
			}
			var snapshot struct {
				HTML  string
				Fresh bool
			}
			if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil || !snapshot.Fresh || snapshot.HTML == "" {
				t.Fatal("missing async snapshot")
			}
			before := eng.calls.Load()
			_, healthAt, _ := p.cache.health.peek()
			_, detailsAt, _ := p.cache.details.peek()
			start = time.Now()
			response = panelRequest(t, h, "GET", "/api/panel/status", "")
			warm := time.Since(start)
			if response.Code != 200 {
				t.Fatalf("warm async status: HTTP %d", response.Code)
			}
			// Race instrumentation can make the cold details load outlive the
			// 1.5-second health TTL. A refresh then is correct, not a cache miss
			// regression. Exact expiry/single-flight behavior has a unit test.
			windowValid := time.Since(healthAt) < panelSnapshotTTL && time.Since(detailsAt) < panelSnapshotTTL
			if windowValid && eng.calls.Load() != before {
				t.Fatal("unexpired warm async snapshot not reused")
			}
			if !windowValid {
				t.Log("the cache window expired during the instrumented load; refresh permitted")
			}
			if eng.store.messageReads.Load() != 0 {
				t.Fatal("materialized conversation content")
			}
			t.Logf("async cold=%.3fms warm=%.3fms full_message_reads=0", float64(cold.Microseconds())/1000, float64(warm.Microseconds())/1000)
			_ = p
		})
	}
}
