# Telegram webhook adapter

`Manager.BindEngine(ctx, controller)` subscribes directly to the committed engine
stream. Call `Start(ctx)` and bind before starting the engine. The subscription is
installed before `BindEngine` returns; rebinding cancels the previous subscription.
Only `message`, `edit`, `delete`, `reaction`, and outbox `read` are delivered.
`Test` bypasses the engine and sends `event:"test"` with `test:true`.

## Delivery guarantees

- The envelope is schema 1 from `docs/contratos/webhooks.md`, with
  `X-Telegram-MCP-*` headers and no phone number or delivered/played receipt.
- Delivery IDs are `d_` plus the first 32 hex characters of SHA-256 of
  `webhook_id + "|" + engine_event_id`. Retries reuse the exact serialized body,
  timestamp and signature; only `X-Telegram-MCP-Attempt` changes.
- Queues are **memory only**. Restart loses pending deliveries; it does not
  replay already committed facts. Newly recovered engine changes retain
  `source:"difference"` and `catch_up:true`.
- Each webhook is ordered, bounded at 1,000 pending deliveries (including the
  active attempt), drops the oldest pending delivery on overflow, and counts
  drops. Deletion or expiry invalidates queued message/edit deliveries. An
  HTTP request already sent cannot be recalled; invalidation prevents retries.
- Ten failed attempts disable a webhook and discard its queue. The production
  retry delays and three-second timeout remain unchanged. Redirects fail.
- Local peer resolution (`Network:false`) enriches chat kind/name and matches
  migration links in both directions; this never initiates a Telegram write.
- Self-destructing media exposes only `type` and `ephemeral:true`. Normal media
  exposes metadata, never bytes, local paths or remote file references. TTL
  message text includes its expiry so receivers can enforce deletion too.

## Source compatibility

Manager configuration/status/test signatures remain unchanged. Legacy Go DTO
fields used by the old panel examples are retained with `json:"-"`; they do not
belong to the wire contract and cannot reintroduce the historical payload. New code
uses `Event.Identity`, `Message.MessageID`, `Message.ChatID`,
`Message.SenderID`, `Reference.MessageID`, `Reaction.ToID` and the top-level
`Delete`, `Reaction`, and `Read` payloads. `Receipt` is never serialized or
dispatched. `SetAccount` is a legacy local-slot fallback only; binding supplies
the actual Telegram identity.

## Frozen engine limitations

The frozen `engine.Deletion` has only peer and IDs, no revoke flag. Lifecycle
expiry deletes are identifiable by `Source("ttl")` and retain `reason:"ttl"`.
Other engine deletes report `reason:"deleted"`, not an invented revoke reason.
`SourceSend` identifies our own deletion for `include_own` filtering, but cannot
distinguish delete-for-me from revoke-for-everyone. An integration that knows
`Delete.Revoke` can use `Manager.Dispatch` with `Event.Delete.Reason` set to
`"revoked_by_me"`; this is filtered by `include_own` even without engine metadata.
Extending that distinction in the subscribed stream requires an engine contract
change. Reply metadata
has no quoted-message author, so `sender_id` is omitted rather than invented;
only the supplied quote (at most 200 Unicode characters) is exported.
Unknown peer kinds/names are omitted when local resolution lacks a snapshot.
Arbitrary `ServiceAction.Data` is not forwarded: the safe service type is.

## Verification

Tests drive the actual `internal/engine/fake` through `Subscribe` and an HTTP
receiver. They cover all five payloads, catch-up, identity, ephemeral metadata,
secret exclusions, inbox-read exclusion, split/unresolved deletes, migrated
filters, byte-identical retry attempts, per-hook stable IDs, queue invalidation,
overflow, cancellation/rebinding, non-durable restart behavior, registration,
updates/deletion, test delivery, signing, and ten-failure disablement.
