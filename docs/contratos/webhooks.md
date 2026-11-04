# Webhooks contract

Version 1 (S0 draft for freezing), 2026-11-01. Source: `docs/PLANO.md` v0.4
§4.2 and `docs/PARIDADE.md` W01–W06 and R01–R10. Owner: J (`internal/webhook`,
`internal/state/webhooks.go`, `internal/panel/webhooks.go`). Source of events:
`Controller.Subscribe` ([engine.md](engine.md) §6).

## 1. What changes relative to the historical upstream product

The left column describes the historical upstream product's `internal/webhook`,
as it stood at `d093b20` (the parent of `015961e`, which replaced the upstream
CLI with the engine controller).

| Historical upstream product (`internal/webhook`) | Here |
|---|---|
| events `message`, `reaction`, `receipt` (`d093b20:internal/webhook/webhook.go:40`) | `message`, `edit`, `delete`, `reaction`, `read` (+ `test`) |
| loopback HTTP relay receiving from the upstream CLI's sync (`d093b20:internal/webhook/relay.go:21-24`) | direct subscription to `Controller.Subscribe`; the relay (`relay.go`) is removed |
| random `delivery_id` per event (`randomHex(8)`; `d093b20:internal/webhook/relay.go:127`) | `delivery_id` deterministic per (webhook, event), stable across redeliveries (§2) |
| `Same(a, b)` for the upstream phone-number/LID identities (`d093b20:internal/webhook/webhook.go:118-120`) | equality of `chat_id`, plus the basic group → migrated supergroup link |
| legacy product-branded upstream headers (`d093b20:internal/webhook/webhook.go:321-323`) | `X-Telegram-MCP-*` (open decision D11, tied to the naming decision) |

Registration, filters, the secret shown once, the signature, the test
button, the in-memory queue, the retry policy and automatic deactivation
keep the same observable behavior (R01–R10).

## 2. Envelope

Each delivery is a `POST` with `Content-Type: application/json` and this body:

```json
{
  "schema": 1,
  "event": "message",
  "delivery_id": "d_3f7c1a9e0b2d4c6e8f1a3b5c7d9e0f12",
  "event_id": "9a1c0e…",
  "occurred_at": "2026-11-01T13:04:05Z",
  "sent_at": "2026-11-01T13:04:05.412Z",
  "source": "update",
  "catch_up": false,
  "test": false,
  "account": { "id": "primary", "self_id": 123456789, "username": "bruno" },
  "chat": { "id": "channel:1234567890", "type": "channel", "kind": "supergroup", "name": "Grupo de teste" },
  "message": { … }
}
```

| Field | Rule |
|---|---|
| `schema` | envelope version; a new field does not change the version, but an incompatible change does |
| `event` | `message`, `edit`, `delete`, `reaction`, `read` or `test` |
| `delivery_id` | `"d_" + hex(SHA-256(webhook_id + "\|" + event_id))[:32]`. It is **unique per webhook and per fact**, the same across all attempts and the same if the same fact is offered again. The receiver deduplicates by it |
| `event_id` | `engine.Event.ID`: the same for every webhook that receives the fact |
| `occurred_at` | Telegram's date (the message's `date`, the edit's `edit_date`); for deletion, reaction and read, the time of receipt, because Telegram provides no other |
| `sent_at` | when the delivery entered the queue. **Does not change between attempts**, so that the body and the signature are identical |
| `source`, `catch_up` | from `engine.Event`. `catch_up:true` means the fact was recovered by `getDifference` after the daemon was down; it did not arrive live |
| `account` | slot and Telegram identity. The phone number is not in the envelope; decision D11 |
| `chat` | `chat_id` in the `type:id` format (engine.md §2), `kind` and name. `null` only in an unresolved `delete` (§3) |
| payload | exactly one key, named after the event: `message` (also in `edit`), `delete`, `reaction`, `read`; `test` uses `message` |

`access_hash`, `file_reference`/`Media.Remote`, local media path, token,
session and the webhook's secret never appear in the envelope.

## 3. Payloads per event

**`message` and `edit`** (W01, W04): projection of `engine.Message`.

