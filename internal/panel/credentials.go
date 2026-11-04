package panel

import (
	"html/template"
	"net/http"
	"strings"
	"sync"
)

// CredentialSetup is the desktop's first-use page before the daemon exists.
// It must be mounted only behind Internal, never on a network listener. Even
// same-origin network requests cannot submit credentials through this handler.
// save persists credentials and retries startup; its errors are never rendered.
func CredentialSetup(save func(id, hash string) error) http.Handler {
	page := template.Must(template.New("credentials").Funcs(funcs()).Parse(credentialSetupPage))
	var mu sync.Mutex
	complete := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		if !isInternal(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		failed := false
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/instalacao"):
		case r.Method == http.MethodPost && r.URL.Path == "/api/credentials":
			if complete {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil {
				failed = true
			} else {
				failed = save(r.PostForm.Get("api_id"), r.PostForm.Get("api_hash")) != nil
			}
			if !failed {
				complete = true
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if failed {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = page.Execute(w, failed)
	})
}
