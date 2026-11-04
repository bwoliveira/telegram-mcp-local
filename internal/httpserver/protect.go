package httpserver

import (
	"log/slog"
	"net/http"
	"reflect"
	"runtime"
	"time"
)

// protect runs after authentication. Invalid anonymous requests cannot exhaust
// the pairing budget. It never logs panic values or request payloads.
func protect(next http.Handler, logger *slog.Logger, browser *browserAuth) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &privateResponse{ResponseWriter: w}
		w = response
		defer func() {
			if value := recover(); value != nil {
				pcs := make([]uintptr, 32)
				n := runtime.Callers(3, pcs)
				slog.New(Redact(logger.Handler())).Error("HTTP handler panic", "panic_type", reflect.TypeOf(value), "stack", pcs[:n])
				if response.status == 0 {
					http.Error(w, "internal server error", 500)
				}
			}
		}()
		if r.Method == http.MethodPost && (r.URL.Path == "/api/pair/code" || r.URL.Path == "/api/pair/password") {
			browser.mu.Lock()
			now := browser.now()
			if !now.Before(browser.pairWindow.Add(time.Minute)) {
				browser.pairWindow = now
				browser.pairAttempts = [2]int{}
			}
			slot := 0
			if r.URL.Path == "/api/pair/password" {
				slot = 1
			}
			browser.pairAttempts[slot]++
			limited := browser.pairAttempts[slot] > 5
			browser.mu.Unlock()
			if limited {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "too many login attempts", 429)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
