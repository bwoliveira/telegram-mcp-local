# Store lifecycle

Implemented in `lifecycle.go`, on DDL v1 without schema changes.

## Boot and execution

`RunJanitor(ctx, onDeleted)` blocks. Before starting sync and HTTP routes, run
the synchronous `JanitorOnce(ctx, onDeleted)` barrier and handle its error; only
then start `RunJanitor` in a goroutine. The first `RunJanitor` iteration also
performs a full sweep before installing timers.

```go
if err := s.JanitorOnce(ctx, publishTTLDeletes); err != nil {
    return err
}
go func() {
    err := s.RunJanitor(ctx, publishTTLDeletes)
    // Handle error/cancellation in the daemon supervisor.
    _ = err
}()
// Now start sync/HTTP.
```

- Only one `RunJanitor` per store instance is accepted.
- Each `engine.Applied.Deleted` contains only deletions actually committed. The caller constructs the `delete` event with `source="ttl"`; temporary links and the webhook queue belong to the layer consuming these events.
- `Sweep` uses batches of up to 500, with `ttl` tombstones, through the core helper. If a later batch fails, already committed batches remain in the result.
- `Cleanup` must accompany a standalone `Sweep` call. `JanitorOnce` runs both and calls `onDeleted` outside the mutex, after the cleanup attempt.
- D6: maximum interval of 60 s and a timer for the next expiry, with DDL second precision. The modernc v1.60.1 writer connection commit hook wakes the timer for a new earlier expiry. The driver callback only sends a nonblocking signal; the re-read uses the writer connection after commit. A 1 s fallback query covers a replaced connection/external writer.
- Runtime operational failures are retried after 1 s, without abandoning TTL. Cancellation stops the job and removes the hook; the return may join the last operational error with the context error. The boot barrier returns failures directly so the daemon does not falsely announce readiness.

## Retention and cleanup

`Cleanup(ctx)` uses explicit defaults of 30 days for media/profile photos and
7 days for exports. `Cleanup(ctx, Retention{MediaDays: n, ExportDays: m})`
selects both periods; zero disables the category and negative numbers are
invalid. Retention expiry is strict: items *older* than N days. Messages,
transcripts, tombstones, pending deletes and triage are not purged by age.

The unlink queue is durable and acknowledged only after removing the file (or
confirming its absence). Paths must be under `s.dir` (the private account folder)
or `<data>/exports/` when the database follows the canonical
`<data>/accounts/<slot>/store.db` layout. Each root has its own `os.Root`;
no unlink operation receives access to the general data folder/other accounts:

- traversal and directories are rejected;
- symlink parents are rejected, including aliases pointing back to the root;
- `os.Root` confines the operation, and a parent handle pins the directory used for identity checking and unlink;
- database, WAL, SHM/journal and still-referenced files are protected by normalization and file identity, including hardlink aliases;
- a symlink in the final position can be removed without following its target;
- failures remain queued and do not prevent attempts on other items.

After retention/unlink, cleanup attempts FTS `optimize`, `incremental_vacuum`
and finally `wal_checkpoint(TRUNCATE)`, without interleaving store writes. A
busy checkpoint is an error, not false forensic confirmation. Checkpoint is
last so vacuum does not repopulate a truncated WAL. Retention failure does not
skip independent forensic cleanup steps.

## Transcripts

`SaveTranscript(ctx, key, Transcript{...})` validates account, existence, expiry,
`media_ttl`, message/conversation protection and access in the same transaction
as insertion. Replacement performs an atomic DELETE + INSERT: DDL v1 only has
an INSERT FTS trigger for transcripts. No independent retention is created.

## Local tests, without network or real accounts

```sh
# With the Go toolchain on PATH:
GOPROXY=off go test ./internal/store -run TestLifecycle -count=1
GOPROXY=off go test -race ./internal/store -run TestLifecycle -count=1
```

Tests cover batching and partial rollback, replay, cascades, two FTS layouts with
DB/WAL/SHM and full-folder forensic checks, queue after reopening, transcript
guards/replacement, default/zero/boundary retention, profile photos, busy
checkpoint and retry, paths/aliases, boot, D6 and earlier TTL insertion.
Download/forward/export refusals, reads without expired messages, routes,
webhooks and link revocation also require tests of their owning layers; local
tests do not pretend to run those integrations.
