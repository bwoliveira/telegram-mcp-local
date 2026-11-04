package panel

// Status must tell three situations apart without crying wolf: updates still
// being replayed (active synchronization), a ready account whose stored
// history is partial (backfill limits), and terminal or inaccessible gaps that
// replay can never fill. Only the first, and real sync/config problems, may
// raise the Status tab's warning mark; none of them promises complete history.
import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

func realSnapshotUpdates() engine.UpdatesStatus {
	return engine.UpdatesStatus{CoverageGaps: 771, TerminalGaps: 771, InaccessibleGaps: 769, GapCauses: map[engine.GapRecoveryCause]int{
		engine.GapRecoveryAccessLost: 769, engine.GapRecoveryChannelTooLong: 1, engine.GapRecoveryUnknownBaseline: 1,
	}}
}

func TestCoverageTextSeparatesSyncPartialAndTerminal(t *testing.T) {
	partial := []conversationCoverage{{ID: "user:1", Complete: true}, {ID: "user:2"}, {ID: "channel:3"}}
	complete := []conversationCoverage{{ID: "user:1", Complete: true}}
	connected := engine.StateConnected
	for _, c := range []struct {
		name     string
		st       engine.Status
		conv     []conversationCoverage
		tone     string
		has, not []string
	}{
		{"recoverable gaps are active sync", engine.Status{State: connected, Updates: engine.UpdatesStatus{OpenGaps: 2, CoverageGaps: 2}}, complete, "sync", []string{"2"}, []string{"Pronto"}},
		{"held checkpoint is active sync", engine.Status{State: connected, Updates: engine.UpdatesStatus{Held: true}}, complete, "sync", nil, []string{"Pronto"}},
		{"history download is active sync", engine.Status{State: connected, History: engine.HistoryProgress{Running: &engine.PeerRef{Type: engine.PeerUser, ID: 1}}}, partial, "sync", []string{"histórico"}, []string{"Pronto"}},
		{"resyncing is active sync", engine.Status{State: engine.StateResyncing}, complete, "sync", nil, []string{"Pronto"}},
		{"terminal gaps are information", engine.Status{State: connected, Updates: realSnapshotUpdates()}, complete, "info", []string{"Pronto", "771", "não comprova"}, []string{"recuperação", "recuperando", "Sem lacunas"}},
		{"terminal plus partial conversations", engine.Status{State: connected, Updates: realSnapshotUpdates()}, partial, "info", []string{"Pronto", "771", "2 de 3"}, []string{"recuperação"}},
		{"backfill limit is partial, ready", engine.Status{State: connected}, partial, "info", []string{"Pronto", "2 de 3", "não comprova"}, []string{"Sem lacunas conhecidas"}},
		{"nothing known missing", engine.Status{State: connected}, complete, "ok", []string{"Sem lacunas conhecidas"}, nil},
		{"disconnected is unmeasured", engine.Status{State: engine.StatePaused, Updates: realSnapshotUpdates()}, partial, "off", nil, nil},
	} {
		tone, text := coverageText(c.st, c.conv)
		if tone != c.tone {
			t.Errorf("%s: tone %q, want %q (%s)", c.name, tone, c.tone, text)
		}
		for _, s := range c.has {
			if !strings.Contains(text, s) {
				t.Errorf("%s: text %q lacks %q", c.name, text, s)
			}
		}
		for _, s := range c.not {
			if strings.Contains(text, s) {
				t.Errorf("%s: text %q says %q", c.name, text, s)
			}
		}
		if strings.Contains(text, "histórico completo") && !strings.Contains(text, "não comprova histórico completo") {
			t.Errorf("%s: text promises completeness: %q", c.name, text)
		}
	}
}

