package sidecar

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func testArchive(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"whisper-cli", "ffmpeg"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		data := []byte("test " + name)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func writeTestManifest(t *testing.T, version, url string, body []byte) string {
	t.Helper()
	sum := sha256.Sum256(body)
	metadata := ReleaseFile{URL: url + ".metadata", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	m := manifest{Version: version, Packages: map[string]Package{key(): {
		URL: url, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)), Accel: "cpu",
		SBOM: metadata, Notices: metadata,
	}}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEmbeddedManifestIncludesAllReleasePlatforms(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_SIDECARS", "")
	m, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"darwin/universal":   "metal",
		"linux/amd64":        "cpu",
		"linux/arm64":        "cpu",
		"windows/amd64":      "cpu",
		"windows/amd64/cuda": "cuda",
	}
	if len(m.Packages) != len(want) {
		t.Fatalf("embedded release has %d packages, want %d", len(m.Packages), len(want))
	}
	for platform, accel := range want {
		if pkg, ok := m.Packages[platform]; !ok || pkg.Accel != accel {
			t.Errorf("missing or incorrect package for %s: %+v", platform, pkg)
		}
	}
}

func TestManifestRejectsPreviousVersionOnEveryPlatform(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_DEV", "1")
	t.Setenv("TELEGRAM_MCP_SIDECARS", writeTestManifest(t, "sidecars-1", "https://example.invalid/sidecar.tar.gz", testArchive(t)))
	if _, err := loadManifest(); err == nil || !strings.Contains(err.Error(), "unsupported sidecar manifest") {
		t.Fatalf("legacy sidecars-1 manifest: got %v, want unsupported version", err)
	}
}

func TestEmbeddedManifestAcceptsReleaseBytes(t *testing.T) {
	archivePath := os.Getenv("TELEGRAM_MCP_SIDECAR_FIXTURE")
	if archivePath == "" {
		t.Skip("set TELEGRAM_MCP_SIDECAR_FIXTURE to a reviewed archive for the release-byte integration")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("release-byte fixture is exercised on Linux and macOS")
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_MCP_SIDECARS", "")
	m, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := m.Packages[key()]
	if !ok {
		t.Fatalf("embedded manifest has no package for %s", key())
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != pkg.SHA256 || int64(len(archive)) != pkg.Size {
		t.Fatalf("fixture does not match embedded manifest: %s/%d, want %s/%d", got, len(archive), pkg.SHA256, pkg.Size)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	pkg.URL = server.URL + "/" + filepath.Base(archivePath)
	if err := Install(context.Background(), t.TempDir(), pkg, nil); err != nil {
		t.Fatalf("exact embedded release bytes were refused: %v", err)
	}
	pkg.SHA256 = "00" + pkg.SHA256[2:]
	if err := Install(context.Background(), t.TempDir(), pkg, nil); err == nil {
		t.Fatal("release archive was accepted under a nonmatching manifest digest")
	}
}

func TestManifestRejectsOldReleaseAndInstallsExactBytes(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("test archive is not code signed")
	}
	archive := testArchive(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	t.Setenv("TELEGRAM_MCP_DEV", "1")
	t.Setenv("TELEGRAM_MCP_SIDECARS", writeTestManifest(t, "sidecars-1", server.URL+"/sidecar.tar.gz", archive))
	if _, err := loadManifest(); err == nil {
		t.Fatal("legacy sidecars-1 manifest was accepted")
	}

	t.Setenv("TELEGRAM_MCP_SIDECARS", writeTestManifest(t, manifestVersion, server.URL+"/sidecar.tar.gz", archive))
	m, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	pkg := m.Packages[key()]
	if err := Install(context.Background(), t.TempDir(), pkg, nil); err != nil {
		t.Fatalf("exact pinned archive was refused: %v", err)
	}

	pkg.SHA256 = "00" + pkg.SHA256[2:]
	if err := Install(context.Background(), t.TempDir(), pkg, nil); err == nil {
		t.Fatal("archive whose bytes do not match the manifest was accepted")
	}
}

// captureLog redirects the standard logger for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})
	return &buf
}

func TestManifestOverrideRefusedWithoutDevMode(t *testing.T) {
	path := writeTestManifest(t, manifestVersion, "https://example.invalid/sidecar.tar.gz", []byte("archive"))
	t.Setenv("TELEGRAM_MCP_SIDECARS", path)
	for _, dev := range []string{"", "0", "true", "yes", " 1"} {
		t.Run("dev="+dev, func(t *testing.T) {
			t.Setenv("TELEGRAM_MCP_DEV", dev)
			logs := captureLog(t)
			if _, err := loadManifest(); !errors.Is(err, errOverrideRefused) {
				t.Fatalf("custom manifest with TELEGRAM_MCP_DEV=%q: err = %v, want refusal", dev, err)
			}
			if _, ok := ForThisSystem(); ok {
				t.Fatal("ForThisSystem returned a package from a refused custom manifest")
			}
			if !strings.Contains(logs.String(), "WARNING") || !strings.Contains(logs.String(), "TELEGRAM_MCP_DEV=1") {
				t.Fatalf("refusal was not logged loudly: %q", logs.String())
			}
			if strings.Contains(logs.String(), "DEVELOPMENT MODE") {
				t.Fatalf("refused override logged as accepted: %q", logs.String())
			}
		})
	}
}

func TestManifestOverrideAcceptedInDevModeWithWarning(t *testing.T) {
	const url = "https://example.invalid/dev-sidecar.tar.gz"
	path := writeTestManifest(t, manifestVersion, url, []byte("archive"))
	t.Setenv("TELEGRAM_MCP_SIDECARS", path)
	t.Setenv("TELEGRAM_MCP_DEV", "1")
	logs := captureLog(t)
	m, err := loadManifest()
	if err != nil {
		t.Fatalf("custom manifest refused in dev mode: %v", err)
	}
	if got := m.Packages[key()].URL; got != url {
		t.Fatalf("package URL = %q, want custom %q", got, url)
	}
	out := logs.String()
	for _, want := range []string{"WARNING", "DEVELOPMENT MODE", "TELEGRAM_MCP_SIDECARS", strconv.Quote(path), "NOT the reviewed release bytes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dev-mode warning missing %q: %q", want, out)
		}
	}
}

func TestEmbeddedManifestLoadsSilentlyWithoutOverride(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_SIDECARS", "")
	t.Setenv("TELEGRAM_MCP_DEV", "")
	logs := captureLog(t)
	if _, err := loadManifest(); err != nil {
		t.Fatalf("embedded manifest refused: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("embedded manifest logged a warning: %q", logs.String())
	}
}
