# Database contract (`internal/store`)

Version 1 (S0 draft for freezing), 2026-11-01. Front B owns the
implementation. Front A consumes the database through `engine.Persister`
([engine.md](engine.md)). Fronts C, H, J and K consume the reads.

## 1. File, connection and migrations

- File: `<data folder>/accounts/<slot>/store.db`, in mode `0600`, in a
  `0700` folder. Each account slot has one file. Even so, every table
  has the `account` column, so that the key is explicit and to allow a
  future merge without a key migration (open decision D3).
- Driver: `modernc.org/sqlite` v1.60.1, without CGO, the same version as in `go.mod`.
  The local probe with this version ships SQLite 3.53.4 and FTS5 with
  `contentless_delete` working.
- PRAGMAs on every connection: `journal_mode=WAL`, `synchronous=NORMAL`,
  `foreign_keys=ON`, `secure_delete=ON`, `busy_timeout=5000`,
  `temp_store=MEMORY`. On creation: `auto_vacuum=INCREMENTAL`.
  `synchronous=NORMAL` in WAL can lose the last commits on a power
  failure, but preserves their order. Because the checkpoint is written **after** the
  data (I1), a loss takes the newest checkpoint with it. The result is a
  replay, not a hole.
- **A single writer:** there is one write connection (`SetMaxOpenConns(1)`),
  and every `Apply` goes through it. Reads use a separate pool, opened with
  `mode=ro`. No `SQLITE_BUSY` reaches the caller (test S16).
- Migrations: `PRAGMA user_version` + one function per version, each in a
  transaction. A database with a version higher than the binary's is refused with a
  clear error, without downgrading the version.
- Data version 2: recovers zero `topic_id` for `forum=1` peers, preserves
  already-known topics and rebuilds mentions/replies in the same transaction.
  It uses `reply_top_id`, explicit roots and local reply chains; with no
  indexed target, it uses the replied-to ID as the root, and with no reply it uses General (`1`).
  v1 did not persist `forum_topic`: a reply to a missing message without a top may
  end up in a conservative partition; there is no perfect reconstruction of that lost
  bit. Non-forum peers, content, versions, TTL and cursors
  are not changed. The index only accepts a later reply in the same topic.
- Data version 6: adds `reactions.version INTEGER NOT NULL DEFAULT 0` in a
  transaction. Existing v5 rows retain all state and start at revision zero.
  Each canonical counts/recent-state change increments the per-message revision
  in the same transaction, including history hydration. Empty state retains its
  row/revision; exact replay changes neither state nor revision. Each applied
  reaction carries its own internal revision, including multiple transitions
  for one message in a batch; the revision is not exposed in JSON.
- `state.db` inherited from the historical upstream product (transcripts, triage,
  webhooks, clients and settings; opened at `72ff152:internal/state/state.go:41`):
  the recommendation is to merge into the same `store.db` the tables
  tied to messages (`transcripts`, `triage`), so that TTL and deletion
  erase them in the same transaction (open decision D3). This contract already designs
  `transcripts` here.

## 2. Keys

| Entity | Key |
|---|---|
| peer | `(account, peer_type, peer_id)`, with `peer_type ∈ {user, chat, channel}` |
| message | `(account, peer_type, peer_id, msg_id)` = `engine.MessageKey` |
| physical row | `messages.row_id INTEGER PRIMARY KEY`, internal only, used as the FTS `rowid` and as the FK target; **never** leaves the store |
| channel state | `(account, channel_id)` |
| cursor | `(account, peer_type, peer_id)` |

A `msg_id` alone is never a key. `user:5`, `chat:5` and `channel:5` are three
different peers. Tests S11 and S12.

## 3. Peers and `access_hash`

- `access_hash` is read only by the engine (`Persister.AccessHash`). It does not appear
  in tool reads, exports or logs.
