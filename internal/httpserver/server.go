// Package httpserver serves the MCP endpoint over Streamable HTTP on the
// loopback interface only.
//
// Listening on 127.0.0.1 keeps the network out, but not the browser: any page
// open on this machine can send requests to localhost. The Host check stops
// DNS rebinding and the Origin check stops cross-site requests, so only a
// local program, not a web page, can reach the tools. A mandatory bearer
// token closes private routes to other local programs as well.
package httpserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

const (
	maxBody      = 1 << 20
	maxPanelBody = 64 << 10
)

type Options struct {
	Addr  string // host:port, loopback
	Token string // mandatory for private HTTP routes; empty fails closed
	// PanelBootstrapSecret is a separate local authority that may issue a
	// human browser session. It must never equal or be derived from Token.
	PanelBootstrapSecret string
	Logger               *slog.Logger
	// SessionDir is the account's private data directory. Empty keeps sessions
	// ephemeral (for in-process embedders and tests).
	SessionDir string
	// SessionAccount binds persisted browser sessions to an account slot.
	SessionAccount string
	// Now is an optional clock for session expiration tests.
	Now func() time.Time
	// Trusted recognizes the desktop's unforgeable in-process context only.
	// Never derive this decision from a header, cookie, query or remote address.
	Trusted func(*http.Request) bool
	// Register adds further routes, such as the control panel.
	Register func(*http.ServeMux)
}

// Handler is every route behind the loopback guard: what the HTTP listener
// serves.
func Handler(server *mcp.Server, opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return Guard(Routes(server, opts), logger)
}

// Routes authenticates every route, including everything Register adds. The
// desktop may bypass bearer authentication using Options.Trusted. The network
// listener must additionally wrap this handler in Guard (or use Handler).
func Routes(server *mcp.Server, opts Options) *http.ServeMux {
	mux := http.NewServeMux()
	browser := newBrowserAuth(opts.Now)
	browser.configurePersistence(opts.SessionDir, opts.SessionAccount, opts.Token)
	mux.HandleFunc("GET /bootstrap", browser.exchange)
	mux.HandleFunc("POST /api/session/bootstrap", browser.issue)
	mux.HandleFunc("GET /api/session", browser.info)
	mux.HandleFunc("POST /api/session/logout", browser.logout)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			// Streamable HTTP allows a GET for a server-initiated stream; this
			// server never pushes, so it says so instead of holding a connection.
			w.Header().Set("Allow", "POST")
			http.Error(w, "this MCP endpoint accepts POST", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil || len(body) > maxBody {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// Tool calls that pause sync (history, live lookups) can take minutes;
		// the context ends when the client hangs up.
		// The bridge names the client it speaks for; a direct HTTP client is
		// known by its User-Agent.
		hint := r.Header.Get("X-MCP-Client")
		if hint == "" {
			hint = "ua:" + r.UserAgent()
		}
		response := server.Handle(mcp.WithClientHint(r.Context(), hint), body)
		if response == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	})
	mux.Handle("/media/", server.Links())
	if opts.Register != nil {
		opts.Register(mux)
	}
	// /health is the same report as the health tool, for scripts and curl;
	// it answers 503 when a check fails, so a monitor needs no JSON parsing.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {

		h := server.Health(r.Context(), 0)
		w.Header().Set("Content-Type", "application/json")
		if h.Status == "fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(h)
	})
	// /healthz only says the process is up.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	// Register into a private mux, then put a single authentication boundary in
	// front of it. This covers future panel routes, redirects and method errors
	// without relying on each handler remembering to authenticate.
	outer := http.NewServeMux()
	outer.Handle("/", authenticate(mux, opts, browser))
	return outer
}