// Only real sync/config problems raise the mark; incomplete or terminally
// inaccessible history is information, whatever the health report calls it.
func TestToneTreatsHistoryCoverageAsInformation(t *testing.T) {
	ok := []mcp.Check{{Name: "daemon", Status: "ok"}, {Name: "paired", Status: "ok"}, {Name: "sync", Status: "ok"}, {Name: "receiving", Status: "ok"}}
	connected := engine.Status{State: engine.StateConnected, Updates: realSnapshotUpdates()}
	for _, c := range []struct {
		name   string
		checks []mcp.Check
		sync   engine.Status
		want   string
	}{
		{"unavailable coverage still warns", append(ok[:4:4], mcp.Check{Name: "coverage", Status: "warn"}), connected, "warn"},
		{"coverage info is information", append(ok[:4:4], mcp.Check{Name: "coverage", Status: "info"}), connected, "ok"},
		{"recoverable update gaps still warn", append(ok[:4:4], mcp.Check{Name: "updates", Status: "warn"}, mcp.Check{Name: "coverage", Status: "info"}), engine.Status{State: engine.StateConnected, Updates: engine.UpdatesStatus{OpenGaps: 1}}, "warn"},
		{"replay while resyncing is on its way", []mcp.Check{{Name: "sync", Status: "warn"}, {Name: "updates", Status: "warn"}, {Name: "coverage", Status: "info"}}, engine.Status{State: engine.StateResyncing, Updates: engine.UpdatesStatus{Held: true}}, "ok"},
		{"failed history request still warns", append(ok[:4:4], mcp.Check{Name: "history_request", Status: "warn"}), connected, "warn"},
		{"session failure still fails", []mcp.Check{{Name: "paired", Status: "fail"}, {Name: "coverage", Status: "info"}}, connected, "fail"},
	} {
		if got := tone(c.checks, c.sync); got != c.want {
			t.Errorf("%s: tone = %q, want %q", c.name, got, c.want)
		}
	}
}

func terminalStatusSnapshot() snapshot {
	s := snapshot{Sync: engine.Status{State: engine.StateConnected, Mode: engine.ModeReadOnly, Updates: realSnapshotUpdates()}, SnapshotReady: true,
		Health: mcp.Health{Status: "warn", Checks: []mcp.Check{{Name: "daemon", Status: "ok"}, {Name: "paired", Status: "ok"}, {Name: "sync", Status: "ok"},
			{Name: "receiving", Status: "ok"}, {Name: "coverage", Status: "info"}}}}
	s.SyncTone, s.SyncLabel = syncLabel(s.Sync)
	s.readChecks()
	return s
}

