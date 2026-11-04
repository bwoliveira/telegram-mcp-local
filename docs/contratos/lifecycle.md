# Data lifecycle contract

Version 1 (S0 draft for freezing), 01/11/2026. Source: `docs/PLANO.md` v0.4
section 3, "Ciclo de vida dos dados" (data lifecycle), and §2, item 5 (terms
§1.4). Owners: A (detection in the engine), B (removal in the store), J (media,
transcription, export), K (panel and account actions).

General rule: **what disappears on Telegram disappears here, for real.**
Removing means `DELETE` of the row and of everything derived from it, plus
`unlink` of the file. A "deleted" flag is not enough. Only a tombstone with the
ids, without content, remains, to prevent an old copy from coming back
(store.md I6).

## 1. Where a message's content can be

| Place | How it disappears | Proof |
|---|---|---|
| `messages` (row) | `DELETE` | count equal to 0 |
| `messages_fts` | trigger `messages_ad` (contentless_delete + secure-delete) | `MATCH` equal to 0 and file forensics |
| `mentions`, `reactions`, `transcripts`, `media_files` | FK `ON DELETE CASCADE` | count equal to 0 |
| downloaded media file | trigger → `files_to_unlink` → janitor `unlink` | `stat` → `ENOENT` |
| temporary link `/media/{token}` | the janitor revokes the tokens that point to the file | `GET` → 404 |
| NDJSON export | retention (§3) or `purge_media`; never includes TTL (§2) | `grep` for the marker in the folder equal to 0 |
| WAL and free pages | `secure_delete=ON` + `wal_checkpoint(TRUNCATE)` at the end of the job | file forensics |
| in-memory webhook queue | the pending delivery of a deleted message is discarded before sending (webhooks.md §6) | test WH14 |
| logs | never contain text (security.md §9) | test SEC-L |

"File forensics" means: after the job, read `store.db`, `store.db-wal` and the
data folder byte by byte and search for the test's unique marker in lowercase.
Today the probe with `modernc.org/sqlite` v1.60.1 passes this; the test ensures
it keeps passing.

## 2. TTL and self-destructing content

Telegram has three cases. All of them are treated as a deadline that also
applies to us (terms §1.4, "do not prevent disappearance").

| Case | Signal | Rule |
|---|---|---|
| Chat auto-delete (`ttl_period`) | `Message.TTLPeriod`, `Dialog.TTLPeriod` | `expires_at = date + ttl_period`. On write, if `expires_at <= now`, the message is discarded (`Skipped`) |
| View-once / timed media | `Media.TTLSeconds > 0` | The content is **never** downloaded, transcribed, exported, forwarded or delivered by webhook. Only the type is stored (`media_ttl`) and the text shows "[mídia temporária]" (temporary media). `Download`/`TranscribeNative`/`Forward` → `ErrProtectedContent` |
| Expired on the server | `messageMediaPhoto/Document` without content, `updateDeleteMessages` | same as a deletion (§3) |

- **Deadline:** the janitor runs every 60 s and at boot. It deletes
  `messages WHERE expires_at <= now()` in batches of 500 inside a transaction,
  writes a tombstone with `reason='ttl'`, emits the `delete` event with
  `source=ttl` and finishes with the WAL checkpoint. The maximum accepted delay
  is `60 s + batch duration`. For a shorter deadline, the open decision is D6.
- Daemon off: at boot, the janitor runs **before** sync and the HTTP routes.
  Nobody reads an expired message because it "survived the shutdown".
- A change of `ttl_period` in the chat does not rewrite old messages. As on
  Telegram, the timer applies to new messages.
- Reading: every store query filters `expires_at IS NULL OR expires_at > now()`.
  An expired message not yet swept is never returned.

## 3. Deletions, retention and cleanup

- **Known deletion** (`updateDeleteMessages`, `updateDeleteChannelMessages`,
  our own `Delete` with `Revoke`, or our own "for me only" deletion):
  `Apply{Deletes}` → `DELETE` + tombstone in the same transaction (store.md §5.2).
  A "for me only" deletion also removes locally, because the user asked for the
  message to disappear from the account.
- **Deletion Telegram does not announce:** messages deleted while the account
  was beyond the reach of `getDifference`, or after a `differenceTooLong`.
  These deletions are not detectable without re-querying each id. This limit is
  documented; we do not promise to cover it. A backfill that finds
  `messageEmpty` for a known id applies the deletion.
