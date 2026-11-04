package updater

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestApplyRefusesInvalidChecksumsWithoutReplacing(t *testing.T) {
	old := relaunch
	relaunch = func(string, map[string]string) error { return nil }
	t.Cleanup(func() { relaunch = old })
	body := []byte("untrusted replacement")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	name := (Target{Kind: "appimage"}).assetName()
	for _, tc := range []struct {
		name, manifest string
		status         int
	}{
		{"mismatch", strings.Repeat("0", 64) + "  " + name, http.StatusOK},
		{"missing entry", sum + "  other.AppImage", http.StatusOK},
		{"missing manifest", "", http.StatusOK},
		{"oversized manifest", sum + "  " + name + "\n" + strings.Repeat("\n", 1<<20), http.StatusOK},
		{"scanner failure", sum + "  " + name + "\n" + strings.Repeat("x", 65<<10), http.StatusOK},
		{"malformed digest", strings.Repeat("z", 64) + "  " + name, http.StatusOK},
		{"duplicate entry", sum + "  " + name + "\n" + strings.Repeat("0", 64) + "  " + name, http.StatusOK},
		{"checksum server failure", sum + "  " + name, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := Target{Kind: "appimage", Path: filepath.Join(dir, "installed")}
			if err := os.WriteFile(target.Path, []byte("original v1"), 0o755); err != nil {
				t.Fatal(err)
			}
			var payloads atomic.Int32
			manifest := []byte("# version: 2.0.0\n# source_commit: " + strings.Repeat("a", 40) + "\n# date: 2026-11-03T12:00:00Z\n" + tc.manifest + "\n")
			signature := signTestManifest(manifest)
			var srv *httptest.Server
			srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/releases/latest":
					assets := []asset{{Name: name, URL: srv.URL + "/file"}}
					if tc.name != "missing manifest" {
						assets = append(assets,
							asset{Name: "checksums.txt", URL: srv.URL + "/sums"},
							asset{Name: "checksums.txt.minisig", URL: srv.URL + "/sums.minisig"})
					}
					_ = json.NewEncoder(w).Encode(release{Tag: "v2.0.0", Assets: assets})
				case "/file":
					payloads.Add(1)
					_, _ = w.Write(body)
				case "/sums":
					w.WriteHeader(tc.status)
					_, _ = w.Write(manifest)
				case "/sums.minisig":
					_, _ = w.Write(signature)
				default:
					http.NotFound(w, r)
				}
			}))
			srv.Start()
			defer srv.Close()
			u := &Updater{Repo: "o/r", Version: "1.0.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: filepath.Join(dir, "downloads"), Target: target}
			if found, err := u.Check(context.Background()); err != nil || !found {
				t.Fatalf("check: found=%v err=%v", found, err)
			}
			if err := u.Apply(context.Background()); err == nil {
				t.Fatal("Apply accepted invalid checksums")
			}
			if got, err := os.ReadFile(target.Path); err != nil || string(got) != "original v1" {
				t.Fatalf("invalid checksum replaced v1: %q, %v", got, err)
			}
			if u.State().Phase != Failed {
				t.Fatalf("state = %+v", u.State())
			}
			if entries, err := os.ReadDir(u.Dir); err == nil {
				for _, entry := range entries {
					if entry.Name() != highWaterName {
						t.Fatalf("rejected artifact remains: %s", entry.Name())
					}
				}
			}
			wantPayloads := int32(0)
			if tc.name == "mismatch" {
				wantPayloads = 1
			}
			if payloads.Load() != wantPayloads {
				t.Fatalf("payload requests = %d, want %d", payloads.Load(), wantPayloads)
			}
		})
	}
}

func TestInstallRechecksStagedChecksum(t *testing.T) {
	old := relaunch
	relaunch = func(string, map[string]string) error { return nil }
	t.Cleanup(func() { relaunch = old })
	dir := t.TempDir()
	target := Target{Kind: "appimage", Path: filepath.Join(dir, "installed")}
	if err := os.WriteFile(target.Path, []byte("original v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("verified v2")
	srv := fakeGitHub(t, "v2.0.0", target.assetName(), body, fmt.Sprintf("%x", sha256.Sum256(body)))
	defer srv.Close()
	u := &Updater{Repo: "o/r", Version: "1.0.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: filepath.Join(dir, "downloads"), Target: target}
	if found, err := u.Check(context.Background()); err != nil || !found {
		t.Fatalf("check: found=%v err=%v", found, err)
	}
	file, err := u.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed after download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := u.Install(context.Background()); err == nil {
		t.Fatal("Install accepted a changed staged file")
	}
	if got, err := os.ReadFile(target.Path); err != nil || string(got) != "original v1" {
		t.Fatalf("changed staging file replaced v1: %q, %v", got, err)
	}
	if u.State().Phase != Failed {
		t.Fatalf("state = %+v", u.State())
	}
}

func TestCheckInvalidatesStaleCandidate(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			body := []byte("v2 candidate")
			target := Target{Kind: "appimage", Path: filepath.Join(t.TempDir(), "installed")}
			name := target.assetName()
			manifest := testManifest("2.0.0", name, body)
			signature := signTestManifest(manifest)
			var next atomic.Bool
			var srv *httptest.Server
			srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/releases/latest":
					if next.Load() {
						w.WriteHeader(status)
						_, _ = fmt.Fprint(w, `{"tag_name":"v1.0.0"}`)
						return
					}
					fmt.Fprintf(w, `{"tag_name":"v2.0.0","assets":[{"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q},{"name":"checksums.txt.minisig","browser_download_url":%q}]}`, name, srv.URL+"/file", srv.URL+"/sums", srv.URL+"/sums.minisig")
				case "/file":
					_, _ = w.Write(body)
				case "/sums":
					_, _ = w.Write(manifest)
				case "/sums.minisig":
					_, _ = w.Write(signature)
				}
			}))
			srv.Start()
			defer srv.Close()
			u := &Updater{Repo: "o/r", Version: "1.0.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: t.TempDir(), Target: target}
			if found, err := u.Check(context.Background()); err != nil || !found {
				t.Fatalf("check: found=%v err=%v", found, err)
			}
			if _, err := u.Download(context.Background()); err != nil {
				t.Fatal(err)
			}
			next.Store(true)
			_, _ = u.Check(context.Background())
			if _, err := u.Download(context.Background()); err == nil {
				t.Fatal("Download reused the candidate after release withdrawal or failed check")
			}
		})
	}
}

func TestCheckCannotInterruptApply(t *testing.T) {
	entered, releaseDownload := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sums" {
			close(entered)
			<-releaseDownload
		}
		http.Error(w, "fixture failure", http.StatusInternalServerError)
	}))
	defer srv.Close()
	u := &Updater{Repo: "o/r", Version: "1.0.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: t.TempDir(), Target: Target{Kind: "appimage"},
		latest: &release{Assets: []asset{{Name: "checksums.txt", URL: srv.URL + "/sums"}, {Name: "checksums.txt.minisig", URL: srv.URL + "/sums.minisig"}}}}
	done := make(chan error, 1)
	go func() { done <- u.Apply(context.Background()) }()
	<-entered
	// A check must not invalidate the candidate while Apply is using it.
	_, err := u.Check(context.Background())
	close(releaseDownload)
	<-done
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Check during Apply = %v, want ErrBusy", err)
	}
}
