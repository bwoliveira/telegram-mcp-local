// Package updater checks GitHub Releases for a newer version without downloading
// or installing anything. On an explicit Apply request, it downloads the file
// for this system, checks the release's SHA-256 (and macOS code signature), and
// replaces the running app. The caller quits so a helper can restart it.
//
// Engine implementations ship inside the app and update together with it.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// Phases of an update.
const (
	Idle        = "idle"
	Checking    = "checking"
	Current     = "current"
	Available   = "available"
	Downloading = "downloading"
	Ready       = "ready"
	// Manual is a version that has to be installed by hand: a .deb.
	Manual = "manual"
	Failed = "error"
	// Unsupported is a build that cannot update itself: a development
	// version, or an install the updater does not know how to replace.
	Unsupported = "unsupported"
)

// State is where an update stands.
type State struct {
	Phase     string
	Current   string
	Latest    string
	Page      string
	Progress  int
	Error     string
	CheckedAt time.Time
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type release struct {
	Tag    string  `json:"tag_name"`
	Page   string  `json:"html_url"`
	Draft  bool    `json:"draft"`
	Pre    bool    `json:"prerelease"`
	Assets []asset `json:"assets"`
}

type Updater struct {
	Repo    string // owner/name on GitHub
	Version string // the running version, as tagged without the v
	// API is GitHub's API, replaceable in tests.
	API    string
	Client *http.Client
	// PublicKey overrides the embedded minisign trust anchor in tests.
	PublicKey string
	Logger    *slog.Logger
	// Dir is where downloads are kept until they are installed.
	Dir string
	// Target says what is installed and how to replace it; the default is
	// worked out from the running program.
	Target Target

	mu       sync.Mutex
	devFeed  bool  // ConfigureDevFeed accepted a test feed (TELEGRAM_MCP_DEV=1)
	refused  error // ConfigureDevFeed refused an override; checks stay off
	state    State
	latest   *release
	file     asset
	staged   string
	digest   string // SHA-256 of the verified staged file
	running  bool
	applying bool
}

// ErrBusy means an update is already being downloaded or installed.
var ErrBusy error = busyError{}

type busyError struct{}

func (busyError) Error() string { return i18n.T("a atualização já está em andamento") }

// Apply downloads, checks and installs the new version, once at a time: a
// second call while one runs returns ErrBusy at once. The phase becomes
// downloading before Apply returns its first byte, so a page polling the
// state sees the work start.
func (u *Updater) Apply(ctx context.Context) error {
	u.mu.Lock()
	if u.applying || u.running {
		u.mu.Unlock()
		return ErrBusy
	}
	if u.latest == nil {
		u.mu.Unlock()
		return errors.New(i18n.T("nenhuma versão nova para instalar"))
	}
	u.applying = true
	u.state.Phase, u.state.Progress, u.state.Error = Downloading, 0, ""
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.applying = false
		u.mu.Unlock()
	}()
	if _, err := u.download(ctx); err != nil {
		return err
	}
	return u.install(ctx)
}

// Target is the installed app this updater replaces.
type Target struct {
	// Kind is dmg (a macOS .app), setup (the Windows installer), appimage,
	// or deb (installed by a package manager, updated by hand).
	Kind string
	// Path is the .app bundle or the AppImage file; unused for setup.
	Path string
}

// DetectTarget works out how the running app was installed.
func DetectTarget(exe string) Target {
	switch runtime.GOOS {
	case "darwin":
		if i := strings.Index(exe, ".app/Contents/MacOS/"); i > 0 {
			return Target{Kind: "dmg", Path: exe[:i+len(".app")]}
		}
	case "windows":
		return Target{Kind: "setup", Path: exe}
	case "linux":
		if p := os.Getenv("APPIMAGE"); p != "" {
			return Target{Kind: "appimage", Path: p}
		}
		return Target{Kind: "deb"}
	}
	return Target{}
}

