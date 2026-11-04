# Panel HTTP capability boundary

The loopback listener separates callers by route capability rather than treating a valid daemon token as universal panel authority. The capability matrix lives in `internal/httpserver/capabilities.go`; the authentication middleware resolves the registered `http.ServeMux` pattern before selecting credentials.

## Capability matrix

| Capability | Credentials | Examples |
| --- | --- | --- |
| Public | None; still subject to Host, Origin, and preflight guards | `/healthz`, static assets, icons |
| MCP | Bearer token or the desktop's in-process context; browser cookies are never accepted | `/mcp` |
| Programmatic API | Bearer token, browser session, or the desktop's in-process context | `/api/status`, pairing operations, client setup, history, media, transcription, and webhooks |
| Human panel | Desktop in-process context, or browser session with CSRF on unsafe methods | HTML routes, account lifecycle, mode/language/settings changes, connection removal, updater actions, quit, and erase |
| Browser session | Session cookie and CSRF on unsafe methods | `/api/session`, `/api/session/logout` |
| Bootstrap issuance | Separate panel bootstrap authority from the account's private data directory, or the desktop's in-process context; the MCP bearer is explicitly rejected | `POST /api/session/bootstrap` |
| Bootstrap exchange | One-time code issued by the bootstrap endpoint | `GET /bootstrap` |

On the external listener, an `Authorization` header is rejected on every human panel or browser-session route, even if the token is valid and a browser session is also present. The desktop's unforgeable in-process context remains authoritative and ignores incidental headers. The bootstrap endpoint additionally requires its own secret and returns `403 Forbidden` to an MCP bearer. This prevents a daemon bearer from becoming a session or CSRF bypass without breaking the embedded app. `/mcp` remains bearer-compatible for Hermes and other MCP clients.

## Bootstrap authority and operating-system boundary

The daemon creates the bootstrap secret on first start at `accounts/primary/panel-bootstrap.key` below the configured data directory. The account directory is mode `0700` and the file is mode `0600` on Unix. `telegram-mcp open` reads it locally and sends it only to the bootstrap endpoint; generated MCP client configurations contain only the MCP bearer and never this secret. To rotate it offline, stop the service (or fully quit the desktop app), delete only `accounts/primary/panel-bootstrap.key`, and start the service or app again; the next start generates a new value before accepting requests. Existing valid browser sessions remain independent and survive first-time secret creation or rotation according to their normal lifetime.

This separation protects against a caller that has only the MCP client token, including copied client configurations, remote MCP clients, and an agent without arbitrary file access. It does **not** protect against a process running as the same operating-system user that can read arbitrary files in the account's data directory. That is the operating system's account-isolation boundary, not a boundary this loopback service can strengthen by adding another file-backed secret.

## Fail-closed route registration

The programmatic bearer routes are an exact, method-qualified allowlist. Any newly registered route that is not in the allowlist defaults to the human-panel capability. Adding a bearer route therefore requires an explicit capability change and a test proving that programmatic access is intended.

## Request and response safeguards

- Unsafe non-MCP requests are read before the handler with a 64 KiB limit plus one byte. Oversized bodies receive `413 Request Entity Too Large` before any handler effect.
- A non-empty unsafe `/api/` request must use `application/json` and contain exactly one complete JSON value. Unsupported content types receive `415 Unsupported Media Type`; malformed or trailing JSON receives `400 Bad Request`, all before any handler effect.
- Security headers, including the baseline Content Security Policy, are set by the authentication middleware so terminal pages and redirects retain them.
- `GET /api/state` is observational. Stale QR renewal is performed only by the protected `POST /api/pair/refresh` endpoint.
- Client-configuration conflicts return only `{"conflict":true,"client":"…"}`. Existing commands, arguments, credentials, URL user information, queries, and fragments are never returned. Internal labels retain at most a command basename or a URL stripped of user information, query, and fragment.
