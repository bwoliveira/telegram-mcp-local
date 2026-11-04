package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

func TestPanelBootstrapSecretCreatedPrivatelyAndRotates(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	first, err := EnsurePanelBootstrapSecret(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := EnsurePanelBootstrapSecret(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != again || first == testToken {
		t.Fatalf("unstable or shared bootstrap authority: first=%t stable=%t", first != "", first == again)
	}
	if runtime.GOOS != "windows" {
		accountInfo, err := os.Stat(filepath.Dir(PanelBootstrapSecretPath(dataDir)))
		if err != nil || accountInfo.Mode().Perm() != 0o700 {
			t.Fatalf("account directory mode: info=%v err=%v", accountInfo, err)
		}
		secretInfo, err := os.Stat(PanelBootstrapSecretPath(dataDir))
		if err != nil || secretInfo.Mode().Perm() != 0o600 {
			t.Fatalf("bootstrap file mode: info=%v err=%v", secretInfo, err)
		}
	}
	rotated, err := RotatePanelBootstrapSecret(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if rotated == first {
		t.Fatal("rotation retained the old bootstrap authority")
	}
	read, err := ReadPanelBootstrapSecret(dataDir)
	if err != nil || read != rotated {
		t.Fatalf("rotated authority readback: equal=%t err=%v", read == rotated, err)
	}
}

func TestOldPanelBootstrapSecretIsRejectedAfterRotation(t *testing.T) {
	dataDir := t.TempDir()
	old, err := EnsurePanelBootstrapSecret(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := RotatePanelBootstrapSecret(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(mcp.New(mcp.Config{}), Options{Token: testToken, PanelBootstrapSecret: rotated})
	for _, tc := range []struct {
		name, header string
		want         int
	}{
		{name: "MCP bearer", want: http.StatusForbidden},
		{name: "old bootstrap authority", header: old, want: http.StatusForbidden},
		{name: "rotated bootstrap authority", header: rotated, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(http.MethodPost, "/api/session/bootstrap")
			if tc.name == "MCP bearer" {
				r.Header.Set("Authorization", "Bearer "+testToken)
			} else {
				r.Header.Set(PanelBootstrapHeader, tc.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), old) || strings.Contains(w.Body.String(), rotated) {
				t.Fatal("HTTP response exposed a panel bootstrap authority")
			}
			if tc.want != http.StatusOK && w.Header().Get("Location") != "" {
				t.Fatal("refused authority received a panel capability")
			}
		})
	}
}
