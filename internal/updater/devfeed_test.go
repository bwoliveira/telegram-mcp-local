package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// countingFeed wraps fakeGitHub and counts every request it receives.
func countingFeed(t *testing.T, tag, name string, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	sum := sha256.Sum256(body)
	inner := fakeGitHub(t, tag, name, body, hex.EncodeToString(sum[:]))
	t.Cleanup(inner.Close)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, inner.URL+r.URL.Path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(resp.Body)
		// The release JSON names the inner server; point it back here.
		_, _ = w.Write(bytes.ReplaceAll(out.Bytes(), []byte(inner.URL), []byte("http://"+r.Host)))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The production path: no override, nothing changes and nothing is logged.
func TestDevFeedAbsentChangesNothing(t *testing.T) {
	logger, logs := captureLogger()
	u := &Updater{Repo: "o/r", Version: "1.2.0", Logger: logger}
	if err := u.ConfigureDevFeed(envMap(map[string]string{DevModeEnv: "1"})); err != nil {
		t.Fatal(err)
	}
	if u.API != "" || u.PublicKey != "" || u.devFeed || u.refused != nil {
		t.Fatalf("no override must leave the updater on the production feed: %+v", u)
	}
	if logs.Len() != 0 {
		t.Fatalf("unexpected log: %s", logs)
	}
}

// Without TELEGRAM_MCP_DEV=1 exactly, an override is refused loudly, every
// check reports the refusal, and the feed is never contacted.
func TestDevFeedRefusedWithoutDevelopmentMode(t *testing.T) {
	target := Target{Kind: "appimage", Path: filepath.Join(t.TempDir(), "Telegram-MCP.AppImage")}
	srv, hits := countingFeed(t, "v1.3.0", target.assetName(), []byte("the new app"))
	for _, dev := range []string{"", "0", "true", "yes", " 1", "1 ", "01"} {
		for _, env := range []map[string]string{
			{UpdatesAPIEnv: srv.URL, UpdatesPublicKeyEnv: testPublicKey()},
			{UpdatesAPIEnv: srv.URL},
			{UpdatesPublicKeyEnv: testPublicKey()},
		} {
			env[DevModeEnv] = dev
			logger, logs := captureLogger()
			u := &Updater{Repo: "o/r", Version: "1.2.0", Logger: logger, Dir: t.TempDir(), Target: target}
			if err := u.ConfigureDevFeed(envMap(env)); !errors.Is(err, ErrDevFeedRefused) {
				t.Fatalf("dev=%q env=%v: err = %v, want refusal", dev, env, err)
			}
			if u.API != "" || u.PublicKey != "" {
				t.Fatalf("dev=%q: a refused override was applied: API=%q key=%q", dev, u.API, u.PublicKey)
			}
			for _, want := range []string{"WARNING", DevModeEnv + "=1", "refused"} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("dev=%q: refusal log lacks %q: %s", dev, want, logs)
				}
			}
			if strings.Contains(logs.String(), "DEVELOPMENT MODE") {
				t.Fatalf("dev=%q: a refused override must not claim development mode: %s", dev, logs)
			}
			found, err := u.Check(context.Background())
			if found || !errors.Is(err, ErrDevFeedRefused) {
				t.Fatalf("dev=%q: check = %v, %v; want the refusal", dev, found, err)
			}
			if st := u.State(); st.Phase != Failed || !strings.Contains(st.Error, DevModeEnv) {
				t.Fatalf("dev=%q: state %+v must show the refusal", dev, st)
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("a refused test feed was contacted %d times", n)
	}
}

// A binary stamped with the production key (every release build) refuses a
// test feed even in development mode.
func TestDevFeedRefusedInReleaseBuild(t *testing.T) {
	old := ReleasePublicKey
	ReleasePublicKey = testPublicKey()
	t.Cleanup(func() { ReleasePublicKey = old })
	logger, logs := captureLogger()
	u := &Updater{Repo: "o/r", Version: "1.2.0", Logger: logger, Target: Target{Kind: "appimage", Path: "/x"}}
	err := u.ConfigureDevFeed(envMap(map[string]string{DevModeEnv: "1", UpdatesAPIEnv: "http://127.0.0.1:1", UpdatesPublicKeyEnv: testPublicKey()}))
	if !errors.Is(err, ErrDevFeedReleaseBuild) {
		t.Fatalf("err = %v, want release-build refusal", err)
	}
	if u.API != "" || u.PublicKey != "" || u.devFeed {
		t.Fatal("a release build applied a test feed")
	}
	if !strings.Contains(logs.String(), "WARNING") || strings.Contains(logs.String(), "DEVELOPMENT MODE") {
		t.Fatalf("log: %s", logs)
	}
	if _, err := u.Check(context.Background()); !errors.Is(err, ErrDevFeedReleaseBuild) {
		t.Fatalf("check err = %v", err)
	}
}

// In development mode the real app's default HTTP client reaches a loopback
// feed, signed with a disposable key, and every integrity rule still holds.
func TestDevFeedServesTheRealClient(t *testing.T) {
	body := []byte("the new app")
	target := Target{Kind: "appimage", Path: filepath.Join(t.TempDir(), "Telegram-MCP.AppImage")}
	srv, hits := countingFeed(t, "v1.3.0", target.assetName(), body)
	logger, logs := captureLogger()
	u := &Updater{Repo: "o/r", Version: "1.2.0", Logger: logger, Dir: t.TempDir(), Target: target}
	if err := u.ConfigureDevFeed(envMap(map[string]string{DevModeEnv: "1", UpdatesAPIEnv: srv.URL, UpdatesPublicKeyEnv: testPublicKey()})); err != nil {
		t.Fatal(err)
	}
	if u.Client != nil {
		t.Fatal("the test must exercise the default client, as the app does")
	}
	for _, want := range []string{"WARNING: DEVELOPMENT MODE", UpdatesAPIEnv, srv.URL, "NOT the release key", "Never use this in production"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("development-mode log lacks %q: %s", want, logs)
		}
	}
	found, err := u.Check(context.Background())
	if err != nil || !found {
		t.Fatalf("check: %v %v", found, err)
	}
	path, err := u.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, body) {
		t.Fatal("downloaded the wrong bytes")
	}
	if hits.Load() == 0 {
		t.Fatal("the test feed was not used")
	}
	if high, _ := os.ReadFile(filepath.Join(u.Dir, highWaterName)); strings.TrimSpace(string(high)) != "1.3.0" {
		t.Fatalf("high-water mark = %q", high)
	}
}

