//go:build linux

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestReleaseServerReplacesAndRestartsExecutable(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "Telegram MCP fixture.AppImage")
	candidate := filepath.Join(dir, "v2")
	for _, build := range []struct{ version, path string }{{"1.0.0", installed}, {"2.0.0", candidate}} {
		cmd := exec.Command("go", "build", "-o", build.path, "-ldflags=-X main.version="+build.version, "./testdata/updatefixture")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v: %s", build.version, err, out)
		}
	}
	v1, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	assetName := (Target{Kind: "appimage"}).assetName()
	v1Manifest := testManifest("1.0.0", assetName, v1)
	v2Manifest := testManifest("2.0.0", assetName, v2)
	v1Signature := signTestManifest(v1Manifest)
	v2Signature := signTestManifest(v2Manifest)
	var downloads atomic.Int32
	var publishedV2 atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("release fixture must not receive authentication")
		}
		switch r.URL.Path {
		case "/repos/bwoliveira/telegram-mcp-local/releases/latest":
			version, body := "v1.0.0", v1
			if publishedV2.Load() {
				version, body = "v2.0.0", v2
			}
			_ = json.NewEncoder(w).Encode(release{Tag: version, Page: srv.URL + "/release/" + version, Assets: []asset{
				{Name: assetName, URL: srv.URL + "/" + version + "/file", Size: int64(len(body))},
				{Name: "checksums.txt", URL: srv.URL + "/" + version + "/checksums.txt"},
				{Name: "checksums.txt.minisig", URL: srv.URL + "/" + version + "/checksums.txt.minisig"},
			}})
		case "/v1.0.0/checksums.txt":
			downloads.Add(1)
			_, _ = w.Write(v1Manifest)
		case "/v2.0.0/checksums.txt":
			downloads.Add(1)
			_, _ = w.Write(v2Manifest)
		case "/v1.0.0/checksums.txt.minisig":
			downloads.Add(1)
			_, _ = w.Write(v1Signature)
		case "/v2.0.0/checksums.txt.minisig":
			downloads.Add(1)
			_, _ = w.Write(v2Signature)
		case "/v1.0.0/file":
			downloads.Add(1)
			_, _ = w.Write(v1)
		case "/v2.0.0/file":
			downloads.Add(1)
			_, _ = w.Write(v2)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Start()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, installed)
	cmd.Env = append(os.Environ(), "UPDATER_FIXTURE_DIR="+dir, "TELEGRAM_MCP_UPDATES_API="+srv.URL, "UPDATER_FIXTURE_PUBLIC_KEY="+testPublicKey())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !waited {
			_ = cmd.Process.Kill() // Only the fixture PID belongs to this test.
			_ = cmd.Wait()
		}
	})
	dec := json.NewDecoder(stdout)
	var state State
	if err := dec.Decode(&state); err != nil {
		t.Fatalf("v1 check: %v", err)
	}
	if state.Phase != Current || state.Current != "1.0.0" || downloads.Load() != 0 {
		t.Fatalf("server v1 must leave the installed v1 current: %+v", state)
	}
	publishedV2.Store(true)
	if _, err := fmt.Fprintln(stdin, "check"); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&state); err != nil {
		t.Fatalf("v2 check: %v", err)
	}
	if state.Phase != Available || state.Current != "1.0.0" || state.Latest != "2.0.0" {
		t.Fatalf("v2 notice: %+v", state)
	}
	if got, err := os.ReadFile(installed); err != nil || !bytes.Equal(got, v1) || downloads.Load() != 0 {
		t.Fatalf("check downloaded or replaced v1 without consent: downloads=%d err=%v", downloads.Load(), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "restarted")); !os.IsNotExist(err) {
		t.Fatalf("v2 started before Apply: %v", err)
	}
	if _, err := fmt.Fprintln(stdin, "apply"); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&state); err != nil || state.Phase != Ready {
		t.Fatalf("apply: state=%+v err=%v", state, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("v1 exit: %v: %s", err, stderr.String())
	}
	waited = true
	deadline := time.Now().Add(10 * time.Second)
	for {
		marker, err := os.ReadFile(filepath.Join(dir, "restarted"))
		if err == nil {
			if string(marker) != "2.0.0" {
				t.Fatalf("restarted marker = %q", marker)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("real relaunch helper did not execute v2: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, err := os.ReadFile(installed); err != nil || !bytes.Equal(got, v2) {
		t.Fatalf("installed executable is not the downloaded v2: %v", err)
	}
	if downloads.Load() != 3 {
		t.Fatalf("download count = %d, want signed manifest, signature and executable", downloads.Load())
	}
	t.Log("v1.0.0 notice only -> explicit Apply -> verified minisign manifest and SHA-256 -> replaced executable -> v1 exit -> real helper restarted v2.0.0")
}