func renderStatus(t *testing.T, s snapshot, v statusView) string {
	t.Helper()
	var b strings.Builder
	l := layout{Title: "Status", Active: "status", HealthTone: s.Tone, LiveKey: syncKey(s.Sync)}
	if err := parseTemplates().ExecuteTemplate(&b, "status", statusPage{l, s, v, true}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The template shows terminal gaps as information: no warning mark, no
// problems list, the coverage check without "Atenção", and the cause counts.
func TestStatusTemplateTerminalGapsAreInformation(t *testing.T) {
	s := terminalStatusSnapshot()
	conv := []conversationCoverage{{Name: "A", ID: "user:1", Complete: true}, {Name: "B", ID: "channel:2"}}
	v := statusView{ConversationCoverage: conv}
	v.CoverageTone, v.CoverageText = coverageText(s.Sync, conv)
	page := renderStatus(t, s, v)
	mustContain(t, "terminal status", page, "Nenhum problema detectado", `data-coverage="info"`, `data-open-gaps>0<`, `data-terminal-gaps>771<`,
		`data-gap-cause="access_lost"`, "769", `data-gap-cause="channel_too_long"`, `data-gap-cause="unknown_baseline"`, `data-check="coverage" data-check-status="info"`)
	for _, unwanted := range []string{`class="nav__alert`, `class="problems__warn"`, `Atenção:`, `data-coverage="warn"`, `data-coverage="sync"`} {
		if strings.Contains(page, unwanted) {
			t.Errorf("terminal status shows %q", unwanted)
		}
	}
	coverage := page[strings.Index(page, `data-check="coverage"`):]
	coverage = coverage[:strings.Index(coverage, "</li>")]
	if strings.Contains(coverage, "Atenção") || strings.Contains(coverage, "pill--warn") {
		t.Errorf("coverage check row looks like a warning: %s", coverage)
	}
}

// Replay in progress is shown as synchronization under way, not as ready.
func TestStatusTemplateActiveSync(t *testing.T) {
	s := snapshot{Sync: engine.Status{State: engine.StateConnected, Updates: engine.UpdatesStatus{OpenGaps: 3, CoverageGaps: 3, GapCauses: map[engine.GapRecoveryCause]int{engine.GapRecoveryReplayPending: 3}}}, SnapshotReady: true,
		Health: mcp.Health{Checks: []mcp.Check{{Name: "sync", Status: "ok"}, {Name: "updates", Status: "warn"}, {Name: "coverage", Status: "info"}}}}
	s.SyncTone, s.SyncLabel = syncLabel(s.Sync)
	s.readChecks()
	v := statusView{}
	v.CoverageTone, v.CoverageText = coverageText(s.Sync, nil)
	page := renderStatus(t, s, v)
	mustContain(t, "syncing status", page, `data-coverage="sync"`, `data-open-gaps>3<`, `class="problems__warn"`, "nav__alert--warn")
	if strings.Contains(page, "Pronto") {
		t.Error("active sync claims to be ready")
	}
	if text := describeUpdates(engine.Status{State: engine.StateConnected, Updates: engine.UpdatesStatus{Held: true}}); strings.Contains(text, " 0 ") {
		t.Errorf("held checkpoint without gaps talks about 0 gaps: %q", text)
	}
}

// Every new Status string has English and Spanish (the catalogue test checks
// keys; this checks the page really switches language).
func TestStatusReadinessLocalized(t *testing.T) {
	defer i18n.Set(i18n.Source)
	for _, lang := range []string{"en", "es"} {
		i18n.Set(lang)
		s := terminalStatusSnapshot()
		conv := []conversationCoverage{{Name: "B", ID: "channel:2"}}
		v := statusView{ConversationCoverage: conv}
		v.CoverageTone, v.CoverageText = coverageText(s.Sync, conv)
		page := renderStatus(t, s, v)
		for _, pt := range []string{"Pronto:", "Lacunas sem recuperação", "Lacunas em recuperação", "registros de perda de acesso", "Informação"} {
			if strings.Contains(page, pt) {
				t.Errorf("%s Status keeps Portuguese %q", lang, pt)
			}
		}
	}
}

// Full HTTP regression with the real store: the measured real-account shape
// (only terminal gaps plus partial conversations) keeps every page's Status
// tab free of the warning mark, in the server HTML, the hydrated Status, and
// the background pulse that the navigation follows.
func TestTerminalGapsKeepNavigationQuiet(t *testing.T) {
	p, h, eng := phaseFixture(t, "")
	seedTerminalGaps(t, p, eng)
	ctx := context.Background()
	if _, err := p.cache.health.get(ctx, p.loadHealth); err != nil {
		t.Fatal(err)
	}
	status := hydratedStatusBody(t, h)
	mustContain(t, "/status", status, `data-coverage="info"`, `data-terminal-gaps>`, `data-open-gaps>0<`, "Pronto")
	for _, path := range []string{"/", "/conta", "/status", "/funcoes", "/receitas", "/configuracoes"} {
		page := pageBody(t, h, path)
		nav := page[strings.Index(page, `<nav class="nav"`):]
		nav = nav[:strings.Index(nav, "</nav>")]
		if strings.Contains(nav, "nav__alert") || strings.Contains(nav, `data-live-tone="warn"`) {
			t.Errorf("%s nav warns for terminal history gaps: %s", path, nav)
		}
	}
	if strings.Contains(status, `class="problems__warn"`) {
		t.Error("Status lists terminal history as a problem")
	}
	rec := panelRequest(t, h, "GET", "/api/pulse", "")
	var pulse struct{ Tone string }
	if err := json.Unmarshal(rec.Body.Bytes(), &pulse); err != nil || pulse.Tone != "ok" {
		t.Fatalf("pulse tone %q (%v): %s", pulse.Tone, err, rec.Body)
	}
}

// seedTerminalGaps reproduces the real snapshot's shape in the synthetic
// store: one global state_lost without provenance, one channel_too_long,
// one access_lost summary per scope, and partial backfill cursors.
func seedTerminalGaps(t *testing.T, p *Panel, eng *phaseEngine) {
	t.Helper()
	db, err := sql.Open("sqlite", eng.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO gaps(account,reason,detected_at) VALUES('primary','state_lost',1)`,
		`INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('primary','user',3,'channel_too_long',1)`,
		`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<40) INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) SELECT 'primary','user',i,'access_lost',i FROM n`,
		`UPDATE backfill_cursors SET complete=0 WHERE peer_id>100`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_, updates, err := eng.store.ReadStatus(context.Background(), eng.account)
	if err != nil || updates.OpenGaps != 0 || updates.TerminalGaps != 42 || updates.CoverageGaps != 42 {
		t.Fatalf("seeded status %+v %v", updates, err)
	}
}