func authenticate(next *http.ServeMux, opts Options, browser *browserAuth) http.Handler {
	private := protect(browser.revokeOnAccountLogout(next), opts.Logger, browser)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method == http.MethodOptions {
			http.Error(w, "preflight is not accepted", http.StatusForbidden)
			return
		}
		capability := capabilityFor(next, r)
		if opts.Trusted != nil && opts.Trusted(r) {
			// The desktop context is the authority here. Any incidental auth header
			// is ignored, and legacy in-process JSON calls without a media type are
			// normalized before the shared body checks.
			if unsafeMethod(r.Method) && strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Content-Type") == "" {
				r.Header.Set("Content-Type", "application/json")
			}
			serveAuthorized(w, r, capability, private)
			return
		}
		// Origin and Fetch Metadata constrain the external listener, not the
		// unforgeable in-process desktop authority (including Wails origins).
		if !allowedOrigin(r) {
			http.Error(w, "cross-origin requests are not accepted", http.StatusForbidden)
			return
		}
		if !allowedFetchSite(r) {
			http.Error(w, "cross-site requests are not accepted", http.StatusForbidden)
			return
		}

		// Authorization identifies a program on the external listener. It must
		// never be accepted on a human route, even when a valid browser session
		// accompanies it; otherwise the daemon token becomes a CSRF bypass.
		if (capability == capabilityHuman || capability == capabilitySession) && hasAuthorization(r) {
			http.Error(w, "panel session required", http.StatusForbidden)
			return
		}
		if capability == capabilityBootstrap && opts.Token != "" {
			serveAuthorized(w, r, capability, private)
			return
		}

		bearer := validToken(r, opts.Token)
		id, session := "", false
		if opts.Token != "" {
			id, session = browser.session(r)
		}
		switch capability {
		case capabilityPublic:
			serveAuthorized(w, r, capability, private)
			return
		case capabilityBootstrapIssue:
			if opts.PanelBootstrapSecret != opts.Token && validPanelBootstrapSecret(r.Header, opts.PanelBootstrapSecret) {
				serveAuthorized(w, r, capability, private)
				return
			}
			// The MCP bearer is intentionally insufficient to mint human panel
			// authority. Use 403 for both missing and wrong bootstrap credentials.
			http.Error(w, "panel bootstrap authority required", http.StatusForbidden)
			return
		case capabilityMCP:
			if bearer {
				serveAuthorized(w, r, capability, private)
				return
			}
		case capabilityProgrammatic:
			if bearer {
				serveAuthorized(w, r, capability, private)
				return
			}
		case capabilityHuman, capabilitySession:
			// These deliberately have no bearer branch. They proceed only through
			// the trusted desktop path above or the browser session below.
		}
		if capability != capabilityMCP && !bearer && session {
			if !preparePanelBody(w, r, capability) {
				return
			}
			if unsafeMethod(r.Method) && !browser.checkCSRF(w, r, id) {
				return
			}
			private.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path
		privatePath := path == "/mcp" || strings.HasPrefix(path, "/mcp/") || path == "/health" || path == "/api" || strings.HasPrefix(path, "/api/") || path == "/media" || strings.HasPrefix(path, "/media/")
		if !privatePath && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			// Safe minimal browser mode: never invoke a panel handler and never
			// include status, account data, QR codes or messages.
			if path != "/access" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; form-action 'none'")
				w.Header().Set("Location", "/access")
				w.WriteHeader(http.StatusSeeOther)
				return
			}
			writeAccessPage(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="local-mcp"`)
		http.Error(w, "missing or wrong bearer token", http.StatusUnauthorized)
	})
}

func serveAuthorized(w http.ResponseWriter, r *http.Request, capability routeCapability, next http.Handler) {
	if !preparePanelBody(w, r, capability) {
		return
	}
	next.ServeHTTP(w, r)
}

func preparePanelBody(w http.ResponseWriter, r *http.Request, capability routeCapability) bool {
	if capability != capabilityMCP && unsafeMethod(r.Method) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxPanelBody+1))
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return false
		}
		if len(raw) > maxPanelBody {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		trimmed := bytes.TrimSpace(raw)
		jsonAPI := strings.HasPrefix(r.URL.Path, "/api/")
		if jsonAPI && len(trimmed) != 0 && mediaType != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return false
		}
		if jsonAPI && len(trimmed) != 0 {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return false
			}
			if err := decoder.Decode(&struct{}{}); err != io.EOF {
				http.Error(w, "request body must contain one JSON value", http.StatusBadRequest)
				return false
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return true
}

// Guard rejects any request that did not come from a local program.
func Guard(next http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodOptions {
			http.Error(w, "preflight is not accepted", http.StatusForbidden)
			return
		}
		if !localHost(r) {
			logger.Warn("rejected request with foreign Host")
			http.Error(w, "this server answers only on localhost", http.StatusForbidden)
			return
		}
		// Another localhost app open in the browser is still a foreign origin:
		// the Origin must be this server itself.
		if !allowedOrigin(r) {
			logger.Warn("rejected cross-origin request")
			http.Error(w, "cross-origin requests are not accepted", http.StatusForbidden)
			return
		}
		if !allowedFetchSite(r) {
			http.Error(w, "cross-site requests are not accepted", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	return r.URL.Path != "/mcp" && !strings.HasPrefix(r.URL.Path, "/mcp/") && len(origins) == 1 && origins[0] == "http://"+r.Host
}

func allowedFetchSite(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin" || (site == "none" && r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Mode") == "navigate")
}

func localHost(r *http.Request) bool {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || addr == nil {
		return false
	}
	localIP, localPort, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(localIP)
	port, err := strconv.Atoi(localPort)
	if err != nil || port < 1 || port > 65535 || ip == nil || !ip.IsLoopback() {
		return false
	}
	// Compare the entire authority, not just a permissively parsed IP. This
	// rejects rebinding names, alternate loopback IPs and alternate ports.
	portString := strconv.Itoa(port)
	return r.Host == "127.0.0.1:"+portString || r.Host == "localhost:"+portString || r.Host == "[::1]:"+portString
}

func validToken(r *http.Request, token string) bool {
	values := r.Header.Values("Authorization")
	if token == "" || len(values) != 1 {
		return false
	}
	got, ok := strings.CutPrefix(values[0], "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// NewServer is the HTTP server for a listener. Tool calls that pause sync can
// take minutes, so only reading the request head is bounded.
func NewServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
}

// Shutdown stops a server, giving requests in flight a few seconds.
func Shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
