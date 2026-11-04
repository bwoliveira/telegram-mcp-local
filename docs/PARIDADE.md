# Telegram MCP capability matrix

This technical catalogue is not evidence of real-account acceptance.
See README.md and THIRD_PARTY_NOTICES.md for retained upstream attribution.

## 1. Tools (40)

| ID | Original tool | Parameters (name:type, * required) | Response (main fields) | Upstream | Telegram tool | Telegram method / source | Track | fake | real | Limitation |
|---|---|---|---|---|---|---|---|---|---|---|
| T01 | `health` | `max_silence_hours`:number | `status`, `summary`, `checks[{name,status,detail,fix}]`, `activity`, `checked_at` | `internal/mcp/tools.go:57`, `internal/mcp/health.go:22`, `internal/mcp/health.go:183` | `health` | local + MTProto client state + `updates.getState` | C | [x] | [ ] | There is no "delivered": health comes from MTProto and the database, not from the phone |
| T02 | upstream status tool (named after the predecessor service) | — | `gateway`, `version`, `endpoint`, `mode`, `ready`, `logged_out`, `error`, `paused` | `internal/mcp/tools.go:64`, `internal/mcp/status.go:8` | `telegram_status` | `users.getFullUser`, `account.getAuthorizations`, `updates.getState` + local coverage | C | [x] | [ ] | The session comes from `account.getAuthorizations`; there is no "phone online" |
| T03 | `list_chats` | `search`:string, `limit`:integer, `fields`:string[] | `chats[{jid,kind,name,last_message_ts,archived,pinned,muted_until,unread,unread_count}]`, `count`, `history_since` | `internal/mcp/tools.go:69`, `internal/mcp/handlers.go:286`, `internal/mcp/reads.go:27` | `list_chats` | `messages.getDialogs` (+ `messages.getFolders` for folders, archived, pinned) | C | [x] | [ ] | `kind` now distinguishes user, bot, group, supergroup and channel; Telegram folders are named filters |
| T04 | `get_chat_messages` | `*chat_jid`:string, `since`:string, `until`:string, `limit`:integer, `order`:string, `fields`:string[], `max_content_chars`:integer, `count_only`:boolean | `chat_jid`, `chat_name`, `messages[]`, `count`, `untrusted_content` | `internal/mcp/tools.go:78`, `internal/mcp/handlers.go:322`, `internal/mcp/reads.go:22` | `get_chat_messages` | `messages.getHistory` (offset_id, add_offset, limit) | C | [x] | [ ] | Restricted channels require a member account (plan §3.3); the server may refuse old history |
| T05 | `search_messages` | `*query`:string, `chat_jid`:string, `since`:string, `until`:string, `limit`:integer, `fields`:string[], `max_content_chars`:integer | `query`, `messages[]`, `count`, `full_text`, `history_since`, `untrusted_content` | `internal/mcp/tools.go:92`, `internal/mcp/handlers.go:406` | `search_messages` | local FTS5 over text and transcripts; `messages.search` when the index does not cover it | C | [x] | [ ] | Server search does not accept every filter (date, direction, media type) |
| T06 | `list_contacts` | `search`:string, `limit`:integer | `contacts[]`, `count` | `internal/mcp/tools.go:105`, `internal/mcp/handlers.go:471` | `list_contacts` | `contacts.getContacts` (+ `contacts.search`) | C | [x] | [ ] | Telegram also returns seen "non-contacts"; names may come back empty |
| T07 | `list_groups` | `search`:string, `limit`:integer | `groups[]`, `count` | `internal/mcp/tools.go:113`, `internal/mcp/handlers.go:481` | `list_groups` | `messages.getDialogs` filtered by peer + channels; local display | C | [x] | [ ] | Includes channels, which are not groups; marked by `kind` |
| T08 | `get_group` | `*group_jid`:string, `search`:string, `live`:boolean | `jid`, `name`, `owner`, `community`, `community_jid`, `created_at`, `participants[]`, `size`, `snapshot_at` | `internal/mcp/tools.go:121`, `internal/mcp/handlers.go:492`, `internal/index/index.go:216` | `get_group` | `messages.getFullChat` / `channels.getFullChannel` (+ `channels.getParticipants`) | C | [x] | [ ] | Participant list limited by Telegram: returned marked as partial |
| T09 | `send_text_message` | `*to`:string, `*text`:string, `reply_to`:string, `mentions`:string[], `dry_run`:boolean | `message_id` (from `id`), `note` ("accepted by" the predecessor service…) | `internal/mcp/tools.go:130`, `internal/mcp/handlers.go:568`, `internal/mcp/handlers.go:553` | `send_text_message` | `messages.sendMessage` with mention entities (UTF-16 offset) and `reply_to_msg_id` | D | [x] | [ ] | A mention requires the user's `access_hash`; "accepted" is not "delivered" |
| T10 | `send_media_message` | `*to`:string, `*type`:string (image, video, audio, document, sticker), `*url`:string, `caption`:string, `filename`:string, `reply_to`:string, `dry_run`:boolean | `message_id`, `type`, `file`, `caption`, `filename`, `note` | `internal/mcp/tools.go:141`, `internal/mcp/handlers.go:598` | `send_media_message` | `messages.uploadMedia` + `messages.sendMedia` (`inputMediaUploadedPhoto` / `inputMediaUploadedDocument`) | G | [x] | [ ] | Telegram upload limit (2 GB, 4 GB premium); a sticker must be WebP |
| T11 | `download_media` | `*message_id`:string, `chat_jid`:string, `link`:boolean | `path`, `bytes`, `media_type`, `mime_type`, `filename`, embedded content, `link` | `internal/mcp/tools.go:154`, `internal/mcp/handlers.go:970`, `internal/mcp/medialinks.go:21` | `download_media` | `upload.getFile` with a refreshed `file_reference` | J | [x] | [ ] | `file_reference` expires: requires a refresh; gotd's downloader, not the predecessor's |
| T12 | `transcribe_audio` | `*message_id`:string, `chat_jid`:string, `language`:string, `refresh`:boolean | `transcript` (text, `cached`), `context`, `review`, `setup`, `untrusted_content` | `internal/mcp/tools.go:163`, `internal/mcp/transcription.go:45` | `transcribe_audio` | local whisper.cpp; `messages.transcribeAudio` only as an explicit option | J | [x] | [ ] | Telegram's native transcription is optional and per account; the default is the local model |
| T13 | `save_transcript` | `*message_id`:string, `chat_jid`:string, `*text`:string, `language`:string, `model`:string | `saved`, `transcript` | `internal/mcp/tools.go:173`, `internal/mcp/transcription.go:72` | `save_transcript` | local | J | [x] | [ ] | — |
| T14 | `sync_history` | `chat_jid`:string, `count`:integer, `rounds`:integer, `chats`:integer | `started`, `reason`, `job{started_at,finished_at,chats,count,rounds,chats_done,messages_added,current_chat,failures}`, `next` | `internal/mcp/tools.go:184`, `internal/mcp/history.go:26`, `internal/mcp/history.go:14` | `sync_history` | backfill via `messages.getHistory` with a cursor, without depending on the phone | H | [x] | [ ] | FloodWait and rate limits; per-conversation coverage goes in the result |
| T15 | `delete_message` | `*message_id`:string, `chat_jid`:string, `for_me`:boolean, `confirm`:boolean | `preview`, `for_me`, `would_delete`, `next`, `deleted`, `message_id`, `chat_jid` | `internal/mcp/tools.go:194`, `internal/mcp/handlers.go:843` | `delete_message` | `messages.deleteMessages` with `revoke` | G | [x] | [ ] | Deleting for everyone depends on permission; `for_me` in a channel/supergroup is refused, never converted into a global deletion |
| T16 | `edit_message` | `*message_id`:string, `chat_jid`:string, `*text`:string | `message_id`, `note` (via `sendResult`) | `internal/mcp/tools.go:204`, `internal/mcp/handlers.go:780` | `edit_message` | `messages.editMessage` | G | [x] | [ ] | Only the account's own messages; Telegram does not keep an edit history |
| T17 | `react_to_message` | `*message_id`:string, `chat_jid`:string, `emoji`:string (default 👍) | `done`, `action`, `result` | `internal/mcp/tools.go:213`, `internal/mcp/handlers.go:794` | `react_to_message` | `messages.sendReaction` | G | [x] | [ ] | A reaction is per participant; in a channel only those allowed by the admin |
| T18 | `check_numbers` | `*numbers`:string[] | `results`, `note` | `internal/mcp/tools.go:222`, `internal/mcp/handlers.go:875` | `resolve_contact` | `contacts.resolveUsername` / `contacts.resolvePhone` | G | [x] | [ ] | Telegram privacy: `@username` depends on existence and access; phone depends on "who can find me" |
| T19 | `get_profile_picture` | `*to`:string, `full`:boolean | path, bytes, mime, content | `internal/mcp/tools.go:229`, `internal/mcp/handlers.go:889` | `get_profile_picture` | `users.getFullUser` (photo) / `photos.getUserPhotos` | G | [x] | [ ] | The photo may be hidden by privacy; returns an empty `photo` |
| T20 | `send_location` | `*to`:string, `*latitude`:number, `*longitude`:number, `name`:string, `address`:string | `message_id`, `note` | `internal/mcp/tools.go:237`, `internal/mcp/handlers.go:741` | `send_location` | `messages.sendMedia` with `inputMediaGeoPoint` | G | [x] | [ ] | Telegram has no address field: name and address go in the point's label |
| T21 | `send_poll` | `*to`:string, `*question`:string, `*options`:string[] (2 to 12), `max_answers`:integer (default 1) | `message_id`, `note` | `internal/mcp/tools.go:248`, `internal/mcp/handlers.go:763` | `send_poll` | `messages.sendMedia` with `inputMediaPoll` | G | [x] | [ ] | A quiz with a correct answer only through the app; poll anonymous by default, and the preview discloses the effective `anonymous: true` and `quiz: false` in its approval binding |
| T22 | `get_poll_results` | `*message_id`:string, `chat_jid`:string | results per option, voters, total | `internal/mcp/tools.go:258`, `internal/mcp/handlers.go:919` | `get_poll_results` | `messages.getPollResults` (+ `messages.getPollVotes`) | G | [x] | [ ] | An anonymous poll does not reveal who voted |
| T23 | `organise_chat` | `*chat_jid`:string, `*action`:string (archive, unarchive, pin, unpin, mute, unmute) | `done`, `action`, `chat_jid`, `result` | `internal/mcp/tools.go:266`, `internal/mcp/handlers.go:904` | `organise_chat` | `folders.editPeerFolders` (archive), `messages.toggleDialogPin`, `account.updateNotifySettings` (mute) | G | [x] | [ ] | Telegram only has the fixed "Archived" folder (id 1); muting is per peer |
| T24 | `forward_message` | `*message_id`:string, `chat_jid`:string, `*to`:string, `dry_run`:boolean | `forward{id,from,timestamp,text,media_type,filename}` | `internal/mcp/tools.go:274`, `internal/mcp/messaging.go:177` | `forward_message` | `messages.forwardMessages` | G | [x] | [ ] | Forwarding keeps the original author; editing afterwards is not possible |
| T25 | `mark_chat_read` | `*chat_jid`:string, `receipts`:boolean | `done`, `chat_jid`, `receipts`, `result` | `internal/mcp/tools.go:284`, `internal/mcp/messaging.go:198` | `mark_chat_read` | `messages.readHistory` (with `max_id`) | D | [x] | [ ] | "Mark as unread" has no guaranteed method in Telegram |
| T26 | `send_typing` | `*to`:string, `typing`:boolean, `audio`:boolean | `done`, `to`, `typing` | `internal/mcp/tools.go:292`, `internal/mcp/messaging.go:215` | `send_typing` | `messages.setTyping` | G | [x] | [ ] | The state expires in ~5 s; in a forum topic it needs `top_msg_id` |
| T27 | `get_message_context` | `*message_id`:string, `chat_jid`:string, `before`:integer, `after`:integer, `fields`:string[], `max_content_chars`:integer | `chat_jid`, `chat_name`, `before[]`, `message`, `after[]`, `untrusted_content` | `internal/mcp/tools.go:301`, `internal/mcp/reads.go:150` | `get_message_context` | local (`messages.getHistory` with offset_id and add_offset when missing) | C | [x] | [ ] | The window comes from the database: a message deleted in Telegram disappears |
| T28 | `message_stats` | `group_by`:string (chat, sender, day, month; default chat), `limit`:integer (default 50), filters `chat_jid`, `since`, `until`, `direction`, `media_type`, `exclude_groups` | `group_by`, `buckets`, `total`, `groups`, `truncated`, `note` | `internal/mcp/tools.go:313`, `internal/mcp/reads.go:189` | `message_stats` | local (aggregate SQL) | H | [x] | [ ] | Only counts what was synced; coverage goes in the result |
| T29 | `export_messages` | filters `chat_jid`, `since`, `until`, `direction`, `media_type`, `exclude_groups` | `path`, `format` (NDJSON), `count`, `bytes`, `note` | `internal/mcp/tools.go:321`, `internal/mcp/reads.go:261` | `export_messages` | local (NDJSON, oldest first) | H | [x] | [ ] | The file stays on the daemon's disk; the client needs access to the path |
| T30 | `list_unread` | `limit`:integer, `per_chat`:integer, `include_muted`:boolean, `include_archived`:boolean | `chats[]`, `count`, `unread_messages`, `note`, `untrusted_content` | `internal/mcp/tools.go:326`, `internal/mcp/triage.go:45` | `list_unread` | local + `messages.getPeerDialogs` (server counters) | C | [x] | [ ] | The server counter diverges from the database until the next sync |
| T31 | `list_unanswered` | `since`:string, `include_groups`:boolean, `include_group_mentions`:boolean, `min_age_hours`:number, `ignore_closing`:boolean, `include_muted`:boolean, `include_archived`:boolean, `include_handled`:boolean, `limit`:integer | `chats[{chat_jid,name,group,last_message,waiting_messages,age_hours,waiting_since,mention}]`, `count`, `truncated`, `since`, `note` | `internal/mcp/tools.go:336`, `internal/mcp/triage.go:142` | `list_unanswered` | local, same product rule as the original | H | [x] | [ ] | Depends on the backfill: an unsynced conversation does not appear |
| T32 | `list_mentions` | `since`:string, `chat_jid`:string, `only_unanswered`:boolean, `limit`:integer | `mentions[]`, `count`, `since`, `note`, `untrusted_content` | `internal/mcp/tools.go:351`, `internal/mcp/triage.go:263` | `list_mentions` | local (indexed entities); remote complement not implemented | H | [x] | [ ] | Telegram does not expose historical mentions via API: history is only our index |
| T33 | `mark_handled` | `*chat_jid`:string, `note`:string, `clear`:boolean | `cleared`, `chat_jid`, `handled`, `name`, `note` | `internal/mcp/tools.go:361`, `internal/mcp/triage.go:303` | `mark_handled` | local | H | [x] | [ ] | — |
| T34 | `snooze_chat` | `*chat_jid`:string, `*until`:string, `note`:string | `snoozed`, `chat_jid`, `name`, `until`, `note` | `internal/mcp/tools.go:370`, `internal/mcp/triage.go:321` | `snooze_chat` | local | H | [x] | [ ] | — |
| T35 | `manage_group_participants` | `*group_jid`:string, `*action`:string (add, remove, promote, demote), `*participants`:string[], `confirm`:boolean | `preview`, `group`, `would_remove`, `next`, `done`, `action`, `group_jid`, `result` | `internal/mcp/tools.go:379`, `internal/mcp/groups.go:19` | `manage_group_participants` | add: `messages.addChatUser` / `channels.inviteToChannel`; remove or ban: `messages.deleteChatUser` / `channels.editBanned`; promote or demote: `messages.editChatAdmin` / `channels.editAdmin` | I | [x] | [ ] | Without permission Telegram returns a clear error; in a channel only admins |
| T36 | `update_group` | `*group_jid`:string, `name`:string, `description`:string | `done`, `group_jid` | `internal/mcp/tools.go:389`, `internal/mcp/groups.go:54` | `update_group` | title: `messages.editChatTitle` / `channels.editTitle`; description: `messages.editChatAbout` | I | [x] | [ ] | A basic group uses `messages.editChatAbout`; without permission, error |
| T37 | `get_group_invite_link` | `*group_jid`:string, `reset`:boolean, `confirm`:boolean | `preview`, `group`, `next`, `group_jid`, `reset` | `internal/mcp/tools.go:398`, `internal/mcp/groups.go:91` | `get_group_invite_link` | `messages.exportChatInvite` (with `revoke` when `reset`) | I | [x] | [ ] | A group with approval requires a join request; the link may expire |
| T38 | `leave_group` | `*group_jid`:string, `confirm`:boolean | `preview`, `group_jid`, `group`, `next`, `left` | `internal/mcp/tools.go:407`, `internal/mcp/groups.go:115` | `leave_group` | `messages.deleteChatUser` (oneself) / `channels.leaveChannel` | I | [x] | [ ] | The last owner must transfer or delete the group first |
| T39 | `media_stats` | — | `inventory{dir,files,bytes,by_type,by_chat,duplicate_bytes,oldest_download,newest_download,retention_days,exports,exports_dir}`, `note` | `internal/mcp/tools.go:415`, `internal/mcp/media.go:218`, `internal/mcp/media.go:86` | `media_stats` | local (disk) | J | [x] | [ ] | Only counts what we downloaded, not what exists in Telegram |
| T40 | `purge_media` | `chat_jid`:string, `older_than_days`:integer, `min_megabytes`:integer, `confirm`:boolean, `preview_fingerprint`:string | `preview`, `preview_fingerprint`, `expires_in_seconds`, `would_delete_files`, `would_free_bytes`, `largest`, `next`, `deleted_files`, `freed_bytes` | `internal/mcp/tools.go:420`, `internal/mcp/media.go:263` | `purge_media` | local (disk) | J | [x] | [ ] | Deletes nothing in Telegram; preview → explicit human approval → confirm with the single-use fingerprint (expires after 10 minutes); changed files require a new preview |

