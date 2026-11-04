# gotd spike SQLite persister

`Open(path)` creates a private database (`0600`, new directories `0700`), and
`(*Store).Close()` closes it. It implements `engine.Persister` and
`Dialogs(ctx, account)`. It neither connects to Telegram nor stores MTProto sessions.
`VersionKey(key)` identifies entries in `Applied.Versions`.

## Guarantees implemented and exercised offline

- `Apply` is transactional; errors return `Applied{}` and roll back the entire page/cursor.
- Message/edit replay changes neither rows nor versions; copies with an older or equal `edit_date` do not replace the newest version.
- Keys include account, peer namespace, peer and message. Deletion without a peer reaches only `user`/`chat`; the `channel` namespace is independent.
- Deletion removes content and reaction state and records a tombstone. A permanent IDs-only fence also records non-channel deletions without a peer: a later copy cannot resurrect them even before the conversation is known.
- Cursors and messages share a transaction; `newest_id` only increases, `oldest_id` only decreases and `complete` never regresses.
- A min hash does not replace a full hash; public peer readers do not expose it.
- Global/partial and per-channel checkpoints, identity binding, authentication, dialogs, monotonic reads, reactions, participants and gaps persist.
- Access loss removes channel state, marks the cursor/gap and retains history.
- Subprocess crash tests use abrupt exit 137 both during the transaction and after commit but before checkpoint. Replay recovers without duplicating messages or events. No test accesses a real account.

## Deliberate PoC limits

**This is not the production `internal/store` schema.** It uses account-scoped JSON records, its own `application_id` and one serialized read/write connection. It rejects databases of another format and future versions. It does not provide FTS, mentions/transcripts, a full index, export, retention or a media janitor. The concurrency test is short; it does not replace the 10-second S16 test from workstream B.

**Conservative policy:** protected messages, messages from protected peers, messages with any TTL/expiry (even future), and temporary media are omitted entirely. Only content-free tombstones are stored. Thus no TTL content can expire while stored during downtime. A peer becoming protected removes already stored messages. This is stricter than the final contract, which will allow protected text and TTL with a janitor/timer. `Batch.Downloads` is rejected with `engine.ErrNotSupported` before any changes, because the PoC does not implement the unlink queue and must not promise file cleanup.

The engine/adapter must hold the checkpoint after an `Apply` failure and restart the updates manager (I4). The store does not infer ordering between independent `Apply` and `Save*` calls: they must pass through the engine barrier.

Deletions use `secure_delete=ON`. Offline forensic tests show the text marker disappears from the database/WAL after `wal_checkpoint(TRUNCATE)`; physical WAL cleanup is an explicit step, not a guarantee of `Apply` alone.

```sh
# With the Go toolchain on PATH:
go test -race -count=1 ./internal/engine/gotdengine/pocstore
go vet ./internal/engine/gotdengine/pocstore
```