- A "min" hash, coming from `min` constructors in large groups, is marked with
  `hash_min=1` and **never replaces** a full hash. A full hash always
  replaces a min one. `AccessHash()` returns only full hashes. For the
  other cases, the engine uses `inputPeerUserFromMessage`/`inputPeerChannelFromMessage`
  (test S13).
- gotd's `updates.ChannelAccessHasher`/`UserAccessHasher` adapter writes
  through `Apply{Peers}`. `updates.Manager.loadChannels` ignores channels without a hash
  (`OnLoadChannelStateFailed`); therefore, a `channel_state` without a full hash in
  `peers` is treated as a gap (`state_lost`).
- Migrated basic group (`messageActionChatMigrateTo`):
  `peers.migrated_to_*` points to the channel. History reads may join
  the two peers when asked, but the keys remain separate.

## 4. Deletions that Telegram does not locate

`updateDeleteMessages`, outside channels, carries only ids. Because these ids are
unique per account outside channels, the store resolves `(account, msg_id)` only
among `user` and `chat` peers (`ResolveMessageIDs`). Ids that do not yet exist
in the database go to `pending_deletes`. If a backfill or difference later brings
a non-channel message with that id, it is discarded in the same
transaction and the pending entry becomes a tombstone (test S06c).

## 5. Updates invariant (the critical point)

### 5.1 Decision: idempotent replay, not a checkpoint in the same transaction

The plan prefers checkpoint and message in the same transaction "if possible with
gotd". Reading the gotd v0.163.0 code (`telegram/updates/state_apply.go`,
`state.go`, `state_channel.go`) shows that this is **not possible without a fork**:

1. For live updates, `updates.Manager` calls `handler.Handle(ctx,
   updates)` and only **afterwards** calls `storage.SetPts`/`SetQts`/`SetSeq`/
   `SetDateSeq`/`SetChannelPts`, in separate `StateStorage` calls.
2. This order cannot be generalized to differences: `channelDifference`
   queues `OtherUpdates` on another worker and may write its PTS before the
   dispatch. The channel worker may also discard those updates as old
   after advancing the PTS in memory. A handler error does not block every
   state-advance path.
3. The global and per-channel states run in different goroutines. Ordering
   and the gap buffer (`gap_buffer`, `sort_pts`) are internal. We do not infer
   a checkpoint from the individual PTS of the updates.
4. The `durableDifference` response middleware applies **all** the
   supported components of `OtherUpdates` and `NewMessages`, with peers, in a single synchronous
   `Apply` before returning each global/slice/channel difference to the manager.
   No worker sees the response PTS before that commit. On failure, it does not deliver
   the response and holds all checkpoints of the generation. The successful response
   stays intact for gotd's bookkeeping; the later handler is an idempotent
   replay, with no duplicate events. It does not require forking the dependency.

Therefore, the plan's alternative applies: **idempotent replay proven by tests**,
with these invariants:

| ID | Invariant |
|---|---|
| **I1** | **The checkpoint is never ahead of the data.** Live updates do a synchronous `Apply` before returning to the manager. Global/slice/channel differences first go through the synchronous response barrier: all supported data is committed before the manager receives the new PTS, including when `OtherUpdates` would only be queued. Data and checkpoint remain in separate transactions, in that order. |
| **I2** | **`Apply` is idempotent.** Applying the same `Batch` twice, or content older than what is stored, changes no row and returns an empty `Applied`. Without `Applied`, there is no event. |
| **I3** | **Cursor together with the page.** The backfill `Cursor` goes into the same transaction as the page's messages. |
| **I4** | **Failure holds the checkpoint.** If `Apply` or a save fails, the engine keeps an in-memory latch for the whole generation, rejecting new batches and every `Set*` with `engine.ErrCheckpointHeld`. A later `Apply` never clears an earlier failure. The engine cancels, waits for completion and resets the manager before releasing a new `Run(forget=false)` generation, which reads only the previous durable state. A crash loses the latch, but not the previous checkpoint, and so it also resends the stretch. |
| **I5** | **Nothing is lost silently.** `differenceTooLong`, `channelTooLong`, lost state and lost access become a row in `gaps`, shown in `telegram_status`/`health`, and trigger a backfill. |
| **I6** | **Deletion beats an old copy.** A key with a tombstone is never recreated by backfill, difference or search (S06b). |