**Tool acceptance notes (Stage 3):**

1. Each tool needs a fake test with at least: the happy path, a missing required argument, and one engine error path.
2. Writes require real verification in the plan's test environment (Saved Messages + private basic group, supergroup and channel). The rest is marked as unverified.
3. `mentions` (T09) follows telegram-cli's logic: entities with offsets in UTF-16, not in runes.
4. `dry_run` (T09, T10, T24) returns the draft and the resolved recipient without sending; it is the same contract as the original (`internal/mcp/messaging.go:73`).

---

## Security wave integration (base `ebcd74e`)

All seven fronts retain their history through explicit merge commits. No real
Telegram account, installed service, repository publication or release is part
of this verification; the existing **real** checkboxes remain unchanged.

| Front / owner | Contract change | Automated evidence | Real evidence / limits |
|---|---|---|---|
| #36 CI | Private homelab/public hosted runner selection; SHA-pinned checkout | Workflow lint; combined-tree CI | Repository remains private; public-runner launch checklist is pending. |
| #37 dependencies | Go 1.26.10, Wails 3.1.0 and fixed networking/crypto modules | Module verification, vet, race suite, govulncheck, native Linux build and Windows/macOS compile checks | No new native Windows/macOS runtime acceptance. |
| #38 build inputs | Immutable sidecar hashes/FFmpeg signature, LGPL-only configuration, binary-derived notices and CycloneDX | `test-build-inventory.py`, `test-sidecar-security.py`, `test-packaging.py`; real Linux payload inventories generated and checked in CI | Native sidecar rebuild/signature matrix and actual release-artifact inspection remain release gates. |
| #39 signing / N | Signed, version-bound manifests verified before checksum parsing and updater effects | `internal/updater` tests, `test-install.py`, `test-release.py` | Disposable fixture keys only; no release key or public release installed. |
| #40 HTTP/panel / F,K | Bearer only for MCP/allowlisted APIs; human capabilities need session/CSRF or trusted desktop, strict body limits and CSP | HTTP/panel regression tests, executable serve test; fake TCP smoke checks bearer `GET /` = 403 | No real account; trusted Wails-origin context regression does not certify native GUI execution. |
| #41 webhook / J,K | Private-network opt-in, numeric validated dialing, redirect/metadata/rebinding protection, bounded retries/response/DNS/dial work | `internal/webhook` security tests, real HTTP fallback regression, localized browser opt-in tests | In-memory delivery queue remains non-durable; no external receiver/account acceptance. |
| #42 MCP / C,G,J | Strict fields and required arguments, safe roots/download destinations, contact-hash redaction, expiring purge approval | `mcp_security_test.go`, `purge_recovery_test.go`, all-40 fake dispatcher and standalone bridge smoke | Quarantine recovers on the next confirmed purge; collisions/legacy flat files fail visibly without deleting bytes. Windows lacks POSIX directory fsync/power-loss guarantees. |

