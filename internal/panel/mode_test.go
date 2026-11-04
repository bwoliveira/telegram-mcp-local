package panel

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func modeRequest(t *testing.T, h http.Handler, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "http://127.0.0.1:47831/api/mode", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// POST /api/mode takes exactly {"mode": ..., "confirm": true}, persists the
// choice as engine.mode and applies it through Controller.SetMode.
func TestPanelModeChangePersistsAndApplies(t *testing.T) {
	p, h := panelWithFake(t)
	ctx := context.Background()
	if p.Engine.Status().Mode == engine.ModeReadWrite {
		t.Fatal("fake starts writable")
	}
	w := modeRequest(t, h, `{"mode":"read_write","confirm":true}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("enable writes: %d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["mode"] != "read_write" {
		t.Fatalf("answer: %s %v", w.Body, err)
	}
	if m := p.Engine.Status().Mode; m != engine.ModeReadWrite {
		t.Fatalf("engine mode = %s", m)
	}
	if v, _ := p.State.Setting(ctx, ModeSetting); v != "read_write" {
		t.Fatalf("persisted %s = %q", ModeSetting, v)
	}
	w = modeRequest(t, h, `{"mode":"read_only","confirm":true}`, nil)
	if w.Code != http.StatusOK || p.Engine.Status().Mode != engine.ModeReadOnly {
		t.Fatalf("back to read-only: %d %s %s", w.Code, w.Body, p.Engine.Status().Mode)
	}
	if v, _ := p.State.Setting(ctx, ModeSetting); v != "read_only" {
		t.Fatalf("persisted %s = %q", ModeSetting, v)
	}
}

// Anything other than the exact payload changes nothing.
func TestPanelModeRejectsInexactPayloads(t *testing.T) {
	p, h := panelWithFake(t)
	for _, body := range []string{
		``,
		`{}`,
		`{"mode":"read_write"}`,
		`{"mode":"read_write","confirm":false}`,
		`{"mode":"read_write","confirm":"true"}`,
		`{"mode":"read_write","confirm":1}`,
		`{"mode":"write","confirm":true}`,
		`{"mode":"READ_WRITE","confirm":true}`,
		`{"mode":"","confirm":true}`,
		`{"confirm":true}`,
		`{"mode":"read_write","confirm":true,"extra":1}`,
		`{"mode":"read_write","confirm":true,"Mode":"read_only"}`,
		`{"mode":"read_write","mode":"read_write","confirm":true}`,
		`{"mode":"read_write","confirm":true}{"x":1}`,
		`{"mode":"read_write","confirm":true} trailing`,
		`["read_write",true]`,
		`{"mode":null,"confirm":true}`,
	} {
		w := modeRequest(t, h, body, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: want 400, got %d %s", body, w.Code, w.Body)
		}
		if p.Engine.Status().Mode == engine.ModeReadWrite {
			t.Fatalf("%q enabled writes", body)
		}
		if v, _ := p.State.Setting(context.Background(), ModeSetting); v != "" {
			t.Fatalf("%q persisted %q", body, v)
		}
	}
}

// The mode is a person's choice on the panel: a program presenting the bearer
// token is refused even when it reaches the panel handler directly, and so is
// another origin, a form or a GET.
func TestPanelModeIsNeverAnAgentAPI(t *testing.T) {
	p, h := panelWithFake(t)
	p.Token = "synthetic-panel-token"
	mux := http.NewServeMux()
	p.Register(mux)
	agent := modeRequest(t, mux, `{"mode":"read_write","confirm":true}`, map[string]string{"Authorization": "Bearer synthetic-panel-token"})
	cross := modeRequest(t, mux, `{"mode":"read_write","confirm":true}`, map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"})
	bearerInBrowser := modeRequest(t, mux, `{"mode":"read_write","confirm":true}`, map[string]string{"Origin": "http://127.0.0.1:47831", "Sec-Fetch-Site": "same-origin", "Authorization": "Bearer synthetic-panel-token"})
	for name, w := range map[string]*httptest.ResponseRecorder{"agent": agent, "cross-origin": cross, "bearer in browser": bearerInBrowser} {
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d %s", name, w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:47831/api/mode", strings.NewReader("mode=read_write&confirm=true"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form := httptest.NewRecorder()
	h.ServeHTTP(form, r)
	if form.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form: want 415, got %d", form.Code)
	}
	get := panelRequest(t, h, "GET", "/api/mode", "")
	if get.Code == http.StatusOK && strings.Contains(get.Body.String(), "read_write") {
		t.Errorf("GET changed or exposed a mode switch: %d %s", get.Code, get.Body)
	}
	if p.Engine.Status().Mode == engine.ModeReadWrite {
		t.Fatal("a refused request enabled writes")
	}
}

type failingMode struct {
	engine.Controller
	err error
}

func (f failingMode) SetMode(context.Context, engine.Mode) error { return f.err }

// When the engine refuses, nothing is persisted and the error is readable.
func TestPanelModeEngineFailureIsNotPersisted(t *testing.T) {
	p, _ := panelWithFake(t)
	p.Engine = failingMode{Controller: p.Engine, err: errors.New("engine closed")}
	mux := http.NewServeMux()
	p.Register(mux)
	w := modeRequest(t, Internal(mux), `{"mode":"read_write","confirm":true}`, nil)
	if w.Code < 400 || strings.Contains(w.Body.String(), `"mode":"read_write"`) {
		t.Fatalf("engine failure answered %d %s", w.Code, w.Body)
	}
	if v, _ := p.State.Setting(context.Background(), ModeSetting); v != "" {
		t.Fatalf("persisted %q after engine failure", v)
	}
}

// If saving the choice fails after writes were enabled, the engine goes back
// to read-only rather than staying writable without a record of why.
func TestPanelModePersistFailureRevertsToReadOnly(t *testing.T) {
	p, h := panelWithFake(t)
	_ = p.State.Close()
	w := modeRequest(t, h, `{"mode":"read_write","confirm":true}`, nil)
	if w.Code < 400 {
		t.Fatalf("persist failure answered %d %s", w.Code, w.Body)
	}
	if m := p.Engine.Status().Mode; m == engine.ModeReadWrite {
		t.Fatal("writes stayed enabled without being persisted")
	}
}
