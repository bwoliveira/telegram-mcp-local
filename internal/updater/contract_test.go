package updater

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReleaseAPIIsConfigurableAndUnauthenticated(t *testing.T) {
	for _, tc := range []struct{ api, want string }{
		{"", "https://api.github.com/repos/bwoliveira/telegram-mcp-local/releases/latest"},
		{"http://127.0.0.1:12345/api/", "http://127.0.0.1:12345/api/repos/bwoliveira/telegram-mcp-local/releases/latest"},
	} {
		t.Run(tc.api, func(t *testing.T) {
			requests := 0
			u := &Updater{Repo: "bwoliveira/telegram-mcp-local", Version: "1.0.0", API: tc.api, Target: Target{Kind: "setup"},
				Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					requests++
					if r.URL.String() != tc.want || r.Method != http.MethodGet {
						t.Errorf("release request: %s %s, want GET %s", r.Method, r.URL, tc.want)
					}
					if r.Header.Get("Authorization") != "" || r.URL.User != nil {
						t.Error("updater must not embed credentials in the release request")
					}
					if r.Header.Get("User-Agent") != "telegram-mcp/1.0.0" || r.Header.Get("Accept") != "application/vnd.github+json" {
						t.Errorf("unexpected release API headers: %v", r.Header)
					}
					return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
				})}}
			if found, err := u.Check(context.Background()); err != nil || found || u.State().Phase != Current {
				t.Fatalf("empty private/test release: found=%v err=%v state=%+v", found, err, u.State())
			}
			if requests != 1 {
				t.Fatalf("check made %d requests, want metadata only", requests)
			}
		})
	}
}

func TestReleaseAssetNamesRemainCompatible(t *testing.T) {
	arch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}
	for kind, want := range map[string]string{
		"dmg": "Telegram-MCP.dmg", "setup": "Telegram-MCP-Setup.exe",
		"appimage": "Telegram-MCP-" + arch + ".AppImage", "deb": "",
	} {
		if got := (Target{Kind: kind}).assetName(); got != want {
			t.Errorf("%s asset = %q, want %q", kind, got, want)
		}
	}
}