`telegram-mcp-bridge` is exercised against an isolated fake daemon, with exactly
40 unique tools and no use of the installed service port. The integration
verification record is [SECURITY-INTEGRATION.md](SECURITY-INTEGRATION.md).

## 2. Webhooks

The original has three events, registration via API, filters, HMAC signature and retry. In Telegram there are five events (§4.2).

| ID | Original event | Original envelope | Telegram event | Telegram source | Track | fake | real | Note |
|---|---|---|---|---|---|---|---|---|
| W01 | `message` | `event`, `delivery_id`, `sent_at`, `account`, `message{id,chat_jid,chat_name,group,timestamp,from_me,sender_jid,sender_name,text,media,reply_to,reaction,forwarded,edited,revoked,location,poll}` (`internal/webhook/webhook.go:42`) | `message` | `updateNewMessage` / `updateNewChannelMessage` (MTProto updates) | J | [x] | [ ] | Keeps the same envelope, unique `delivery_id` |
| W02 | `reaction` | `message{..., reaction{to,emoji}}` (`internal/webhook/webhook.go:96`) | `reaction` | `updateMessageReactions` | J | [x] | [ ] | A removed reaction is an event with an empty `emoji` |
| W03 | `receipt` | `receipt{chat_jid,sender_jid,message_ids,type,timestamp}` (`internal/webhook/webhook.go:102`) | `read` | `updateReadHistoryOutbox` (reads of our messages) | J | [x] | [ ] | Telegram has no per-person "delivered": only read |
| W04 | — | — | `edit` | `updateEditMessage` / `updateEditChannelMessage` | J | [x] | [ ] | New in Telegram; the original marks `edited` inside `message` |
| W05 | — | — | `delete` | `updateDeleteMessages` / `updateDeleteChannelMessages` | J | [x] | [ ] | New in Telegram; the original marks `revoked` inside `message` |
| W06 | test (`POST /api/webhooks/{id}/test`) | `test:true` + sample `message` (`internal/webhook/webhook.go:531`) | `test` | local, same signed envelope | J | [x] | [ ] | The panel's test button uses this path |

