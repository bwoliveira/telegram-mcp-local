# Changelog

Changes to Telegram MCP, with upstream history preserved. Based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [SemVer](https://semver.org/).

## [Unreleased]

### Security

- Route private CI to the homelab pool and public contributions to hosted runners;
  pin checkout by commit and Go 1.26.10 by verified image digest (#36, #37).
- Upgrade Wails to 3.1.0 and patch Go networking/cryptography dependencies (#37).
- Verify native downloads by immutable hashes, FFmpeg signatures and LGPL-only
  build configuration; derive linked-module notices and CycloneDX SBOMs from
  binaries, with inventory/security regression gates in CI (#38).
- Require minisign-authenticated, version-bound release manifests before parsing
  checksums or installing updates; placeholders fail closed (#39).
- Separate human panel capabilities from daemon bearer access, require strict
  bounded JSON and CSRF, redact client conflicts and apply CSP centrally (#40).
- Reject webhook SSRF destinations, DNS rebinding and redirects; require explicit
  localized private-network consent and bound delivery/concurrent dial budgets
  with working fallback across validated addresses (#41).
- Enforce strict MCP arguments, safe approved file roots and URL transport,
  redact contact hashes and require expiring single-use purge fingerprints;
  recover interrupted quarantine without overwriting fresh downloads (#42).
- Preserve trusted Wails in-process authority without weakening external listener
  Origin/Fetch Metadata checks. Windows quarantine recovery handles process
  termination; directory power-loss durability remains Unix-only.
- Redact app logs and drop chat/message identifiers from HTTP logs; widen
  `.gitignore` and narrow secret-scan allowlists (#44).
- Issue panel sessions only with a separate local bootstrap secret, never with
  the MCP bearer; `open` without a display refuses instead of printing the
  one-time link unless `--print` is given (#47).
- Show every material parameter in write previews and bind approval to exactly
  what was shown, for all 15 write tools (#50).
- Keep the previous healthy version until an update reports healthy, roll back
  automatically, and refuse signed versions below the highest one seen (#49).
- Point the runtime at hardened `sidecars-2` packages only; require SBOMs and
  notices for every final artifact, including the Go standard library licence
  (#48).
- Share webhook limits across all accounts in the process and translate NAT64
  only for known prefixes (#46).
- Add a fail-closed public snapshot exporter for the launch repository (#43).
- Strip image text metadata before snapshot scans, bind image review to the
  exported bytes, and parse workflow runners semantically (#52).
- Show the effective anonymous and quiz poll settings in previews, and keep the
  oldest webhooks across all accounts when the process limit is exceeded (#53).
- Record old and new artifact identities for update transactions, keep the
  backup on ambiguous state, and promote the healthy Windows installer before
  the next update (#54).
- Inventory every native file shipped in the AppImage and DEB, accept Debian
  provenance only for identical or reproducibly transformed bytes, validate DMG
  inventories semantically, and allow the sidecar manifest override only in
  development mode (#55).
- Build the AppImage on Ubuntu 24.04 with a symbol-level ABI check, and run
  terminal subcommands such as `serve` headless from the desktop binary (#58).
- Allow a loopback or tailnet update feed and test key only with
  `TELEGRAM_MCP_DEV=1`; keep the Windows update monitor alive after the app
  exits, retry locked files in the NSIS installer, and open the SQLite store
  on Windows paths (#59).
- Validate Windows statistics export ACLs, keep profile photo paths on the
  intended drive, and make the affected tests portable across platforms (#61).
- Pin the production minisign key `CF15D44BF3EA0B47` and version 1.0.0 in
  `install.sh`, refuse any other key, and document manual signature
  verification (#62).
- Protect downloaded media on Windows with protected DACLs and port native test
  fixtures to Windows (#63).
- Restore a passing public snapshot rehearsal: replace a private-network
  address in the development feed tests with a synthetic one, use a reserved
  `.invalid` address in a native inventory fixture, and record exact line
  exceptions for third-party license author addresses and the documented
  development feed address range.

### Added

- Static site in Portuguese, English and Spanish with overview, download and
  headless pages; the preview is protected by Cloudflare Access and is not a
  public launch.
- Local Account conversation preview with SQL-paginated name/unread/type filters,
  message history and FTS5 text/transcript search, preserving the inherited JSON
  contract without Telegram reads or read receipts while browsing. Background
  refresh keeps reconciling during a search, shows and backs off list or
  conversation failures independently, and pages messages by a stable
  `(date, id)` cursor (`before`/`since` on `GET /api/chats/{jid}`).
- Explicit bounded history import through the existing Telegram backfill
  controller, with visible unavailable/error states instead of pending handlers.
- Fixture-backed headless browser coverage for conversation pagination/search,
  Status loading and client discovery, plus per-route performance budgets and
  authenticated curl/phase measurement harnesses. See [panel evidence](docs/PANEL_PARITY.md).

- Stage 4 desktop tray connection/login states and per-user macOS/Windows/Linux autostart, retaining the embedded panel and close-to-tray behavior.
- First-use API credential form with strict validation and existing vault persistence; headless environment seeding is preserved. CLI installer with SHA-256 checks and `open --print` one-use remote panel URLs for SSH tunnels.
- Private clean-tag release scripts for Linux amd64/arm64 AppImage and DEB, Windows NSIS, five CLI archives, checksums and third-party notices. Separate universal macOS Developer ID signing, notarization and stapling, plus explicit unsigned test mode.
- Opt-in updater integrity/state tests and a local release-server v1-to-v2 real executable replacement/restart test. Checks never install automatically; DEB remains manual. See [Stage 4 evidence and native QA limits](docs/E4-DISTRIBUTION.md).

- Private headless foundation: gotd by default, per-account SQLite, text and transcript FTS5, boot TTL and a supervised janitor.
- Read tools, explicit text sending and read receipts with previews, single-use fingerprints and read-only mode by default. QR renewal, phone/code/2FA login and readiness handling after logout or revocation.
- Telegram panel with one-use bootstrap session, CSRF protection, explicit mode confirmation, conversation coverage, client setup backups, Hermes Agent guide, recipes, help, settings, webhooks and transcription controls. Disconnect, log out and erase-all are distinct actions.
- Mandatory token, Host/Origin validation, payload-free transport logging, negative security tests, secret storage for API credentials and session, and durable write-mode intent.
- Stage 3 fronts for messages and groups, triage and analytics, media and transcription, webhooks, panel, shared peer resolution, persistent status and offline recovery. The all-40 fake-backed dispatcher test and the offline P41 installer test are present; they do not establish real Telegram parity.
- Master plan, parity matrix, contribution rules, credits, licensing, third-party notices and private self-hosted CI configuration.

### Changed

- Project identity is Telegram MCP, an independent project not affiliated with Telegram. The future site is <https://telegram-mcp.bwoliveira.xyz>; no publication is implied.
- Version-control protections exclude local databases, sessions, media and models. Inherited panel `/whatsapp*` routes were removed; third-party notices include adapted telegram-cli mention logic and the Apache-2.0 gotd logger.
- Private CI is configured for `go test -race -count=1 ./...`. **Final combined-tree vet, tests, build, packaging and CI gates await coordinator evidence**; prior branch results do not certify this tree. Real-account tests and service installation have not been performed as part of this documentation delivery.

### Fixed

- Browser panel sessions now survive daemon, desktop and host restarts using
  account-scoped private, atomic hash-only snapshots. Idle expiry remains 12
  hours and absolute expiry seven days; logout, account changes and bearer
  rotation revoke access. Locked reloads prevent overlapping processes from
  restoring revoked cookies; activity checkpoints are coalesced to one minute.
- Public panel access instructions now match the panel style and negotiate
  Portuguese, English or Spanish from `Accept-Language`, explaining local
  `telegram-mcp open` and remote `open --print` with an SSH tunnel. Existing
  loopback, Host/Origin, CSRF and single-use bootstrap requirements are unchanged.
- Panel pages no longer synchronously calculate global history/health or discover
  client CLIs. Heavy telemetry loads asynchronously through an event-invalidated,
  short-lived cache; lifecycle/mode stay live and polling has loading/error feedback.
- Status uses grouped conversation coverage and SQL activity aggregates rather
  than per-chat global counts and full-message materialization. Transactional V8
  adds a covering index, preserving main's V5/V6/V7 migrations and validating
  unique, contiguous migration versions before opening the database.
- Conversation refresh removes deleted/expired text, updates edited messages and
  transcripts, enables imported-history pagination, and retains loaded lists over
  100 conversations without accepting stale in-flight responses.

- Tool catalogs now advertise supported media types and refresh when the bridge observes changed definitions. Tool descriptions state availability limits directly, and terminal and backend diagnostics use English independently of the panel language.
- Supported attachments expose download metadata, and successfully sent media is ingested before immediate download attempts. Opaque wide Telegram IDs are serialized as decimal strings for clients that cannot represent 64-bit integers exactly.
- Reaction-only updates reach event consumers without false visible edits; partial reaction snapshots preserve known state. Hidden content revisions retain their ordering across restarts.
- New channel dialogs are reconciled with FloodWait-aware retry, migrated groups no longer duplicate chat listings, and group approvals bind relevant permissions instead of unrelated display changes. See [offline polish evidence](docs/REAL_ACCOUNT_POLISH.md); real-account acceptance and final combined-suite/CI verification remain pending.
- QR post-scan handling avoids inappropriate key revocation during DC migration, including late Self responses. Post-authorization failures attempt remote logout and retain recoverable credentials if cleanup fails. QR renewal/retry, terminal CLI reasons and authorization serialization were improved.
- Logs preserve safe templates and metadata without credentials or payloads. Stale messaging claiming the real engine was not integrated was removed.
- Stage 3 integration adds shared conversation resolution and durable account-scoped transactional media purge; its queue allows unlink recovery. S16's end-of-timeout `SQLITE_INTERRUPT` was addressed by draining admitted operations and using a separate SQL context; final combined-suite evidence remains pending.

### Pending integration and validation

- The coordinator is completing the shared `MediaTool` dispatcher, private `RegisterMediaAPIs` wiring, immediate A28 retention sweep, optional engine capability forwarding and media aliases. Until verified in the combined tree, do not treat these seams as complete.
- Real Telegram verification, explicit approval of limits, desktop/platform QA and release remain pending. See [Stage 3 integration](docs/E3-INTEGRACAO.md) and [parity](docs/PARIDADE.md).

The entries below are the history of Bruno Orlandi's **WhatsApp MCP Local**, not Telegram MCP releases. Upstream tags are not releases of this fork.

## [1.6.0] - 2026-10-30

### Added
- The wacli version appears under Settings › Version and updates.

### Changed
- Large file and audio sends no longer fail at a fixed 45-second timeout; the timeout scales with file size. Interrupted sends no longer duplicate messages.
- Archiving, pinning and marking read no longer wait for the phone, which could previously take five minutes. New messages restore archived chats according to WhatsApp's keep-archived setting.
- Withdrawn poll votes no longer reappear after restart or old history. High-resolution link previews are the default.
- Windows supports the new wacli named pipe with fallback to the previous socket. Fixed pairing on some Linux ARM64 servers; thanks @mhdsilva (#10). Security updates include Go 1.27.3 and x/net.

### Attention
On first launch after updating, wacli resynchronizes archived, pinned and muted state. If that fails, the marks may disappear until WhatsApp is opened on the phone; messages are not lost.

Includes wacli 0.25.1.

## [1.5.0] - 2026-10-14

### Added
- English and Spanish alongside Portuguese in the panel, tray, notifications and app; new installations follow the computer's language.
- Language buttons with flags and hover labels during setup, QR pairing and Settings › Language. Changes take effect immediately without interrupting a connection; System follows the computer.
- A Portuguese, Spanish and English Windows installer, a localized Linux shortcut, and English and Spanish website and README with a language selector.
- CLI panel language selection through Settings or `WHATSAPP_MCP_LANG` (`pt-BR`, `es`, `en`).

### Changed
- Existing users remain in Portuguese after updating even when the computer uses English or Spanish; Settings › Language changes it.
- Language affects the interface only: messages, names, values and webhook notifications, including tests, remain unchanged for AI tools.

Includes wacli 0.20.0.

## [1.4.0] - 2026-10-10

### Added
- Name the connected number, for example Personal or Work; AI tools see that label in the connection title and status.
- Webhook notifications include `account`, with the number and its label.

### Changed
- Internally, the connected number became a complete unit in preparation for multiple numbers. Single-number users keep the same folders, MCP address and AI connections.

Includes wacli 0.20.0.

## [1.3.1] - 2026-10-09

### Fixed
- Reactions now reach recipients in individual and group chats, including reactions to one's own group messages.

### Changed
- Settings › Version and updates now shows only the installed version and a current/new-version badge.

Includes wacli 0.20.0.

## [1.3.0] - 2026-10-07

### Added
- Replies quoting messages and @mentions; static and animated WebP stickers, including previously sent or received stickers.
- Draft previews before sending, forwarding, marking chats read, typing indicators, and confirmed delete-for-me or delete-for-everyone.
- Group administration: add, remove and promote participants, change name and description, get or reset invite links and leave. Removal, reset and leaving require confirmation.
- Lists of unanswered and unread chats and mentions, with waiting time and optional group mentions; closing replies such as “ok” or “thank you” can be ignored.
- Local handled and snooze marks; message counts by chat, person, day or month, limited reads, message context and full-chat exports.
- Guided connections for Claude Desktop, Claude Code, ChatGPT / Codex, Cursor and other tools, with one-click setup for Codex and Cursor, automatic completion and client logos.
- Help tab, light/dark/system themes, downloaded-file inventory and manual or automatic deletion, also available to AI tools.
- Webhooks with add, test, disable, delete, delivery status, automatic disabling on failure, signature documentation and [local API](docs/api.md#webhooks).

### Changed
- Settings moved to the gear and now collects port, startup/closing, transcription, updates, appearance and data; tabs became Connect MCP, WhatsApp, Status, Features, Recipes and Help.
- The Mac `.dmg` prompts users to drag the app to Applications. Archiving, pinning and muting no longer pause message reception.
- Large files use a local link when over 20 MB or unsupported by the AI tool; photos, audio and documents otherwise arrive intact. Transcription settings show enabled status; usage moved to Help.

Includes wacli 0.20.0.

## [1.2.0] - 2026-10-06

### Added
- README prompt for Claude Code or another command-running agent to download the correct version, install, open the app, guide QR pairing and check operation.
- [Local agent API](docs/api.md) for app and WhatsApp status, QR or phone-code pairing, Claude configuration and opening the window.

### Changed
- Requesting a phone code replaces the displayed QR code.

Includes wacli 0.20.0.

## [1.1.1] - 2026-10-06

### Fixed
- The Status tab no longer shows a false warning during the first seconds while receiving is connected but sending is still loading.
- Status and connection pages refresh in the background without changing pages, waiting if a dialog is open or the user is typing.

Includes wacli 0.20.0.

## [1.1.0] - 2026-10-06

### Changed
- Closing the window, including with ⌘Q, keeps the app running in the Mac menu bar and AI tools connected.
- Fully quitting from the menu-bar/Windows notification icon, app menu or Settings requires confirmation and warns that MCP and message reception stop.

### Fixed
- On Mac, the window no longer appears over another full-screen app; it opens on its own desktop or in the full-screen display.

Includes wacli 0.20.0.

## [1.0.0] - 2026-10-05

The first public release of WhatsApp MCP Local.

### Added
- Audio transcription installs and verifies the correct programs for macOS, Windows or Linux; it uses Metal on Mac and CUDA on Windows with NVIDIA.

### Changed
- The project became **WhatsApp MCP Local** at [BrOrlandi/whatsapp-mcp-local](https://github.com/BrOrlandi/whatsapp-mcp-local); 0.1.x users receive it via automatic update.
- CLI data moved to `~/.whatsapp-mcp` and the service became `whatsapp-mcp`; users of the 0.1.x service must uninstall it with the old version and reinstall.

Includes wacli 0.20.0.

## [0.1.1] - 2026-10-05

### Added
- An update notice on every screen offers one-click updates; MCP is unavailable only during restart. Update checks immediately show progress and whether a new version exists.

### Changed
- Update checks run twice daily rather than daily. The app shows Loading and refreshes while WhatsApp connects. Connection and tool-count labels refer to MCP, and Status uses less technical language.

### Fixed
- On Windows, closing the app did not always close the WhatsApp connection when running without a console window.

Includes wacli 0.20.0.

## [0.1.0] - 2026-10-05

The first WhatsApp MCP app release.

### Added
- macOS, Windows and Linux app: open, scan a phone QR code and connect Claude without a terminal or browser.
- Menu-bar operation, connection indicator and startup on login; closing the window leaves the app running.
- One-click connection for Claude Desktop (chat and Cowork) and Claude Code; copyable configuration for other MCP clients such as Codex.
- Configurable port with detection and selection of a free port if 47821 is occupied; Claude Desktop follows the change.
- Daily update checks and one-click installation with download verification; erase all local data and disconnect WhatsApp from Settings.

### Changed
- Requested audio transcription runs locally with Whisper large-v3-turbo; OpenAI key support is planned for a future version. The server/agent CLI is now named `whatsapp-mcp`.

Includes wacli 0.20.0.

[1.6.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.6.0
[1.5.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.5.0
[1.4.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.4.0
[1.3.1]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.3.1
[1.3.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.3.0
[1.2.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.2.0
[1.1.1]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.1.1
[1.1.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.1.0
[1.0.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v1.0.0
[0.1.1]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v0.1.1
[0.1.0]: https://github.com/BrOrlandi/whatsapp-mcp-local/releases/tag/v0.1.0