### 5.2 `Apply` idempotency rules

| Item | Rule |
|---|---|
| new message | `INSERT … ON CONFLICT(account,peer_type,peer_id,msg_id) DO UPDATE` only if `excluded.edit_date > messages.edit_date`, or if the stored row is partial (`partial=1`) and the new one is not. Otherwise, nothing changes. Key with a tombstone or with `pending_deletes` (non-channel): discard, `Skipped++` |
| edit | same as the previous item, comparing `edit_date`; a tie is a no-op. If the row does not exist, the edit is inserted as a complete message, because the update carries the whole message |
| deletion | `DELETE` of the row, which removes FTS, mentions, reactions, transcripts and media by FK/trigger, plus `INSERT OR IGNORE` into the tombstone. It enters `Applied.Deleted` only if it deleted a row or created a new tombstone |
| reaction | replaces state and increments its durable revision only if canonical counts or recent peers changed; `Added`/`Removed` compare recent lists. Exact replay leaves the revision unchanged; clearing retains the row and revision |
| read | `read_outbox_max_id = max(current, new)`; appears in `Applied` only if it increased |
| peers | upsert; hash as in §3; empty fields do not erase known fields |
| checkpoint | `SaveUpdatesState`/`PatchUpdatesState` write only if `held=0`; `SaveChannelPts` likewise |

### 5.3 Required tests (front B on the fake; A repeats with gotd)

Each test uses a fault injected into the `Persister` itself, with a test hook
`store.FaultAt(stage)`, and a child process that exits with `os.Exit(137)` at the
exact point, to simulate `kill -9`.

| ID | Case | Acceptance |
|---|---|---|
| S01 | Crash **after** the `Apply` commit and **before** `SetPts` | On restart, `getDifference` resends; each message exists once; FTS finds it once; no event is duplicated (M5); the final `pts` equals the server's |
| S02 | Crash **during** `Apply` (before the commit) | The database has no partial row; the `pts` did not advance; the replay writes everything |
| S03 | `Apply` returns an error (simulated full disk) | `held=1`; `SetPts` returns `ErrCheckpointHeld`; the manager is restarted; with the error removed, the data appears and only then does the `pts` advance |
| S04 | Replay of the same difference twice | Zero rows changed the second time (counted by `changes()`), zero events and `version` unchanged |
| S05 | Out of order: edit before the message, or old copy after the new one | The version with the highest `edit_date` stays; no regression |
| S06a | Backfill concurrent with updates: page with v1, edit v2 already stored | v2 remains |
| S06b | Deletion during an in-flight backfill | The page applied afterwards does not resurrect the message (tombstone) |
| S06c | Non-channel deletion of a still-unknown id, then the backfill brings that id | It is discarded; the tombstone is created |
| S06d | Two backfills of the same conversation at the same time | The cursor never regresses (`oldest_id` only decreases) and there are no duplicates |
| S07 | Channel gap (`updateChannelTooLong` / `OnChannelTooLong`) | Row in `gaps` with `reason=channel_too_long`; backfill scheduled; `filled_at` filled when the backfill covers the stretch; `telegram_status` shows the gap open and then closes it |
| S08 | `differenceTooLong` | Account gap; dialogs updated; backfill of each conversation from the last known id |
| S09 | Edits | The old text disappears from FTS and the new one is found; a mention added by the edit enters the index and a removed one leaves; one `edit` event |
| S10 | Deletions | The row, FTS, mentions, reactions, transcript and media disappear; the tombstone exists; in the **file forensics** (`store.db` and `-wal` after `wal_checkpoint(TRUNCATE)`), the text marker does not appear |
| S11 | Per-account deletion without a peer | Affects only `user`/`chat` peers; an identical `channel:X/msg_id` stays intact |
| S12 | Namespaces | `user:5`, `chat:5` and `channel:5`, with the same `msg_id`, are three rows |
| S13 | Min hash | A min hash does not replace a full hash; a full hash replaces a min one |
| S14 | Lost access | `channel_state` removed; `peers.access=lost`; history kept; `ForEachChannelPts` no longer lists the channel |
| S15 | Migration | A version N−1 database migrates; a database with a higher version is refused; an interrupted migration does not leave the database half-done |
| S16 | Load | 8 goroutines with `Apply` + 8 readers for 10 s: no exposed `SQLITE_BUSY` and no FK violation |