// assetName is the file a release carries for this system.
func (t Target) assetName() string {
	switch t.Kind {
	case "dmg":
		return "Telegram-MCP.dmg"
	case "setup":
		return "Telegram-MCP-Setup.exe"
	case "appimage":
		arch := "x86_64"
		if runtime.GOARCH == "arm64" {
			arch = "aarch64"
		}
		return "Telegram-MCP-" + arch + ".AppImage"
	}
	return ""
}

// Enabled reports whether this build can update itself: a tagged version,
// installed in a way the updater knows.
func (u *Updater) Enabled() bool {
	_, ok := parseVersion(u.Version)
	return ok && u.Repo != "" && u.Target.Kind != ""
}

func (u *Updater) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.state
	s.Current = u.Version
	if s.Phase == "" {
		s.Phase = Idle
	}
	return s
}

func (u *Updater) set(f func(*State)) {
	u.mu.Lock()
	f(&u.state)
	u.mu.Unlock()
}

func (u *Updater) logger() *slog.Logger {
	if u.Logger != nil {
		return u.Logger
	}
	return slog.Default()
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (u *Updater) testHTTPAllowed() bool {
	if u.Client == nil {
		return false
	}
	api := strings.TrimRight(u.API, "/")
	return strings.HasPrefix(api, "http://localhost:") || strings.HasPrefix(api, "http://127.0.0.1:") || strings.HasPrefix(api, "http://[::1]:")
}

func (u *Updater) do(req *http.Request, kind string) (*http.Response, error) {
	allowTestHTTP := u.testHTTPAllowed()
	u.mu.Lock()
	devFeed := u.devFeed
	u.mu.Unlock()
	allowed := func(raw string) bool {
		return releaseURLAllowed(raw, kind, allowTestHTTP) || devFeed && devFeedURLAllowed(raw)
	}
	if !allowed(req.URL.String()) {
		return nil, fmt.Errorf("refusing untrusted %s URL %q", kind, req.URL.Redacted())
	}
	base := u.client()
	client := *base
	previousRedirect := base.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if !allowed(next.URL.String()) {
			return fmt.Errorf("refusing untrusted %s redirect %q", kind, next.URL.Redacted())
		}
		if previousRedirect != nil {
			return previousRedirect(next, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return client.Do(req)
}

// Check asks GitHub for the latest release. It reports whether a newer one
// exists; a repository without releases, or out of reach, is up to date.
func (u *Updater) Check(ctx context.Context) (bool, error) {
	if !u.Enabled() {
		u.set(func(s *State) { s.Phase, s.CheckedAt = Unsupported, time.Now() })
		return false, nil
	}
	u.mu.Lock()
	if u.running || u.applying {
		u.mu.Unlock()
		return false, ErrBusy
	}
	if u.refused != nil {
		err := u.refused
		u.latest, u.file, u.staged, u.digest = nil, asset{}, "", ""
		u.state = State{Phase: Failed, Error: err.Error(), CheckedAt: time.Now()}
		u.mu.Unlock()
		return false, err
	}
	u.running = true
	// A withdrawn release or a failed refresh must not leave an installable
	// candidate (or an old staged download) behind in memory.
	u.latest, u.file, u.staged, u.digest = nil, asset{}, "", ""
	u.state = State{Phase: Checking}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.running = false
		u.mu.Unlock()
	}()

	rel, err := u.fetchLatest(ctx)
	now := time.Now()
	if err != nil {
		u.set(func(s *State) { s.Phase, s.Error, s.CheckedAt = Failed, err.Error(), now })
		return false, err
	}
	if rel == nil || !newer(rel.Tag, u.Version) {
		u.set(func(s *State) { s.Phase, s.CheckedAt = Current, now })
		return false, nil
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	if u.Target.Kind == "deb" {
		u.set(func(s *State) { s.Phase, s.Latest, s.Page, s.CheckedAt = Manual, latest, rel.Page, now })
		return true, nil
	}
	want := u.Target.assetName()
	var found *asset
	for i := range rel.Assets {
		if rel.Assets[i].Name == want {
			found = &rel.Assets[i]
		}
	}
	if found == nil {
		err := errors.New(i18n.Tf("a versão %s não tem o arquivo %s", latest, want))
		u.set(func(s *State) {
			s.Phase, s.Error, s.Latest, s.Page, s.CheckedAt = Failed, err.Error(), latest, rel.Page, now
		})
		return false, err
	}
	u.mu.Lock()
	u.latest, u.file = rel, *found
	u.state = State{Phase: Available, Latest: latest, Page: rel.Page, CheckedAt: now}
	u.mu.Unlock()
	return true, nil
}

func (u *Updater) fetchLatest(ctx context.Context) (*release, error) {
	api := u.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+"/repos/"+u.Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "telegram-mcp/"+u.Version)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := u.do(req.WithContext(ctx), "api")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("não foi possível consultar as versões"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // no release yet, or a private repository
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(i18n.Tf("o GitHub respondeu %s", resp.Status))
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.Draft || rel.Pre {
		return nil, nil
	}
	return &rel, nil
}

// Download fetches the new version and checks it against the release's
// checksums.txt.
func (u *Updater) Download(ctx context.Context) (string, error) {
	if err := u.beginOperation(); err != nil {
		return "", err
	}
	defer u.endOperation()
	return u.download(ctx)
}

// beginOperation excludes checks and explicit Apply calls while a standalone
// Download or Install is using a candidate. Apply owns both steps together.
func (u *Updater) beginOperation() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running || u.applying {
		return ErrBusy
	}
	u.running = true
	return nil
}

func (u *Updater) endOperation() {
	u.mu.Lock()
	u.running = false
	u.mu.Unlock()
}

func (u *Updater) download(ctx context.Context) (string, error) {
	u.mu.Lock()
	rel, file := u.latest, u.file
	u.staged, u.digest = "", ""
	u.mu.Unlock()
	if rel == nil {
		return "", errors.New(i18n.T("nenhuma versão nova para instalar"))
	}
	want, err := u.checksum(ctx, rel, file.Name)
	if err != nil {
		u.fail(err)
		return "", err
	}
	if err := os.MkdirAll(u.Dir, 0o755); err != nil {
		u.fail(err)
		return "", err
	}
	path := filepath.Join(u.Dir, file.Name)
	u.set(func(s *State) { s.Phase, s.Progress, s.Error = Downloading, 0, "" })
	if err := u.fetch(ctx, file, path, want); err != nil {
		os.Remove(path)
		u.fail(err)
		return "", err
	}
	if u.Target.Kind == "appimage" {
		if err := syncDir(u.Dir); err != nil {
			os.Remove(path)
			u.fail(err)
			return "", fmt.Errorf("sync update directory: %w", err)
		}
	}
	u.mu.Lock()
	u.staged, u.digest = path, want
	u.mu.Unlock()
	return path, nil
}

func (u *Updater) fail(err error) {
	u.set(func(s *State) { s.Phase, s.Error = Failed, err.Error() })
	u.logger().Warn("update failed", "error", err)
}

// checksum authenticates the signed release manifest before returning the
// selected file's SHA-256. An unsigned checksum file is never trusted.
func (u *Updater) checksum(ctx context.Context, rel *release, name string) (string, error) {
	var sums, signature *asset
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case "checksums.txt":
			sums = &rel.Assets[i]
		case "checksums.txt.minisig":
			signature = &rel.Assets[i]
		}
	}
	if sums == nil {
		return "", errors.New(i18n.T("a versão nova não publicou checksums.txt; ela não será instalada"))
	}
	if signature == nil {
		return "", errors.New("the release did not publish checksums.txt.minisig; it will not be installed")
	}
	const maxChecksums = 1 << 20
	body, err := u.readReleaseAsset(ctx, *sums, maxChecksums)
	if err != nil {
		return "", err
	}
	sig, err := u.readReleaseAsset(ctx, *signature, maxSignatureSize)
	if err != nil {
		return "", err
	}
	publicKey := u.PublicKey
	if publicKey == "" {
		publicKey = ReleasePublicKey
	}
	if err := verifyMinisign(body, sig, publicKey); err != nil {
		return "", fmt.Errorf("release manifest signature verification failed: %w", err)
	}
	requested := strings.TrimPrefix(rel.Tag, "v")
	digest, err := parseReleaseManifest(body, u.Version, requested, name, time.Now())
	if err != nil && strings.Contains(err.Error(), "does not have "+name) {
		return "", errors.New(i18n.Tf("checksums.txt não tem %s", name))
	}
	if err != nil {
		return "", err
	}
	if err := enforceAuthenticatedHighWater(u.Dir, requested); err != nil {
		return "", err
	}
	return digest, nil
}

