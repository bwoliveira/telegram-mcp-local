# Local read API (`internal/store`)

All APIs receive an explicit `context.Context` and `engine.AccountID` (or the
account from `MessageKey`). They use only `s.reader`, the read-only SQLite pool,
and do not call Telegram. `s.checkAccount` rejects a different account. No
response contains `row_id`, `access_hash`, `media_remote` or `Media.Remote`.

## Core integration

`queries.go` implements **only** the APIs below and the helpers
`scanMessage(scanner interface{ Scan(...any) error }) (engine.Message, error)`
and `messageColumns`. The core retains `Message`, `LookupPeer`, `FindPeers`,
`Cursor` and `ResolveMessageIDs`, without changing `engine.Persister`. A
deliberate exception to the public read API: `Message` is the **internal engine
query** and restores `Media.Remote` for `DownloadMedia`; that field is `json:"-"`
in the contract and is never returned by the APIs below.

To use the scanner, the core must select exactly `messageColumns` with `FROM
messages m`, and apply account/key/TTL in the `WHERE` clause. The helper
reconstructs public relational fields: UTC dates, sender, entities, media,
location, poll, reply, forward, topic, album, service, flags, counters, TTL,
reactions and conversation name. It does not read `media_remote`; TTL/protected
media is not marked downloadable. Invalid JSON produces an error, not a
partially decoded response.

## Common rules

- Limit: `0` means 100; negative values or values above 1000 return
  `engine.ErrInvalid`. Negative offsets and topics are also invalid.
- Every message query filters `expires_at IS NULL OR expires_at > now()`;
  the janitor need not have run. Peer/group listings contain no message content
  and have no TTL of their own.
- Message order: `(date, peer_type, peer_id, msg_id)` descending. Keys remain
  separate even when numeric IDs coincide.
- `MessageOptions`: `Limit`, `Before`, inclusive `Since`, exclusive `Until`,
  `TopicID *int`, `IncludeMigrated *bool`.
- `IncludeMigrated == nil` means **true** (D4). History traverses migration
  links in both directions, without duplicates or changing original keys.
  `false` isolates the requested peer.
- `Before` is an exclusive `MessagePosition {Date, Key}` bound, not a physical
  SQLite ID. `Next` appears only when an additional row was found. Reuse the
  position with the same filters. This is not a transactional snapshot: later
  writes may change the set between pages.
- Account failure returns `engine.ErrAccountMismatch`; invalid filters return
  `engine.ErrInvalid`. Context and SQLite errors propagate.

## Messages and context

```go
ListMessages(ctx, account, peer, MessageOptions) (MessagePage, error)
Context(ctx, key, before, after) ([]engine.Message, error)
```

`MessagePage` contains `Messages`, `Next`, `Access` and `AccessLostAt` for the
requested peer. Access loss does not block local history (D7). A peer without
messages returns an empty page.

`Context` requires the full key and an unexpired local anchor; otherwise it
returns `engine.ErrMessageNotFound`. `before` and `after` each accept 0..1000.
It returns the nearest neighbors and the anchor in ascending chronological
order; neighbors include migrated history by default. It does not fill gaps over
the network.

## FTS search and coverage

```go
SearchMessages(ctx, account, SearchOptions) (SearchPage, error)
```

`SearchOptions` embeds `MessageOptions` and adds `Query`, `Peer *PeerRef`
(nil = entire account), `Media` and `Sender *PeerRef`. `Query` is an **FTS5**
expression, not SQL: terms, prefixes (`reuni*`), phrases (`"reunião amanhã"`),
`AND`/`OR`/`NOT` and grouping are accepted. Empty or invalid syntax returns
`engine.ErrInvalid`. The expression is passed as a parameter; search covers
`text` and `transcript`. The tokenizer removes accents. To search by author,
use the `Sender` filter, not the FTS `sender` column.

`SearchPage` contains:

- `Messages []SearchHit`: each hit embeds `engine.Message` and `Transcript`;
- `Next`: the same key-based pagination as messages;
- `FullText: true`: FTS was actually used, without silent fallback to LIKE;
- `Coverage`: `HistorySince` (the smallest known cursor `oldest_date` in the
  scope), `Complete`, `Cursors` and open `Gaps`. `HistorySince` describes the
  observed start; it does **not** promise absence of gaps. Use `Complete` and
  `Gaps`. With no cursor, an incomplete/inaccessible/errored cursor or gaps,
  `Complete=false`. Global search considers all account gaps, including peers
  not yet present in history tables. Peer search includes account gaps and gaps
  of peers linked by migration.

`FullText` does not mean complete history. The D5 explicit remote-search policy
belongs to the consumer; the store never uses the network.

## Chats, contacts and groups

```go
ListChats(ctx, account, ChatOptions) ([]Chat, error)
ListContacts(ctx, account, PeerOptions) ([]engine.Peer, error)
ListGroups(ctx, account, PeerOptions) ([]engine.Peer, error)
Group(ctx, account, peer) (engine.Group, bool, error)
```

`PeerOptions`: `Query` (literal substring of name, primary username/aliases,
phone or normalized name), `Limit`, `Offset`. `%` and `_` are not wildcards.
Contacts require `is_contact=1` and the user namespace. Groups include group,
supergroup and channel kinds. Order is stable by normalized name and peer key.

`ChatOptions` embeds `PeerOptions` and adds `FolderID *int`, `Kind` and
`UnreadOnly`. `Chat` contains `Peer`, **server** counters in `Dialog`, the local
unexpired `LastMessage` and `AccessLostAt`. Order: pinned/pin_order, date of
last local unexpired message, peer key. A dialog whose content has expired still
exists but does not return expired content.

`Group` returns `(zero, false, nil)` if no peer exists; the user namespace is
invalid. It returns a snapshot, information and participants with local names.
Without a snapshot, it returns the known peer with `Partial=true`, without
claiming the participant list is complete. Lost access does not prevent reading.

## Unread messages and mentions

```go
ListUnread(ctx, account, UnreadOptions) (MessagePage, error)
ListMentions(ctx, account, MentionOptions) (MentionPage, error)
```

`UnreadOptions` embeds `MessageOptions` and `Peer *PeerRef`. It selects only
incoming messages (`out=0`) above the `read_inbox_max_id` of their own
namespace/dialog. It does not confuse server counters with local content.
`IncludeBots` defaults to `false`: before pagination, it excludes only user
dialogs whose peer kind is `bot`. Dialogs with missing peer metadata and
group/channel dialogs are preserved. Set `IncludeBots` to `true` to restore bot
dialogs.

`MentionOptions` embeds `MessageOptions`, `Peer *PeerRef`, `Answered *bool`
and `ServerUnread *bool`. Nil does not restrict; false/true independently
select the respective state. `MentionPage` contains `Mentions` and `Next`.
Each mention embeds the message and adds `ByName`, `ByUsername`, `ByReply`,
`ByFlag`, `AnsweredAt`, `AnsweredBy *MessageKey` and `ServerUnread`.

The response is the relational state maintained by `Apply`: the first later
outgoing message in the same peer/topic answers the mention; deleting that
answer recalculates the state. `answered` is never inferred from `server_unread`.
