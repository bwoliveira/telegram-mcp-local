# Engine contract (`internal/engine`)

Version 1 (S0 draft for freezing), 01/11/2026. Source: `docs/PLANO.md` v0.4,
section 3 and Stages 1 to 3. The contract's code lives in
[`internal/engine/engine.go`](../../internal/engine/engine.go): types and
interfaces only, no implementation. When this text and the file disagree, this
text prevails and the file must be fixed.

## 1. Role and boundaries

The engine replaces the historical upstream CLI integration package. It is the only package that imports gotd
and talks to Telegram. The other packages talk to it through the types of this
contract.

| Who | Receives | Must not |
|---|---|---|
| `internal/mcp` (C, D, G, H, I, J) | `engine.Engine` | change the mode, log out of the account, disconnect, log in |
| `internal/panel`, `internal/daemon` (E, F, K) | `engine.Controller` | read `access_hash`, the session or `file_reference` |
| `internal/webhook` (J) | `Controller.Subscribe` | listen to events that were not persisted |
| `internal/store` (B) | implements `engine.Persister` | call Telegram |
| `internal/secrets` (S3) | implements `engine.SessionStore` | know the session format |
| `internal/engine/fakeengine` (S2) | implements `engine.Controller` without network | — |

Fixed rules:

1. **No gotd type leaves the engine.** The fake, the MCP and the panel compile
   without gotd. `access_hash`, `file_reference`, the QR token and the session
   bytes do not appear in any serialisable DTO (`json:"-"`, opaque `Media.Remote`).
2. **Local reads belong to the store.** `list_chats`, `get_chat_messages`,
   `search_messages`, `get_message_context`, `list_contacts`, `list_groups`,
   `list_unread`, triage, statistics and export read `internal/store`.
   The engine only does what requires the network, such as refreshing dialogs
   or fetching a missing window, and writes the result to the store before
   responding.
3. **Persist before emitting.** The engine builds an `engine.Batch` and calls
   `Persister.Apply`. Only what made it into `Applied` becomes an `Event`
   (see [store.md](store.md), invariants I1–I3). An event never describes
   something the store does not have.
4. **Writes go through a single gate.** Every write checks `Mode()` and
   `DryRun` inside the engine, below the MCP layer. That way, a bug in a handler
   cannot send in read-only mode ([security.md](security.md) §7–§8).
5. **Reading does not mark as read.** No read method calls
   `messages.readHistory`, `channels.readHistory`,
   `messages.readMentions`/`readReactions`, `updateStatus(online)` or
   `messages.getMessagesViews(increment=true)`. Only `MarkRead` sends a
   receipt. Negative test M1 (§8).
6. **No global state.** One instance per `AccountID`, because the inherited
   daemon already supports several accounts (`internal/daemon/account.go`).

## 2. Identity

- `PeerRef{Type, ID}`, in the text form `user:<id>`, `chat:<id>` or
  `channel:<id>`, is the `chat_id` of tools, `/api`, webhooks, triage and
  transcripts. The id is the raw Telegram id, without the Bot API's `-100`
  prefix. The `access_hash` never appears in this form.
- `MessageKey{Account, Peer, ID}` is the only message identity. Outside
  channels, the id is sequential per account, but the contract does not use
  that property as a key. It serves only to resolve `updateDeleteMessages`
  (`Persister.ResolveMessageIDs`).
- An id on its own never identifies a message in the tools. `message_id` comes
  with `chat_id`, or uses the composite form `<chat_id>/<id>` (e.g.
  `channel:12345/678`). See open decision D2 in the [README](README.md).
- Migrated basic group: `Peer.MigratedTo` points to the supergroup. The two
  conversations keep distinct keys (`chat:` and `channel:`), and the store
  links them for reading only (store.md §3).

## 3. Interfaces

