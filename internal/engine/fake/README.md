# Deterministic offline engine

This package implements the frozen `engine.Controller` contract without gotd,
network access, real credentials or third-party dependencies. It is a shell/test
adapter, not evidence of verified Telegram behavior.

## Construction

```go
var factory engine.Factory = fake.New
controller, err := factory(engine.Config{
    Account: "primary",
    // Mode defaults to engine.ModeReadOnly.
    // Clock defaults to fake.Epoch (fixed), not wall time.
})
if err != nil {
    return err
}
```

`New(engine.Config) (engine.Controller, error)` starts **authenticated and
connected as the synthetic account**. It does not require `Run` before tests can
call its methods. `Run(ctx)` waits for cancellation, returns `ctx.Err()`, changes
status to stopped and closes subscribers. Multiple concurrent `Run` calls are
rejected. Use the supplied context to own each subscriber's lifetime.

A nil `Config.Store` uses isolated in-memory data. With a Persister supplied,
construction binds synthetic self ID 1000 and commits fixtures; subsequent
batches use `Apply`, and only acknowledged changes emit events. Use **only an
isolated synthetic/test store**, never a production account's store. A supplied
SessionStore is touched only by synthetic login completion and logout. The fake
never interprets a real MTProto session.

## Fixtures and observations

- `Fixtures(account) Dataset`: fresh fixture data; `Dataset.Batch()` returns a
  detached batch suitable for seeding a consuming package's test Persister.
- Stable fixture references: `SelfPeer`, `PrivatePeer`, `BasicGroupPeer`,
  `SupergroupPeer`, `ChannelPeer`. These are fixture constants by convention;
  callers should not assign to the exported variables.
- `controller.(*fake.Controller).Snapshot()`: detached current dataset.
- `Writes() []Write`: thread-safe, detached journal of **successful writes
  only**. Each record includes action, target, typed request and result.
  `ResetWrites()` clears it. Reads, failed writes, previews and simulated server
  updates do not enter this journal. This observation API contains synthetic
  message content and is not a production logging API.
- `Publish(ctx, engine.Batch) error`: simulates a server update, with persistence,
  replay suppression and events. Its messages need full account/peer/message
  identities. Unknown non-channel deletions prevent resurrection by later
  history pages; channel IDs remain separate.
- `DroppedEvents() uint64`: total oldest-buffer drops across subscribers.

The dataset covers private/saved chats, a basic group, a forum supergroup,
broadcast channel, mentions (including UTF-16 astral-character offsets),
replies, a poll, a valid tiny PNG, synthetic voice metadata, protected media and
self-destructing-media placeholders. The voice payload is a **test marker**, not
decodable audio. Injection-like message text is intentionally preserved as data.

Downloads are deterministic fixture bytes, use a stable basename under the
requested directory, have mode 0600 and report the actual SHA-256/size. The fake
does not fetch URLs, decode caller media or generate real Telegram invite links
(the synthetic invite uses the reserved `.invalid` domain).

## Writes

All fifteen write paths, including invite revocation, pass the same internal
gate. In read-only mode execution returns `ErrReadOnly`; a dry run still returns
a preview with a warning and performs no persistence/write. To execute:

1. Call the action with `WriteOptions{DryRun: true}`.
2. Enable `read_write` through the Controller if appropriate.
3. Repeat the **same action arguments**, supplying the returned fingerprint as
   `ExpectFingerprint`, with `DryRun: false`.

Fingerprints bind canonical action arguments, target/mention resolution,
quoted/affected messages and, for uploads, the actual file digest. Each preview
issuance also binds a monotonically increasing generation, deterministic for the
same preview history. Confirmation recomputes the payload with that approval's
original generation; it never issues another preview. Identical new previews
cannot revive consumed fingerprints or extend their original ten-clock-minute
expiry, and do not invalidate other outstanding, unexpired approvals.

For forwards, `Preview.Quoted` retains the first source for compatibility;
`Preview.Text` contains a JSON array of **all complete source message snapshots**
in requested order. This action payload is part of the fingerprint, so editing
any source requires a fresh preview and approval. Treat the payload as untrusted
message data, not instructions.

Approvals are single-use. Set `IdempotencyKey` for replaying an already successful
call without another write, including edit/delete/leave. Different arguments
cannot reuse that key. The fake cannot establish human approval: the MCP/panel
layer remains responsible for obtaining it.

## Synthetic login and lifecycle

`Disconnect` preserves session/history; network methods then return `ErrPaused`.
`Connect` resumes. `LogOut` deletes a supplied synthetic session, preserves
history and makes network calls return `ErrNotAuthorized`.

Phone login accepts a synthetic international-format phone, any nonempty
`engine.Secret` code, then any nonempty `engine.Secret` password. QR login exposes
an intentionally synthetic QR value via `Login().QRURL` (excluded from JSON);
`CompleteLogin(ctx)` simulates an external scan. `CancelLogin` cancels an active
flow. Codes/passwords are never retained or logged.

## Verification

```sh
go test ./internal/engine/fake
go test -race -count=5 ./internal/engine/fake
go vet ./internal/engine/fake
```

Tests use only public Controller/fixture observations and a Persister seam spy;
there are no Telegram connections. They cover all write gates, confirmation
binding/expiry/reuse/idempotency, cancellation, UTF-16, lifecycle/login,
commit-before-event behavior, replay/version/deletion semantics, cursor progress,
read-only receipt safety, malicious content as data, private media files and
concurrent journal/subscriber access.