```json
"message": {
  "id": 4567, "chat_id": "user:777", "date": "…", "edit_date": null,
  "from_me": false, "sender_id": "user:777", "sender_name": "Ana",
  "text": "…", "entities": [ { "type": "mention_name", "offset": 0, "length": 3, "user_id": 123456789 } ],
  "media": { "type": "voice", "mime_type": "audio/ogg", "bytes": 12345, "duration_seconds": 4.2 },
  "reply_to": { "id": 4560, "sender_id": "user:123456789", "text": "excerpt up to 200 characters" },
  "forward": { "from": "channel:42", "from_name": "" },
  "topic_id": 0, "location": null, "poll": null, "service": null,
  "mentioned": true, "protected": false,
  "ttl_period": 0, "expires_at": null
}
```

- `edit` carries the whole **new** version and `edit_date`. The previous text
  is not sent, because Telegram does not show it to anyone.
- Media: metadata only. The file is not attached and there is no link. Media
  with a timer or view-once (`media_ttl > 0`) arrives as
  `{"type":"…","ephemeral":true}`, without a file name (lifecycle.md §2).
- Message with `ttl_period > 0`: see decision D12. The recommendation is to
  deliver it with `expires_at` and document that the receiver must delete it
  by the deadline.

**`delete`** (W05):

```json
"delete": { "message_ids": [4567, 4568], "unresolved_ids": [], "reason": "deleted" }
```

- `reason` can be `deleted` (Telegram), `ttl` (expired; lifecycle.md §2) or
  `revoked_by_me` (our own `Delete{Revoke}`).
- A deletion that the store resolves to several conversations becomes **one
  event per conversation**. Ids the store does not know (`unresolved_ids`,
  outside channels) go out in an event with `chat:null`, delivered only to
  webhooks without a conversation filter.

**`reaction`** (W02):

```json
"reaction": {
  "message_id": 4567,
  "counts": [ { "emoji": "👍", "count": 2, "chosen": true } ],
  "added":   [ { "by": "user:777", "emoji": "👍" } ],
  "removed": [],
  "to": 4567, "emoji": "👍", "by": "user:777"
}
```

`to`, `emoji` and `by` exist for compatibility with the historical upstream
product's reaction payload (`to`/`emoji`, `d093b20:internal/webhook/webhook.go:95-98`)
and are filled in when the change is a single one with a known author. A
removed reaction has `emoji:""`. In large groups, Telegram often does not say
who reacted; in that case, only `counts` is filled in.

A reaction fact is a **committed state transition**, not a particular count
array. The engine identity includes the committed payload (counts, chosen
state, added and removed reactors) and the store's durable per-message
reaction revision (engine.md §6). Equal totals with a different reactor or
chosen state have different identities. An A → B → A cycle also has distinct
`event_id` and therefore distinct `delivery_id` values, even with anonymous
reactors or changes received at the same instant. The revision is internal;
it is not added to the webhook payload. Exact replay of the currently
persisted canonical snapshot does not emit another event, including after a
database reopen. Redelivery of an existing event retains its identity;
restarting does not make the in-memory webhook queue durable (§6).

**`read`** (W03): only the other side reading **our** messages
(`updateReadHistoryOutbox`/`updateReadChannelOutbox`).

```json
"read": { "max_id": 4570, "message_ids": [4568, 4570] }
```

`message_ids` are our messages that this mark has come to cover, up to 100.
Telegram has no per-person "delivered". In groups, the field indicates that
someone read up to there, without saying who.

**`test`** (W06): `test:true`, `event:"test"`, random `delivery_id` and a
marked sample `message`. It uses the same signature and the same sending path.

## 4. Signature

- Headers:
  - `X-Telegram-MCP-Signature: sha256=<hex(HMAC-SHA256(secret, body))>`, the
    same scheme as `webhook.Sign`;
  - `X-Telegram-MCP-Event: <event>`;
  - `X-Telegram-MCP-Delivery: <delivery_id>`;
  - `X-Telegram-MCP-Attempt: <1..10>`, outside the signature and informative;
  - `User-Agent: telegram-mcp-local/<version>`.
- The secret is 32 random bytes and appears **only once**, in the creation
  response (`POST /api/webhooks`, R04). It does not appear in `GET`, in logs
  or in status.
- Replay protection: the receiver deduplicates by `delivery_id` and may
  reject a `sent_at` older than 5 minutes. The whole retry fits in under one
  minute.