**Requirements inherited from the original (§4.2) — each one is an acceptance row:**

| ID | Requirement | Reference in the original | Track | fake | real |
|---|---|---|---|---|---|
| R01 | Registration with a valid HTTP(S) URL and a validated event list | `internal/webhook/webhook.go:383`, `internal/webhook/webhook.go:391`, `internal/webhook/webhook.go:405` | J | [x] | [ ] |
| R02 | Editing and removing a webhook by id, with a clear error when the id does not exist | `internal/webhook/webhook.go:438`, `internal/webhook/webhook.go:484` | J | [x] | [ ] |
| R03 | Filters by event, by conversation (`chats`), "only mine" (`include_own`) and `enabled`; the default is message, enabled, without own, all chats | `internal/webhook/webhook.go:369`, `internal/webhook/webhook.go:237` | J | [x] | [ ] |
| R04 | Secret shown only once, on creation, and never again | `internal/panel/webhooks.go:44`, `internal/webhook/webhook.go:405` | J | [x] | [ ] |
| R05 | Signature header `X-<predecessor>-MCP-Signature: sha256=<hex>` over the body, with HMAC-SHA256 of the secret; headers `X-<predecessor>-MCP-Event` and `X-<predecessor>-MCP-Delivery` (prefix named after the predecessor product) | `internal/webhook/webhook.go:355`, `internal/webhook/webhook.go:321` | J | [x] | [ ] |
| R06 | Test button that delivers a signed `test` event | `internal/webhook/webhook.go:538` | J | [x] | [ ] |
| R07 | Retry with ten attempts and an increasing wait between them | `internal/webhook/webhook.go:113`, `internal/webhook/webhook.go:292` | J | [x] | [ ] |
| R08 | Automatic disabling after consecutive failures in under a minute | `internal/webhook/webhook.go:276` | J | [x] | [ ] |
| R09 | Same `delivery_id` on resend, so the receiver can deduplicate | `internal/webhook/webhook.go:321` | J | [x] | [ ] |
| R10 | In-memory queue: pending deliveries are lost on restart, and this is documented | `internal/webhook/webhook.go:209` | J | [x] | [ ] |

---

## 3. Panel

One row per visible function of each tab. "Tab" is the page; "function" is what the person does on it.

