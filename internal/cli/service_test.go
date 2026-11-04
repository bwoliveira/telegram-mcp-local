package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

type fakeService struct {
	spec                                           service.Spec
	installed, removed, started, stopped, statuses int
}

func (f *fakeService) Install(s service.Spec) (string, error) {
	f.spec = s
	f.installed++
	return "isolated/telegram-mcp.service", nil
}
func (f *fakeService) Uninstall() error        { f.removed++; return nil }
func (f *fakeService) Start() error            { f.started++; return nil }
func (f *fakeService) Stop() error             { f.stopped++; return nil }
func (f *fakeService) Status() (string, error) { f.statuses++; return "active", nil }

func TestInstallUsesAuthenticatedOpenExactlyOnce(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_ENGINE", "fake")
	var cfg config
	bootstrap := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			fmt.Fprintln(w, "ok")
		case "/api/session/bootstrap":
			bootstrap++
			if r.Method != "POST" || r.Header.Get("Authorization") != "" || r.Header.Get(httpserver.PanelBootstrapHeader) != "synthetic-panel-bootstrap-marker" {
				t.Error("bootstrap did not use the separate panel authority")
			}
			fmt.Fprintf(w, `{"url":%q}`, cfg.baseURL()+"/bootstrap?c=synthetic-install-code")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	cfg = testServerConfig(t, s)
	cfg.Token = ""
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.StoreDir = filepath.Join(cfg.DataDir, "store")
	testutil.NativeSecrets(t, cfg.DataDir)
	manager := &fakeService{}
	opened := 0
	var out bytes.Buffer
	err := runService(context.Background(), cfg, []string{"install"}, manager, func(u string) error {
		opened++
		if !strings.Contains(u, "/bootstrap?c=") {
			t.Error("opened plain unauthenticated panel")
		}
		return nil
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if manager.installed != 1 || bootstrap != 1 || opened != 1 {
		t.Fatalf("installed=%d bootstrap=%d opened=%d", manager.installed, bootstrap, opened)
	}
	if _, ok := manager.spec.Env["TELEGRAM_MCP_TOKEN"]; ok {
		t.Fatal("service spec contains token")
	}
	if manager.spec.Env["TELEGRAM_MCP_PORT"] == "" {
		t.Fatal("service lost port")
	}
	if strings.Contains(out.String(), "synthetic-install-code") {
		t.Fatal("bootstrap code printed")
	}
}

func TestServiceStatusAndInvalidArgumentsHaveNoInstallationEffects(t *testing.T) {
	f := &fakeService{}
	var out bytes.Buffer
	if err := runService(context.Background(), config{}, []string{"status"}, f, nil, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "active" || f.statuses != 1 {
		t.Fatalf("status %q calls=%d", &out, f.statuses)
	}
	for _, args := range [][]string{nil, {"install", "--bad"}, {"uninstall", "extra"}, {"unknown-secret-marker"}} {
		err := runService(context.Background(), config{}, args, f, nil, &out)
		if err == nil || strings.Contains(err.Error(), "unknown-secret-marker") {
			t.Fatalf("unsafe validation: %v", err)
		}
	}
	if f.installed != 0 || f.removed != 0 {
		t.Fatal("invalid arguments changed service")
	}
}

func TestConfigOutputNeverPrintsCredential(t *testing.T) {
	var out bytes.Buffer
	writeConfig(&out, config{Port: 47831, Token: "synthetic-config-secret-marker", DataDir: "/test/data"})
	for _, forbidden := range []string{"synthetic-config-secret-marker", "TELEGRAM_MCP_TOKEN", "Authorization", "Bearer"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("config contains %s", forbidden)
		}
	}
	if !strings.Contains(out.String(), `"bridge"`) || !strings.Contains(out.String(), "TELEGRAM_MCP_DATA") {
		t.Fatalf("not a usable persisted-token bridge configuration: %s", &out)
	}
}