```go
type Engine interface      // what the MCP uses
type Controller interface  // Engine + Run, Changes, Subscribe, login, Disconnect/Connect/LogOut, SetMode
type Persister interface   // implemented by internal/store (front B)
type SessionStore interface // implemented by internal/secrets (front S3)
type Factory func(Config) (Controller, error) // gotd and fakeengine
```

### Optional capabilities consolidated in Stage 3

The named interfaces below belong to `internal/engine/engine.go`; consumers
must not declare divergent anonymous versions:

- `ConfirmedDeleter.DeleteConfirmed(ctx, DeleteRequest, confirmed bool)` separates
  human approval from the fingerprint. MCP sends only `confirm && !dry_run`.
  The daemon's stable controller forwards the capability; its absence never
  authorises a global revocation by inference from the fingerprint.
- `HistoryProgressReader.HistoryProgress(ctx, PeerRef)` queries local progress,
  without starting a backfill. The daemon forwards the capability when
  available.
- `CursorReader.Cursor(ctx, AccountID, PeerRef)` allows querying the durable
  checkpoint in the store, taking priority over the engine's optional method.
- `ReadyWaiter.WaitReady(ctx)` synchronises asynchronous startup before login.

These capabilities do not give the MCP access to login, mode or lifecycle.

Full signatures: `internal/engine/engine.go`. Common semantics:

- Every method honours `ctx`. Cancelling the call never leaves a half-done
  write in the store, because `Apply` is a transaction.
- Before login, every network method returns `ErrNotAuthorized`. After
  `Disconnect`, it returns `ErrPaused`. After an external revocation, it
  returns `ErrRevoked`.
- Errors are sentinels comparable with `errors.Is`: `ErrPeerNotFound`,
  `ErrNoAccess`, `ErrPermission`, `ErrProtectedContent`, `ErrReadOnly` etc.
  The types `*AmbiguousError`, `*FloodWaitError` and `*RPCError` carry details.
  `RPCError.Type` preserves Telegram's name (`CHAT_ADMIN_REQUIRED`) so that the
  tool can show it to the user (PLANO §4.3).
- FloodWait: gotd's `floodwait` middleware waits up to `Config.FloodWaitMax`.
  Beyond that, the call returns `*FloodWaitError` with `Until`; the engine does
  not get stuck and does not retry. Backfill records `flood_wait` and
  `flood_wait_until` in the cursor.
- Resolution: `Resolve` looks in the store first, by name, `@username`,
  phone and `type:id` form. Only with `Network=true` does it use
  `contacts.resolveUsername`, `contacts.resolvePhone` or `contacts.search`.
  More than one exact result in a write yields `*AmbiguousError`, without
  guessing (parity with `resolveRecipient`, `internal/mcp/messaging.go:29`).