- **Configurable retention** (panel P42, API A28), always with an explicit
  default:
  - downloaded media: `media_retention_days`, default 30; `0` turns it off;
  - exports: `export_retention_days`, default 7;
  - transcripts: follow the message; they have no retention of their own;
  - messages: no automatic retention by default. The history is the product;
    `message_retention_days` is optional.
- `purge_media` and `POST /api/media/purge` delete the file and the
  `media_files`/`exports` row in the same transaction as the `files_to_unlink`
  queue.
- **Janitor:** it is a single job. It does TTL and then retention; next it
  processes `files_to_unlink` and revokes temporary links. Last it runs
  `INSERT INTO messages_fts(messages_fts) VALUES('optimize')` (or `merge`,
  if S10/L03 pass with it), `PRAGMA wal_checkpoint(TRUNCATE)` and
  `PRAGMA incremental_vacuum`. If it dies midway, it resumes from where it
  stopped, because each step is idempotent.

## 4. Disconnect, log out and erase everything

These are three distinct actions, with three buttons, three routes and three
tests. None implies another unless the user asks.

| | Disconnect | Log out of the account | Erase everything |
|---|---|---|---|
| Method | `Controller.Disconnect` | `Controller.LogOut` | `Host.EraseEverything` (daemon) |
| Telegram | no call; closes the connection | `auth.logOut` (revokes the session) | `auth.logOut`, if there is still a session |
| Sync | stops; `StatePaused`, persisted at boot | stops; `StateLoggedOut` | stops |
| Local session | **kept** | **deleted** (`SessionStore.DeleteSession`) | deleted |
| History (`store.db`) | kept | kept | **deleted** (file, `-wal`, `-shm`) |
| Media, transcripts, exports | kept | kept | deleted |
| `state.db`, webhooks, tokens, configuration | kept | kept | deleted; the configured AI clients are warned that the token is no longer valid |
| MCP afterwards | local reads work; network → `ErrPaused` | local reads work; network → `ErrNotAuthorized` | nothing |
| Undo | `Connect` | new login (same `self_id` required, or "erase everything" first) | — |
| Confirmation in the panel | one click | simple confirmation | type the word (P47, inherited) |
| Route | `POST /api/account/disconnect` | `POST /api/account/logout` | `POST /configuracoes/apagar` (inherited) |

- A failure in `auth.logOut`, for example with no network, **does not prevent**
  logging out: the local session is deleted anyway and the panel warns "also
  end it in Settings → Devices on Telegram".
- "Erase everything" deletes files with `unlink`, after closing the database.
  It does not promise secure deletion at the disk level (SSD/journaling); the
  panel text says so.
- Upstream: in the historical upstream product, `EraseEverything` disconnects
  the upstream messaging account (through the upstream CLI) and deletes the
  folder (`cmd/app/host.go:135`). We keep the behaviour and the confirmation
  word.

## 5. External revocation and loss of access

**External revocation:** happens when the session is ended on the phone or
when Telegram invalidates the key (`AUTH_KEY_UNREGISTERED`, `SESSION_REVOKED`,
`AUTH_KEY_DUPLICATED`, `USER_DEACTIVATED`).

1. The engine stops sync and the `updates.Manager` (`Reset`).
2. `SessionStore.DeleteSession` is called (the key is no longer usable).
3. `Persister.SetAuthState(revoked, reason)` is written, together with the
   event `auth{state:revoked, reason}`.
4. `Status.State = revoked`; `telegram_status`, `health` and the panel show the
   reason and the next step ("log in again or erase everything").
5. The history stays, including the tombstone and the TTL. The janitor keeps
   running: a message that expires while the account is revoked is deleted
   anyway.

**Loss of access to a conversation:** occurs with `CHANNEL_PRIVATE`,
`CHAT_FORBIDDEN`, `USER_BANNED_IN_CHANNEL`, `updateChannel` showing `left` or
`kicked`, gotd's `OnChannelInaccessible`, or when the account itself uses
`Leave`.

1. `peers.access` becomes `left|kicked|banned|lost`, and the conversation's
   `channel_state` is removed (`DropChannelState`), because gotd must not keep
   asking for differences for it.