### Durable browser access — offline HTTP evidence

Owner: F/E/K (panel security and access). Browser authorization now survives
service/host restarts; this supersedes the original in-memory D13/SEC-B05
recommendation without changing the bootstrap or HTTP security boundary.

| Function | Automated evidence | Real evidence / limits |
|---|---|---|
| Same cookie after daemon restart, browser lifetime | `internal/httpserver/session_persistence_test.go: TestPersistentSessionSurvivesNewServer`; `internal/daemon/browser_session_test.go: TestBrowserSessionSurvivesDaemonRestart` | Actual isolated TCP listeners stopped/recreated with the same temporary account directory and browser cookie jar; fake engine only. No installed-service restart or real-account write. |
| Idle/absolute expiry, corrupt-file recovery, unsafe-file refusal, account/directory/bearer isolation | `TestPersistentSessionExpiryAndCleanup`, `TestPersistentSessionCorruptFileIsDiscarded`, `TestPersistentSessionRejectsUnsafeFiles`, `TestPersistentSessionAccountAndTokenIsolation` | Linux filesystem and fake-clock tests, hash-only snapshots in `0700` directory / `0600` files; Windows protected owner-only ACL tests cross-compiled, not natively executed. |
| Durable session/account logout, coalesced last activity, no stale-process resurrection | `TestPersistentSessionLogoutIsDurable`, `TestPersistentSessionAccountLogoutRevokesAll`, `TestPersistentSessionLastCheckpointCoalesces`, `TestPersistentSessionOverlappingServersCannotResurrectLogout`; `TestSessionLockBlocksOtherProcess` | OS-lock process proof and overlapping handler tests. Checkpoints at most once per minute; crash recovery may expire idle sessions less than one minute earlier, never later. |
| Public localized `/access`, local/SSH entry instructions | `internal/httpserver/access_test.go: TestAccessPageLocalizedByAcceptLanguage`, `TestAccessPageExplainsAuthorization`, `TestAccessPageStaticWithoutSecretsOrInputs`, `TestAccessPageCSPHashesItsOnlyStylesheet`, `TestAccessPageKeepsGuards`, `TestAccessPageNamesListeningPort` | Portuguese/English/Spanish `Accept-Language` fixtures; no secrets, input form, script or private panel data. No installed browser/account acceptance claimed. |

`internal/httpserver/README.md` documents storage, atomic replacement, error
behavior, process-local CSRF/bootstrap keys and platform verification limits.
All existing loopback, Host/Origin, CSRF, bearer-only MCP and single-use link
negative tests remain in force. No real Telegram parity checkbox is changed.

### 3.1 Installation (`/instalacao`)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P01 | Installation | Step wizard (messaging account → AI tool), with resume | `internal/panel/panel.go:608`, `internal/panel/panel.go:618` | E | [x] | [ ] | Same flow; step 1 becomes QR, code or password (2FA) | — |
| P02 | Installation | Installation page with state and verification | `internal/panel/panel.go:634` | E | [x] | [ ] | — | — |
| P03 | Installation | Advance or skip the step | `internal/panel/panel.go:666` | E | [x] | [ ] | — | — |
| P04 | Installation | Pairing by QR code, renewed automatically | `internal/panel/panel.go:1096`, `internal/panel/panel.go:1160` | E | [x] | [ ] | Telegram: `auth.exportLoginToken` + QR (`tg://login?token=`) | — |
| P05 | Installation | Pairing by phone number, with an 8-character code | `internal/panel/templates.go:427`, `internal/panel/panel.go:1096` | E | [x] | [ ] | Telegram: code sent in the app + 2FA password (`auth.checkPassword`) | — |
| P06 | Installation | Cancel pairing and try again | `internal/panel/panel.go:1126` | E | [x] | [ ] | — | — |
| P07 | Installation | Choose the language before connecting | `internal/panel/templates.go:75`, `internal/panel/language.go:53` | E | [x] | [ ] | — | — |
| P08 | Installation | Tick "open at login" during installation | `internal/panel/templates.go:65`, `internal/panel/app.go:117` | E | [x] | [ ] | — | — |