// The test key replaces the trust anchor, not the verification: a feed signed
// with another key is still refused.
func TestDevFeedStillRequiresTheConfiguredKey(t *testing.T) {
	target := Target{Kind: "appimage", Path: filepath.Join(t.TempDir(), "Telegram-MCP.AppImage")}
	srv, _ := countingFeed(t, "v1.3.0", target.assetName(), []byte("the new app"))
	seed := sha256.Sum256([]byte("another disposable key"))
	other := ed25519.NewKeyFromSeed(seed[:])
	raw := append([]byte("Ed"), []byte("otherkey")...)
	raw = append(raw, other.Public().(ed25519.PublicKey)...)
	otherKey := base64.StdEncoding.EncodeToString(raw)
	u := &Updater{Repo: "o/r", Version: "1.2.0", Dir: t.TempDir(), Target: target}
	if err := u.ConfigureDevFeed(envMap(map[string]string{DevModeEnv: "1", UpdatesAPIEnv: srv.URL, UpdatesPublicKeyEnv: otherKey})); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background()); err == nil || !strings.Contains(err.Error(), "different key") {
		t.Fatalf("a manifest signed by another key must be refused, got %v", err)
	}
}

// The high-water mark still refuses a signed replay below the highest version
// ever authenticated.
func TestDevFeedKeepsTheHighWaterMark(t *testing.T) {
	target := Target{Kind: "appimage", Path: filepath.Join(t.TempDir(), "Telegram-MCP.AppImage")}
	srv, _ := countingFeed(t, "v1.3.0", target.assetName(), []byte("the new app"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, highWaterName), []byte("1.4.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &Updater{Repo: "o/r", Version: "1.2.0", Dir: dir, Target: target}
	if err := u.ConfigureDevFeed(envMap(map[string]string{DevModeEnv: "1", UpdatesAPIEnv: srv.URL, UpdatesPublicKeyEnv: testPublicKey()})); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background()); err == nil || !strings.Contains(err.Error(), "after authenticated version 1.4.0") {
		t.Fatalf("a replay below the high-water mark must be refused, got %v", err)
	}
}

func TestDevFeedRefusesInvalidOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"key without feed", map[string]string{UpdatesPublicKeyEnv: testPublicKey()}},
		{"malformed key", map[string]string{UpdatesAPIEnv: "http://127.0.0.1:1", UpdatesPublicKeyEnv: "not a key"}},
		{"public host", map[string]string{UpdatesAPIEnv: "http://evil.example"}},
		{"private LAN", map[string]string{UpdatesAPIEnv: "http://192.168.1.10:8080"}},
		{"outside the tailnet range", map[string]string{UpdatesAPIEnv: "http://100.128.0.1:8080"}},
		{"tailnet host name", map[string]string{UpdatesAPIEnv: "http://feed.tailnet.ts.net:8080"}},
		{"credentials", map[string]string{UpdatesAPIEnv: "http://user:pass@127.0.0.1:1"}},
		{"query", map[string]string{UpdatesAPIEnv: "http://127.0.0.1:1/?x=1"}},
		{"file scheme", map[string]string{UpdatesAPIEnv: "file:///tmp/feed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.env[DevModeEnv] = "1"
			u := &Updater{Repo: "o/r", Version: "1.2.0", Target: Target{Kind: "appimage", Path: "/x"}}
			if err := u.ConfigureDevFeed(envMap(tc.env)); err == nil {
				t.Fatal("override accepted")
			}
			if u.API != "" || u.PublicKey != "" || u.devFeed {
				t.Fatal("a refused override was applied")
			}
			if _, err := u.Check(context.Background()); err == nil {
				t.Fatal("check must keep reporting the refusal")
			}
		})
	}
}