- Mentions (`Mention`) become `messageEntityMentionName`/`inputMessageEntityMentionName`
  with offset and length in **UTF-16** (logic of the reference Telegram CLI
  credited in [the plan's source credits](../PLANO.md#source-credits), see `docs/PARIDADE.md:128`; S1 conformance
  cases). The engine does not cut texts at 4,000 characters: it splits the text
  at the server limit only when explicitly asked; otherwise, it returns
  `ErrInvalid` with the limit.

## 4. Login

The `Controller` replaces the historical upstream supervisor's `StartPairing`,
`CancelPairing` and `Pairing` flow:

| State (`Login.State`) | Input | Gotd |
|---|---|---|
| `qr` | `StartLogin{Method: qr}` | `qrlogin.QR.Auth` / `Export`; `OnLoginToken` renews the QR, `QRVersion++` |
| `code` | `StartLogin{Method: phone, Phone}` | `auth.sendCode`; `SubmitCode` → `auth.signIn` |
| `password` | `SESSION_PASSWORD_NEEDED` | `SubmitPassword` → SRP `auth.checkPassword` |
| `syncing` → `done` | authorised | `BindSelf`, first page of dialogs, `updates.Manager.Run` |
| `error` / `cancelled` | failure / `CancelLogin` | — |

- `Login.QRURL` (`tg://login?token=…`) is a credential while it is valid. It is
  not serialised (`json:"-"`), does not go into logs and only becomes a PNG at
  `GET /api/pair/qr.png`, a route that requires a token (security.md §1).
- Code and password come in as `engine.Secret`, whose `String`, `Format`,
  `LogValue` and `MarshalJSON` return `[redacted]`.
- Login of another user into an already bound slot: `ErrAccountMismatch`.
  Another person's history never gets mixed in (`Persister.SelfID`/`BindSelf`).
- DC migration (qrlogin's `MigrationNeededError`): handled inside the engine.

## 5. Synchronisation

```text
telegram.Client ── UpdateHandler ──► updates.Manager ──► engine handler
                                         │                   │
             StateStorage/AccessHasher ◄─┘                   ▼
             (adapter over Persister)              Batch → Persister.Apply (1 tx)
                                                             │
                                                             ▼
                                               Applied → Event → subscribers
```

- gotd's `updates.Manager` receives `Storage`, `AccessHasher` and
  `UserAccessHasher` from an adapter over the `Persister`. A probe compiled
  that adapter against gotd v0.163.0, outside the repository. The
  `MaxChannelDifferenceConcurrency` comes from `Config.MaxChannelDifference`.
- gotd calls `SetPts`/`SetChannelPts` **after** the handler returns, and only
  logs the handler's error. The ordering and failure contract is in
  [store.md](store.md) §5. Summary: the handler writes the data synchronously
  before returning; if `Apply` fails, the engine **holds the checkpoint** and
  the adapter refuses new `Set*` calls with `ErrCheckpointHeld`.
  The engine restarts the manager with `Reset` + `Run(forget=false)`, and
  `getDifference` resends the stretch, which is applied idempotently.
- gotd callbacks → `GapMark` in the store + `coverage` event:
  `OnChannelTooLong` → `channel_too_long`; `OnTooLong` → `difference_too_long`;
  `OnLoadUserStateFailed`/`OnLoadChannelStateFailed` → `state_lost`;
  `OnChannelInaccessible` → `access_lost` + `AccessChange` (lifecycle.md §5).
  Access-loss evidence is terminal/inaccessible rather than retryable sync work;
  it does not schedule another difference or permanently block readiness.
  `state_lost` recovery uses authoritative manager differences and the original
  persisted recovery anchor. Only a final difference with data and checkpoint
  committed can fill it; legacy rows without an original baseline remain
  visible. A history-head scan cannot prove that missed edits/deletions were
  recovered. See [../REAL_ACCOUNT_REGRESSIONS.md](../REAL_ACCOUNT_REGRESSIONS.md).
- Backfill: `messages.getHistory` with `offset_id` = `cursor.oldest_id`. Each
  page is a `Batch` with `Cursor`, written together with the messages
  (store.md I3). Concurrency is limited by `Config.BackfillConcurrency`,
  default 1. Concurrency with updates: store.md §6.
- Messages from `difference` or `channelDifference` come out with
  `CatchUp=true` (§6): the webhook delivers them flagged, without pretending
  they arrived live.

## 6. Events

`Subscribe(ctx, SubscribeOptions) <-chan Event` is the internal bus, used by
the webhook, the live panel and the tray. It replaces the HTTP relay
(`internal/webhook/relay.go`) and the historical upstream CLI's `--events`
flag.

| `EventKind` | Telegram origin | Payload |
|---|---|---|
| `message` | `updateNewMessage`, `updateNewChannelMessage`, `updateShortMessage`, `updateShortChatMessage`, send response | `Message` |
| `edit` | `updateEditMessage`, `updateEditChannelMessage` | `Message` (new version) |
| `delete` | `updateDeleteMessages`, `updateDeleteChannelMessages`, TTL expiry | `Deletion` |
| `reaction` | `updateMessageReactions` | `ReactionEvent` |
| `read` | `updateReadHistoryOutbox`, `updateReadChannelOutbox` | `ReadUpdate{Outbox:true}` |
| `connection` | `ConnState` change | `ConnectionChange` |
| `auth` | login, `auth.logOut`, `AUTH_KEY_UNREGISTERED`, `SESSION_REVOKED` | `AuthChange` |
| `coverage` | gap callbacks, backfill that closes a gap | `GapMark` |
| `access` | `CHANNEL_PRIVATE`, `CHAT_FORBIDDEN`, `updateChannel` with `left`/`kicked` | `AccessChange` |

The inbox read, when the account itself reads on another device
(`updateReadHistoryInbox`), updates counters in the store and does **not**
generate a `read` event. The `read` event only means that the other side read
our messages (PLANO §4.2).

Guarantees:

- **Emitted after commit.** No event goes out without the corresponding
  `Apply` being confirmed.
- **Replay without duplicates.** A batch already applied returns an empty
  `Applied` and emits nothing (store.md I2).
- **`Event.ID` is stable.** It is computed as
  `hex(sha256(account|kind|peer|msg_id|version))[:32]`, where `version` is the
  `edit_date` for an edit, the hash of the durable transition revision and
  committed reaction payload for a reaction, the `max_id` for a read and `0`
  for messages and deletions. A reaction's version is
  `hex(sha256(decimal(revision) + "|" + JSON(ReactionEvent)))`; the revision
  itself is internal and excluded from the JSON payload. The same fact
  produces the same id after a restart. Webhooks derive `delivery_id` from
  this value (webhooks.md §2).
- **Reaction facts are transitions, not count totals.** The store assigns a
  monotonically increasing per-message reaction revision in the transaction
  that changes the canonical counts (including chosen state) or recent
  reactors. Each committed transition carries its own revision, including
  several transitions for the same message in one batch. Returning from A to
  B to A is a new fact, even with identical counts and no known reactors;
  replacing one reactor with another is also a new fact. Repeating the
  currently persisted canonical snapshot is a no-op: no revision increment
  and no event, including after reopening the database. Empty-state removal
  retains the revision so a later addition cannot reuse an earlier identity.
  Receipt time, an in-memory counter and randomness are not reaction versions.
- **Order.** Order is guaranteed per conversation, equal to commit order.
  Across conversations there is no guaranteed order.
- **Slow subscriber.** Each subscriber has a buffer (`Buffer`). When it fills
  up, the oldest event **of that subscriber** is dropped and counted.
  Sync is never blocked by a subscriber. The webhook has its own queue
  (`maxQueue=1000`, as in the historical upstream product).
- Event payloads carry message text, which webhooks need. For that reason the
  bus never goes to logs (security.md §9).

## 7. Swap table: historical upstream CLI call → `Engine`/`Controller`/store

Surveyed with `grep` on `main` at `72ff152`. Front S2 deletes the historical upstream CLI integration package
and wires each row to the fake; fronts C, D, G, H, I and J wire them to the
real engine.

| Today (file:line) | Upstream CLI call | After |
|---|---|---|
| `daemon/account.go:68,79,82` | historical CLI construction, supervisor creation and `sup.Run(ctx)` | `Factory(engine.Config{...})` → `Controller.Run(ctx)` |
| `daemon/account.go:70` | opening the historical CLI database with `index.Open` (read-only) | `store.Open(dir)`, read and write, owner B; `Config.Store = store` |
| `daemon/account.go:124`, `daemon.go:238` | `Supervisor()` | `Engine()` returning `engine.Controller` |
| `mcp/server.go:25-26,120-121` | historical CLI and supervisor fields | one field `eng engine.Engine` + `store *store.Store` |
| `panel/panel.go:46` | historical supervisor field | `Engine engine.Controller` |
| `panel/panel.go:332,337,351` | historical account, status and pairing types | `engine.Account`, `engine.Status`, `engine.Login` (inherited JSON names kept) |
| `panel/panel.go:412-467` | `settling`, `syncKey`, `syncLabel` over the historical status state | the same helpers over `engine.ConnState` |
| `panel/panel.go:1120` | `StartPairing(phone)`, `ErrPairingRunning` | `StartLogin(LoginRequest)`, `ErrLoginRunning`; new `SubmitCode`, `SubmitPassword`, `ResendCode` |
| Bruno Orlandi upstream source credit: `wacli/pairing.go:111` | `CancelPairing()` | `CancelLogin()` |
| Bruno Orlandi upstream source credit: `wacli/pairing.go:52` | `Pairing()` | `Login()` |
| Bruno Orlandi upstream source credit: `wacli/pairing.go:284` | `Logout(ctx)` (historical CLI logout command) | `LogOut(ctx)` (`auth.logOut` + `SessionStore.DeleteSession`) |
| Bruno Orlandi upstream source credit: `wacli/pairing.go:298` | `Account(ctx)` | `Account(ctx)` (`users.getFullUser(self)`, cached) |
| Bruno Orlandi upstream source credit: `wacli/supervisor.go:97` | `SetWebhook(url, secret)` + loopback relay | `Subscribe(ctx, SubscribeOptions)`; the relay is removed |
| Bruno Orlandi upstream source credit: `wacli/supervisor.go:105,131` | `Changes()`, `Status()` | `Changes()`, `Status()` |
| Bruno Orlandi upstream source credit: `wacli/supervisor.go:413,441` | `Delegated`, `Exclusive` (pause sync because of the store lock) | **removed**: the engine serialises internally; there is no inter-process lock |
| Bruno Orlandi upstream source credit: `wacli/cli.go:135`, `mcp/messaging.go:18` | historical CLI lock-error check, `delegatedOrExclusive` | removed; handle `*FloodWaitError` |
| `cmd/app/host.go:44` | historical CLI version lookup | `Diagnostics().Library` |
| `mcp/status.go:20`, `mcp/health.go:60` | `--read-only doctor` | `Diagnostics(ctx)` + `Status()` |
| `mcp/handlers.go:298` | `chats list --query` | `store.ListChats` (+ `RefreshDialogs` when stale) |
| `mcp/handlers.go:356` | `messages list --chat --after --before --asc` | `store.Messages` |
| `mcp/handlers.go:440` | `messages search` | `store.Search` (FTS5); `SearchRemote` when the index does not cover it |
| `mcp/handlers.go:500` | `groups info --jid` (live) | `RefreshGroup(GroupRequest)` |
| `mcp/handlers.go:587` | `send text --to --message [--reply-to] [--mention]` | `SendText(SendTextRequest)` |
| `mcp/handlers.go:598-677` | `send file` (URL downloaded by the MCP) | `SendMedia(SendMediaRequest{Path})` |
| `mcp/handlers.go:745` | `send location` | `SendLocation` |
| `mcp/handlers.go:770` | `send poll --multi N` | `SendPoll{Multiple}` (Telegram has no "at most N") |
| `mcp/handlers.go:791` | `messages edit` | `Edit(EditRequest)` |
| `mcp/handlers.go:794-820` | `react` | `React(ReactRequest)` |
| `mcp/handlers.go:854,867` | `messages delete --for-me`, `messages revoke` | `Delete{Revoke:false}`, `Delete{Revoke:true}` |
| `mcp/handlers.go:883` | `contacts check` | `ResolveContacts(queries)` (`resolve_contact`) |
| `mcp/handlers.go:898` | `profile picture` | `ProfilePhoto(PhotoRequest)` |
| `mcp/handlers.go:912` | `chats archive/pin/mute…` | `Organise(OrganiseRequest)` |
| `mcp/handlers.go:927` | `poll show` | `PollResults(PollRequest)` |
| `mcp/handlers.go:958` | `media download --output` | `Download(DownloadRequest)` |
| `mcp/reads.go:169` | `messages context --before --after` | `store.Context`; `FetchWindow` when missing |
| `mcp/history.go:91` | `history backfill --count --requests` | `Backfill(BackfillRequest{Count, Rounds})` |
| `mcp/messaging.go:194` | `messages forward` | `Forward(ForwardRequest)` |
| `mcp/messaging.go:208` | `chats read` | `MarkRead(MarkReadRequest)` |
| `mcp/messaging.go:228` | `presence typing` | `SetTyping(TypingRequest)` |
| `mcp/groups.go:48` | `groups participants add/remove/promote/demote` | `ManageParticipants` (PLANO §4.3) |
| `mcp/groups.go:67,72` | `groups rename`, `groups description` | `UpdateGroup{Title, About}` |
| `mcp/groups.go:105` | `groups invite link get/revoke` | `InviteLink{Revoke}` |
| `mcp/groups.go:129` | `groups leave` | `Leave(LeaveRequest)` |
| `panel/panel.go:378-381,1042,1057,1076`, `index.*` | `index.Coverage`, `Gaps`, `OwnName`, `RecentChats`, `Conversation`, `ChatOldest` | the same functions in `internal/store` (front B), over the store.md schema |
| `cmd/app/host.go:137` | `EraseEverything` (upstream CLI logout + delete folder) | lifecycle.md §4 "erase everything" |

Tools without a row in this table stay as they are today: `save_transcript`,
`mark_handled`, `snooze_chat`, `media_stats` and `purge_media` are local.

## 8. Contract conformance tests

The `internal/engine/enginetest` package, from front S2, takes a `Factory` and
runs the same suite on the fake and, in Stage 2, on the real engine with a test
server or a recording. The minimum cases are these:

| ID | Case | Proof |
|---|---|---|
| M1 | No read sends a receipt | A spy invoker sees zero `messages.readHistory`, `channels.readHistory`, `readMentions`, `readReactions`, `getMessagesViews(increment=true)` and `account.updateStatus(offline=false)` after running all reads |
| M2 | Read-only mode refuses every write | The 14 writes and `InviteLink{Revoke}` return `ErrReadOnly` with `DryRun=false`; the spy invoker records no call |
| M3 | `DryRun` does not touch Telegram | The 14 writes and `InviteLink{Revoke}`, with `DryRun=true` in both modes, return `Preview` with `Fingerprint`; the invoker records no write call |
| M4 | Event only after commit | With `Persister` failing in `Apply`, no event is emitted |
| M5 | Replay does not duplicate the event | The same `Batch` applied twice results in one event |
| M6 | Stable `Event.ID` | The same update before and after a restart has the same `ID` |
| M7 | Slow subscriber does not stall sync | A subscriber that never reads does not block; the drop counter increases |
| M8 | Secrets do not leak | `fmt %v/%+v/%#v`, `slog` and `json` of `Secret`, `Login` and `Media` do not contain the code, the password, `QRURL` or `Remote` |
| M9 | Different account in the slot | Login with another `self_id` results in `ErrAccountMismatch`, without touching history |
| M10 | Ambiguity | Two contacts "Ana" and `SendText{to resolve}` result in `*AmbiguousError` |
| M11 | UTF-16 mention | Text with an emoji before the mention: offset in UTF-16 units matches the reference Telegram CLI ([the plan's source credits](../PLANO.md#source-credits)) |
| M12 | Long FloodWait | `FLOOD_WAIT_600` with `FloodWaitMax=30s` returns `*FloodWaitError{Until}` without blocking |
| M13 | Revocation | `AUTH_KEY_UNREGISTERED` produces `StateRevoked`, `AuthChange{revoked}` and a `DeleteSession` call, and history remains (lifecycle.md §5) |
| M14 | Controller outside the MCP | Compile or reflection test: `mcp.Server` only receives `engine.Engine` (no reachable `SetMode`) |