## 6. Backfill

- Per-conversation cursor: `newest_id`, `oldest_id`, `complete`, `state`,
  `flood_until`. Backfill goes down from `oldest_id`, or from `top_message_id` the
  first time, with `messages.getHistory(offset_id=oldest_id, limit=count)`.
- Page and cursor are in one transaction (I3). A crash midway repeats the
  page, which is idempotent.
- An empty page, or the group creation message, sets
  `complete=1`. `CHANNEL_PRIVATE` sets `state=no_access`.
- `cursor.newest_id` rises with live updates. Thus, `oldest_id..newest_id`
  is the covered stretch, except for open `gaps` rows. The per-conversation coverage
  that `telegram_status` and `sync_history` show comes from here.

## 7. Mentions (for historical `list_mentions`)

One row in `mentions` per message that mentions the account, created in the same
`Apply` that writes the message (updates **and** backfill) when any
of these cases occurs:

- `by_name`: entity `mention_name` with `user_id = self_id`;
- `by_username`: entity `mention` whose text, without `@` and case-insensitively,
  is one of the account's current `usernames`;
- `by_reply`: `reply_to` points to an `out=1` message of the same peer;
- `by_flag`: the server's `Message.Mentioned`, which covers cases we do not
  reconstruct.

`answered_at`/`answered_by` receive the date and id of the **first later
`out=1` message** in the same peer (and in the same `topic_id` in forums). The value
is computed in the `Apply` that writes that message and recomputed if it is
deleted. `server_unread` is a separate state, updated by the dialog counters
and by `getUnreadMentions`, and is never confused with `answered`
(PLANO §4.1). Edits recompute the row (S09).

## 8. FTS5

- Table `messages_fts` **contentless** (`content=''`) with
  `contentless_delete=1`: the text is only in `messages.text`, not in two
  copies. The columns are `text`, the media caption (which is in `text`),
  `transcript` and `sender`.
- Tokenizer `unicode61 remove_diacritics 2` (the probe confirmed that
  `reuniao` finds `reunião`); `prefix='2 3'`.
- FTS5 `secure-delete` is turned on at creation. The probe with the data from §5
  showed that, in one segment layout, `secure-delete` alone left the
  token in the file, while `secure-delete` + `optimize` + `wal_checkpoint(TRUNCATE)`
  left no residue in either tested layout. That is why the cleanup job
  (lifecycle.md §3) ends with `optimize` (or the cheapest command that passes
  S10/L03) and with the WAL checkpoint. The file forensics test is
  mandatory; it outweighs this recommendation.
- `search_messages` answers `full_text:true` when FTS covered the query and
  returns `history_since` with the coverage (parity T05).

## 9. DDL (version 1)

