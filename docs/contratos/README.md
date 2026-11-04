# Stage 1 contracts (workstream S0)

These contracts come before the parallel work (`docs/PLANO.md` v0.4, section 3).
Workstreams S2 (fake), A (engine), B (store), C to K (tools, panel, webhooks,
service) implement against them. A change here requires the coordinator's
approval and notice to the affected workstreams.

| Contract | File | Implementation owners | Consumers |
|---|---|---|---|
| Engine: interface, events, DTOs, historical upstream CLI → `Engine` table | [engine.md](engine.md) + [`internal/engine/engine.go`](../../internal/engine/engine.go) | A (gotd), S2 (fakeengine) | C, D, E, F, G, H, I, J, K |
| Database: DDL, keys, `access_hash`, update state, cursors, mentions, FTS5, checkpoint invariant | [store.md](store.md) | B | A (via `engine.Persister`), C, H, J, K |
| Lifecycle: TTL, deletions, retention, disconnect/log out/erase everything, revocation, loss of access, Secret Chats | [lifecycle.md](lifecycle.md) | A, B, J, K | all |
| Minimum security: token, Host/Origin, CSRF, bootstrap, read-only, preview, logs, injection | [security.md](security.md) | F, A, C, D, E, K, S3 | all |
| Webhooks: envelope, `delivery_id`, HMAC, events, tests | [webhooks.md](webhooks.md) | J | external receivers |

Status: **draft v1 for freezing**. `internal/engine/engine.go` compiles
(`go build` and `go vet` clean with Go 1.26.5). There is no implementation in
this workstream.

## Verification done in S0

- Against gotd v0.163.0 (source code read in `telegram/updates`,
  `telegram/auth/qrlogin`, `session` and `contrib/storage`): a throwaway
  probe, outside the repository, implemented `updates.StateStorage`,
  `updates.ChannelAccessHasher`, `updates.UserAccessHasher` and
  `session.Storage` on top of `engine.Persister`/`engine.SessionStore`, wired
  `telegram.NewClient` + `updates.New` + `qrlogin.NewQR` and passed
  `go build` and `go vet`. The interface can be implemented with gotd without
  exposing its types.
- Against `modernc.org/sqlite` v1.60.1, the same as in `go.mod` (SQLite 3.53.4):
  the DDL from store.md §9 was created without error. Each `msg_id` repeated in
  three namespaces became three rows. Edit and deletion updated the FTS. The
  cascade removed transcript, mention, reaction and media and triggered
  `files_to_unlink`. The forensic check of the file, with `secure_delete`,
  `optimize` and `wal_checkpoint(TRUNCATE)`, did not find the marker in
  `store.db` or in `-wal`. Without `secure_delete`, the marker stayed in the file.
- Real tests with an account: none. Stage 1 does not connect an account.

## Open decisions for the coordinator

Each decision has a recommendation. Until there is a decision, the workstreams
follow the recommendation.

1. **D1 — Transactional checkpoint or idempotent replay.** gotd calls
   `SetPts` after the handler, in separate calls, and ignores the handler's
   error. Because of that, checkpoint and data in the same transaction only
   work with a fork of `telegram/updates`. **Recommendation:** idempotent
   replay with I1–I6 (store.md §5) and the S01–S16 suite. Do not fork now.
   Reassess only if S01/S03 fail with real gotd in Stage 2.
2. **D2 — Shape of `message_id` in the tools.** In Telegram, an id on its own
   does not identify a message. **Recommendation:** integer `message_id` always
   accompanied by `chat_id` (`user:…`, `chat:…`, `channel:…`). Responses also
   carry `ref:"channel:123/456"`, which is accepted as an alternative input.
   An id without `chat_id` produces a clear error.
3. **D3 — One database per slot and merge with `state.db`.** **Recommendation:**
   one `store.db` per account slot (the `account` columns are kept for a future
   merge). `transcripts` and `triage` move to `store.db`, because they need to
   disappear in the same transaction as the message. `webhooks`, `clients` and
   `settings` stay in the global `state.db`.
4. **D4 — Migrated basic groups.** **Recommendation:** separate keys
   (`chat:` and `channel:`), linked by `migrated_to`. `get_chat_messages` for
   the supergroup includes the old group's history when there is
   `include_migrated:true`, with default `true`.
5. **D5 — Scope of remote search.** **Recommendation:** `search_messages` uses
   the local FTS and only calls `messages.search`/`searchGlobal` when the local
   response declares incomplete coverage and the person or the AI passes
   `remote:true`. Remote results enter the store with `partial=1`.
6. **D6 — TTL deadline.** Today the janitor runs every 60 s. **Recommendation:**
   keep 60 s plus an exact timer for the next expiration, when it is less than
   60 s away. This way, the deadline stays close to the second without
   scanning the database continuously.
7. **D7 — History of a conversation with lost access.** The plan says to keep
   it until "erase everything". **Recommendation:** keep and mark it, and offer
   in the panel "erase this conversation's history" (local deletion with a
   `user_purge` tombstone). Do not erase automatically.
8. **D8 — Protected content (`noforwards`) in the export.**
   **Recommendation:** include the text with `"protected":true` and never
   include media. If the owner prefers the conservative side, omit the text too.
9. **D9 — Mandatory fingerprint to confirm a write.** This gives more safety
   and adds one extra call per send. **Recommendation:** require
   `preview_fingerprint` (10 min, single use) on all writes. The cost of one
   extra call is small compared to sending to the wrong "Ana".
10. **D10 — `/media/{link}` requires bearer.** The plan requires a token for
    media too. Some MCP clients download links without headers.
    **Recommendation:** require bearer or cookie. If a real client cannot,
    open an exception only for single-use links with 5 min and 128 bits, with
    the owner's approval.
11. **D11 — Header names and phone number in the envelope.** The headers
    depend on the naming decision (PLANO, decision 3). **Recommendation:**
    provisional `X-Telegram-MCP-*`, changed in a single place when the name is
    fixed. Phone number out of the envelope (the historical upstream product
    sent the account's number, `d093b20:internal/webhook/webhook.go:49-51`):
    `self_id` and `username` are enough to identify the account.
12. **D12 — Messages with `ttl_period` in webhooks.** Many people use the
    global auto-delete timer. Blocking these messages would empty the
    webhooks. **Recommendation:** deliver the text with `expires_at` and
    document that the receiver must delete it by the deadline. Ephemeral media
    is never delivered, and the NDJSON export still excludes messages with TTL
    (lifecycle.md §8).
13. **D13 — Durable panel sessions (supersedes the initial recommendation).**
    Owner-requested browser sessions survive process/host restarts using private,
    account-scoped hash-only snapshots, retaining the 12-hour idle / seven-day
    maximum limits. Logout/account changes revoke them durably; bootstrap codes
    and CSRF process keys remain ephemeral. See security.md §4 and the
    `internal/httpserver/README.md` persistence/verification details.
14. **D14 — Write tools in the catalog in read-only mode.**
    **Recommendation:** list them (parity P33 and a stable catalog for the
    clients), with a warning in the description and an error that points to
    the panel. Hiding them would change the tool list on every mode switch.