2. One unfilled terminal row in `gaps` summarizes `access_lost` per account and
   typed peer (including account-wide scope), together with the first `access`
   event. Later observations monotonically refresh `detected_at`, even across
   metadata state oscillations or restarts, rather than appending episodes.
   Access-state changes still emit their own `access` events. Schema v9 compacts
   existing unfilled duplicates transactionally, retaining the latest detection;
   filled evidence and other causes are untouched. Returning to `ok` does not
   fill this evidence or prove history recovery.
   This is terminal/inaccessible coverage evidence, counted separately as
   `updates.inaccessible_gaps`, not a recoverable `updates.open_gaps` entry.
   It remains visible to coverage reads but does not permanently block
   `ready:true`; this does not claim that inaccessible history was recovered.
3. The history **stays** and is marked: `list_chats` shows `access:"lost"` and
   `get_chat_messages` responds with `access_lost_at`.
4. The backfill cursor goes to `state=no_access`, and no new attempt is made
   until an update shows access is back.
5. Test L08. Open decision D7 (delete or keep the history of a conversation
   with lost access).

## 6. Protected content (`noforwards`)

Chats and messages with `Protected` may be read and indexed, because the
official client also shows the content. They are never forwarded
(`Forward` → `ErrProtectedContent`), downloaded out of the app (`download_media`
→ error), exported as media, or sent via temporary link. The text goes into
the NDJSON export with `"protected":true`. Open decision D8: include or omit
the text of these messages in the export.

## 7. Secret Chats

They are out of scope. The engine does not negotiate end-to-end keys.
`updateEncryption`, `updateNewEncryptedMessage` and `updateEncryptedChatTyping`
are discarded without writing anything; so is `encryptedMessage` coming from
`getDifference`. In onboarding (P01) and in the README the sentence "Secret chats exist only
on the devices where they were created and do not appear here" appears
(localized in the panel). `telegram_status` reports
`secret_chats:"unsupported"`. Test L09.

## 8. Exports

- The format is NDJSON, with a header line carrying the filter, the backfill
  coverage (`history_since` and the open gaps) and the date.
- Never includes: messages with `ttl_period > 0` or `media_ttl > 0`,
  tombstones, `access_hash`, `media_remote` or local media paths.
- The file is written to `<data>/exports/`, with permission `0600`, and
  registered in `exports`. It leaves through the same retention (§3) and
  through `purge_media`.

## 9. Required tests

| ID | Case | Acceptance |
|---|---|---|
| L01 | TTL expires with the daemon on | Within 60 s + batch: row, FTS, transcript, mention, media and temporary link disappear; `ttl` tombstone; `delete` event |
| L02 | TTL expires with the daemon off | At boot, the janitor runs before the routes: an immediate request to `get_chat_messages` does not return the message |
| L03 | File forensics | Unique marker in text, transcript and file name: after L01/S10 and the job, it does not appear in `store.db`, `-wal` or the data folder |
| L04 | Crash between commit and `unlink` | `files_to_unlink` survives; at boot, the file is deleted |
| L05 | View-once media | `download_media`, `transcribe_audio`, `forward_message` and `export_messages` refuse or omit; webhook without `media.file`; no file is created |
| L06 | Media and export retention | Items older than N days disappear; newer ones stay; `0` turns it off |
| L07a | Disconnect | Session kept; zero network calls (spy invoker); local reads respond; `Connect` comes back without login |
| L07b | Log out | `auth.logOut` called; session deleted; history intact; a network failure still deletes the local session and warns |
| L07c | Erase everything | Session, `store.db*`, `state.db`, media, exports and transcription model (if requested) disappear; the folder is empty, except for what the test lists as allowed |
| L08 | Loss of access | `CHANNEL_PRIVATE` in the fake → `access=lost`, `channel_state` removed, history present and marked, `access` event, cursor `no_access` |
| L09 | Secret Chat | `updateNewEncryptedMessage` in the fake: zero rows, zero events, zero files |
| L10 | External revocation | `AUTH_KEY_UNREGISTERED` → `DeleteSession` called, `auth_state=revoked`, panel shows reason, history stays and the janitor keeps deleting TTL |
| L11 | Reads never see expired | Message with `expires_at` in the past and janitor stopped: no read returns it |
| L12 | Export | Does not contain TTL, tombstones, hashes or paths; is subject to retention |