### 3.2 Connect AI tool (`/`)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P09 | Connect | Overview: messaging account connected and how many tools use the MCP | `internal/panel/templates.go:215` | E | [ ] | [ ] | — | — |
| P10 | Connect | List of connections, with state (working, configured, waiting) | `internal/panel/templates.go:253`, `internal/panel/clients.go:382` | E | [ ] | [ ] | — | — |
| P11 | Connect | Rename a connection | `internal/panel/panel.go:954` | E | [ ] | [ ] | — | — |
| P12 | Connect | Disconnect a tool (with a backup of the client's configuration) | `internal/panel/panel.go:969`, `internal/panel/clients.go:465` | E | [ ] | [ ] | Backup before editing, same as the original | — |
| P13 | Connect | Choose the tool (Claude Desktop, Claude Code, ChatGPT/Codex, Cursor, other) | `internal/panel/panel.go:789`, `internal/panel/panel.go:745` | E | [x] | [ ] | fake: `TestToolState, TestHermesSetupKeepsTheTokenOutOfTheYAML` (E3-K) | — |
| P14 | Connect | Claude Desktop walkthrough, one-click | `internal/panel/panel.go:807`, `internal/panel/clients.go:652` | E | [x] | [ ] | fake: `TestIntegrationOneClickDesktopKeepsABackup` (E3-K) | — |
| P15 | Connect | Claude Code walkthrough | `internal/panel/clients.go:440` | E | [ ] | [ ] | — | — |
| P16 | Connect | Codex walkthrough, with ready-made TOML and verification | `internal/panel/clients.go:529`, `internal/panel/clients.go:160` | E | [ ] | [ ] | — | — |
| P17 | Connect | Cursor walkthrough | `internal/panel/clients.go:493` | E | [ ] | [ ] | — | — |
| P18 | Connect | Ready-made text for any other MCP tool | `internal/panel/recipes.go:149` | E | [x] | [ ] | Talks about Telegram, not the predecessor service; fake: `TestVerificationPromptIsTelegramAndReadOnly, TestNoLegacyBrandInPanelTemplatesOrCatalogues` (E3-K) | — |
| P19 | Connect | Write the client configuration through the API | `internal/panel/panel.go:1131` | E | [x] | [ ] | fake: `TestIntegrationSettingsMutationsNeedSessionAndCSRF` (E3-K) | — |
| P20 | Connect | Client state and progress (so the page updates itself) | `internal/panel/panel.go:1006` | E | [x] | [ ] | — | `TestAsyncStatusAndPulseReuseTelemetryWithoutCachingMode`; [panel evidence](PANEL_PARITY.md). |
| P21 | Connect | Agent-readable status, with next step | `internal/panel/agent.go:84`, `internal/panel/agent.go:115` | F | [ ] | [ ] | — | — |
| P22 | Connect | Bring the app window to a page | `internal/panel/agent.go:135` | L | [ ] | [ ] | Desktop app only | — |

### 3.3 Account (upstream account tab, at a route named after the predecessor service)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P23 | Account | Account card: name, number, last message, stored messages, conversations, history since | `internal/panel/templates.go:317` | E | [x] | [ ] | The number comes from `users.getFullUser` (`phone`) | — |
| P24 | Account | Give the number a name (Personal, Work…) | `internal/panel/panel.go:940` | E | [x] | [ ] | — | — |
| P25 | Account | Preview of the 10 recent conversations, updated automatically | `internal/panel/templates.go:186`, `internal/panel/panel.go:1041` | E | [x] | [ ] | — | `TestConversationAPIsLocalPagination`, `TestStoreBackedPanelInHeadlessBrowser`, `TestPreviewRefreshReconcilesLoadedWindows`; local SQL name/unread/type filters and pagination. |
| P26 | Account | Open a conversation and compare with the phone | `internal/panel/panel.go:1055` | E | [x] | [ ] | — | `TestConversationInheritedHTTPContract`, `TestConversationAPIPrivacyBoundary`, `TestStoreBackedPanelInHeadlessBrowser`; no live read/receipt. |
| P27 | Account | Request history for this conversation | `internal/panel/panel.go:1080`, `internal/panel/templates.go:196` | E | [x] | [ ] | Backfill via `messages.getHistory` | `TestPanelHistoryInheritedContractUsesSyncHistory`, `TestPanelHistoryExistingJobAndUnsupportedImport`; explicit bounded import, not browse. |
| P28 | Account | Request history for the recent conversations | `internal/panel/templates.go:204` | E | [x] | [ ] | — | `TestPanelHistoryInheritedContractUsesSyncHistory` (single/recent targets); unavailable importer never reports success. |
| P29 | Account | Disconnect the computer from the account (danger zone) | `internal/panel/panel.go:928` | E | [x] | [ ] | `auth.logOut`; stored messages stay | — |

### 3.4 Status (`/status`)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P30 | Status | Health with checks and summary | `internal/panel/templates.go:361`, `internal/panel/panel.go:863` | E | [x] | [ ] | MTProto-specific checks | `TestCachedPageBudgetsOnStoreFixtures`, `TestPanelStatusPerformance`, `TestStoreBackedPanelInHeadlessBrowser`, `TestStatusReportsKnownLocalDepthWithoutClaimingFullHistory`; async coverage and retained local depth, [measurements](PANEL_PARITY.md). |
| P31 | Status | State pulse for the live elements | `internal/panel/panel.go:1032` | E | [x] | [ ] | — | `TestAsyncStatusAndPulseReuseTelemetryWithoutCachingMode`, `TestPulseStartsOnlyOneBackgroundRefreshAndShutdownWaits`; live lifecycle, timestamped cached health. |
| P32 | Status | Periods without any message (computer off) | `internal/panel/templates.go:389` | E | [ ] | [ ] | — | — |

### 3.5 Functions (`/funcoes`)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P33 | Functions | Catalogue of the tools the AI can use, in the person's language | `internal/panel/panel.go:910`, `internal/mcp/catalogue.go:29` | K | [x] | [ ] | 40 tools + extras from §4.5 | — |

### 3.6 Recipes (`/receitas`)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P34 | Recipes | Ready-made recipes: schedule a message, watch keywords, summary of what went unanswered, poll and tally, a group's daily summary, read the document that arrived, archive of what was agreed | `internal/panel/panel.go:920`, `internal/panel/recipes.go:26` | K | [x] | [ ] | "Schedule a message" depends on §4.5 (scheduled sending); fake: `TestNoLegacyBrandInPanelTemplatesOrCatalogues (recipes)` (E3-K) | — |

### 3.7 Help (`/ajuda`)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P35 | Help | Frequently asked questions and sample requests | `internal/panel/panel.go:723` | K | [x] | [ ] | Own text: local MCP, not the predecessor service; fake: `TestRenderedPagesSayTelegramInEveryLanguage` (E3-K) | — |

### 3.8 Settings (`/configuracoes`)

| ID | Tab | Function | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| P36 | Settings | MCP address and port change, with client reconfiguration | `internal/panel/app.go:96`, `internal/panel/templates.go:615` | K | [x] | [ ] | fake: `TestIntegrationHostSettingsNeedCSRF, TestSettingsOnTheCommandLine` (E3-K) | — |
| P37 | Settings | Notify the connected tools after changing the port | `internal/panel/templates.go:593` | K | [x] | [ ] | fake: `TestIntegrationHostSettingsNeedCSRF (data-reconfigure)` (E3-K) | — |
| P38 | Settings | Open at login and close to the tray | `internal/panel/app.go:117` | K | [x] | [ ] | Desktop app only; fake: `TestIntegrationHostSettingsNeedCSRF` (E3-K) | — |
| P39 | Settings | Quit the app | `internal/panel/app.go:158` | K | [x] | [ ] | fake: `TestIntegrationHostSettingsNeedCSRF` (E3-K) | — |
| P40 | Settings | Webhooks: create, list, edit, test, remove | `internal/panel/webhooks.go:21`, `internal/panel/templates.go:653` | J | [x] | [ ] | Events from section 2; fake: `TestIntegrationSettingsMutationsNeedSessionAndCSRF (routes and panel card)` (E3-K) | — |
| P41 | Settings | Transcription: install the model from the screen and see its state | `internal/panel/panel.go:891`, `internal/panel/panel.go:899`, `internal/panel/templates.go:688` | J | [x] | [ ] | Offline fake: `TestIntegrationP41LocalASRInstallOffline` (injected installer, no model download); does not prove a real installation | — |
| P42 | Settings | Downloaded files: space used, automatic retention, delete | `internal/panel/webhooks.go:77`, `internal/panel/templates.go:733` | J | [x] | [ ] | fake: `TestIntegrationSettingsMutationsNeedSessionAndCSRF` (E3-K) | — |
| P43 | Settings | Installed version and update (check, install and restart) | `internal/panel/app.go:77`, `internal/panel/templates.go:700` | L | [ ] | [ ] | Stage 4 updater | — |
| P44 | Settings | Appearance: light, dark or system theme | `internal/panel/templates.go:712` | K | [x] | [ ] | fake: `TestSettingsOnTheCommandLine` (E3-K) | — |
| P45 | Settings | Language: system, pt-BR, en, es | `internal/panel/templates.go:723`, `internal/panel/language.go:53` | K | [x] | [ ] | Same set of languages as the original; fake: `TestLanguagePickers, TestIntegrationSettingsMutationsNeedSessionAndCSRF` (E3-K) | — |
| P46 | Settings | Data: open the data folder | `internal/panel/app.go:76`, `internal/panel/templates.go:751` | L | [ ] | [ ] | — | — |
| P47 | Settings | Erase everything, by typing the confirmation word | `internal/panel/app.go:168`, `internal/panel/templates.go:759` | K | [x] | [ ] | Logs out of the connected devices and deletes session, database, transcripts and media; fake: `TestIntegrationHostSettingsNeedCSRF` (E3-K) | — |
| P48 | Settings | Confirmation screens: quit and data erased | `internal/panel/templates.go:774`, `internal/panel/templates.go:781` | K | [x] | [ ] | fake: `TestIntegrationHostSettingsNeedCSRF` (E3-K) | — |
| P49 | Connect | Hermes Agent walkthrough: `mcp_servers` YAML with the token in `${TELEGRAM_MCP_TOKEN}` in `.env`, and ready-made text | — (new) | K | [x] | [ ] | fake: `TestHermesSetupKeepsTheTokenOutOfTheYAML` (E3-K) | — |
| P50 | All | "Telegram MCP" brand, its own blue, no Telegram logo, non-affiliation notice in the footer/Help, credits to the upstream; no predecessor-brand text outside the credits | — (new) | K | [x] | [ ] | fake: `TestNoLegacyBrandInPanelTemplatesOrCatalogues`, `TestNoLegacyBrandGuardCatchesALeak`, `TestRenderedPagesSayTelegramInEveryLanguage` (E3-K, owner's decision during integration) | — |
| P51 | Account and Settings | Disconnect, Log out of the account and Erase everything kept separate, each with its own confirmation | `internal/panel/templates.go:759` | K | [x] | [ ] | fake: `TestIntegrationContaFormsAndLoginNeedCSRF`, `TestIntegrationHostSettingsNeedCSRF`, `TestSettingsOnTheCommandLine` (E3-K) | — |

**Panel acceptance notes (Stage 4):** QA on Mac, a Windows VM and Linux with a desktop; each row from P01 to P48 is marked per tab, with a screenshot in the record. The old tabs keep redirecting (`internal/panel/panel.go:166`).

---

## 4. HTTP API `/api/*`

The panel's administrative routes. Body limited to 64 KiB (`internal/panel/panel.go:283`). Origin validated on every write (`internal/panel/panel.go:217`).

| ID | Method and route | What it does | Upstream | Track | fake | real | Telegram note | Evidence |
|---|---|---|---|---|---|---|---|---|
| A01 | `GET /api/status` | Agent-readable state, with next step | `internal/panel/agent.go:84` | F | [ ] | [ ] | — | — |
| A02 | `GET /api/state` | Screen state (sync, account, pairing, activity) | `internal/panel/panel.go:1006` | F | [x] | [ ] | — | `TestCheapPagesRenderWithoutHeavyStateOrDiscovery`, `TestAsyncStatusAndPulseReuseTelemetryWithoutCachingMode`; local Account/Login, lightweight state. |
| A03 | `GET /api/pulse` | Light pulse for live updates | `internal/panel/panel.go:1032` | F | [x] | [ ] | — | `TestAsyncStatusAndPulseReuseTelemetryWithoutCachingMode`, `TestPanelSnapshotEventsAreScopedAndCancellationDrains`. |
| A04 | `GET /api/chats` | List of recent conversations for the preview | `internal/panel/panel.go:1041` | F | [x] | [ ] | — | `TestConversationAPIsLocalPagination`, `TestConversationInheritedHTTPContract`; SQL pagination, inherited JSON. |
| A05 | `GET /api/chats/{jid}` | Messages of a conversation | `internal/panel/panel.go:1055` | F | [x] | [ ] | Typed Telegram peer stays in the inherited `{jid}` field; no predecessor identifier model. Additive `cursor` field and `before`/`since` keyset bounds (date, id) | `TestConversationHTTPRetainedTranscriptAndFromMe`, `TestConversationAPIPrivacyBoundary`, `TestConversationHTTPCursorPagination`, `TestPanelConversationCursorPagesTotalOrder`; typed Telegram id remains in `{jid}`. |
| A06 | `POST /api/history` | Requests history (per conversation or recent ones) | `internal/panel/panel.go:1080` | F | [x] | [ ] | — | `TestPanelHistoryInheritedContractUsesSyncHistory`, `TestPanelHistoryExistingJobAndUnsupportedImport`; explicit bounded `RequestHistory`. |
| A07 | `GET /api/pair/qr.png` | Pairing QR as a PNG | `internal/panel/panel.go:1160` | F | [ ] | [ ] | QR from `auth.exportLoginToken` | — |
| A08 | `POST /api/pair` | Starts pairing (QR or phone) | `internal/panel/panel.go:1096` | F | [ ] | [ ] | Phone requires code + 2FA password | — |
| A09 | `POST /api/pair/cancel` | Cancels the pairing in progress | `internal/panel/panel.go:1126` | F | [ ] | [ ] | — | — |
| A10 | `POST /api/clients/{client}` | Writes an MCP client's configuration | `internal/panel/panel.go:1131` | F | [ ] | [ ] | — | — |
| A11 | `POST /api/idioma` | Changes the panel language | `internal/panel/language.go:53` | F | [ ] | [ ] | — | — |
| A12 | `GET /api/asr` | Local transcription state | `internal/panel/panel.go:891` | J | [ ] | [ ] | — | — |
| A13 | `POST /api/asr/install` | Downloads and installs the transcription model | `internal/panel/panel.go:899` | J | [ ] | [ ] | — | — |
| A14 | `POST /api/configuracoes` | Open at login, close to the tray, language | `internal/panel/app.go:117` | L | [ ] | [ ] | — | — |
| A15 | `POST /api/abrir` | Opens a link in the system browser | `internal/panel/app.go:146` | L | [ ] | [ ] | Only http and https | — |
| A16 | `POST /api/app/open` | Shows the app window on a page | `internal/panel/agent.go:135` | L | [ ] | [ ] | — | — |
| A17 | `POST /api/abrir-pasta` | Opens the data folder | `internal/panel/app.go:76` | L | [ ] | [ ] | — | — |
| A18 | `GET /api/atualizacao` | Update state | `internal/panel/app.go:77` | L | [ ] | [ ] | — | — |
| A19 | `POST /api/atualizacao/verificar` | Checks whether there is a new version | `internal/panel/app.go:78` | L | [ ] | [ ] | — | — |
| A20 | `POST /api/atualizacao/instalar` | Installs and restarts | `internal/panel/app.go:79` | L | [ ] | [ ] | Checks SHA-256 (Stage 4) | — |
| A21 | `GET /api/webhooks` | Lists webhooks, events and the signature format | `internal/panel/webhooks.go:26` | J | [x] | [ ] | — | — |
| A22 | `POST /api/webhooks` | Creates a webhook and returns the secret once | `internal/panel/webhooks.go:34` | J | [x] | [ ] | — | — |
| A23 | `GET /api/webhooks/{id}` | One webhook | `internal/panel/webhooks.go:51` | J | [x] | [ ] | — | — |
| A24 | `PATCH /api/webhooks/{id}` | Edits a webhook | `internal/panel/webhooks.go:55` | J | [x] | [ ] | — | — |
| A25 | `DELETE /api/webhooks/{id}` | Removes a webhook | `internal/panel/webhooks.go:66` | J | [x] | [ ] | — | — |
| A26 | `POST /api/webhooks/{id}/test` | Delivers a test event | `internal/panel/webhooks.go:72` | J | [x] | [ ] | — | — |
| A27 | `GET /api/media` | Media and export inventory | `internal/panel/webhooks.go:77` | J | [x] | [ ] | — | — |
| A28 | `POST /api/media/retention` | Sets retention in days and runs the cleanup | `internal/panel/webhooks.go:80` | J | [x] | [ ] | `TestIntegrationSettingsRetentionSweepsImmediately`: PASS; setting + immediate sweep | — |
| A29 | `POST /api/media/purge` | Deletes downloaded media or exports | `internal/panel/webhooks.go:93` | J | [x] | [ ] | — | — |

**Outside `/api/`, but part of the server contract:**

| ID | Route | What it does | Upstream | Track | fake | real | Evidence |
|---|---|---|---|---|---|---|---|
| A30 | `POST /mcp` | MCP endpoint (JSON-RPC), optional bearer | `internal/httpserver/server.go:49` | F | [ ] | [ ] | — |
| A31 | `GET /health` | Detailed health, 503 on failure | `internal/httpserver/server.go:91` | F | [ ] | [ ] | — |
| A32 | `GET /healthz` | Simple liveness | `internal/httpserver/server.go:106` | F | [ ] | [ ] | — |
| A33 | `GET /media/{token}` | Temporary link for large media | `internal/mcp/medialinks.go:21`, `internal/mcp/handlers.go:970` | J | [x] | [ ] | — |

---

### Additional fork-only read endpoints

These do not renumber the inherited A01–A33 contract. They use the same mandatory
panel session/bearer, loopback, Host/Origin and no-store protections.

| Method and route | Purpose | Evidence |
|---|---|---|
| `GET /api/search` | Local paginated text/transcript FTS5; optional typed `chat_id` | `TestConversationAPIsLocalPagination`, `TestConversationAPIInvalidQueries`, store privacy tests |
| `GET /api/panel/health` | Timestamped aggregate health/counts for initial async Account/Connect loading | `TestAsyncStatusAndPulseReuseTelemetryWithoutCachingMode` |
| `GET /api/panel/status` | Complete async status fragment with batched per-conversation coverage | `TestPanelStatusPerformance`, `TestStoreBackedPanelInHeadlessBrowser`, authenticated curl evidence |
| `GET /api/panel/clients` | Client-discovery flags only, without returning configurations/tokens | `TestCheapPagesRenderWithoutHeavyStateOrDiscovery`, `TestStoreBackedPanelInHeadlessBrowser` |

## 5. How this list is closed

- Stage 3 closes when T01–T40, W01–W06 and R01–R10 have a fake test and real verification (or an approved limitation).
- Stage 4 closes when P01–P48 and A01–A33 have QA on the three platforms, and auto-update goes from version 1 to 2 on the test server.
- Any extra Telegram tool (forum topics, channels, Saved Messages, scheduled sending, pinning, folders) goes into §4.5 of the plan, not here: parity is what the original has.

## E2 headless integration — offline evidence

- C: T01–T08, T27 and T30; D: T09 and T25. The other tools remain catalogued, but are not announced as full E2 parity.
- E: P01–P08, P23, P24, P29, P30 and P33; fake means automated tests, not QA on a real account or OS. P25–P28 remain unavailable in the UI; this does not prevent querying history through MCP.
- Evidence: `internal/daemon/integration_test.go`, `regression_test.go`, `tools_test.go`; `internal/engine/gotdengine/offline_mcp_integration_test.go`, `qr_renewal_test.go`; `internal/store/read_*_test.go`; `internal/panel/{integration,csrf,mode,telegram}_test.go`; `internal/httpserver/{security,contract_integration}_test.go`.
- Real Store opened per account, FTS5 over text/transcripts, TTL barrier before the engine and HTTP, local per-conversation coverage in Status. `POST /api/mode` requires session+CSRF and confirmation, never a bearer. An independent intent prevents restoring writes after a State failure while disabling them.
- Integration round 1: three P2s fixed (readiness of a new login, fail-closed mode on restart and the credentials' initialisation lock). Round 2: a P2 about POSIX semantics on Windows fixed directly; Windows execution remains unverified. There was no third round.
- `go vet ./...`, `go test -race -count=1 ./...`, the build of every command and 8 packaging contracts passed on the host with `GOFLAGS=-tags=gtk3`. The integration does not change `internal/engine/engine.go`.
- No real connection/login, service installation or `systemctl` was run. The real columns stay empty. Items (2)–(6) of the Stage 2 Done list depend on later authorisation/execution; the opt-in model test (SEC-I03), the full real RPC catalogue and real-transport side effects were not replaced by fixtures.
