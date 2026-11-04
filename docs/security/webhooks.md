# Webhook network security

Telegram MCP webhooks send projected event payloads to operator-configured HTTP endpoints. The webhook URL is therefore treated as an outbound network capability, not as an ordinary string.

## Destination policy

Public `http://` and `https://` destinations are allowed after their host resolves successfully. The webhook transport resolves the host again for every new connection, validates every returned address, and dials a validated IP directly. This prevents a hostname that was public during configuration from switching to a private address at delivery time.

Private destinations require an explicit per-webhook opt-in:

```json
{
  "url": "http://127.0.0.1:8080/telegram",
  "allow_private_network": true
}
```

The opt-in covers loopback, RFC 1918 IPv4, IPv6 unique-local addresses, and link-local unicast addresses. It is persisted with the webhook, returned as `allow_private_network` by the webhook API, and is preserved across restarts. A panel that edits webhooks must show this value as an explicit warning-bearing control; it must not silently enable it merely because the URL is local. This opt-in is needed by legitimate local receivers, including local Hermes integrations.

Cloud metadata endpoints and other special-use ranges remain blocked even when `allow_private_network` is true. The block includes unspecified and multicast addresses, carrier-grade NAT, documentation and benchmarking ranges, known metadata addresses, and IPv4 addresses hidden inside mapped IPv6, the well-known NAT64 prefix (`64:ff9b::/96`), 6to4, or Teredo forms. Native global IPv6 addresses are not classified from their last 32 bits alone. DNS answers that mix allowed and blocked addresses are rejected in full.

Existing enabled webhooks without the opt-in are revalidated at startup. Unsafe legacy entries are disabled before a worker is started. A disabled webhook may retain a private URL without the opt-in, but it cannot be enabled or tested until the opt-in is set.

The webhook HTTP client:

- does not use environment proxy variables;
- never follows redirects;
- disables connection reuse so every delivery performs a fresh validated dial;
- keeps the existing three-second attempt timeout;
- limits concurrent outbound resolutions and dials to eight.

## Resource limits

One process permits at most 32 enabled webhooks, shared by every account. Startup reconciles older state against one process-wide retention order: webhook `created_at`, then webhook ID. The oldest enabled webhooks across all accounts are kept and the newer ones above the limit are disabled with a visible reason, regardless of which account starts first. When an account that starts later holds an older enabled webhook, the newest running webhook of another account is disabled and stopped to free its slot. Exact ties on both creation time and ID keep the webhook already running.

This displacement applies only to startup reconciliation of stored state. Creating or re-enabling a webhook at runtime never displaces a running one; it is rejected while the limit is reached. Account startups are serialized for this reconciliation.

Each webhook still has a 1,000-delivery queue limit. In addition, all active and queued webhook payloads share a 16 MiB byte budget. A delivery that would exceed the aggregate budget is dropped and counted in the webhook status. `queued_bytes` exposes the retained bytes for each running webhook. Stopping, replacing, deleting, invalidating, or completing a delivery releases its accounting exactly once.

Chat-filter migration resolution runs from a snapshot after the manager mutex is released. Each local lookup has a 250 ms deadline, so a slow local resolver cannot serialize webhook status and configuration operations behind the global lock.

## Payload integrity and privacy

The network hardening does not change the wire contract:

- only the projected webhook payload is sent;
- the secret is returned only when the webhook is created and remains omitted from later API responses;
- `X-Telegram-MCP-Signature` remains an HMAC-SHA256 over the exact immutable request body;
- retries reuse the same body, delivery ID, and signature;
- a successful delivery is still any HTTP 2xx response.
