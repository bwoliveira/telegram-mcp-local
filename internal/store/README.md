# Relational store — Stage 2/B

`Store` satisfies `engine.Persister` without changing the frozen contract. It
uses `modernc.org/sqlite` (an existing dependency, with FTS5 enabled), relational
DDL v1 embedded in `migrations/001.sql`, a serialized writer and a `mode=ro`
reader pool. It does not use gotd, Telegram, RPCs or PoC paths.

## Integration

```go
s, err := store.Open(ctx, filepath.Join(dataDir, "accounts", slot, "store.db"), account)
if err != nil { return err }
defer s.Close() // after cancelling and awaiting all consumers
if err := s.JanitorOnce(ctx, publishTTLDeletes); err != nil { return err }
// Pass s to the engine as engine.Persister.
// Start RunJanitor(ctx, publishTTLDeletes) under service supervision.
```

The path is explicit; do not pass the data root or share a file between
accounts. `Open` binds the account atomically and rejects another slot, a
nonempty unversioned database or a future version. Directory/file modes are
0700/0600; WAL, foreign_keys, secure_delete, busy_timeout and temp_store are
also configured for new pool connections. Migration v1 is an interruptible
transaction.

`BindSelf` must occur before ingestion to index mentions of SelfID/username.
`Apply` persists all batch components in one transaction and returns `Applied`
only after COMMIT. `SaveUpdatesState`/channel state are deliberately separate:
the **engine** must hold the checkpoint and restart its generation after a
failure. The store does not turn an Apply failure into authorization for a
later checkpoint. `VersionKey(key)` follows `account/type:id/msgID`.

- Tombstones prevent resurrection; pending deletes without peers never affect channels.
- Replay ignores older copies/edits, preserving versions, cursors and FTS.
- A min hash does not replace a full hash; user/chat/channel namespaces do not collide.
- Migrated histories are joined by default, preserving the original key (D4).
- Access loss preserves history and removes only that channel's checkpoint.
- Message TTL is authoritative; dialog policy infers TTL only for new live/send messages whose date is not before the snapshot, never for old history.
- TTL media has only a placeholder without text/caption, location or Remote.
- `answered` and `server_unread` are independent; deleting/expiring the answer recalculates the mention. Entities use UTF-16 offsets, including emoji before the mention.

Public signatures and DTOs: [API.md](API.md). Boot integration, D6 timers,
retention, forensic cleanup and transcripts: [README.lifecycle.md](README.lifecycle.md).
Triage/handled tables are reserved in the DDL; full triage tools are outside
this workstream.

## Document subtype repair (schema v4)

A same-revision remote refresh may refine a cached `document` into `video`,
`video_note`, `audio`, `voice`, `sticker` or `animation`. This is classification
repair, not a content edit: message versions/events, captions, original TTL and
expiry, credentials, protection/download policy, reactions, provenance and
other media fields remain unchanged. Older revisions, tombstones, expired rows,
view-once media and already-specific/non-document types are not reclassified.

The transactional v4 migration repairs `media_type` and `media_json.type` only
when JSON already retains a specific subtype or explicit `document_attributes`
flags. The converter now retains only non-sensitive attribute-presence flags
(`audio`, `voice`, `video`, `round_message`, `sticker`, `animated`), never raw TL
attributes or file credentials; view-once media retains none of these flags.
Sticker and animation take priority over video, while round-message/voice flags
require their video/audio attribute. Migration and version advancement commit
or roll back together; reopening and rerunning the migration are idempotent.

**Evidence limitation:** historical `Media` JSON generally contains neither a
specific subtype nor document attributes. MIME type, filename, dimensions and
duration do not establish Telegram semantics (for example, voice vs audio or
round video vs video). Such rows intentionally remain `document`; a later
authoritative remote read can repair them. This migration does not contact
Telegram and does not claim to recover evidence that was never persisted.

`TestDocumentSubtypeMigrationV4AtomicIdempotent25K` uses 25,000 synthetic rows,
an injected pre-commit failure, exact row comparisons, classification counts,
forced migration replay, normal reopen and SQLite integrity checks. It logs
migration/reopen durations and enforces a 30-second per-open budget.

## Offline evidence

```sh
# With the Go toolchain on PATH:
GOPROXY=off go vet ./internal/... ./cmd/telegram-mcp/... ./cmd/telegram-mcp-bridge/...
GOPROXY=off go test -race -count=1 ./internal/... ./cmd/telegram-mcp/... ./cmd/telegram-mcp-bridge/...
go build ./cmd/telegram-mcp ./cmd/telegram-mcp-bridge
```

- S01/S02: a subprocess exits with code 137 after/before COMMIT; reopening, replay and global/channel checkpoints checked. This does not replace the updates.Manager/hold test from workstream A.
- S03–S14: transactional failure and separate checkpoint, zero-write replay, out-of-order edits, concurrent backfill, gaps, mentions, FTS cleanup, non-channel IDs, isolation, min hash and access loss.
- S15: interrupted subprocess migration, resumption and future-version rejection.
- S16: 8 writers + 8 readers, 10 seconds, no busy errors in WAL under `-race`.
- Lifecycle: L01–L04 and store portions of L05/L06/L11/L12, including expired items offline/at boot, earlier timer, batch 500/rollback, TTL vs protection and retention. L07–L10 and download/forward/export/link/webhook refusals require integration by the owning layers and are not claimed as run here.
- Forensics: two FTS layouts; after deletion and Cleanup, token absent from DB/WAL/SHM and private files. Failure/busy checkpoint is not cleanup success.

The commands above use synthetic fixtures, not a real account or Telegram
network. Full-tree vet additionally requires the GUI development libraries
installed by CI; see `docs/CI.md`.
