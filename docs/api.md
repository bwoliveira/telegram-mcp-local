# Local API

> **Historical API documentation:** This page contains inherited behavior and examples that have not been verified for Telegram MCP. Do not assume that these endpoints, commands, or security properties apply to the current implementation.

Telegram MCP listens at `http://127.0.0.1:47831`, accessible only to programs on this computer. Besides MCP at `/mcp`, it provides a small API for an AI agent (Claude Code, Codex, Cursor…) to install, connect, and check the app without its window. The app and command-line version share the same API; only `/api/app/open` is exclusive to the app.

## Current Telegram triage: bot conversations

The MCP schemas for `list_unread` and `list_unanswered` expose optional boolean
`include_bots`, with **default `false`**. Only direct user conversations whose
persisted peer metadata says `kind=bot` are excluded. `include_bots: true`
includes them subject to the other filters. Groups/channels with bots remain
eligible; missing metadata is not proof of a bot. Filtering precedes result
limits, counts and pagination. Reads stay local and send no receipt.

This behavior is tested with temporary real SQLite stores and an offline fake
engine, not a live account (`internal/mcp/read_bots_sqlite_test.go`).

## Access

- **All private routes**, including reads, `/health`, and `/media/{token}`, require bearer authentication or a local dashboard session. `/mcp` requires bearer authentication and does not accept a browser session. The media URL is not itself a credential.
- **Actions** (`POST`, `PATCH`, `DELETE`) require `Content-Type: application/json`, even without a body. A page from another website open in the browser cannot read or call anything here.
- The local token is mandatory: `Authorization: Bearer $TELEGRAM_MCP_TOKEN`. Use `telegram-mcp config` to obtain the configuration and `telegram-mcp open` to open the authenticated dashboard. Do not put credentials in URLs or logs.
- Session-based actions require `X-CSRF-Token` (obtained from `GET /api/session`). Bearer clients do not need CSRF. Host and Origin are checked; CORS preflight is rejected. Only `/healthz` and public assets are open.
- **The port** is 47831 unless changed in Settings. It is stored in `config.json` (the `port` field) in the data directory:

  | System | Data directory |
  |---|---|
  | macOS | `~/Library/Application Support/Telegram MCP/` |
  | Windows | `%LOCALAPPDATA%\Telegram MCP\` |
  | Linux | `$XDG_DATA_HOME/telegram-mcp/` (or `~/.local/share/telegram-mcp/`) |
  | Command line | `~/.telegram-mcp/` |

`TELEGRAM_MCP_DATA` overrides the default directory. Directory names are storage identifiers and do not change with the displayed branding.

## Endpoints

| Method and path | Description |
|---|---|
| `GET /api/status` | Current state and what to do next (below) |
| `POST /api/pair` | Starts Telegram QR login; `{"method":"phone","phone":"+5511912345678"}` starts phone login |
| `GET /api/pair/qr.png` | Current QR code as PNG while `pairing.state` is `qr` |
| `POST /api/pair/cancel` | Cancels pairing in progress |
| `POST /api/pair/code` | Submits the `code` provided by the person |
| `POST /api/pair/password` | Submits the two-step `password` when requested |
| `POST /api/pair/resend` | Requests a new code when permitted |
| `POST /api/clients/claude-code` | Configures MCP in Claude Code on this computer. `{"replace": true}` replaces an old configuration |
| `POST /api/clients/claude-desktop` | The same for Claude Desktop (chat and Cowork), which must be restarted |
| `POST /api/clients/codex` | The same for Codex (the ChatGPT app, Codex app, terminal and editor), using `codex mcp add` |
| `POST /api/clients/cursor` | The same for Cursor, in `~/.cursor/mcp.json` for all projects (with a backup of the previous file) |
| `POST /api/app/open` | App only: opens the window, optionally at a page: `{"path": "/status"}` |
| `GET /api/webhooks` | Webhooks, including each one's state and last delivery ([below](#webhooks)) |
| `POST /api/webhooks` | Creates a webhook and returns the secret used to sign deliveries |
| `GET /api/webhooks/{id}` | Gets one webhook |
| `PATCH /api/webhooks/{id}` | Changes a webhook; `{"enabled": true}` re-enables a disabled one |
| `DELETE /api/webhooks/{id}` | Deletes a webhook |
| `POST /api/webhooks/{id}/test` | Sends a test delivery immediately and reports the response |
| `GET /api/media` | Disk usage of files downloaded by tools, by type, by conversation and duplicates |
| `POST /api/media/retention` | `{"days":30}` or `0` to keep forever; configures retention **and immediately runs a sweep**, returning `retention_days`, `deleted_files`, `freed_bytes` |
| `POST /api/media/purge` | `{what,older_than_days,confirm}`; `what` is `media` or `exports`. Without `confirm:true`, only previews; when confirmed, returns `deleted_files`, `freed_bytes` |
| `POST /api/media/download` | `{"chat_id":"user:123","message_id":"45"}`; registered local download |
| `GET /api/transcription` | `available`, `supported`, `ready`, `accel`, `missing`, `install` at the top level, without local paths |
| `POST /api/transcription/transcribe` | `{chat_id,message_id,language,refresh,native}`; local by default, native only with explicit `native:true` |
| `POST /api/transcription/save` | `{chat_id,message_id,text,language,model}`; preserves `raw_text` and updates search |
| `GET /api/asr` | Compatibility: state in `status`; prefer `/api/transcription` |
| `POST /api/asr/install` | Explicitly installs sidecars/model and returns progress |
| `GET /health` | Full health report; returns 503 when something fails |
| `GET /healthz` | Only reports that the process is running |

JSON errors use `{"error":"…"}`. Media and webhook APIs use 400 for an invalid request and 404 for a missing resource; inherited dashboard actions can use 409. A 401 indicates missing credentials; a 403 can indicate a rejected Host, Origin or CSRF token. The person must provide the code and password in the local interface, never store them in prompts, logs or command history.

## `GET /api/status`

```json
{
  "app": "Telegram MCP",
  "version": "1.2.0",
  "desktop": true,
  "mcp_url": "http://127.0.0.1:47831/mcp",
  "telegram": {
    "state": "connected",
    "paired": true,
    "connected": true,
    "phone": "+55 (11) 91234-5678",
    "name": "Maria Silva",
    "label": "Personal",
    "last_message_at": "2026-10-06T03:15:49Z"
  },
  "pairing": { "state": "idle" },
  "health": { "status": "ok", "summary": "working", "checks": [] },
  "next": "ready"
}
```

`telegram.connected` is true when Telegram is receiving and ready to send. `next` indicates what to do:

| `next` | Meaning | Action |
|---|---|---|
| `ready` | Connected, receiving and ready to send | Use MCP |
| `pair` | No Telegram session | Open the app or call `POST /api/pair` |
| `scan` | Waiting for a QR scan | Scan `pairing.qr_png`, also shown in the app window |
| `code` | Waiting for the received code | The person enters the code in the local interface |
| `password` | Waiting for the two-step password | The person enters the password in the local interface |
| `connect` | Account manually disconnected | Resume the connection in the dashboard |
| `syncing` | Loading history | Wait |
| `wait` | Starting, reconnecting or briefly paused | Ask again in a few seconds |
| `error` | Synchronization stopped | Read `health.checks`: each failing check says what to do |

On the phone, scan the QR code under *Telegram › Settings › Devices › Link Desktop Device*. Phone login asks for the received code and, if configured, the two-step password; it never returns the code inside `pairing`.

## Webhooks

A webhook is an address that receives a `POST` for each new message. A script on this computer or the network can then respond, log it or send a notification elsewhere. To create one:

```sh
curl -s -X POST -H "Authorization: Bearer $TELEGRAM_MCP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"url":"http://127.0.0.1:8080/telegram","events":["message"],"include_own":false,"enabled":true,"allow_private_network":true}' \
  http://127.0.0.1:47831/api/webhooks