- The sample verification (`docs/api.md`) uses constant-time comparison.

## 5. Filters

| Filter | Rule |
|---|---|
| `events` | subset of `message, edit, delete, reaction, read`. The default is `["message"]` (R03) |
| `include_own` | the default `false` drops `message`/`edit` with `from_me:true`, reactions whose `by` is the account itself and `delete` with `reason:"revoked_by_me"`. `read` is never own |
| `chats` | list of `chat_id` (`user:…`, `chat:…`, `channel:…`). Empty means all. A migrated basic group matches its supergroup, in both directions |
| `enabled` | disabled receives nothing and enqueues nothing |

In registration validation (R01), the URL must be `http` or `https` and the
events must be known. A malformed `chat_id` produces a clear error.

## 6. Queue, retry and deactivation

- Each webhook has an in-memory queue, in order, with `maxQueue=1000`; when it
  fills up, it discards the oldest delivery and counts it (`dropped`).
- Attempts follow the historical upstream product's `retryWaits`
  (`d093b20:internal/webhook/webhook.go:113`): 10 attempts, a 3 s timeout
  each, under one minute in total. Any `2xx` counts as success. Redirects are
  not followed; `3xx` counts as failure.
- If the 10 attempts fail in a row, the webhook is disabled with the reason,
  the queue is discarded and the panel shows the state (R08).
- Each attempt is recorded (`RecordDelivery`: time, result and count),
  without the body.
- A message deleted or expired while its delivery waits in the queue is
  removed before sending, and the corresponding `delete` is enqueued
  normally (lifecycle.md §1).
- **Daemon restart:** pending deliveries **are lost**, because the queue lives
  in memory (R10, and we do not promise durable delivery). Facts that
  occurred while the daemon was off arrive after boot, via `getDifference`,
  as new events with `catch_up:true`. Facts already stored before the crash
  are not re-emitted, because replay is idempotent (store.md I2). This is
  documented in the panel and in `docs/api.md`.

## 7. Mandatory tests (PLANO §4.2 + what this contract adds)

| ID | Case | Acceptance |
|---|---|---|
| WH01 | Registration | Invalid URL, `ftp:` scheme and unknown event → clear error; a valid registration appears in the list |
| WH02 | Edit and removal | `PATCH` changes filters without changing the secret; `DELETE` removes and stops the queue; nonexistent id → clear 404 |
| WH03 | Filters | Matrix event × `include_own` × `chats` × `enabled` in the fake: only the expected combinations receive |
| WH04 | Secret once | Creation returns the secret; individual `GET`, list, status and logs do not contain it |
| WH05 | Signature | The test receiver recomputes the HMAC of the received body and checks it; with the wrong secret, it does not match |
| WH06 | Retry | The receiver fails 3 times and then responds 200: 4 attempts, the same `delivery_id`, the **same body byte for byte** and `Attempt` 1..4 |
| WH07 | Deactivation | The receiver always 500: 10 attempts in < 60 s, webhook disabled with reason and queue discarded |
| WH08 | Same `delivery_id` | The same `Event` offered twice to the manager → the same `delivery_id`; different webhooks → different `delivery_id`s |
| WH09 | Restart | Queue with 5 pending, daemon restarted: the 5 do not arrive (documented); a new message received during the outage arrives with `catch_up:true`; nothing already delivered is repeated |
| WH10 | Events | `message`, `edit`, `delete`, `reaction` and `read` generated in the fake from the updates in the table in engine.md §6, each with the payload on this page |
| WH11 | Outbound-only `read` | `updateReadHistoryInbox` does not generate a webhook; `updateReadHistoryOutbox` does |
| WH12 | Split `delete` | A deletion covering two conversations → two events; unknown id → `chat:null` event only for a webhook without a filter |
| WH13 | No secrets in the envelope | Markers in `access_hash`, `Media.Remote`, media path and secret → absent from the body |
| WH14 | Deleted in the queue | Message enqueued and deleted before sending → not sent; the `delete` is |
| WH15 | Ephemeral media | View-once media → `ephemeral:true`, without file name or size |
| WH16 | Full queue | 1001 deliveries with the receiver stopped → `dropped=1` and the oldest discarded |
| WH17 | Test | The test button delivers a signed `test:true`, without going through the engine |
