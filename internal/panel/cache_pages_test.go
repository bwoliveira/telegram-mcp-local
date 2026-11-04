package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

type panelProbeController struct {
	engine.Controller
	heavy, accounts, logins, diagnostics, subscriptions atomic.Int64
	events                                              chan engine.Event
	cancelSeen                                          chan struct{}
	block                                               <-chan struct{}
}

func (e *panelProbeController) LightStatus() engine.Status { return e.Controller.Status() }
func (e *panelProbeController) Status() engine.Status {
	e.heavy.Add(1)
	if e.block != nil {
		<-e.block
	}
	return e.Controller.Status()
}
func (e *panelProbeController) Account(ctx context.Context) (engine.Account, error) {
	e.accounts.Add(1)
	return e.Controller.Account(ctx)
}
func (e *panelProbeController) Login() engine.Login { e.logins.Add(1); return e.Controller.Login() }
func (e *panelProbeController) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	e.diagnostics.Add(1)
	return e.Controller.Diagnostics(ctx)
}
func (e *panelProbeController) Subscribe(ctx context.Context, opts engine.SubscribeOptions) <-chan engine.Event {
	e.subscriptions.Add(1)
	if e.cancelSeen != nil {
		go func() { <-ctx.Done(); close(e.cancelSeen) }()
	}
	return e.events
}

func TestCheapPagesRenderWithoutHeavyStateOrDiscovery(t *testing.T) {
	p, h := panelWithFake(t)
	p.AccountID = "primary"
	probe := &panelProbeController{Controller: p.Engine}
	p.Engine = probe
	if err := p.State.SetSetting(context.Background(), setupSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if err := p.State.SetSetting(context.Background(), accountLabelForTest(), "fixture"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/ajuda", "/funcoes", "/receitas", "/conectar", "/configuracoes", "/conta", "/status"} {
		// A held discovery mutex models an arbitrarily slow subprocess: neither
		// setup rendering nor /api/state may wait for it.
		p.code.mu.Lock()
		p.codex.mu.Lock()
		beforeAccount, beforeLogin := probe.accounts.Load(), probe.logins.Load()
		start := time.Now()
		rec := panelRequest(t, h, "GET", path, "")
		elapsed := time.Since(start)
		p.codex.mu.Unlock()
		p.code.mu.Unlock()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status %d", path, rec.Code)
		}
		if probe.heavy.Load() != 0 || probe.diagnostics.Load() != 0 {
			t.Fatalf("%s did heavy work before render", path)
		}
		budget := 150 * time.Millisecond
		if path == "/status" {
			budget = 300 * time.Millisecond
			if !strings.Contains(rec.Body.String(), "data-status-snapshot") {
				t.Fatal("missing status loading state")
			}
		}
		if elapsed >= budget {
			t.Fatalf("%s cold render %s exceeds %s", path, elapsed, budget)
		}
		if path == "/ajuda" || path == "/funcoes" || path == "/receitas" || path == "/configuracoes" {
			if probe.accounts.Load() != beforeAccount || probe.logins.Load() != beforeLogin {
				t.Fatalf("static %s read account/login", path)
			}
		}
		t.Logf("%s cold=%s", path, elapsed)
	}
}

// Keep the account identity setting literal out of fixture calls above.
func accountLabelForTest() string { return "account.label" }