func (u *Updater) readReleaseAsset(ctx context.Context, file asset, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, file.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.do(req, "asset")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if file.Name == "checksums.txt" {
			return nil, errors.New(i18n.Tf("checksums.txt respondeu %s", resp.Status))
		}
		return nil, fmt.Errorf("%s responded %s", file.Name, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", file.Name, limit)
	}
	return body, nil
}

func (u *Updater) fetch(ctx context.Context, file asset, path, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, file.URL, nil)
	if err != nil {
		return err
	}
	resp, err := u.do(req, "asset")
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("o download parou"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(i18n.Tf("o download respondeu %s", resp.Status))
	}
	total := file.Size
	if total <= 0 {
		total = resp.ContentLength
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
			h.Write(buf[:n])
			done += int64(n)
			if total > 0 {
				pct := int(done * 100 / total)
				u.set(func(s *State) { s.Progress = pct })
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("%s: %w", i18n.T("o download parou"), rerr)
		}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return errors.New(i18n.Tf("o arquivo baixado não confere com checksums.txt (sha256 %s, esperado %s)", got, want))
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

// Install puts the downloaded version in place. It returns once the swap is
// staged; the app must then quit, and a helper starts the new version as
// soon as the old one is gone.
func (u *Updater) Install(ctx context.Context) error {
	if err := u.beginOperation(); err != nil {
		return err
	}
	defer u.endOperation()
	return u.install(ctx)
}

func (u *Updater) install(ctx context.Context) error {
	u.mu.Lock()
	path, want := u.staged, u.digest
	version := ""
	if u.latest != nil {
		version = strings.TrimPrefix(u.latest.Tag, "v")
	}
	u.mu.Unlock()
	if path == "" || want == "" {
		return errors.New(i18n.T("baixe a versão nova antes de instalar"))
	}
	// Download and Install may be separate calls. Never trust bytes left in
	// staging without checking them again immediately before installation.
	if err := verifyStaged(ctx, path, want); err != nil {
		u.mu.Lock()
		u.staged, u.digest = "", ""
		u.mu.Unlock()
		_ = os.Remove(path)
		u.fail(err)
		return err
	}
	if err := prepareForInstall(u.Dir, u.Version); err != nil {
		u.fail(err)
		return err
	}
	var err error
	switch u.Target.Kind {
	case "dmg":
		err = installDMG(ctx, path, u.Target.Path, u.Dir, u.Version, version)
	case "setup":
		err = installSetup(path, u.Target.Path, u.Dir, u.Version, version, want)
	case "appimage":
		err = installAppImage(path, u.Target.Path, u.Dir, u.Version, version)
	default:
		err = errors.New(i18n.T("este jeito de instalar não se atualiza sozinho"))
	}
	if err != nil {
		u.fail(err)
		return err
	}
	u.set(func(s *State) { s.Phase, s.Progress = Ready, 100 })
	return nil
}

// verifyStaged checks the local file again and honors cancellation before any
// installer or executable replacement can start.
func verifyStaged(ctx context.Context, path, want string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	h := sha256.New()
	if _, err := io.Copy(h, in); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return errors.New(i18n.Tf("o arquivo baixado não confere com checksums.txt (sha256 %s, esperado %s)", got, want))
	}
	return nil
}

// parseVersion reads 1.2.3 (with or without the v) as numbers.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return out, false // a pre-release or a development build
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newer reports whether tag is a later version than current.
func newer(tag, current string) bool {
	a, ok1 := parseVersion(tag)
	b, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
