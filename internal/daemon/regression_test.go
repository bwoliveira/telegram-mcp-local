package daemon

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestReadonlyIntentSurvivesStateDatabaseFailureAndRestart(t *testing.T) {
	cfg := testConfig(t, 0)
	d, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Engine().SetMode(context.Background(), engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	if err := d.Primary().st.SetSetting(context.Background(), "engine.mode", string(engine.ModeReadWrite)); err != nil {
		t.Fatal(err)
	}
	d.Primary().st.Close()
	req := httptest.NewRequest("POST", "wails://localhost/api/mode", strings.NewReader(`{"mode":"read_only","confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	d.AppHandler().ServeHTTP(rec, req)
	if rec.Code != 500 || d.Engine().Mode() != engine.ModeReadOnly {
		t.Fatal("failed state persistence left writes enabled", rec.Code)
	}
	d.Stop()
	next, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Stop()
	if next.Engine().Mode() != engine.ModeReadOnly {
		t.Fatal("stale read_write restored after failed downgrade persistence")
	}
}

type delayedController struct {
	engine.Controller
	entered, release chan struct{}
	ready            atomic.Bool
}

func (d *delayedController) WaitReady(ctx context.Context) error {
	close(d.entered)
	select {
	case <-d.release:
		d.ready.Store(true)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *delayedController) StartLogin(ctx context.Context, r engine.LoginRequest) error {
	if !d.ready.Load() {
		return engine.ErrPaused
	}
	return d.Controller.StartLogin(ctx, r)
}
func TestReloginWaitsForTransportReadiness(t *testing.T) {
	cfg := testConfig(t, 0)
	calls := 0
	delayed := &delayedController{entered: make(chan struct{}), release: make(chan struct{})}
	cfg.Factory = func(c engine.Config) (engine.Controller, error) {
		calls++
		f, err := fake.New(c)
		if err != nil {
			return nil, err
		}
		if calls == 1 {
			return f, nil
		}
		if err := f.LogOut(context.Background()); err != nil {
			return nil, err
		}
		delayed.Controller = f
		return delayed, nil
	}
	d, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	if err := d.Engine().LogOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- d.Engine().StartLogin(context.Background(), engine.LoginRequest{Method: engine.LoginQR})
	}()
	select {
	case <-delayed.entered:
	// Bounds a hang only; slow hosted runners can take over a second here.
	case <-time.After(10 * time.Second):
		t.Fatal("readiness not awaited")
	}
	select {
	case err := <-result:
		t.Fatal("login raced transport readiness", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(delayed.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("login blocked after ready")
	}
}
