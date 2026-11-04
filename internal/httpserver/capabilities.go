package httpserver

import "net/http"

type routeCapability uint8

const (
	capabilityHuman routeCapability = iota
	capabilityProgrammatic
	capabilityMCP
	capabilitySession
	capabilityBootstrap
	capabilityBootstrapIssue
	capabilityPublic
)

// programmaticRoutes is the bearer allowlist. Every registered route not in
// this table is a human panel route by default and therefore requires either
// the desktop's in-process context or a browser session (plus CSRF on writes).
// Keep method-qualified patterns exact so adding a route cannot silently grant
// the daemon bearer access to it.
var programmaticRoutes = map[string]struct{}{
	"/health":                            {},
	"/media/":                            {},
	"GET /api/status":                    {},
	"GET /api/state":                     {},
	"GET /api/pulse":                     {},
	"GET /api/panel/status":              {},
	"GET /api/panel/health":              {},
	"GET /api/panel/clients":             {},
	"GET /api/chats":                     {},
	"GET /api/chats/{jid}":               {},
	"GET /api/search":                    {},
	"POST /api/history":                  {},
	"GET /api/pair/qr.png":               {},
	"POST /api/pair":                     {},
	"POST /api/pair/refresh":             {},
	"POST /api/pair/cancel":              {},
	"POST /api/pair/code":                {},
	"POST /api/pair/password":            {},
	"POST /api/pair/resend":              {},
	"POST /api/clients/{client}":         {},
	"GET /api/asr":                       {},
	"POST /api/asr/install":              {},
	"POST /api/app/open":                 {},
	"GET /api/webhooks":                  {},
	"POST /api/webhooks":                 {},
	"GET /api/webhooks/{id}":             {},
	"PATCH /api/webhooks/{id}":           {},
	"DELETE /api/webhooks/{id}":          {},
	"POST /api/webhooks/{id}/test":       {},
	"GET /api/media":                     {},
	"POST /api/media/retention":          {},
	"POST /api/media/purge":              {},
	"POST /api/media/download":           {},
	"GET /api/transcription":             {},
	"POST /api/transcription/transcribe": {},
	"POST /api/transcription/save":       {},
}

func capabilityFor(mux *http.ServeMux, r *http.Request) routeCapability {
	_, pattern := mux.Handler(r)
	switch pattern {
	case "/mcp":
		return capabilityMCP
	case "GET /bootstrap":
		return capabilityBootstrap
	case "POST /api/session/bootstrap":
		return capabilityBootstrapIssue
	case "GET /api/session", "POST /api/session/logout":
		return capabilitySession
	case "/healthz", "GET /assets/", "GET /favicon.svg", "GET /favicon.ico", "GET /apple-touch-icon.png":
		return capabilityPublic
	}
	if _, ok := programmaticRoutes[pattern]; ok {
		return capabilityProgrammatic
	}
	return capabilityHuman
}

func hasAuthorization(r *http.Request) bool {
	return len(r.Header.Values("Authorization")) != 0
}
