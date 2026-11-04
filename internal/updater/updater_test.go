package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		tag, current string
		want         bool
	}{
		{"v1.2.0", "1.1.9", true},
		{"v1.10.0", "1.9.0", true},
		{"v2.0.0", "1.99.99", true},
		{"v1.2.0", "1.2.0", false},
		{"v1.1.0", "1.2.0", false},
		{"v1.3.0-rc1", "1.2.0", false},
		{"v1.3.0", "dev", false},
		{"v1.3.0", "8ace907", false},
	} {
		if got := newer(c.tag, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.tag, c.current, got)
		}
	}
}

// fakeGitHub serves a release with one file and its signed manifest.
func fakeGitHub(t *testing.T, tag, name string, body []byte, sum string) *httptest.Server {
	t.Helper()
	manifest := testManifest(strings.TrimPrefix(tag, "v"), name, body)
	actual := fmt.Sprintf("%x", sha256.Sum256(body))
	if sum != actual && len(sum) == 64 {
		lines := strings.Split(string(manifest), "\n")
		lines[3] = sum + "  " + name
		manifest = []byte(strings.Join(lines, "\n"))
	}
	signature := signTestManifest(manifest)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example.test/r","assets":[
				{"name":%q,"browser_download_url":"%s/dl/file","size":%d},
				{"name":"checksums.txt","browser_download_url":"%s/dl/sums"},
				{"name":"checksums.txt.minisig","browser_download_url":"%s/dl/sums.minisig"}]}`, tag, name, srv.URL, len(body), srv.URL, srv.URL)
		case "/dl/file":
			_, _ = w.Write(body)
		case "/dl/sums":
			_, _ = w.Write(manifest)
		case "/dl/sums.minisig":
			_, _ = w.Write(signature)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func TestCheckAndDownloadVerifiesTheChecksum(t *testing.T) {
	body := []byte("the new app")
	h := sha256.Sum256(body)
	target := Target{Kind: "appimage", Path: filepath.Join(t.TempDir(), "Telegram-MCP.AppImage")}
	srv := fakeGitHub(t, "v1.3.0", target.assetName(), body, hex.EncodeToString(h[:]))
	defer srv.Close()
	u := &Updater{Repo: "o/r", Version: "1.2.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: t.TempDir(), Target: target}

	found, err := u.Check(context.Background())
	if err != nil || !found {
		t.Fatalf("check: %v %v", found, err)
	}
	if st := u.State(); st.Phase != Available || st.Latest != "1.3.0" {
		t.Fatalf("state %+v", st)
	}
	path, err := u.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "the new app" {
		t.Fatal("downloaded the wrong bytes")
	}
}

func TestDownloadRefusesAMismatch(t *testing.T) {
	body := []byte("tampered")
	target := Target{Kind: "appimage", Path: "/x"}
	srv := fakeGitHub(t, "v1.3.0", target.assetName(), body, strings.Repeat("a", 64))
	defer srv.Close()
	u := &Updater{Repo: "o/r", Version: "1.2.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: t.TempDir(), Target: target}
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background()); err == nil || !strings.Contains(err.Error(), "não confere") {
		t.Fatalf("a file that does not match checksums.txt must be refused, got %v", err)
	}
	if entries, _ := os.ReadDir(u.Dir); len(entries) != 1 || entries[0].Name() != highWaterName {
		t.Fatalf("a refused download left unexpected files: %v", entries)
	}
}

func TestApplyRefusesSignatureAndTransportFailuresBeforePayload(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
	}{
		{"missing signature", "missing"},
		{"invalid signature", "invalid"},
		{"wrong valid key", "wrong-key"},
		{"tampered manifest", "tampered"},
		{"signed version mismatch", "version-mismatch"},
		{"forbidden checksum host", "forbidden-host"},
		{"HTTP redirect", "http-redirect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			installed := filepath.Join(dir, "Telegram-MCP.AppImage")
			if err := os.WriteFile(installed, []byte("installed v1"), 0o755); err != nil {
				t.Fatal(err)
			}
			target := Target{Kind: "appimage", Path: installed}
			payload := []byte("candidate v2")
			manifestVersion := "2.0.0"
			if tc.mode == "version-mismatch" {
				manifestVersion = "2.0.1"
			}
			manifest := testManifest(manifestVersion, target.assetName(), payload)
			signature := signTestManifest(manifest)
			servedManifest := manifest
			if tc.mode == "tampered" {
				servedManifest = append(append([]byte(nil), manifest...), []byte("# altered\n")...)
			}
			if tc.mode == "invalid" {
				signature = []byte("invalid signature\n")
			}
			var payloadRequests atomic.Int32
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/releases/latest":
					sumsURL := srv.URL + "/sums"
					if tc.mode == "forbidden-host" {
						sumsURL = "https://evil.example/checksums.txt"
					}
					assets := []asset{
						{Name: target.assetName(), URL: srv.URL + "/file", Size: int64(len(payload))},
						{Name: "checksums.txt", URL: sumsURL},
					}
					if tc.mode != "missing" {
						assets = append(assets, asset{Name: "checksums.txt.minisig", URL: srv.URL + "/sums.minisig"})
					}
					_ = json.NewEncoder(w).Encode(release{Tag: "v2.0.0", Assets: assets})
				case "/sums":
					if tc.mode == "http-redirect" {
						http.Redirect(w, r, "http://evil.example/checksums.txt", http.StatusFound)
						return
					}
					_, _ = w.Write(servedManifest)
				case "/sums.minisig":
					_, _ = w.Write(signature)
				case "/file":
					payloadRequests.Add(1)
					_, _ = w.Write(payload)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			publicKey := testPublicKey()
			if tc.mode == "wrong-key" {
				publicKey = otherTestPublicKey()
			}
			u := &Updater{Repo: "o/r", Version: "1.0.0", API: srv.URL, Client: srv.Client(), PublicKey: publicKey, Dir: filepath.Join(dir, "downloads"), Target: target}
			if found, err := u.Check(context.Background()); err != nil || !found {
				t.Fatalf("check: found=%v err=%v", found, err)
			}
			if err := u.Apply(context.Background()); err == nil {
				t.Fatal("unsafe release unexpectedly applied")
			}
			if payloadRequests.Load() != 0 {
				t.Fatalf("payload fetched %d times before trust checks passed", payloadRequests.Load())
			}
			got, err := os.ReadFile(installed)
			if err != nil || string(got) != "installed v1" {
				t.Fatalf("installed executable changed: %q err=%v", got, err)
			}
		})
	}
}

func TestDebIsUpdatedByHand(t *testing.T) {
	srv := fakeGitHub(t, "v1.3.0", "whatever", nil, "")
	defer srv.Close()
	u := &Updater{Repo: "o/r", Version: "1.2.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: t.TempDir(), Target: Target{Kind: "deb"}}
	if found, err := u.Check(context.Background()); err != nil || !found {
		t.Fatal(found, err)
	}
	if st := u.State(); st.Phase != Manual || st.Page != "https://example.test/r" {
		t.Fatalf("state %+v", st)
	}
	if err := u.Apply(context.Background()); err == nil || u.State().Phase != Manual {
		t.Fatalf("DEB must remain a manual update, got err=%v state=%+v", err, u.State())
	}
}

func TestNoReleaseIsUpToDate(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	u := &Updater{Repo: "o/r", Version: "1.2.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Target: Target{Kind: "setup"}}
	if found, err := u.Check(context.Background()); err != nil || found || u.State().Phase != Current {
		t.Fatalf("%v %v %+v", found, err, u.State())
	}
}
