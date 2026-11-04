package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

func authTestPNG(t *testing.T, size int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, size, size))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestQRAuthPageRenewsPrivately(t *testing.T) {
	p, err := newAuthQRPage()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	var generation atomic.Int32
	generation.Store(1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pair/qr.png" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer synthetic-bearer-marker" {
			t.Error("unsafe QR request")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(authTestPNG(t, int(generation.Load())))
	}))
	defer s.Close()
	client := daemonClient()
	defer client.CloseIdleConnections()
	if err := p.update(context.Background(), client, testServerConfig(t, s)); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p.path)
	generation.Store(2)
	if err := p.update(context.Background(), client, testServerConfig(t, s)); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p.path)
	if bytes.Equal(first, second) {
		t.Fatal("QR did not renew")
	}
	entries, err := os.ReadDir(filepath.Dir(p.path))
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary files leaked")
	}
	if err := service.CheckPrivateDataPath(p.path, false); err != nil {
		t.Fatal("replacement page is not private")
	}
}

func TestQRAuthPagePermissionsWithOpenUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	if os.Getenv("TELEGRAM_QR_UMASK_CHILD") == "1" {
		p, err := newAuthQRPage()
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(authTestPNG(t, 2))
		}))
		defer s.Close()
		c := daemonClient()
		defer c.CloseIdleConnections()
		if err := p.update(context.Background(), c, testServerConfig(t, s)); err != nil {
			t.Fatal(err)
		}
		for path, mode := range map[string]os.FileMode{p.path: 0600, filepath.Dir(p.path): 0700} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal("umask weakened QR artifact permissions")
			}
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Static shell program; the executable is a positional argument, not interpolated code.
	cmd := exec.Command("sh", "-c", `umask 000; exec "$1" -test.run '^TestQRAuthPagePermissionsWithOpenUmask$'`, "qr-test", exe)
	cmd.Env = append(os.Environ(), "TELEGRAM_QR_UMASK_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestQRAuthPageRejectsUntrustedResponses(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	for _, tc := range []struct {
		name   string
		status int
		mime   string
		body   []byte
	}{
		{"unauthorized", 401, "text/plain", []byte("synthetic-qr-secret")},
		{"redirect", 302, "image/png", authTestPNG(t, 2)},
		{"wrong-type", 200, "text/html", []byte("<script>synthetic-qr-secret</script>")},
		{"invalid-png", 200, "image/png", []byte("synthetic-qr-secret")},
		{"oversized", 200, "image/png", bytes.Repeat([]byte("x"), (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", destination.URL)
				w.Header().Set("Content-Type", tc.mime)
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer s.Close()
			p, err := newAuthQRPage()
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			c := daemonClient()
			defer c.CloseIdleConnections()
			err = p.update(context.Background(), c, testServerConfig(t, s))
			if err == nil || strings.Contains(err.Error(), "synthetic-qr-secret") {
				t.Fatalf("unsafe QR response: %v", err)
			}
			if _, err := os.Stat(p.path); !os.IsNotExist(err) {
				t.Fatal("unsafe page published")
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("QR redirect followed")
	}
}

func TestQRAuthCancelAndTwoFactorCleanArtifacts(t *testing.T) {
	for _, mode := range []string{"cancel", "2fa", "output-error", "daemon-error", "transition"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var cancelled, passwords atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-bearer-marker" {
					t.Error("missing bearer")
				}
				switch r.URL.Path {
				case "/api/pair":
					var request engine.LoginRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != engine.LoginQR || request.Phone != "" {
						t.Error("wrong QR login request")
					}
					fmt.Fprint(w, `{"state":"qr","method":"qr","qr_version":1}`)
				case "/api/pair/qr.png":
					if mode == "transition" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(authTestPNG(t, 2))
				case "/api/state":
					if mode == "daemon-error" {
						w.WriteHeader(500)
						fmt.Fprint(w, "synthetic-daemon-secret")
						return
					}
					fmt.Fprint(w, `{"pairing":{"state":"password","method":"qr"}}`)
				case "/api/pair/password":
					passwords.Add(1)
					fmt.Fprint(w, `{"state":"done","method":"qr"}`)
				case "/api/pair/cancel":
					cancelled.Add(1)
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer s.Close()
			var path string
			var output bytes.Buffer
			err := runAuth(ctx, testServerConfig(t, s), []string{"--qr"}, func(prompt string, secret bool) (string, error) {
				if (mode != "2fa" && mode != "transition") || !secret || !strings.Contains(prompt, "2FA") {
					t.Error("unexpected/unprotected prompt")
				}
				if mode != "transition" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Error("QR left on disk during 2FA")
					}
				}
				return "synthetic-password-marker", nil
			}, authOutputFunc(func(p []byte) (int, error) {
				output.Write(p)
				if strings.HasPrefix(string(p), "QR page (private; open locally): ") {
					path = strings.TrimSpace(strings.TrimPrefix(string(p), "QR page (private; open locally): "))
					if mode == "cancel" {
						cancel()
					}
					if mode == "output-error" {
						return 0, errors.New("synthetic-writer-secret")
					}
				}
				return len(p), nil
			}))
			if (err == nil) != (mode == "2fa" || mode == "transition") {
				t.Fatalf("unexpected result %v", err)
			}
			if mode != "transition" {
				if path == "" {
					t.Fatal("no page reported")
				}
				if _, statErr := os.Stat(filepath.Dir(path)); !os.IsNotExist(statErr) {
					t.Error("QR directory leaked")
				}
			} else if path != "" {
				t.Error("nonexistent QR announced")
			}
			if mode == "2fa" || mode == "transition" {
				if passwords.Load() != 1 || cancelled.Load() != 0 {
					t.Fatal("bad completion")
				}
			} else if cancelled.Load() != 1 {
				t.Fatal("failed login not cancelled")
			}
			if strings.Contains(output.String()+fmt.Sprint(err), "synthetic-password-marker") || strings.Contains(fmt.Sprint(err), "synthetic-") {
				t.Fatal("error/output leaked credential")
			}
		})
	}
}

func TestAuthFlagsRejectCredentialValues(t *testing.T) {
	for _, args := range [][]string{{"--phone", "+5511912345678"}, {"--phone=+5511912345678"}, {"--qr", "--phone"}, {"--qr=true"}, {"--code", "synthetic-code-marker"}} {
		err := runAuth(context.Background(), config{}, args, func(string, bool) (string, error) { t.Error("invalid args prompted"); return "", io.EOF }, io.Discard)
		if err == nil || strings.Contains(err.Error(), "+5511") || strings.Contains(err.Error(), "synthetic-code-marker") {
			t.Fatalf("unsafe argument result: %v", err)
		}
	}
}
