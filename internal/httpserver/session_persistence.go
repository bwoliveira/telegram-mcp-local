package httpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

const browserSessionFile = "browser-sessions.json"
const sessionCheckpointInterval = time.Minute
const maxSessionFileSize = 64 << 10

var errSessionStorage = errors.New("private browser session storage unavailable")

type savedBrowserSession struct {
	Hash    string    `json:"hash"`
	Created time.Time `json:"created"`
	Last    time.Time `json:"last"`
}
type savedBrowserSessions struct {
	Version    int                   `json:"version"`
	Scope      string                `json:"scope"`
	Generation string                `json:"generation"`
	Sessions   []savedBrowserSession `json:"sessions"`
}

// configurePersistence runs before publishing the handler. Codes and the CSRF
// key stay process-local. Changing the directory, account or bearer discards the
// old snapshot, so switching back cannot restore a previous capability.
func (a *browserAuth) configurePersistence(dir, account, token string) {
	if dir == "" {
		return
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		a.storageErr = errSessionStorage
		return
	}
	a.dir = absolute
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("telegram-mcp/browser-sessions/v1\x00" + absolute + "\x00" + account))
	a.scope = hex.EncodeToString(mac.Sum(nil))
	if err = createPrivateSessionDir(absolute); err != nil {
		a.storageErr = errSessionStorage
		return
	}
	a.checkpoint = a.now()
	unlock, err := a.lockSnapshot()
	if err != nil {
		a.storageErr = errSessionStorage
		return
	}
	defer unlock()
	a.storageErr = a.reload(true)
}

// begin is called under mu. Reloading under an OS lock on every authenticated
// request prevents a desktop/daemon sharing the directory from accepting or
// checkpointing a revoked session. Only surviving hashes may merge local idle
// touches; a stale process can never reinsert a deleted capability.
func (a *browserAuth) begin() (func(), bool) {
	noop := func() {}
	if a.storageErr != nil {
		return noop, false
	}
	if a.dir == "" {
		return noop, true
	}
	unlock, err := a.lockSnapshot()
	if err != nil {
		a.storageErr = errSessionStorage
		return noop, false
	}
	if err = a.reload(false); err != nil {
		a.storageErr = errSessionStorage
		unlock()
		return noop, false
	}
	return unlock, true
}

// Use a stable lock inode, not the atomically replaced snapshot. Validate both
// the path and opened file to refuse symlinks, insecure modes/ACLs and swaps.
func (a *browserAuth) lockSnapshot() (func(), error) {
	if err := checkPrivateSessionPath(a.dir, true); err != nil {
		return nil, err
	}
	path := filepath.Join(a.dir, "browser-sessions.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		if err = protectSessionFile(f); err != nil {
			f.Close()
			return nil, errSessionStorage
		}
	} else if errors.Is(err, os.ErrExist) {
		if err = checkPrivateSessionPath(path, false); err != nil {
			return nil, err
		}
		f, err = os.OpenFile(path, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, errSessionStorage
	}
	valid := func() bool {
		if checkPrivateSessionPath(path, false) != nil {
			return false
		}
		info, e := os.Lstat(path)
		if e != nil {
			return false
		}
		actual, e := f.Stat()
		return e == nil && os.SameFile(info, actual)
	}
	if !valid() {
		f.Close()
		return nil, errSessionStorage
	}
	if err = lockSessionFile(f); err != nil {
		f.Close()
		return nil, errSessionStorage
	}
	if !valid() {
		unlockSessionFile(f)
		f.Close()
		return nil, errSessionStorage
	}
	return func() { unlockSessionFile(f); f.Close() }, nil
}

func (a *browserAuth) openSnapshot() (*os.File, error) {
	if err := checkPrivateSessionPath(a.dir, true); err != nil {
		return nil, err
	}
	path := filepath.Join(a.dir, browserSessionFile)
	if err := checkPrivateSessionPath(path, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		f.Close()
		return nil, errSessionStorage
	}
	return f, nil
}

