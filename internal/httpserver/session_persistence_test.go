package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

const sessionFileName = "browser-sessions.json"

func privateSessionTestDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "browser")
	if err := createPrivateSessionDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeSessionFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectSessionFile(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func persistentHandler(dir, account, token string, now *time.Time) http.Handler {
	return Handler(mcp.New(mcp.Config{}), Options{Token: token, PanelBootstrapSecret: testBootstrapSecret, SessionDir: dir, SessionAccount: account, Now: func() time.Time { return *now }})
}
func cookieStatus(h http.Handler, c *http.Cookie) int {
	r := request("GET", "/api/session")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}
func TestPersistentSessionSurvivesNewServer(t *testing.T) {
	now := time.Now().UTC()
	dir := privateSessionTestDir(t)
	h := persistentHandler(dir, "primary", testToken, &now)
	c, csrf, link := bootstrapSession(t, h)
	data, err := os.ReadFile(filepath.Join(dir, sessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(c.Value)) || bytes.Contains(data, []byte(csrf)) || bytes.Contains(data, []byte(testToken)) || bytes.Contains(data, []byte(strings.TrimPrefix(link, "/bootstrap?c="))) {
		t.Fatal("plaintext credential persisted")
	}
	if err := checkPrivateSessionPath(filepath.Join(dir, sessionFileName), false); err != nil {
		t.Fatal(err)
	}
	if c.MaxAge <= 0 || c.Expires.IsZero() {
		t.Fatal("cookie must survive browser restart within the absolute lifetime")
	}
	now = now.Add(time.Hour)
	restarted := persistentHandler(dir, "primary", testToken, &now)
	if got := cookieStatus(restarted, c); got != 200 {
		t.Fatalf("session after restart = %d, want 200", got)
	}
	// Process-local CSRF and bootstrap codes must NOT survive the restart.
	r := request("POST", "/api/session/logout")
	r.AddCookie(c)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	restarted.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("old CSRF accepted: %d", w.Code)
	}
	w = httptest.NewRecorder()
	restarted.ServeHTTP(w, request("GET", link))
	if w.Code != 401 {
		t.Fatalf("bootstrap replay after restart = %d", w.Code)
	}
}
func TestPersistentSessionExpiryAndCleanup(t *testing.T) {
	for _, age := range []time.Duration{12 * time.Hour, 7 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			now := time.Now().UTC()
			dir := privateSessionTestDir(t)
			h := persistentHandler(dir, "primary", testToken, &now)
			c, _, _ := bootstrapSession(t, h)
			if age > 12*time.Hour {
				for i := 0; i < 20; i++ {
					now = now.Add(8 * time.Hour)
					if cookieStatus(h, c) != 200 {
						t.Fatal("expired before absolute limit")
					}
				}
				now = now.Add(8 * time.Hour)
			} else {
				now = now.Add(age)
			}
			h = persistentHandler(dir, "primary", testToken, &now)
			if got := cookieStatus(h, c); got != 401 {
				t.Fatalf("expired session restored: %d", got)
			}
			data, err := os.ReadFile(filepath.Join(dir, sessionFileName))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(hexHash(c.Value))) {
				t.Fatal("expired hash left on disk")
			}
		})
	}
}
func hexHash(s string) string {
	h := credentialHash(s)
	const digits = "0123456789abcdef"
	b := make([]byte, 64)
	for i, v := range h {
		b[i*2] = digits[v>>4]
		b[i*2+1] = digits[v&15]
	}
	return string(b)
}
func TestPersistentSessionRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"file-mode", "directory-mode", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && kind != "symlink" {
				t.Skip("POSIX modes; Windows ACL refusal has dedicated tests")
			}
			now := time.Now().UTC()
			dir := privateSessionTestDir(t)
			h := persistentHandler(dir, "primary", testToken, &now)
			c, _, _ := bootstrapSession(t, h)
			path := filepath.Join(dir, sessionFileName)
			switch kind {
			case "file-mode":
				os.Chmod(path, 0644)
			case "directory-mode":
				os.Chmod(dir, 0755)
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip(err)
				}
			}
			h = persistentHandler(dir, "primary", testToken, &now)
			if got := cookieStatus(h, c); got != 401 {
				t.Fatalf("unsafe session file accepted: %d", got)
			}
			// Refusal must not silently chmod or overwrite an unsafe existing target.
			r := request("POST", "/api/session/bootstrap")
			r.Header.Set(PanelBootstrapHeader, testBootstrapSecret)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 503 {
				t.Fatalf("unsafe storage did not fail closed: %d", w.Code)
			}
		})
	}
}
func TestPersistentSessionCorruptFileIsDiscarded(t *testing.T) {
	for _, data := range []string{"{broken", `{"version":999}`, `{"version":1,"sessions":[{"hash":"invalid"}]}`} {
		t.Run(data, func(t *testing.T) {
			now := time.Now().UTC()
			dir := privateSessionTestDir(t)
			writeSessionFixture(t, filepath.Join(dir, sessionFileName), []byte(data))
			h := persistentHandler(dir, "primary", testToken, &now)
			c, _, _ := bootstrapSession(t, h)
			h = persistentHandler(dir, "primary", testToken, &now)
			if cookieStatus(h, c) != 200 {
				t.Fatal("cannot recover corrupt file")
			}
		})
	}
}
func TestPersistentSessionAccountAndTokenIsolation(t *testing.T) {
	for _, kind := range []string{"account", "directory", "token"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			dir := privateSessionTestDir(t)
			h := persistentHandler(dir, "primary", testToken, &now)
			c, _, _ := bootstrapSession(t, h)
			account, token := "primary", testToken
			switch kind {
			case "account":
				account = "work"
			case "directory":
				data, err := os.ReadFile(filepath.Join(dir, sessionFileName))
				if err != nil {
					t.Fatal(err)
				}
				dir = privateSessionTestDir(t)
				writeSessionFixture(t, filepath.Join(dir, sessionFileName), data)
			case "token":
				token = "synthetic-new-bearer"
			}
			if cookieStatus(persistentHandler(dir, account, token, &now), c) != 401 {
				t.Fatal("session crossed account/directory/token boundary")
			}
			if cookieStatus(persistentHandler(dir, "primary", testToken, &now), c) != 401 {
				t.Fatal("switching back resurrected revoked session")
			}
		})
	}
}
func TestPersistentSessionLogoutIsDurable(t *testing.T) {
	now := time.Now().UTC()
	dir := privateSessionTestDir(t)
	h := persistentHandler(dir, "primary", testToken, &now)
	c, csrf, _ := bootstrapSession(t, h)
	r := request("POST", "/api/session/logout")
	r.AddCookie(c)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if cookieStatus(persistentHandler(dir, "primary", testToken, &now), c) != 401 {
		t.Fatal("logout resurrected after restart")
	}
}
func TestPersistentSessionAccountLogoutRevokesAll(t *testing.T) {
	for _, path := range []string{"/conta/sair", "/api/account/logout"} {
		t.Run(path, func(t *testing.T) {
			now := time.Now().UTC()
			dir := privateSessionTestDir(t)
			effects := 0
			h := Handler(mcp.New(mcp.Config{}), Options{Token: testToken, PanelBootstrapSecret: testBootstrapSecret, SessionDir: dir, SessionAccount: "primary", Now: func() time.Time { return now }, Register: func(mux *http.ServeMux) {
				mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) { effects++; w.WriteHeader(204) })
			}})
			c, csrf, _ := bootstrapSession(t, h)
			other, _, _ := bootstrapSession(t, h)
			r := request("POST", path)
			r.AddCookie(c)
			r.Header.Set("Origin", "http://127.0.0.1:8765")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 || effects != 0 || cookieStatus(h, c) != 200 {
				t.Fatalf("forged logout affected session: %d effects=%d", w.Code, effects)
			}
			r = request("POST", path)
			r.AddCookie(c)
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Origin", "http://127.0.0.1:8765")
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 204 || effects != 1 {
				t.Fatalf("authorized account logout: %d effects=%d", w.Code, effects)
			}
			restarted := persistentHandler(dir, "primary", testToken, &now)
			for _, cookie := range []*http.Cookie{c, other} {
				if cookieStatus(h, cookie) != 401 || cookieStatus(restarted, cookie) != 401 {
					t.Fatal("account logout retained a browser session")
				}
			}
		})
	}
}