```sql
PRAGMA user_version = 1;

CREATE TABLE accounts (
  account        TEXT PRIMARY KEY,
  self_id        INTEGER NOT NULL DEFAULT 0,
  phone          TEXT NOT NULL DEFAULT '',
  username       TEXT NOT NULL DEFAULT '',
  first_name     TEXT NOT NULL DEFAULT '',
  last_name      TEXT NOT NULL DEFAULT '',
  premium        INTEGER NOT NULL DEFAULT 0,
  auth_state     TEXT NOT NULL DEFAULT 'none'
                 CHECK (auth_state IN ('none','authorized','logged_out','revoked')),
  auth_reason    TEXT NOT NULL DEFAULT '',
  bound_at       INTEGER,
  updated_at     INTEGER NOT NULL
);

CREATE TABLE peers (
  account        TEXT NOT NULL,
  peer_type      TEXT NOT NULL CHECK (peer_type IN ('user','chat','channel')),
  peer_id        INTEGER NOT NULL,
  access_hash    INTEGER,                       -- only the engine reads it
  hash_min       INTEGER NOT NULL DEFAULT 0,
  kind           TEXT NOT NULL CHECK (kind IN ('self','user','bot','group','supergroup','channel')),
  title          TEXT NOT NULL DEFAULT '',
  first_name     TEXT NOT NULL DEFAULT '',
  last_name      TEXT NOT NULL DEFAULT '',
  username       TEXT NOT NULL DEFAULT '',
  phone          TEXT NOT NULL DEFAULT '',
  search_name    TEXT NOT NULL DEFAULT '',      -- lowercase, without accents
  is_contact     INTEGER NOT NULL DEFAULT 0,
  forum          INTEGER NOT NULL DEFAULT 0,
  protected      INTEGER NOT NULL DEFAULT 0,    -- noforwards
  participants   INTEGER NOT NULL DEFAULT 0,
  access         TEXT NOT NULL DEFAULT 'ok'
                 CHECK (access IN ('ok','left','kicked','banned','lost','deleted')),
  access_reason  TEXT NOT NULL DEFAULT '',
  access_changed_at INTEGER,
  migrated_to_type TEXT,
  migrated_to_id   INTEGER,
  updated_at     INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id)
) WITHOUT ROWID;
CREATE INDEX peers_search ON peers(account, search_name);

CREATE TABLE peer_usernames (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  username TEXT NOT NULL COLLATE NOCASE,
  PRIMARY KEY (account, peer_type, peer_id, username),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX peer_usernames_lookup ON peer_usernames(account, username);

CREATE TABLE dialogs (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  folder_id          INTEGER NOT NULL DEFAULT 0,  -- 1 = Archived
  pinned             INTEGER NOT NULL DEFAULT 0,
  pin_order          INTEGER NOT NULL DEFAULT 0,
  top_message_id     INTEGER NOT NULL DEFAULT 0,
  read_inbox_max_id  INTEGER NOT NULL DEFAULT 0,
  read_outbox_max_id INTEGER NOT NULL DEFAULT 0,
  unread_count       INTEGER NOT NULL DEFAULT 0,
  unread_mentions    INTEGER NOT NULL DEFAULT 0,
  unread_reactions   INTEGER NOT NULL DEFAULT 0,
  unread_mark        INTEGER NOT NULL DEFAULT 0,
  mute_until         INTEGER,
  ttl_period         INTEGER NOT NULL DEFAULT 0,
  last_message_date  INTEGER,
  refreshed_at       INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX dialogs_recent ON dialogs(account, last_message_date DESC);

CREATE TABLE messages (
  row_id         INTEGER PRIMARY KEY,           -- internal; FTS rowid
  account        TEXT NOT NULL,
  peer_type      TEXT NOT NULL,
  peer_id        INTEGER NOT NULL,
  msg_id         INTEGER NOT NULL,
  date           INTEGER NOT NULL,
  edit_date      INTEGER NOT NULL DEFAULT 0,
  out            INTEGER NOT NULL DEFAULT 0,
  sender_type    TEXT,
  sender_id      INTEGER,
  sender_name    TEXT NOT NULL DEFAULT '',
  post_author    TEXT NOT NULL DEFAULT '',
  text           TEXT NOT NULL DEFAULT '',      -- text or caption
  entities_json  TEXT,                          -- []engine.Entity
  media_type     TEXT,
  media_json     TEXT,                          -- engine.Media without Remote
  media_remote   BLOB,                          -- opaque location; only the engine reads it
  media_ttl      INTEGER NOT NULL DEFAULT 0,    -- >0: view-once, never downloaded
  location_json  TEXT,
  poll_json      TEXT,
  reply_to_msg_id  INTEGER,
  reply_to_peer_type TEXT,
  reply_to_peer_id   INTEGER,
  reply_top_id     INTEGER,
  forward_json   TEXT,
  topic_id       INTEGER NOT NULL DEFAULT 0,
  grouped_id     INTEGER NOT NULL DEFAULT 0,
  service_type   TEXT,
  service_json   TEXT,
  mentioned      INTEGER NOT NULL DEFAULT 0,
  media_unread   INTEGER NOT NULL DEFAULT 0,
  silent         INTEGER NOT NULL DEFAULT 0,
  post           INTEGER NOT NULL DEFAULT 0,
  pinned         INTEGER NOT NULL DEFAULT 0,
  protected      INTEGER NOT NULL DEFAULT 0,
  views          INTEGER NOT NULL DEFAULT 0,
  forwards       INTEGER NOT NULL DEFAULT 0,
  ttl_period     INTEGER NOT NULL DEFAULT 0,
  expires_at     INTEGER,                       -- date + ttl_period
  partial        INTEGER NOT NULL DEFAULT 0,    -- incomplete copy (e.g. a search result)
  version        INTEGER NOT NULL DEFAULT 1,
  source         TEXT NOT NULL,
  first_seen_at  INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  UNIQUE (account, peer_type, peer_id, msg_id),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers
);
CREATE INDEX messages_by_date   ON messages(account, peer_type, peer_id, date);
CREATE INDEX messages_global    ON messages(account, date);
CREATE INDEX messages_out       ON messages(account, peer_type, peer_id, out, date);
CREATE INDEX messages_sender    ON messages(account, sender_type, sender_id, date);
CREATE INDEX messages_media     ON messages(account, media_type) WHERE media_type IS NOT NULL;
CREATE INDEX messages_expiry    ON messages(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX messages_nonchan   ON messages(account, msg_id) WHERE peer_type <> 'channel';

CREATE TABLE message_tombstones (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  msg_id INTEGER NOT NULL,
  reason     TEXT NOT NULL CHECK (reason IN ('deleted','revoked','ttl','access_purge','user_purge')),
  deleted_at INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id, msg_id)
) WITHOUT ROWID;                                -- ids only, never content

CREATE TABLE pending_deletes (                  -- §4: ids without a peer, not seen yet
  account TEXT NOT NULL, msg_id INTEGER NOT NULL, received_at INTEGER NOT NULL,
  PRIMARY KEY (account, msg_id)
) WITHOUT ROWID;

CREATE TABLE reactions (
  row_id      INTEGER PRIMARY KEY REFERENCES messages(row_id) ON DELETE CASCADE,
  counts_json TEXT NOT NULL,
  recent_json TEXT,
  counts_hash TEXT NOT NULL,
  updated_at  INTEGER NOT NULL,
  version     INTEGER NOT NULL DEFAULT 0 -- migration v6; private event identity
);

CREATE TABLE mentions (
  row_id       INTEGER PRIMARY KEY REFERENCES messages(row_id) ON DELETE CASCADE,
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  msg_id       INTEGER NOT NULL,
  topic_id     INTEGER NOT NULL DEFAULT 0,
  date         INTEGER NOT NULL,
  by_name      INTEGER NOT NULL DEFAULT 0,
  by_username  INTEGER NOT NULL DEFAULT 0,
  by_reply     INTEGER NOT NULL DEFAULT 0,
  by_flag      INTEGER NOT NULL DEFAULT 0,
  answered_at  INTEGER,
  answered_by  INTEGER,
  server_unread INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX mentions_by_date ON mentions(account, date DESC);
CREATE INDEX mentions_open    ON mentions(account, peer_type, peer_id, date) WHERE answered_at IS NULL;

CREATE TABLE groups_full (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  about TEXT NOT NULL DEFAULT '', owner_id INTEGER, created_at INTEGER,
  linked_type TEXT, linked_id INTEGER,
  size INTEGER NOT NULL DEFAULT 0, partial INTEGER NOT NULL DEFAULT 0,
  am_admin INTEGER NOT NULL DEFAULT 0, snapshot_at INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id),
  FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers ON DELETE CASCADE
) WITHOUT ROWID;

CREATE TABLE participants (
  account TEXT NOT NULL, group_type TEXT NOT NULL, group_id INTEGER NOT NULL,
  user_type TEXT NOT NULL, user_id INTEGER NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('creator','admin','member','restricted','banned','left')),
  joined_at INTEGER, updated_at INTEGER NOT NULL,
  PRIMARY KEY (account, group_type, group_id, user_type, user_id)
) WITHOUT ROWID;

-- Updates state (gotd updates.State), global and per channel.
CREATE TABLE updates_state (
  account     TEXT PRIMARY KEY,
  pts INTEGER NOT NULL, qts INTEGER NOT NULL, date INTEGER NOT NULL, seq INTEGER NOT NULL,
  held        INTEGER NOT NULL DEFAULT 0,       -- I4
  held_reason TEXT NOT NULL DEFAULT '',
  last_update_at INTEGER,
  updated_at  INTEGER NOT NULL
);
CREATE TABLE channel_state (
  account TEXT NOT NULL, channel_id INTEGER NOT NULL,
  pts INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  PRIMARY KEY (account, channel_id)
) WITHOUT ROWID;                                -- access_hash lives in peers

CREATE TABLE backfill_cursors (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  newest_id   INTEGER NOT NULL DEFAULT 0,
  oldest_id   INTEGER NOT NULL DEFAULT 0,
  oldest_date INTEGER,
  complete    INTEGER NOT NULL DEFAULT 0,
  state       TEXT NOT NULL DEFAULT 'idle'
              CHECK (state IN ('idle','running','flood_wait','no_access','error')),
  flood_until INTEGER,
  last_error  TEXT NOT NULL DEFAULT '',
  fetched     INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id)
) WITHOUT ROWID;

CREATE TABLE gaps (
  id INTEGER PRIMARY KEY,
  account TEXT NOT NULL, peer_type TEXT, peer_id INTEGER,   -- NULL = whole account
  reason TEXT NOT NULL CHECK (reason IN ('channel_too_long','difference_too_long','state_lost','access_lost')),
  from_id INTEGER, to_id INTEGER,
  detected_at INTEGER NOT NULL, filled_at INTEGER,
  recovery_anchor TEXT, recovery_cursor TEXT -- migration v3; NULL for legacy/unknown baselines
);
CREATE INDEX gaps_open ON gaps(account, peer_type, peer_id) WHERE filled_at IS NULL;

-- Downloaded media and transcripts: they go away with the message.
CREATE TABLE media_files (
  row_id INTEGER NOT NULL REFERENCES messages(row_id) ON DELETE CASCADE,
  variant TEXT NOT NULL CHECK (variant IN ('full','thumb')),
  path TEXT NOT NULL UNIQUE, bytes INTEGER NOT NULL, sha256 TEXT NOT NULL,
  downloaded_at INTEGER NOT NULL,
  PRIMARY KEY (row_id, variant)
);
CREATE TABLE profile_photos (
  account TEXT NOT NULL, peer_type TEXT NOT NULL, peer_id INTEGER NOT NULL,
  variant TEXT NOT NULL, path TEXT NOT NULL UNIQUE, bytes INTEGER NOT NULL,
  downloaded_at INTEGER NOT NULL,
  PRIMARY KEY (account, peer_type, peer_id, variant)
);
CREATE TABLE files_to_unlink (                  -- the file system is not transactional
  path TEXT PRIMARY KEY, queued_at INTEGER NOT NULL, reason TEXT NOT NULL
);
CREATE TRIGGER media_files_ad AFTER DELETE ON media_files BEGIN
  INSERT OR IGNORE INTO files_to_unlink(path, queued_at, reason) VALUES (old.path, unixepoch(), 'message_gone');
END;

CREATE TABLE transcripts (
  row_id INTEGER PRIMARY KEY REFERENCES messages(row_id) ON DELETE CASCADE,
  text TEXT NOT NULL, raw_text TEXT, language TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '', source TEXT NOT NULL CHECK (source IN ('local','manual','telegram')),
  created_at INTEGER NOT NULL
);

CREATE TABLE exports (
  id INTEGER PRIMARY KEY, account TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE, filter_json TEXT NOT NULL,
  count INTEGER NOT NULL, bytes INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE TRIGGER exports_ad AFTER DELETE ON exports BEGIN
  INSERT OR IGNORE INTO files_to_unlink(path, queued_at, reason) VALUES (old.path, unixepoch(), 'export_gone');
END;

-- Contentless FTS5 (§8). rowid = messages.row_id.
CREATE VIRTUAL TABLE messages_fts USING fts5(
  text, transcript, sender,
  content='', contentless_delete=1,
  tokenize='unicode61 remove_diacritics 2', prefix='2 3'
);
INSERT INTO messages_fts(messages_fts, rank) VALUES ('secure-delete', 1);

CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, text, transcript, sender) VALUES (new.row_id, new.text, '', new.sender_name);
END;
CREATE TRIGGER messages_au AFTER UPDATE OF text, sender_name ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.row_id;
  INSERT INTO messages_fts(rowid, text, transcript, sender)
    VALUES (new.row_id, new.text, coalesce((SELECT text FROM transcripts WHERE row_id = new.row_id), ''), new.sender_name);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.row_id;
END;
CREATE TRIGGER transcripts_aiu AFTER INSERT ON transcripts BEGIN
  DELETE FROM messages_fts WHERE rowid = new.row_id;
  INSERT INTO messages_fts(rowid, text, transcript, sender)
    SELECT row_id, text, new.text, sender_name FROM messages WHERE row_id = new.row_id;
END;
CREATE TRIGGER transcripts_ad AFTER DELETE ON transcripts BEGIN
  DELETE FROM messages_fts WHERE rowid = old.row_id;
  INSERT INTO messages_fts(rowid, text, transcript, sender)
    SELECT row_id, text, '', sender_name FROM messages WHERE row_id = old.row_id;
END;
```

DDL notes:

- `transcripts_ad` also fires in the message-deletion cascade. In that
  case, the `SELECT` does not find the row, because the message has already been deleted,
  so nothing is reinserted. Front B must prove this in S10.
- The probe with `modernc.org/sqlite` v1.60.1 confirmed: FK `ON DELETE CASCADE`
  fires the `media_files` trigger; contentless FTS with `contentless_delete`
  accepts `DELETE`/`UPDATE` from the triggers; `remove_diacritics 2` works; and,
  with `PRAGMA secure_delete=ON`, the content table's text leaves no
  residue. Without it, the residue appeared.
- `files_to_unlink` is emptied by a janitor **after** the commit (§3 of
  lifecycle.md). A crash between the commit and the `unlink` leaves the queue, which is
  resumed at boot (test L04).
- `triage` (from `internal/state`) migrates with the key `(account, peer_type, peer_id)`;
  its DDL belongs to front H, with no FK to `messages`.