func TestAsyncStatusAndPulseReuseTelemetryWithoutCachingMode(t *testing.T) {
	p, h := panelWithFake(t)
	probe := &panelProbeController{Controller: p.Engine}
	p.Engine = probe
	for range 2 {
		rec := panelRequest(t, h, "GET", "/api/panel/status", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("async status=%d %s", rec.Code, rec.Body)
		}
		var got struct {
			HTML  string
			Fresh bool
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Fresh || !strings.Contains(got.HTML, "data-status-connection") || !strings.Contains(got.HTML, "fakeengine") {
			t.Fatalf("incomplete async fragment")
		}
	}
	if probe.heavy.Load() != 1 || probe.diagnostics.Load() != 1 {
		t.Fatalf("cache miss/storm: status=%d diagnostics=%d", probe.heavy.Load(), probe.diagnostics.Load())
	}
	for range 10 {
		rec := panelRequest(t, h, "GET", "/api/pulse", "")
		if rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	if probe.heavy.Load() != 1 {
		t.Fatal("pulse read store state")
	}
	if err := p.Engine.SetMode(context.Background(), engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	if s := p.snapshot(context.Background()); s.ReadOnly {
		t.Fatal("cached telemetry overrode live mode")
	}
	if err := p.Engine.SetMode(context.Background(), engine.ModeReadOnly); err != nil {
		t.Fatal(err)
	}
	if s := p.snapshot(context.Background()); !s.ReadOnly {
		t.Fatal("mode did not fail closed")
	}
}

func TestPanelSnapshotEventsAreScopedAndCancellationDrains(t *testing.T) {
	p, _ := panelWithFake(t)
	p.AccountID = "primary"
	probe := &panelProbeController{Controller: p.Engine, events: make(chan engine.Event, 64), cancelSeen: make(chan struct{})}
	p.Engine = probe
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	p.Start(ctx)
	if probe.subscriptions.Load() != 1 {
		t.Fatal("duplicate subscriber")
	}
	if _, err := p.cache.health.get(ctx, p.loadHealth); err != nil {
		t.Fatal(err)
	}
	version := p.cache.health.version()
	probe.events <- engine.Event{Kind: engine.EventConnection, Account: "other"}
	// A lifecycle event in the same FIFO proves the other-account one has been
	// consumed without relying on a scheduler sleep.
	probe.events <- engine.Event{Kind: engine.EventAuth, Account: "primary"}
	deadline := time.Now().Add(time.Second)
	for p.cache.health.version() == version && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.cache.health.version() != version+1 {
		t.Fatal("events were not coalesced/scoped")
	}
	if _, _, fresh := p.cache.health.peek(); fresh {
		t.Fatal("event left cached state fresh")
	}
	cancel()
	select {
	case <-p.cache.done:
	case <-time.After(time.Second):
		t.Fatal("cache lifetime leaked")
	}
	select {
	case <-probe.cancelSeen:
	case <-time.After(time.Second):
		t.Fatal("subscription leaked")
	}
	p.refreshHealth()
	if probe.heavy.Load() != 1 {
		t.Fatal("work started after shutdown")
	}
}

func TestPulseStartsOnlyOneBackgroundRefreshAndShutdownWaits(t *testing.T) {
	p, h := panelWithFake(t)
	block := make(chan struct{})
	probe := &panelProbeController{Controller: p.Engine, events: make(chan engine.Event), block: block}
	p.Engine = probe
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	for range 20 {
		if got := panelRequest(t, h, "GET", "/api/pulse", ""); got.Code != 200 {
			t.Fatal(got.Code)
		}
	}
	deadline := time.Now().Add(time.Second)
	for probe.heavy.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if probe.heavy.Load() != 1 {
		t.Fatalf("pulse refresh storm %d", probe.heavy.Load())
	}
	cancel()
	select {
	case <-p.cache.done:
		t.Fatal("shutdown abandoned owned refresh")
	default:
	}
	close(block)
	select {
	case <-p.cache.done:
	case <-time.After(time.Second):
		t.Fatal("refresh was not drained")
	}
	if _, _, fresh := p.cache.health.peek(); fresh {
		t.Fatal("canceled health published")
	}
}

func TestRequestRefreshIsBoundToPanelLifetime(t *testing.T) {
	p, h := panelWithFake(t)
	block := make(chan struct{})
	probe := &panelProbeController{Controller: p.Engine, events: make(chan engine.Event), block: block}
	p.Engine = probe
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	requestDone := make(chan int, 1)
	go func() { requestDone <- panelRequest(t, h, "GET", "/api/panel/status", "").Code }()
	deadline := time.Now().Add(time.Second)
	for probe.heavy.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if probe.heavy.Load() != 1 {
		close(block)
		t.Fatal("request did not start")
	}
	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()
	for p.cache.ctx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopped:
		close(block)
		t.Fatal("Stop abandoned HTTP-owned refresh")
	default:
	}
	close(block)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not drain request")
	}
	if status := <-requestDone; status == 200 {
		t.Fatal("canceled lifetime returned fresh result")
	}
	if _, _, fresh := p.cache.health.peek(); fresh {
		t.Fatal("request published after shutdown")
	}
}

func hydratedStatusBody(t *testing.T, h http.Handler) string {
	t.Helper()
	shell := pageBody(t, h, "/status")
	rec := panelRequest(t, h, "GET", "/api/panel/status", "")
	if rec.Code != 200 {
		t.Fatalf("async status HTTP %d", rec.Code)
	}
	var snapshot struct {
		HTML  string
		Fresh bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil || !snapshot.Fresh {
		t.Fatal("invalid async status")
	}
	return shell + snapshot.HTML
}