func TestPersistentSessionOverlappingServersCannotResurrectLogout(t *testing.T) {
	now := time.Now().UTC()
	dir := privateSessionTestDir(t)
	a := persistentHandler(dir, "primary", testToken, &now)
	c, csrf, _ := bootstrapSession(t, a)
	b := persistentHandler(dir, "primary", testToken, &now)
	if cookieStatus(b, c) != 200 {
		t.Fatal("control: second server cannot see session")
	}
	r := request("POST", "/api/session/logout")
	r.AddCookie(c)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if got := cookieStatus(b, c); got != 401 {
		t.Errorf("stale server accepts revoked session: %d", got)
	}
	now = now.Add(2 * time.Minute)
	cookieStatus(b, c)
	if cookieStatus(persistentHandler(dir, "primary", testToken, &now), c) != 401 {
		t.Fatal("stale checkpoint resurrected revoked session")
	}
	// A later session created by one server must not be lost by the other's save.
	first, _, _ := bootstrapSession(t, a)
	second, _, _ := bootstrapSession(t, b)
	restarted := persistentHandler(dir, "primary", testToken, &now)
	if cookieStatus(restarted, first) != 200 || cookieStatus(restarted, second) != 200 {
		t.Fatal("overlapping session creation lost a session")
	}
}

func TestPersistentSessionLastCheckpointCoalesces(t *testing.T) {
	now := time.Now().UTC()
	dir := privateSessionTestDir(t)
	h := persistentHandler(dir, "primary", testToken, &now)
	c, _, _ := bootstrapSession(t, h)
	path := filepath.Join(dir, sessionFileName)
	before, _ := os.ReadFile(path)
	for i := 0; i < 20; i++ {
		now = now.Add(time.Second)
		if cookieStatus(h, c) != 200 {
			t.Fatal("session rejected")
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("request touches were not coalesced")
	}
	now = now.Add(time.Minute)
	if cookieStatus(h, c) != 200 {
		t.Fatal("session rejected")
	}
	after, _ = os.ReadFile(path)
	if bytes.Equal(before, after) {
		t.Fatal("last activity never checkpointed")
	}
	var value map[string]any
	if err := json.Unmarshal(after, &value); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Hour)
	if cookieStatus(persistentHandler(dir, "primary", testToken, &now), c) != 200 {
		t.Fatal("checkpoint not restored")
	}
}
