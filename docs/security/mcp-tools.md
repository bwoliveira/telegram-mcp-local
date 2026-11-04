# MCP tool security contracts

This document records the security boundaries enforced by the Telegram MCP tool dispatcher. It complements `docs/contratos/security.md`; it does not replace the engine's own write gate.

## Exact tool arguments

Every one of the 40 tool schemas sets `additionalProperties: false`. Runtime decoding uses the same per-tool property allowlist as `tools/list`, so a field that belongs to another tool is rejected before any handler, local file operation, or engine call. The error names both the tool and the unsupported argument instead of silently ignoring it.

Client metadata belongs in the MCP request envelope, not inside a tool's `arguments` object. Hermes, Claude, and other clients may send only the properties advertised for that tool. In particular, `transcribe_audio` has no `native` argument: MCP transcription is local-only and never invokes Telegram's native transcription RPC.

## Telegram write gate

Telegram-changing tools still default to preview. Execution requires all of the following:

1. the engine is in `read_write` mode;
2. `confirm:true` and `dry_run:false`;
3. the exact, unexpired, single-use `preview_fingerprint` returned by the preview; and
4. the same action arguments that were previewed.

For confirmed executions, the dispatcher checks `read_only` mode before recipient, mention, participant, file, or network resolution. Read-only requests therefore cannot use a confirmed tool call to trigger preparatory Telegram or HTTP traffic. The engine repeats this gate below MCP. Ordinary reads and local-only operations do not require a Telegram write preview.

## Local media operations

`download_media`, `transcribe_audio`, and `get_profile_picture` are annotated with `readOnlyHint:false` because they can write files on this computer, even though they do not change Telegram. They remain local operations and do not use the Telegram write approval flow.

The media root is created or tightened to mode `0700` before media writes. Downloaded files remain `0600`. Inline MCP media is limited to 256 KiB; larger downloads return a ten-minute authenticated media link instead of a base64 payload.

`purge_media` has its own local deletion approval:

1. call without `confirm:true` (or with `dry_run:true`) to preview the selected files;
2. obtain explicit human approval of that exact set;
3. repeat the same filters with `confirm:true` and the returned `preview_fingerprint` within ten minutes.

The fingerprint is single-use and is bound to the filters and the approved files, including their SHA-256 digests. Confirmation verifies every approved file before deletion and deletes only that saved set. A file that appears after preview is not deleted; a changed approved file makes confirmation fail closed.

## Logging

Structured tool-call logs include the tool name only when it is one of the 40 registered names. Any other caller-controlled name is logged as `<unknown>`. The JSON-RPC error may still identify the invalid requested name to the caller, but arbitrary values do not enter logs as tool identifiers.
