package cli

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuthFailureReportsDaemonReasonWithoutCancellingTerminalState(t *testing.T) {
	var cancels atomic.Int32
	reason := "login failed after authorization; remote logout confirmed; stage=bind_self reason=account_mismatch"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pair":
			json.NewEncoder(w).Encode(engine.Login{Method: engine.LoginQR, State: engine.LoginFailed, Error: reason})
		case "/api/pair/cancel":
			cancels.Add(1)
			json.NewEncoder(w).Encode(struct{}{})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	err = runAuth(context.Background(), config{Port: port, Token: "synthetic"}, []string{"--qr"}, func(string, bool) (string, error) { t.Fatal("unexpected prompt"); return "", io.EOF }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("daemon reason hidden: %v", err)
	}
	if cancels.Load() != 0 {
		t.Fatal("terminal failure was overwritten with a cancellation")
	}
}