// Assets named by a test feed must also stay on loopback or the tailnet.
func TestDevFeedRefusesAssetsElsewhere(t *testing.T) {
	target := Target{Kind: "appimage", Path: filepath.Join(t.TempDir(), "Telegram-MCP.AppImage")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v1.3.0","assets":[
			{"name":%q,"browser_download_url":"http://192.0.2.1/file","size":3},
			{"name":"checksums.txt","browser_download_url":"http://192.0.2.1/sums"},
			{"name":"checksums.txt.minisig","browser_download_url":"http://192.0.2.1/sums.minisig"}]}`, target.assetName())
	}))
	defer srv.Close()
	u := &Updater{Repo: "o/r", Version: "1.2.0", Dir: t.TempDir(), Target: target}
	if err := u.ConfigureDevFeed(envMap(map[string]string{DevModeEnv: "1", UpdatesAPIEnv: srv.URL, UpdatesPublicKeyEnv: testPublicKey()})); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background()); err == nil {
		t.Fatal("an asset outside loopback and the tailnet was fetched")
	}
}

func TestDevFeedURLPolicy(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1:18765":            true,
		"https://127.0.0.1:18765":           true,
		"http://localhost:18765/feed":       true,
		"http://[::1]:18765":                true,
		"http://100.64.0.1:18765":           true,
		"http://100.96.123.45:18765/x":      true,
		"http://100.127.255.254":            true,
		"http://100.63.255.255":             false,
		"http://100.128.0.0":                false,
		"http://10.0.0.1":                   false,
		"http://api.github.com":             false,
		"ftp://127.0.0.1":                   false,
		"http://u@127.0.0.1":                false,
		"http://127.0.0.1/#x":               false,
		"http://[fd7a:115c:a1e0::1]:18765/": false,
	} {
		if got := devFeedURLAllowed(raw); got != want {
			t.Errorf("devFeedURLAllowed(%q) = %v, want %v", raw, got, want)
		}
	}
}

// The app logs through a redacting handler that drops dynamic messages; the
// development-mode warnings must survive it word for word.
func TestDevFeedWarningsSurviveTheAppLogRedaction(t *testing.T) {
	for _, message := range []string{LogDevFeedTestKey, LogDevFeedReleaseKey, LogDevFeedRefused, LogDevFeedReleaseBuild, LogDevFeedInvalid} {
		var buf bytes.Buffer
		logger := slog.New(httpserver.Redact(slog.NewTextHandler(&buf, nil)))
		logger.Warn(message, "feed", "http://127.0.0.1:1", "error", "detail")
		if !strings.Contains(buf.String(), message) || !strings.Contains(message, "WARNING") {
			t.Fatalf("redacted app log lost the warning %q: %s", message, buf.String())
		}
	}
	var buf bytes.Buffer
	u := &Updater{Repo: "o/r", Version: "1.2.0", Logger: slog.New(httpserver.Redact(slog.NewTextHandler(&buf, nil)))}
	_ = u.ConfigureDevFeed(envMap(map[string]string{DevModeEnv: "1", UpdatesAPIEnv: "http://127.0.0.1:1", UpdatesPublicKeyEnv: testPublicKey()}))
	if !strings.Contains(buf.String(), "WARNING: DEVELOPMENT MODE") {
		t.Fatalf("the app log does not show development mode: %s", buf.String())
	}
}