```

| Field | Description |
|---|---|
| `url` | The address, `http://` or `https://` (required) |
| `events` | `message`, `edit`, `delete`, `reaction`, `read` |
| `include_own` | `true` also delivers messages sent by the account itself, from the phone or through tools |
| `chats` | Only these conversations (canonical IDs such as `user:123`, `chat:456`, `channel:789`); empty means all |
| `enabled` | `false` disables without deleting |
| `allow_private_network` | Explicit per-webhook permission for private, loopback and link-local destinations; defaults to `false` |

**Local receivers require explicit consent.** The loopback example above includes `allow_private_network:true`. In **Settings › Webhooks**, select **Allow local and private-network destinations** when creating or re-enabling a local webhook. Leave it unchecked for public destinations. This permission allows Telegram messages to reach services on this computer or the private network: grant it only for destinations you trust. It does not bypass blocked metadata or special-use destinations.

To re-enable a local webhook that has no stored permission (including an older webhook), explicitly grant it in the same request:

```sh
curl -s -X PATCH -H "Authorization: Bearer $TELEGRAM_MCP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true,"allow_private_network":true}' \
  http://127.0.0.1:47831/api/webhooks/WEBHOOK_ID
```

The list returns `{available,webhooks,events,signature,queue_note}`. Creation returns `{webhook,secret,note}`; GET by ID and PATCH return status without the secret. PATCH is partial and preserves omitted fields and the secret. DELETE returns `{deleted:true}`. The creation response returns `secret` only once. Each delivery is a JSON `POST` with these headers:

- `X-Telegram-MCP-Event`: `message`, `edit`, `delete`, `reaction`, `read` or `test`;
- `X-Telegram-MCP-Delivery`: deterministic ID, stable across attempts;
- `X-Telegram-MCP-Attempt`: attempt from 1 to 10, outside the signature;
- `X-Telegram-MCP-Signature`: `sha256=` followed by the hexadecimal HMAC-SHA256 of the exact body bytes with the secret. Verify it before trusting the content.

```json
{
  "schema": 1,
  "event": "message",
  "delivery_id": "d_9f2c41d07a3b8e6551c0a7e2b4d6f810",
  "event_id": "e_9f2c41d07a3b8e65",
  "occurred_at": "2026-10-07T12:00:00Z",
  "sent_at": "2026-10-07T12:00:01Z",
  "source": "update",
  "catch_up": false,
  "test": false,
  "chat": {"id":"user:123","type":"user","kind":"user","name":"Maria Silva"},
  "message": {
    "id": 45,
    "chat_id": "user:123",
    "date": "2026-10-07T12:00:00Z",
    "edit_date": null,
    "from_me": false,
    "sender_id": "user:123",
    "sender_name": "Maria Silva",
    "text": "Hi! Are you coming tomorrow?",
    "topic_id": 0,
    "mentioned": false,
    "protected": false,
    "ttl_period": 0,
    "expires_at": null
  },
  "account": {"id":"primary","self_id":456,"username":"voce"}
}
```

`account` identifies the Telegram account without a phone number or credentials. `chat` can be null for unresolved deletions. Deduplicate by `delivery_id`.

A message with media includes `media` (`type`, `mime_type`, `filename`, `bytes`, `duration_seconds`), without a file or path; the caption is in `text`. `edit` contains the new version with `edit_date`. `delete` contains `message_ids`, `unresolved_ids` and `reason` (`deleted`/`ttl`; `revoked_by_me` only when known, never inferred). `reaction` contains `message_id`, `counts` and authors in `added`/`removed` only when known. `read` contains `max_id` and `message_ids`, never the account's own reads. There are no `delivered` or `played` confirmations.

**Delivery.** Each webhook has a queue, delivered in order. Any `2xx` response counts as delivered; respond within 3 seconds and do heavy work afterward. A failed delivery is retried at increasing intervals, up to 10 times in less than a minute, while later messages wait in the queue. On the 10th failure, the webhook is **disabled** and its pending queue is **discarded**. `GET /api/webhooks` shows `enabled: false`, `disabled_at` and `disabled_reason`; `PATCH` with `{"enabled": true}` re-enables it with an empty queue. `POST /api/webhooks/{id}/test` sends a test delivery (with `"test": true`) without counting toward disabling the webhook.

The same configuration appears under **Settings › Webhooks** in the app and command-line dashboard. The card's **Documentation** button opens a page with an example of each notification type, the fields, signature verification and a minimal Python receiver.

The queue is in memory: **restart loses pending deliveries; catch-up does not re-emit facts already persisted**. The queue is neither a durable log nor exactly-once delivery. The dashboard only registers the documentation page; `httpserver.RegisterMediaAPIs` registers the APIs once on the private mux, within the common authentication boundary.

Media retention defaults to 30 days, exports to 7. Purge without confirmation returns `{preview,would_delete_files,would_free_bytes}` and never deletes from Telegram. The dashboard confirms and sends `confirm:true`. Protected or expired downloads are rejected. Model and sidecar installation occurs only through an explicit action at `/api/asr/install`, never during transcription. Native transcription requires an explicit request, with no automatic fallback or polling. Media errors do not expose paths, message content or subprocess stderr.

## Examples

```sh
# macOS and Linux
curl -s -H "Authorization: Bearer $TELEGRAM_MCP_TOKEN" http://127.0.0.1:47831/api/status
curl -s -H "Authorization: Bearer $TELEGRAM_MCP_TOKEN" http://127.0.0.1:47831/api/media
```

```powershell
# Windows (PowerShell)
$headers = @{ Authorization = "Bearer $env:TELEGRAM_MCP_TOKEN" }
Invoke-RestMethod -Headers $headers http://127.0.0.1:47831/api/status
```
