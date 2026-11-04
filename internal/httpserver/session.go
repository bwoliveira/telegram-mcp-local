package httpserver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"sync"
	"time"
)

const sessionCookie = "tgmcp_session"
const maxSessions = 128

type browserSession struct{ created, last time.Time }
type browserAuth struct {
	mu                     sync.Mutex
	key                    [32]byte
	codes                  map[[32]byte]time.Time
	sessions               map[[32]byte]browserSession
	now                    func() time.Time
	failures               int
	failureWindow          time.Time
	pairWindow             time.Time
	pairAttempts           [2]int
	dir, scope, generation string
	checkpoint             time.Time
	storageErr             error
}

func newBrowserAuth(now func() time.Time) *browserAuth {
	if now == nil {
		now = time.Now
	}
	a := &browserAuth{codes: make(map[[32]byte]time.Time), sessions: make(map[[32]byte]browserSession), now: now}
	if _, err := rand.Read(a.key[:]); err != nil {
		panic("cannot initialize browser security")
	}
	return a
}
func randomCredential() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("cannot generate credential")
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func credentialHash(s string) [32]byte { return sha256.Sum256([]byte(s)) }
func (a *browserAuth) csrf(id string) string {
	mac := hmac.New(sha256.New, a.key[:])
	mac.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *browserAuth) prune(now time.Time) bool {
	changed := false
	for hash, expiry := range a.codes {
		if !now.Before(expiry) {
			delete(a.codes, hash)
		}
	}
	for hash, s := range a.sessions {
		if !now.Before(s.last.Add(12*time.Hour)) || !now.Before(s.created.Add(7*24*time.Hour)) {
			delete(a.sessions, hash)
			changed = true
		}
	}
	return changed
}
func (a *browserAuth) session(r *http.Request) (string, bool) {
	cookies := r.Cookies()
	var id string
	count := 0
	for _, c := range cookies {
		if c.Name == sessionCookie {
			count++
			id = c.Value
		}
	}
	if count != 1 || len(id) != 43 {
		return "", false
	}
	hash := credentialHash(id)
	a.mu.Lock()
	defer a.mu.Unlock()
	unlock, ready := a.begin()
	if !ready {
		return "", false
	}
	defer unlock()
	now := a.now()
	pruned := a.prune(now)
	if a.storageErr != nil || (pruned && !a.save(now)) {
		return "", false
	}
	s, ok := a.sessions[hash]
	if !ok {
		return "", false
	}
	s.last = now
	a.sessions[hash] = s
	// Checkpoint at most once per minute; a crash can conservatively lose
	// less than one minute of idle activity, never extend the lifetime.
	if !now.Before(a.checkpoint.Add(sessionCheckpointInterval)) && !a.save(now) {
		return "", false
	}
	return id, true
}
func (a *browserAuth) issue(w http.ResponseWriter, r *http.Request) {
	code, err := randomCredential()
	if err != nil {
		http.Error(w, "bootstrap unavailable", 503)
		return
	}
	a.mu.Lock()
	unlock, ready := a.begin()
	if !ready {
		a.mu.Unlock()
		http.Error(w, "session unavailable", 503)
		return
	}
	defer unlock()
	now := a.now()
	if a.storageErr != nil || (a.prune(now) && !a.save(now)) {
		a.mu.Unlock()
		http.Error(w, "session unavailable", 503)
		return
	}
	if len(a.codes) >= maxSessions {
		a.mu.Unlock()
		http.Error(w, "bootstrap capacity reached", 429)
		return
	}
	a.codes[credentialHash(code)] = now.Add(time.Minute)
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": "http://" + r.Host + "/bootstrap?c=" + code})
}
func (a *browserAuth) exchange(w http.ResponseWriter, r *http.Request) {
	id, err := randomCredential()
	if err != nil {
		http.Error(w, "session unavailable", 503)
		return
	}
	code := r.URL.Query().Get("c")
	a.mu.Lock()
	unlock, ready := a.begin()
	if !ready {
		a.mu.Unlock()
		http.Error(w, "session unavailable", 503)
		return
	}
	defer unlock()
	now := a.now()
	if a.storageErr != nil || (a.prune(now) && !a.save(now)) {
		a.mu.Unlock()
		http.Error(w, "session unavailable", 503)
		return
	}
	if !now.Before(a.failureWindow.Add(time.Minute)) {
		a.failureWindow = now
		a.failures = 0
	}
	expiry, valid := a.codes[credentialHash(code)]
	if !valid || len(code) != 43 || !now.Before(expiry) {
		a.failures++
		status := http.StatusUnauthorized
		if a.failures >= 10 {
			status = 429
			w.Header().Set("Retry-After", "60")
		}
		a.mu.Unlock()
		http.Error(w, "invalid or expired bootstrap", status)
		return
	}
	if len(a.sessions) >= maxSessions {
		a.mu.Unlock()
		http.Error(w, "session capacity reached", 429)
		return
	}
	delete(a.codes, credentialHash(code))
	a.sessions[credentialHash(id)] = browserSession{created: now, last: now}
	if !a.save(now) {
		delete(a.sessions, credentialHash(id))
		a.mu.Unlock()
		http.Error(w, "session unavailable", 503)
		return
	}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 60 * 60, Expires: now.Add(7 * 24 * time.Hour)})
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}
func (a *browserAuth) info(w http.ResponseWriter, r *http.Request) {
	id, ok := a.session(r)
	if !ok {
		http.Error(w, "panel session required", 401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"csrf_token": a.csrf(id)})
}
func (a *browserAuth) logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "panel session required", 401)
		return
	}
	a.mu.Lock()
	unlock, ready := a.begin()
	if !ready {
		a.mu.Unlock()
		http.Error(w, "session unavailable", 503)
		return
	}
	defer unlock()
	delete(a.sessions, credentialHash(c.Value))
	if !a.save(a.now()) {
		a.mu.Unlock()
		http.Error(w, "session unavailable", 503)
		return
	}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
func unsafeMethod(method string) bool { return method != http.MethodGet && method != http.MethodHead }
func (a *browserAuth) checkCSRF(w http.ResponseWriter, r *http.Request, id string) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if (r.Header.Get("Origin") == "" && site != "same-origin") || (site != "" && site != "same-origin") {
		http.Error(w, "same-origin browser request required", 403)
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/x-www-form-urlencoded") {
		http.Error(w, "unsupported content type", 415)
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" && mediaType == "application/x-www-form-urlencoded" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return false
		}
		token = r.PostForm.Get("csrf")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.csrf(id))) != 1 {
		http.Error(w, "invalid CSRF token", 403)
		return false
	}
	return true
}