// reload requires the snapshot lock. Corrupt data is bounded and discarded in
// its entirety. A running instance refuses a foreign namespace instead of
// replacing a newer account's snapshot with its stale namespace.
func (a *browserAuth) reload(initial bool) error {
	f, err := a.openSnapshot()
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(f, maxSessionFileSize+1))
		f.Close()
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return errSessionStorage
	}
	var saved savedBrowserSessions
	valid := len(data) <= maxSessionFileSize && json.Unmarshal(data, &saved) == nil && saved.Version == 1 && len(saved.Generation) == 43 && len(saved.Sessions) <= maxSessions
	if valid && saved.Scope != a.scope {
		if !initial {
			return errSessionStorage
		}
		valid = false
	}
	restored := make(map[[32]byte]browserSession)
	now := a.now()
	if valid {
		for _, entry := range saved.Sessions {
			hash, err := hex.DecodeString(entry.Hash)
			if err != nil || len(hash) != 32 || entry.Created.IsZero() || entry.Last.Before(entry.Created) || entry.Last.After(now) || entry.Created.After(now) {
				valid = false
				break
			}
			var key [32]byte
			copy(key[:], hash)
			if _, duplicate := restored[key]; duplicate {
				valid = false
				break
			}
			restored[key] = browserSession{created: entry.Created, last: entry.Last}
		}
	}
	if !valid {
		generation, err := randomCredential()
		if err != nil {
			return errSessionStorage
		}
		a.generation = generation
		clear(a.sessions)
		clear(a.codes)
		return a.persist(now)
	}
	if a.generation != saved.Generation {
		clear(a.codes)
	}
	for hash, s := range restored {
		if local, ok := a.sessions[hash]; ok && local.created.Equal(s.created) && local.last.After(s.last) {
			s.last = local.last
			restored[hash] = s
		}
	}
	a.sessions = restored
	a.generation = saved.Generation
	if a.prune(now) {
		return a.persist(now)
	}
	return nil
}

// persist runs under mu and the OS snapshot lock. New files are private before
// any session bytes are written: fsync + atomic replacement + directory fsync.
// Existing unsafe paths are refused, never chmodded into something we trust.
func (a *browserAuth) persist(now time.Time) error {
	if a.dir == "" {
		return nil
	}
	if a.storageErr != nil {
		return a.storageErr
	}
	if err := checkPrivateSessionPath(a.dir, true); err != nil {
		return errSessionStorage
	}
	if f, err := a.openSnapshot(); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return errSessionStorage
	}
	saved := savedBrowserSessions{Version: 1, Scope: a.scope, Generation: a.generation, Sessions: make([]savedBrowserSession, 0, len(a.sessions))}
	for hash, s := range a.sessions {
		saved.Sessions = append(saved.Sessions, savedBrowserSession{Hash: hex.EncodeToString(hash[:]), Created: s.created, Last: s.last})
	}
	sort.Slice(saved.Sessions, func(i, j int) bool { return saved.Sessions[i].Hash < saved.Sessions[j].Hash })
	data, err := json.Marshal(saved)
	if err != nil {
		return errSessionStorage
	}
	f, err := os.CreateTemp(a.dir, ".browser-sessions-*")
	if err != nil {
		return errSessionStorage
	}
	name := f.Name()
	defer os.Remove(name)
	if err = protectSessionFile(f); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errSessionStorage
	}
	if err = os.Rename(name, filepath.Join(a.dir, browserSessionFile)); err != nil {
		return errSessionStorage
	}
	if err = syncSessionDirectory(a.dir); err != nil {
		return errSessionStorage
	}
	a.checkpoint = now
	return nil
}

func (a *browserAuth) save(now time.Time) bool {
	if a.storageErr == nil {
		a.storageErr = a.persist(now)
	}
	return a.storageErr == nil
}

func (a *browserAuth) revokeAll() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	unlock, ok := a.begin()
	if !ok {
		return false
	}
	defer unlock()
	generation, err := randomCredential()
	if err != nil {
		return false
	}
	clear(a.sessions)
	clear(a.codes)
	a.generation = generation
	return a.save(a.now())
}

func (a *browserAuth) revokeOnAccountLogout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reached only after authentication and CSRF. Revoke before the effect;
		// an I/O failure must not change identities while old cookies are restorable.
		if r.Method == http.MethodPost && (r.URL.Path == "/conta/sair" || r.URL.Path == "/api/account/logout") {
			if !a.revokeAll() {
				http.Error(w, "session unavailable", 503)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func syncSessionDirectory(dir string) error {
	// Windows does not support directory fsync; file sync + atomic replacement
	// still prevent partial JSON, consistent with other private project files.
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
