# HTTP boundary and workstream F integration

Loopback only, Telegram port **47831**. There is no
HTTP mode without credentials. `serve` creates a 32-byte token in the S3 backend;
`bridge` and the separate bridge binary only read an existing credential.

## Panel session (contract with E)

1. The CLI sends `POST /api/session/bootstrap`, bearer, body `{}`; response
   `{"url":"http://127.0.0.1:47831/bootstrap?c=..."}`. Do not log this URL.
2. The browser GETs the URL once, within 60 seconds; it receives a
   `tgmcp_session` cookie and **303 /**. Only hashes of code and session are
   used for lookup. Bootstrap codes stay only in memory; browser-session hashes
   and creation/last-activity timestamps are also saved privately under the
   account's `browser/` directory (see below).
3. `GET /api/session`, with cookie, returns `{"csrf_token":"..."}`. The panel
   embeds/obtains this token and uses `X-CSRF-Token` for every POST/PATCH/DELETE.
   A valid Origin or `Sec-Fetch-Site: same-origin` is also required; JSON or form.
4. `POST /api/session/logout` uses cookie + CSRF; it revokes the session. Without
   a cookie, private pages redirect to `/access`, containing only static instructions.
5. `/mcp` **never** accepts a browser session; it always requires bearer and no Origin.

Workstream E registers `POST /api/mode`: session + CSRF, JSON
`{"mode":"read_write","confirm":true}` (or `read_only`), human confirmation,
`Controller.SetMode` and mode persistence. The boundary blocks bearer on that
route; **F does not replace E's handler/checkbox**, nor wire engine A.
`telegram-mcp config set mode read_write` obtains an ephemeral session, sends
the same confirmed request and ends its session; it is not an MCP tool.

Daemon A must also use `httpserver.Redact` in the Telegram transport logger.
`serve` already installs this policy as the global logger. Free-form messages
are suppressed; only allowed numeric attributes and safe stack/type pass.
HTTP errors have generic bodies; panics never log their value. Codes and 2FA
passwords have separate budgets of five attempts per minute.

## Durable browser sessions

The daemon configures `<data>/accounts/primary/browser/browser-sessions.json`.
The directory is `0700`, the snapshot and stable lock file are `0600`, owned by
this user; Windows uses a protected owner-only DACL instead of POSIX mode bits.
Unsafe existing permissions, symlinks/reparse points and I/O errors fail closed
for browser authentication without exposing paths or credentials in responses.
Corrupt/oversized/unsupported-version snapshots are bounded, discarded and
replaced; expired hashes are removed before use and on restart. No raw cookie,
bearer, CSRF key/token or bootstrap code is written. The namespace is bound to
the account slot, absolute data path and bearer, so copying to another account
or rotating the bearer cannot carry authorization across that boundary.

Every operation reloads under an OS file lock. Last-activity updates remain in
memory and checkpoint at most once a minute; creation and revocation are written
synchronously using private temporary files, file fsync, atomic replacement and
Unix directory fsync. A crash may conservatively lose less than one minute of
idle activity, never extend the 12-hour idle or seven-day absolute limit. The
cookie also has a seven-day browser lifetime; the server remains authoritative.
Logout is never reported successful before the new snapshot is durable. Account
logout invalidates all cookies and outstanding links before changing identity,
after normal authentication/CSRF checks. An unsuccessful account logout can
therefore require a fresh `telegram-mcp open`. A failed save fences browser
authentication and returns an error; it cannot promise durable revocation while
the filesystem is unwritable. Bootstrap links and CSRF process keys never
survive a restart; an existing page obtains its fresh CSRF token from the panel.

Unix race tests exercise permissions, corruption, expiration, coalescing,
overlapping servers and logout. The fake-engine daemon test stops/starts actual
loopback listeners with the same data directory and browser cookie jar. Windows
ACL/locking tests are included and cross-compiled here, not natively executed.
No real Telegram/service installation is part of this evidence.

The focused restart/localization regressions were also exercised against the
pre-change HTTP and daemon wiring (`1d3a8b0`, using a Go overlay that adds only
inert option fields for test compilation): both restart tests fail because the
snapshot is absent, and the access-language test fails because the old static
page has no language header. The same tests pass without the overlay. The
focused suite additionally covers expiry cleanup, unsafe-mode/ACL refusal,
account isolation, durable revocation and cross-process lock contention:

```sh
go test -race -count=1 -timeout 30m ./internal/httpserver ./internal/daemon \
  -run 'TestPersistentSession|TestBrowserSessionSurvivesDaemonRestart|TestSession|TestAccess' -v
```

## Installation (operator approval required)

The real Factory, store and panel are wired. The commands below are a Linux
source-install reference, not permission to install a service or connect a real
account. Use an installed Go toolchain on `PATH` and an approved source checkout.
Provide `TELEGRAM_API_ID`/`TELEGRAM_API_HASH` through a local secret prompt, never
in process arguments or shell history; installation persists them in the secrets
backend before creating the service.

```sh
cd /path/to/telegram-mcp-local
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/telegram-mcp" ./cmd/telegram-mcp
go build -o "$HOME/.local/bin/telegram-mcp-bridge" ./cmd/telegram-mcp-bridge
# The commands below change the local service or start account authorization.
# Run them only after the operator approves those actions.
"$HOME/.local/bin/telegram-mcp" service install
"$HOME/.local/bin/telegram-mcp" service status
"$HOME/.local/bin/telegram-mcp" auth --qr
```

Configure the MCP client's stdio command as
`$HOME/.local/bin/telegram-mcp-bridge` (expand `$HOME` to an absolute path if the
client does not expand it). Do not put codes, passwords, tokens or `api_hash`
in process arguments. If choosing a custom folder, use the same
`TELEGRAM_MCP_DATA` for service installation and the bridge.

The service is **systemd --user telegram-mcp.service**, `UMask=0077`, restarted
on install/reconfiguration. The unit does not contain the bearer. The fallback
encrypts the token in `<data>/secrets`; the key is OUTSIDE that tree, in a
sibling `.telegram-mcp-keys/<sha256-data-path>` folder. A data backup alone
cannot decrypt it. Do not remove that sibling folder during maintenance or
reinstallation. `service uninstall` does not delete the account or data.

## Evidence and limits

Tests exercise the headless binary, stdio bridge, offline unit generation and
syntax, concurrent bootstrap, expiry, restart, logout, CSRF, credential-free
QR/status/media, real registry, Host/Origin, read-only, fingerprints/replay,
injection, protected content, permissions with umask 000, copy without key and
logs. No test installs a service, runs real systemctl or connects to Telegram.

`contract_integration_test.go` and the gotd/store/MCP proof close the previously
unavailable local reads. Explicit skips remain for what the fake does not
demonstrate: I03 (opt-in model), R02 full RPC catalog, I01 external side effects,
I02 real Telegram confirmation, F01 real Factory files and L01 real transport.
These skips **do not mean full security/Stage 2 readiness**. The combined
A/B/C/D/E/F suite must close them; do not enable a real account based only on
workstream F tests.
